// Package qemu provides a bounded QMP client for an already running QEMU
// instance. It does not start, stop, or provision VMs.
package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultTimeout = 10 * time.Second
	defaultMessage = 1 << 20
	maxScreenshot  = 64 << 20
	maxEvents      = 128
	absMin         = 0
	absMax         = 0x7fff
)

var (
	ErrClosed                = errors.New("qmp client is closed")
	ErrUnsupportedCapability = errors.New("qmp capability is unsupported")
)

// Config selects an existing QMP Unix socket. Timeout bounds each operation;
// MaxMessageBytes bounds every JSON line received from QEMU.
type Config struct {
	SocketPath      string
	Timeout         time.Duration
	MaxMessageBytes int
}

// Event preserves asynchronous QMP events without pretending they are guest
// readiness signals. Data remains raw because QMP adds event-specific fields.
type Event struct {
	Name      string
	Data      json.RawMessage
	Timestamp json.RawMessage
}

// Status is the host-side QEMU run state. GuestReady is intentionally absent:
// QMP status does not establish SSH or operating-system readiness.
type Status struct {
	State      string `json:"status"`
	Running    bool   `json:"running"`
	SingleStep bool   `json:"singlestep"`
}

// Snapshot describes a QMP snapshot job. QMP 6.0+ exposes save/load/delete
// jobs, but does not provide a portable snapshot-list command; ListSnapshots
// therefore reports unsupported explicitly.
type Snapshot struct {
	Tag     string   `json:"tag"`
	JobID   string   `json:"job_id,omitempty"`
	VMState string   `json:"vmstate,omitempty"`
	Devices []string `json:"devices,omitempty"`
}

// SnapshotSpec identifies the state and writable block nodes participating in
// a QMP snapshot job. These names are QEMU block-node names, not host paths.
type SnapshotSpec struct {
	Tag     string
	JobID   string
	VMState string
	Devices []string
}

type LifecycleAction string

const (
	Pause     LifecycleAction = "pause"
	Resume    LifecycleAction = "resume"
	Reset     LifecycleAction = "reset"
	Powerdown LifecycleAction = "powerdown"
	Quit      LifecycleAction = "quit"
)

type CommandError struct {
	Class string
	Desc  string
}

func (e *CommandError) Error() string {
	if e.Class == "" {
		return "qmp command failed: " + e.Desc
	}
	return fmt.Sprintf("qmp command failed (%s): %s", e.Class, e.Desc)
}

type Client struct {
	conn         net.Conn
	reader       *bufio.Reader
	mu           sync.Mutex
	closed       atomic.Bool
	closeOnce    sync.Once
	nextID       uint64
	maxMessage   int
	timeout      time.Duration
	events       []Event
	capabilities map[string]bool
}

// Dial attaches to an existing QMP Unix socket and completes capability
// negotiation. It never launches QEMU.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cfg.SocketPath) == "" {
		return nil, errors.New("QMP socket path is required")
	}
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = defaultMessage
	}
	if cfg.MaxMessageBytes > 8<<20 {
		return nil, errors.New("QMP message limit exceeds 8 MiB")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "unix", cfg.SocketPath)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, reader: bufio.NewReaderSize(conn, 32<<10), maxMessage: cfg.MaxMessageBytes, timeout: cfg.Timeout, nextID: 1, capabilities: make(map[string]bool)}
	if err := c.negotiate(ctx); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) negotiate(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	stop := c.startDeadline(ctx)
	defer stop()
	greeting, err := c.readMessage(ctx)
	if err != nil {
		return fmt.Errorf("read QMP greeting: %w", err)
	}
	var envelope struct {
		QMP struct {
			Capabilities []string `json:"capabilities"`
		} `json:"QMP"`
	}
	if err := json.Unmarshal(greeting, &envelope); err != nil || envelope.QMP.Capabilities == nil {
		return errors.New("invalid QMP greeting")
	}
	for _, capability := range envelope.QMP.Capabilities {
		c.capabilities[capability] = true
	}
	return c.doLocked(ctx, "qmp_capabilities", map[string]any{}, nil)
}

// Close disconnects from QEMU. It does not change VM state.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	var err error
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		err = c.conn.Close()
	})
	return err
}

// DrainEvents returns and clears events observed while waiting for replies.
func (c *Client) DrainEvents() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := append([]Event(nil), c.events...)
	c.events = nil
	return out
}

func (c *Client) QueryStatus(ctx context.Context) (Status, error) {
	var status Status
	if err := c.do(ctx, "query-status", nil, &status); err != nil {
		return Status{}, err
	}
	return status, nil
}

// Lifecycle performs one of the fixed QMP lifecycle operations. It accepts no
// arbitrary monitor command or caller-provided QMP command name.
func (c *Client) Lifecycle(ctx context.Context, action LifecycleAction) error {
	command := map[LifecycleAction]string{
		Pause: "stop", Resume: "cont", Reset: "system_reset",
		Powerdown: "system_powerdown", Quit: "quit",
	}[action]
	if command == "" {
		return fmt.Errorf("%w: QMP lifecycle action %q", ErrUnsupportedCapability, action)
	}
	return c.do(ctx, command, nil, nil)
}

func validateSnapshotSpec(spec SnapshotSpec) error {
	if !validSnapshotName(spec.Tag) {
		return errors.New("QMP snapshot tag must be 1..128 printable characters")
	}
	if !validSnapshotName(spec.JobID) {
		return errors.New("QMP snapshot job ID must be 1..128 printable characters")
	}
	if !validSnapshotName(spec.VMState) {
		return errors.New("QMP snapshot vmstate must be 1..128 printable characters")
	}
	if len(spec.Devices) > 256 {
		return errors.New("QMP snapshot has too many devices")
	}
	for _, device := range spec.Devices {
		if !validSnapshotName(device) {
			return errors.New("QMP snapshot device name is invalid")
		}
	}
	return nil
}

func validSnapshotName(value string) bool {
	if value == "" || len(value) > 128 || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f || r == '\r' || r == '\n' {
			return false
		}
	}
	return true
}

// SaveSnapshot starts a QMP snapshot-save background job. QEMU reports job
// completion asynchronously through JOB_STATUS_CHANGE events.
func (c *Client) SaveSnapshot(ctx context.Context, spec SnapshotSpec) error {
	if err := validateSnapshotSpec(spec); err != nil {
		return err
	}
	return c.do(ctx, "snapshot-save", map[string]any{"job-id": spec.JobID, "tag": spec.Tag, "vmstate": spec.VMState, "devices": append([]string(nil), spec.Devices...)}, nil)
}

// LoadSnapshot starts a QMP snapshot-load background job.
func (c *Client) LoadSnapshot(ctx context.Context, spec SnapshotSpec) error {
	if err := validateSnapshotSpec(spec); err != nil {
		return err
	}
	return c.do(ctx, "snapshot-load", map[string]any{"job-id": spec.JobID, "tag": spec.Tag, "vmstate": spec.VMState, "devices": append([]string(nil), spec.Devices...)}, nil)
}

// DeleteSnapshot starts a QMP snapshot-delete background job.
func (c *Client) DeleteSnapshot(ctx context.Context, tag, jobID string) error {
	spec := SnapshotSpec{Tag: tag, JobID: jobID, VMState: "placeholder"}
	if !validSnapshotName(tag) || !validSnapshotName(jobID) {
		return errors.New("QMP snapshot tag and job ID are required")
	}
	return c.do(ctx, "snapshot-delete", map[string]any{"job-id": spec.JobID, "tag": spec.Tag}, nil)
}

// ListSnapshots is unavailable through the portable QMP command set. QEMU's
// legacy info snapshots is an HMP command and is intentionally not tunneled as
// freeform monitor text.
func (c *Client) ListSnapshots(context.Context) ([]Snapshot, error) {
	return nil, ErrUnsupportedCapability
}

// Screenshot asks QEMU to create a PNG at an absolute, currently absent path
// on the local hypervisor. Existing files are rejected to avoid accidental
// overwrite.
func (c *Client) Screenshot(ctx context.Context, outputPath string) error {
	path, err := screenshotPath(outputPath)
	if err != nil {
		return err
	}
	if err := c.do(ctx, "screendump", map[string]any{"filename": path, "format": "png"}, nil); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("QMP screenshot was not created: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("QMP screenshot is not a regular file")
	}
	if info.Size() == 0 || info.Size() > maxScreenshot {
		return fmt.Errorf("QMP screenshot has invalid size %d", info.Size())
	}
	return nil
}

// SendKey sends a QMP qcode press followed by release through the verified
// input-send-event API.
func (c *Client) SendKey(ctx context.Context, key string) error {
	if !qcodePattern.MatchString(key) {
		return errors.New("QMP key must be a nonempty qcode")
	}
	events := []any{keyEvent(key, true), keyEvent(key, false)}
	return c.do(ctx, "input-send-event", map[string]any{"events": events}, nil)
}

// SendPointer sends an absolute pointer move and, optionally, a button event.
// Coordinates use QEMU's absolute input range (0..0x7fff). button may be
// empty for motion.
func (c *Client) SendPointer(ctx context.Context, x, y int, button string, down bool) error {
	if x < absMin || x > absMax || y < absMin || y > absMax {
		return errors.New("QMP pointer coordinates must be between 0 and 32767")
	}
	if button != "" && button != "left" && button != "middle" && button != "right" {
		return errors.New("QMP pointer button must be left, middle, or right")
	}
	events := []any{
		map[string]any{"type": "abs", "data": map[string]any{"axis": "x", "value": x}},
		map[string]any{"type": "abs", "data": map[string]any{"axis": "y", "value": y}},
	}
	if button != "" {
		events = append(events, map[string]any{"type": "btn", "data": map[string]any{"button": button, "down": down}})
	}
	return c.do(ctx, "input-send-event", map[string]any{"events": events}, nil)
}

func keyEvent(key string, down bool) map[string]any {
	return map[string]any{"type": "key", "data": map[string]any{"key": map[string]any{"type": "qcode", "data": key}, "down": down}}
}

func (c *Client) do(ctx context.Context, command string, arguments map[string]any, result any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.doLocked(ctx, command, arguments, result)
}

func (c *Client) doLocked(ctx context.Context, command string, arguments map[string]any, result any) error {
	if c.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID
	c.nextID++
	request := map[string]any{"execute": command, "id": id}
	if arguments != nil {
		request["arguments"] = arguments
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\r', '\n')
	stop := c.startDeadline(ctx)
	defer stop()
	if _, err := c.conn.Write(encoded); err != nil {
		c.closeLocked()
		if contextErr := contextDeadlineError(ctx); contextErr != nil {
			return contextErr
		}
		return fmt.Errorf("write QMP command: %w", err)
	}
	wantID := strconv.FormatUint(id, 10)
	for {
		message, err := c.readMessage(ctx)
		if err != nil {
			c.closeLocked()
			if contextErr := contextDeadlineError(ctx); contextErr != nil {
				return contextErr
			}
			return fmt.Errorf("read QMP response: %w", err)
		}
		var envelope struct {
			Event     string          `json:"event"`
			Data      json.RawMessage `json:"data"`
			Timestamp json.RawMessage `json:"timestamp"`
			ID        json.RawMessage `json:"id"`
			Return    json.RawMessage `json:"return"`
			Error     *struct {
				Class string `json:"class"`
				Desc  string `json:"desc"`
			} `json:"error"`
		}
		if err := json.Unmarshal(message, &envelope); err != nil {
			c.closeLocked()
			return fmt.Errorf("decode QMP response: %w", err)
		}
		if envelope.Event != "" {
			c.events = append(c.events, Event{Name: envelope.Event, Data: envelope.Data, Timestamp: envelope.Timestamp})
			if len(c.events) > maxEvents {
				copy(c.events, c.events[len(c.events)-maxEvents:])
				c.events = c.events[:maxEvents]
			}
			continue
		}
		if string(envelope.ID) != wantID {
			continue
		}
		if envelope.Error != nil {
			if envelope.Error.Class == "CommandNotFound" || envelope.Error.Class == "GenericError" && strings.Contains(strings.ToLower(envelope.Error.Desc), "not supported") {
				return fmt.Errorf("%w: %s", ErrUnsupportedCapability, envelope.Error.Desc)
			}
			return &CommandError{Class: envelope.Error.Class, Desc: envelope.Error.Desc}
		}
		if result != nil && len(envelope.Return) > 0 {
			if err := json.Unmarshal(envelope.Return, result); err != nil {
				return fmt.Errorf("decode QMP result: %w", err)
			}
		}
		return nil
	}
}

func (c *Client) readMessage(ctx context.Context) ([]byte, error) {
	var message []byte
	for {
		part, err := c.reader.ReadSlice('\n')
		message = append(message, part...)
		if len(message) > c.maxMessage {
			return nil, errors.New("QMP message exceeds configured limit")
		}
		if err == nil {
			return bytesTrimSpace(message), nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(message) > 0 {
			return bytesTrimSpace(message), nil
		}
		if contextErr := contextDeadlineError(ctx); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
}

// contextDeadlineError closes the race between a socket deadline firing and
// the context timer setting ctx.Err. A configured client timeout has no
// context deadline and therefore remains a transport timeout.
func contextDeadlineError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return nil
}

func bytesTrimSpace(b []byte) []byte { return []byte(strings.TrimSpace(string(b))) }

func (c *Client) startDeadline(ctx context.Context) func() {
	deadline := time.Now().Add(c.timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = c.conn.SetDeadline(deadline)
	stop := make(chan struct{})
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-ctx.Done():
			_ = c.conn.SetDeadline(time.Now())
		case <-stop:
		}
	}()
	return func() {
		close(stop)
		<-watcherDone
		_ = c.conn.SetDeadline(time.Time{})
	}
}

func (c *Client) closeLocked() {
	c.closeOnce.Do(func() {
		c.closed.Store(true)
		_ = c.conn.Close()
	})
}

func screenshotPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return "", errors.New("QMP screenshot path must be absolute")
	}
	value = filepath.Clean(value)
	parent := filepath.Dir(value)
	info, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("QMP screenshot parent: %w", err)
	}
	if !info.IsDir() {
		return "", errors.New("QMP screenshot parent is not a directory")
	}
	if _, err := os.Lstat(value); err == nil {
		return "", errors.New("QMP screenshot destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return value, nil
}

var qcodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

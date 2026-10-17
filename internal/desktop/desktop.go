// Package desktop provides bounded, observation-based native desktop control.
// It deliberately exposes only the capabilities detected for the current
// display session. It does not bypass compositor or OS permissions.
package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"bifrost/internal/process"
)

const (
	maxActionText   = 64 << 10
	maxProcessBytes = 64 << 10
	maxPNGBytes     = 64 << 20
)

var (
	ErrUnsupportedCapability = errors.New("desktop capability is unsupported")
	ErrStaleObservation      = errors.New("desktop observation is stale")
	ErrInvalidObservation    = errors.New("desktop observation is invalid")
	ErrLeaseBusy             = errors.New("desktop interaction lease is busy")
)

type Backend string

const (
	BackendAuto    Backend = ""
	BackendUnknown Backend = "unknown"
	BackendX11     Backend = "x11"
	BackendWayland Backend = "wayland"
)

// ToolPaths allows tests and deployments to provide explicit executables. An
// explicitly supplied path is used as-is and is still passed as argv[0].
type ToolPaths struct {
	Import   string // X11 ImageMagick import
	Identify string // ImageMagick identify, used for dimensions
	Xdotool  string
	Grim     string
	Ydotool  string
	Wtype    string
}

// Config selects a detected display route. BackendAuto is represented by an
// empty Backend and uses the current session environment.
type Config struct {
	Backend     Backend
	Display     string
	ArtifactDir string
	Env         []string
	Tools       ToolPaths
	Timeout     time.Duration

	// Run is injectable for bounded fixture tests. Production uses process.Run.
	Run    func(context.Context, process.Spec) (process.Result, error)
	Lookup func(string) (string, error)
}

// Capability describes what the adapter can actually perform in this session.
type Capability struct {
	Backend  Backend
	Display  string
	Capture  bool
	Move     bool
	Click    bool
	KeyPress bool
	Type     bool
	Details  []string
}

// Observation is a fresh screenshot lease. Actions consume the observation;
// callers must capture again after every action or display reconnect.
type Observation struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	PNGPath   string    `json:"png_path"`
	Backend   Backend   `json:"backend"`
	Display   string    `json:"display"`
}

type Manager struct {
	cfg  Config
	cap  Capability
	mu   sync.Mutex
	busy bool
	wake chan struct{}
}

type Session struct {
	manager *Manager
	mu      sync.Mutex
	closed  bool
	current *Observation
	serial  uint64
}

var fallbackSerial atomic.Uint64

// New detects the current display route and its installed command adapters.
// Missing tools produce a capability report; they do not make application
// startup fail. Capture/actions return ErrUnsupportedCapability as needed.
func New(cfg Config) (*Manager, error) {
	if cfg.Backend == "" {
		cfg.Backend = detectBackend(cfg.Env)
	}
	if cfg.Display == "" {
		cfg.Display = detectDisplay(cfg.Backend, cfg.Env)
	}
	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = filepath.Join(os.TempDir(), "bifrost-desktop")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Run == nil {
		cfg.Run = func(ctx context.Context, spec process.Spec) (process.Result, error) {
			return process.Run(ctx, spec)
		}
	}
	if cfg.Lookup == nil {
		cfg.Lookup = exec.LookPath
	}
	capability := detectCapability(cfg)
	return &Manager{cfg: cfg, cap: capability, wake: make(chan struct{})}, nil
}

func detectBackend(env []string) Backend {
	lookup := func(key string) string {
		if env == nil {
			return os.Getenv(key)
		}
		for _, item := range env {
			if strings.HasPrefix(item, key+"=") {
				return strings.TrimPrefix(item, key+"=")
			}
		}
		return ""
	}
	if lookup("WAYLAND_DISPLAY") != "" || lookup("XDG_SESSION_TYPE") == "wayland" {
		return BackendWayland
	}
	if lookup("DISPLAY") != "" {
		return BackendX11
	}
	return BackendUnknown
}

func detectDisplay(backend Backend, env []string) string {
	key := "DISPLAY"
	if backend == BackendWayland {
		key = "WAYLAND_DISPLAY"
	}
	if env == nil {
		return os.Getenv(key)
	}
	for _, item := range env {
		if strings.HasPrefix(item, key+"=") {
			return strings.TrimPrefix(item, key+"=")
		}
	}
	return ""
}

func detectCapability(cfg Config) Capability {
	capability := Capability{Backend: cfg.Backend, Display: cfg.Display}
	addMissing := func(name string) string {
		if name == "" {
			return ""
		}
		if _, err := cfg.Lookup(name); err != nil {
			return name + " is unavailable"
		}
		return ""
	}
	available := func(explicit, fallback string) string {
		if explicit != "" {
			return ""
		}
		return addMissing(fallback)
	}
	if cfg.Backend == BackendX11 {
		imp, id, xdotool := cfg.Tools.Import, cfg.Tools.Identify, cfg.Tools.Xdotool
		if imp == "" {
			imp = "import"
		}
		if id == "" {
			id = "identify"
		}
		if xdotool == "" {
			xdotool = "xdotool"
		}
		missing := []string{available(cfg.Tools.Import, imp), available(cfg.Tools.Xdotool, xdotool)}
		if missing[0] == "" && missing[1] == "" && cfg.Display != "" {
			capability.Capture = true
		} else {
			capability.Details = append(capability.Details, compactMissing(missing)...)
		}
		capability.Move, capability.Click, capability.KeyPress, capability.Type = missing[1] == "" && cfg.Display != "", missing[1] == "" && cfg.Display != "", missing[1] == "" && cfg.Display != "", missing[1] == "" && cfg.Display != ""
		_ = id
	} else if cfg.Backend == BackendWayland {
		grim, id, yd, wt := cfg.Tools.Grim, cfg.Tools.Identify, cfg.Tools.Ydotool, cfg.Tools.Wtype
		if grim == "" {
			grim = "grim"
		}
		if id == "" {
			id = "identify"
		}
		if yd == "" {
			yd = "ydotool"
		}
		if wt == "" {
			wt = "wtype"
		}
		if missing := available(cfg.Tools.Grim, grim); missing == "" {
			capability.Capture = cfg.Display != ""
		} else {
			capability.Details = append(capability.Details, missing)
		}
		capability.Move = available(cfg.Tools.Ydotool, yd) == "" && cfg.Display != ""
		capability.Click = capability.Move
		capability.KeyPress = capability.Move
		capability.Type = available(cfg.Tools.Wtype, wt) == "" && cfg.Display != ""
		_ = id
		if !capability.Move {
			capability.Details = append(capability.Details, "ydotool is unavailable; pointer and key actions are disabled")
		}
	} else {
		capability.Details = append(capability.Details, "no supported Linux display session detected")
	}
	return capability
}

func compactMissing(items []string) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

// Capabilities returns a copy safe for callers to retain.
func (m *Manager) Capabilities() Capability {
	capability := m.cap
	capability.Details = append([]string(nil), capability.Details...)
	return capability
}

// Acquire serializes one desktop interaction stream. Closing the session
// releases the lease even when a caller's context is canceled.
func (m *Manager) Acquire(ctx context.Context) (*Session, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		m.mu.Lock()
		if !m.busy {
			m.busy = true
			m.mu.Unlock()
			return &Session{manager: m}, nil
		}
		wake := m.wake
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, errors.Join(ErrLeaseBusy, ctx.Err())
		case <-wake:
		}
	}
}

func (m *Manager) release() {
	m.mu.Lock()
	if m.busy {
		m.busy = false
		close(m.wake)
		m.wake = make(chan struct{})
	}
	m.mu.Unlock()
}

// Close releases the desktop lease. It does not terminate or reconfigure a
// display server.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.current = nil
	s.mu.Unlock()
	s.manager.release()
	return nil
}

func (s *Session) capabilities() Capability { return s.manager.cap }

// Invalidate rejects all observations, for example after reconnect or a
// display-size change detected by an external supervisor.
func (s *Session) Invalidate() {
	s.mu.Lock()
	s.current = nil
	s.serial++
	s.mu.Unlock()
}

// Capture creates a PNG artifact and a fresh action observation.
func (s *Session) Capture(ctx context.Context) (Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Observation{}, errors.New("desktop session is closed")
	}
	capability := s.capabilities()
	if !capability.Capture {
		return Observation{}, fmt.Errorf("%w: capture (%s)", ErrUnsupportedCapability, strings.Join(capability.Details, "; "))
	}
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if err := os.MkdirAll(s.manager.cfg.ArtifactDir, 0o700); err != nil {
		return Observation{}, fmt.Errorf("create desktop artifact directory: %w", err)
	}
	id := newID()
	path := filepath.Join(s.manager.cfg.ArtifactDir, "desktop-"+id+".png")
	args := s.captureArgs(path)
	if err := s.run(ctx, args); err != nil {
		return Observation{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Observation{}, fmt.Errorf("desktop capture artifact: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxPNGBytes {
		return Observation{}, errors.New("desktop capture artifact is invalid or too large")
	}
	width, height, err := s.dimensions(ctx, path)
	if err != nil {
		return Observation{}, err
	}
	if width < 1 || height < 1 {
		return Observation{}, errors.New("desktop capture returned invalid dimensions")
	}
	s.serial++
	observation := Observation{ID: id, Timestamp: time.Now().UTC(), Width: width, Height: height, PNGPath: path, Backend: capability.Backend, Display: capability.Display}
	s.current = &observation
	return observation, nil
}

func (s *Session) captureArgs(path string) []string {
	if s.manager.cap.Backend == BackendWayland {
		return []string{s.tool("grim"), path}
	}
	return []string{s.tool("import"), "-window", "root", path}
}

func (s *Session) dimensions(ctx context.Context, path string) (int, int, error) {
	if s.manager.cap.Backend == BackendX11 {
		if output, err := s.runOutput(ctx, []string{s.tool("xdotool"), "getdisplaygeometry"}); err == nil {
			if width, height, parseErr := parseDimensions(output); parseErr == nil {
				return width, height, nil
			}
		}
	}
	return pngDimensions(path)
}

func pngDimensions(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	var header [24]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		return 0, 0, fmt.Errorf("read PNG header: %w", err)
	}
	signature := []byte{137, 80, 78, 71, 13, 10, 26, 10}
	if !bytes.Equal(header[:8], signature) || string(header[12:16]) != "IHDR" {
		return 0, 0, errors.New("desktop artifact is not a PNG")
	}
	return int(binary.BigEndian.Uint32(header[16:20])), int(binary.BigEndian.Uint32(header[20:24])), nil
}

func parseDimensions(output string) (int, int, error) {
	fields := strings.Fields(output)
	if len(fields) < 2 {
		return 0, 0, fmt.Errorf("desktop dimensions are not two integers")
	}
	width, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, 0, fmt.Errorf("desktop width: %w", err)
	}
	height, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, fmt.Errorf("desktop height: %w", err)
	}
	return width, height, nil
}

type actionKind string

const (
	actionMove  actionKind = "move"
	actionClick actionKind = "click"
	actionKey   actionKind = "keypress"
	actionType  actionKind = "type"
)

func (s *Session) act(ctx context.Context, id, display string, kind actionKind, x, y, button int, keys []string, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("desktop session is closed")
	}
	observation, err := s.takeObservation(id, display, kind, x, y)
	if err != nil {
		return err
	}
	args, err := s.actionArgs(kind, observation, x, y, button, keys, text)
	if err != nil {
		return err
	}
	// Consume before dispatch: after a successful or uncertain input command,
	// callers must capture again instead of assuming the old pixels remain.
	s.current = nil
	return s.run(ctx, args)
}

func (s *Session) takeObservation(id, display string, kind actionKind, x, y int) (Observation, error) {
	capability := s.capabilities()
	if !capabilityFor(kind, capability) {
		return Observation{}, fmt.Errorf("%w: %s", ErrUnsupportedCapability, kind)
	}
	if s.current == nil || id == "" || s.current.ID != id || display == "" || display != s.current.Display || display != capability.Display {
		return Observation{}, ErrStaleObservation
	}
	if kind == actionMove || kind == actionClick {
		if x < 0 || y < 0 || x >= s.current.Width || y >= s.current.Height {
			return Observation{}, fmt.Errorf("desktop coordinates %d,%d outside %dx%d", x, y, s.current.Width, s.current.Height)
		}
	}
	return *s.current, nil
}

func capabilityFor(kind actionKind, capability Capability) bool {
	switch kind {
	case actionMove:
		return capability.Move
	case actionClick:
		return capability.Click
	case actionKey:
		return capability.KeyPress
	case actionType:
		return capability.Type
	default:
		return false
	}
}

func (s *Session) actionArgs(kind actionKind, _ Observation, x, y, button int, keys []string, text string) ([]string, error) {
	if button < 1 || button > 9 {
		button = 1
	}
	switch kind {
	case actionMove:
		if s.manager.cap.Backend == BackendWayland {
			return []string{s.tool("ydotool"), "mousemove", "--absolute", strconv.Itoa(x), strconv.Itoa(y)}, nil
		}
		return []string{s.tool("xdotool"), "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y)}, nil
	case actionClick:
		if s.manager.cap.Backend == BackendWayland {
			return []string{s.tool("ydotool"), "mousemove", "--absolute", strconv.Itoa(x), strconv.Itoa(y), "click", fmt.Sprintf("0x%x", 0xC0+button-1)}, nil
		}
		return []string{s.tool("xdotool"), "mousemove", "--sync", strconv.Itoa(x), strconv.Itoa(y), "click", strconv.Itoa(button)}, nil
	case actionKey:
		if len(keys) == 0 || len(keys) > 32 {
			return nil, errors.New("keypress requires 1 to 32 keys")
		}
		for _, key := range keys {
			if key == "" || strings.IndexByte(key, 0) >= 0 || !utf8.ValidString(key) {
				return nil, errors.New("keypress contains invalid key text")
			}
		}
		if s.manager.cap.Backend == BackendWayland {
			return append([]string{s.tool("ydotool"), "key"}, keys...), nil
		}
		return append([]string{s.tool("xdotool"), "key"}, keys...), nil
	case actionType:
		if len([]byte(text)) > maxActionText || strings.IndexByte(text, 0) >= 0 || !utf8.ValidString(text) {
			return nil, errors.New("typed text is invalid or exceeds 64 KiB")
		}
		if s.manager.cap.Backend == BackendWayland {
			return []string{s.tool("wtype"), "--", text}, nil
		}
		return []string{s.tool("xdotool"), "type", "--delay", "1", "--", text}, nil
	default:
		return nil, errors.New("unknown desktop action")
	}
}

func (s *Session) tool(name string) string {
	tools := s.manager.cfg.Tools
	switch name {
	case "import":
		if tools.Import != "" {
			return tools.Import
		}
		return "import"
	case "identify":
		if tools.Identify != "" {
			return tools.Identify
		}
		return "identify"
	case "xdotool":
		if tools.Xdotool != "" {
			return tools.Xdotool
		}
		return "xdotool"
	case "grim":
		if tools.Grim != "" {
			return tools.Grim
		}
		return "grim"
	case "ydotool":
		if tools.Ydotool != "" {
			return tools.Ydotool
		}
		return "ydotool"
	case "wtype":
		if tools.Wtype != "" {
			return tools.Wtype
		}
		return "wtype"
	default:
		return name
	}
}

func (s *Session) run(ctx context.Context, argv []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	commandCtx, cancel := context.WithTimeout(ctx, s.manager.cfg.Timeout)
	defer cancel()
	spec := process.Spec{Argv: argv, Env: append([]string(nil), s.manager.cfg.Env...), MaxOutputBytes: maxProcessBytes}
	result, err := s.manager.cfg.Run(commandCtx, spec)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("desktop command %q exited with code %d: %s", argv[0], result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	return nil
}

func (s *Session) runOutput(ctx context.Context, argv []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, s.manager.cfg.Timeout)
	defer cancel()
	result, err := s.manager.cfg.Run(commandCtx, process.Spec{Argv: argv, Env: append([]string(nil), s.manager.cfg.Env...), MaxOutputBytes: maxProcessBytes})
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("desktop command %q exited with code %d", argv[0], result.ExitCode)
	}
	return string(result.Stdout), nil
}

func (s *Session) Move(ctx context.Context, observationID, display string, x, y int) error {
	return s.act(ctx, observationID, display, actionMove, x, y, 0, nil, "")
}

func (s *Session) Click(ctx context.Context, observationID, display string, x, y, button int) error {
	return s.act(ctx, observationID, display, actionClick, x, y, button, nil, "")
}

func (s *Session) KeyPress(ctx context.Context, observationID, display string, keys ...string) error {
	return s.act(ctx, observationID, display, actionKey, 0, 0, 0, keys, "")
}

func (s *Session) Type(ctx context.Context, observationID, display, text string) error {
	return s.act(ctx, observationID, display, actionType, 0, 0, 0, nil, text)
}

func newID() string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("%x-%d", time.Now().UnixNano(), fallbackSerial.Add(1))
}

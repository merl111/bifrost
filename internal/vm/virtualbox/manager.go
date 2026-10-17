// Package virtualbox provides bounded, argument-safe VirtualBox inspection
// and explicitly requested display/input operations. It never starts,
// stops, resets, or reconfigures a VM.
package virtualbox

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bifrost/internal/process"
)

var (
	ErrInvalidSelector       = errors.New("invalid VirtualBox machine selector")
	ErrUnsupportedCapability = errors.New("VirtualBox capability is unsupported")
)

const maxOutput = 1 << 20

type Config struct {
	Executable string
	Timeout    time.Duration
}

type Machine struct {
	Name                   string `json:"name"`
	UUID                   string `json:"uuid"`
	State                  string `json:"state"`
	ConfigFile             string `json:"config_file,omitempty"`
	GuestAdditionsRunLevel string `json:"guest_additions_run_level,omitempty"`
}

type Snapshot struct {
	Name        string `json:"name"`
	UUID        string `json:"uuid,omitempty"`
	Description string `json:"description,omitempty"`
	State       string `json:"state,omitempty"`
}

var controlActions = map[string]string{
	"pause":         "pause",
	"resume":        "resume",
	"reset":         "reset",
	"acpi-shutdown": "acpipowerbutton",
	"poweroff":      "poweroff",
}

type Manager struct {
	executable string
	timeout    time.Duration
	run        func(context.Context, []string) (process.Result, error)
}

func New(cfg Config) (*Manager, error) {
	executable := cfg.Executable
	if executable == "" {
		executable = "VBoxManage"
	}
	if _, err := exec.LookPath(executable); err != nil {
		return nil, fmt.Errorf("VBoxManage: %w", err)
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	m := &Manager{executable: executable, timeout: cfg.Timeout}
	m.run = func(ctx context.Context, args []string) (process.Result, error) {
		return process.Run(ctx, process.Spec{Argv: append([]string{executable}, args...), MaxOutputBytes: maxOutput})
	}
	return m, nil
}

// List returns registered machines using VBoxManage's machine-readable list
// boundary. It does not mutate or start any machine.
func (m *Manager) List(ctx context.Context) ([]Machine, error) {
	output, err := m.command(ctx, "list", "vms", "--sorted")
	if err != nil {
		return nil, err
	}
	var machines []Machine
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		space := strings.LastIndex(line, " {")
		if len(line) < 4 || space <= 1 || !strings.HasSuffix(line, "}") {
			return nil, fmt.Errorf("parse VBoxManage list vms line %q", line)
		}
		name, err := strconv.Unquote(line[:space])
		if err != nil {
			return nil, fmt.Errorf("parse VirtualBox machine name: %w", err)
		}
		uuid := strings.TrimSpace(line[space+1:])
		if len(uuid) < 3 || uuid[0] != '{' || uuid[len(uuid)-1] != '}' {
			return nil, fmt.Errorf("parse VirtualBox machine UUID %q", uuid)
		}
		machines = append(machines, Machine{Name: name, UUID: strings.Trim(uuid, "{}")})
	}
	return machines, nil
}

// Show returns host-side VM configuration and state. GuestAdditionsRunLevel
// is an observation only; it does not imply guest SSH or OS readiness.
func (m *Manager) Show(ctx context.Context, selector string) (Machine, error) {
	if err := validateSelector(selector); err != nil {
		return Machine{}, err
	}
	output, err := m.command(ctx, "showvminfo", selector, "--machinereadable")
	if err != nil {
		return Machine{}, err
	}
	values := parseMachineReadable(output)
	if values["name"] == "" || values["UUID"] == "" {
		return Machine{}, errors.New("VBoxManage showvminfo omitted name or UUID")
	}
	return Machine{Name: values["name"], UUID: values["UUID"], State: values["VMState"], ConfigFile: values["CfgFile"], GuestAdditionsRunLevel: values["GuestAdditionsRunLevel"]}, nil
}

// Start launches a registered machine with the requested display mode. Only
// the headless mode is exposed so desktop ownership remains explicit.
func (m *Manager) Start(ctx context.Context, selector string, headless bool) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if !headless {
		return fmt.Errorf("%w: non-headless VirtualBox launch", ErrUnsupportedCapability)
	}
	_, err := m.command(ctx, "startvm", selector, "--type", "headless")
	return err
}

// Control executes one fixed VBoxManage controlvm lifecycle action.
func (m *Manager) Control(ctx context.Context, selector, action string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	command, ok := controlActions[action]
	if !ok {
		return fmt.Errorf("%w: VirtualBox lifecycle action %q", ErrUnsupportedCapability, action)
	}
	_, err := m.command(ctx, "controlvm", selector, command)
	return err
}

func validateSnapshotName(name string) error {
	if strings.TrimSpace(name) == "" || len(name) > 128 || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("invalid VirtualBox snapshot name")
	}
	return nil
}

func (m *Manager) ListSnapshots(ctx context.Context, selector string) ([]Snapshot, error) {
	if err := validateSelector(selector); err != nil {
		return nil, err
	}
	output, err := m.command(ctx, "snapshot", selector, "list", "--machinereadable")
	if err != nil {
		return nil, err
	}
	return parseSnapshots(output), nil
}

func (m *Manager) TakeSnapshot(ctx context.Context, selector, name, description string, live bool) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if err := validateSnapshotName(name); err != nil {
		return err
	}
	if len(description) > 4096 || strings.ContainsAny(description, "\x00\r\n") {
		return errors.New("invalid VirtualBox snapshot description")
	}
	args := []string{"snapshot", selector, "take", name}
	if description != "" {
		args = append(args, "--description="+description)
	}
	if live {
		args = append(args, "--live")
	}
	_, err := m.command(ctx, args...)
	return err
}

func (m *Manager) RestoreSnapshot(ctx context.Context, selector, name string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if err := validateSnapshotName(name); err != nil {
		return err
	}
	_, err := m.command(ctx, "snapshot", selector, "restore", name)
	return err
}

func (m *Manager) DeleteSnapshot(ctx context.Context, selector, name string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if err := validateSnapshotName(name); err != nil {
		return err
	}
	_, err := m.command(ctx, "snapshot", selector, "delete", name)
	return err
}

// Screenshot captures a running VM display to a new local PNG path.
func (m *Manager) Screenshot(ctx context.Context, selector, outputPath string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	path, err := newOutputPath(outputPath)
	if err != nil {
		return err
	}
	if _, err := m.command(ctx, "controlvm", selector, "screenshotpng", path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("VirtualBox screenshot was not created: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("VirtualBox screenshot is not a regular file")
	}
	if info.Size() == 0 || info.Size() > 64<<20 {
		return fmt.Errorf("VirtualBox screenshot has invalid size %d", info.Size())
	}
	return nil
}

// SendText sends text through the verified keyboardputstring API.
func (m *Manager) SendText(ctx context.Context, selector, text string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if text == "" || len(text) > 4096 || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return errors.New("VirtualBox keyboard text must be nonempty UTF-8 up to 4096 bytes")
	}
	_, err := m.command(ctx, "controlvm", selector, "keyboardputstring", text)
	return err
}

// SendScancodes sends validated hexadecimal scan-code bytes through
// keyboardputscancode. Each item is one byte, represented by two hex digits.
func (m *Manager) SendScancodes(ctx context.Context, selector string, codes []string) error {
	if err := validateSelector(selector); err != nil {
		return err
	}
	if len(codes) == 0 || len(codes) > 256 {
		return errors.New("VirtualBox scan codes must contain 1..256 bytes")
	}
	args := []string{"controlvm", selector, "keyboardputscancode"}
	for _, code := range codes {
		if len(code) != 2 {
			return fmt.Errorf("invalid VirtualBox scan code %q", code)
		}
		if _, err := hex.DecodeString(code); err != nil {
			return fmt.Errorf("invalid VirtualBox scan code %q", code)
		}
		args = append(args, strings.ToLower(code))
	}
	_, err := m.command(ctx, args...)
	return err
}

// SendPointer is not advertised because VBoxManage 7.2's controlvm help has
// no pointer-input command. Use a verified guest helper or SDK adapter instead.
func (m *Manager) SendPointer(context.Context, string, int, int, string, bool) error {
	return ErrUnsupportedCapability
}

func (m *Manager) command(ctx context.Context, args ...string) (string, error) {
	if m == nil || m.run == nil {
		return "", errors.New("VirtualBox manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= 0 {
		return "", context.DeadlineExceeded
	}
	if m.timeout > 0 {
		limit := time.Now().Add(m.timeout)
		if deadline, ok := ctx.Deadline(); !ok || limit.Before(deadline) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, limit)
			defer cancel()
		}
	}
	result, err := m.run(ctx, args)
	if err != nil {
		return "", err
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("VBoxManage output exceeded configured limit")
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("VBoxManage exited %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	return string(result.Stdout), nil
}

func validateSelector(selector string) error {
	if strings.TrimSpace(selector) == "" || strings.HasPrefix(selector, "-") || strings.ContainsRune(selector, '\x00') || strings.ContainsAny(selector, "\r\n") {
		return ErrInvalidSelector
	}
	return nil
}

func newOutputPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" || !filepath.IsAbs(value) {
		return "", errors.New("VirtualBox screenshot path must be absolute")
	}
	value = filepath.Clean(value)
	parent, err := os.Stat(filepath.Dir(value))
	if err != nil {
		return "", fmt.Errorf("VirtualBox screenshot parent: %w", err)
	}
	if !parent.IsDir() {
		return "", errors.New("VirtualBox screenshot parent is not a directory")
	}
	if _, err := os.Lstat(value); err == nil {
		return "", errors.New("VirtualBox screenshot destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return value, nil
}

func parseMachineReadable(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		if parsed, err := strconv.Unquote(value); err == nil {
			value = parsed
		}
		values[key] = value
	}
	return values
}

func parseSnapshots(output string) []Snapshot {
	values := parseMachineReadable(output)
	var result []Snapshot
	if name := values["SnapshotName"]; name != "" {
		result = append(result, Snapshot{Name: name, UUID: values["SnapshotUUID"], Description: values["SnapshotDescription"], State: values["SnapshotState"]})
	}
	for index := 1; ; index++ {
		prefix := fmt.Sprintf("SnapshotName-%d", index)
		name := values[prefix]
		if name == "" {
			break
		}
		result = append(result, Snapshot{Name: name, UUID: values[fmt.Sprintf("SnapshotUUID-%d", index)], Description: values[fmt.Sprintf("SnapshotDescription-%d", index)], State: values[fmt.Sprintf("SnapshotState-%d", index)]})
	}
	return result
}

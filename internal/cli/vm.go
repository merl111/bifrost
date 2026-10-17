package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bifrost/internal/vm/qemu"
	"bifrost/internal/vm/virtualbox"
)

// vmCommandTimeout bounds one VM operation. VM commands attach to already
// running hypervisors and never start, stop, or reconfigure them.
const vmCommandTimeout = 30 * time.Second

var qemuCLIKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// runVM executes the positional VM command surface. Keeping VM arguments
// positional means values such as key names and text are not interpreted as
// Bifrost global flags.
func runVM(ctx context.Context, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("vm requires qemu or virtualbox")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	ctx, cancel := context.WithTimeout(nonNilContext(ctx), vmCommandTimeout)
	defer cancel()
	// opts is reserved for the global JSON/output policy. VM output is JSON
	// for both reads and explicit actions so scripts can consume it reliably.
	_ = opts
	switch args[0] {
	case "qemu":
		return runQEMU(ctx, args[1:], stdout)
	case "virtualbox":
		return runVirtualBox(ctx, args[1:], stdout)
	default:
		return fmt.Errorf("unknown vm backend %q (use qemu or virtualbox)", args[0])
	}
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func runQEMU(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("vm qemu requires launch, status, lifecycle, screenshot, key, pointer, or snapshot")
	}
	if args[0] == "launch" {
		if len(args) != 2 {
			return errors.New("usage: vm qemu launch CONFIG.json")
		}
		cfg, err := readQEMULaunchConfig(args[1])
		if err != nil {
			return err
		}
		instance, err := qemu.Launch(ctx, cfg)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "launched", "pid": instance.PID(), "socket": cfg.SocketPath, "pid_path": cfg.PIDPath})
	}
	if args[0] == "snapshot" {
		return runQEMUSnapshot(ctx, args[1:], stdout)
	}
	lifecycle := map[string]bool{"pause": true, "resume": true, "reset": true, "powerdown": true, "quit": true}
	want := map[string]int{"status": 2, "screenshot": 3, "key": 3, "pointer": 6}
	if lifecycle[args[0]] {
		if len(args) != 2 {
			return fmt.Errorf("usage: vm qemu %s SOCKET", args[0])
		}
		client, err := qemu.Dial(ctx, qemu.Config{SocketPath: args[1], Timeout: vmCommandTimeout})
		if err != nil {
			return fmt.Errorf("connect QMP: %w", err)
		}
		defer client.Close()
		if err := client.Lifecycle(ctx, qemu.LifecycleAction(args[0])); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "qemu", "action": args[0]})
	}
	expected, ok := want[args[0]]
	if !ok {
		return fmt.Errorf("unknown vm qemu command %q", args[0])
	}
	if len(args) != expected {
		switch args[0] {
		case "status":
			return errors.New("usage: vm qemu status SOCKET")
		case "screenshot":
			return errors.New("usage: vm qemu screenshot SOCKET ABS_PATH")
		case "key":
			return errors.New("usage: vm qemu key SOCKET KEY")
		default:
			return errors.New("usage: vm qemu pointer SOCKET X Y BUTTON DOWN")
		}
	}
	socket := ""
	if len(args) > 1 {
		socket = args[1]
	}
	if strings.TrimSpace(socket) == "" {
		return errors.New("QMP socket path is required")
	}
	var pointerX, pointerY int
	var pointerDown bool
	var err error
	if args[0] == "key" && !qemuCLIKeyPattern.MatchString(args[2]) {
		return errors.New("QMP key must be a nonempty qcode")
	}
	if args[0] == "pointer" {
		pointerX, err = strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("invalid pointer X %q: %w", args[2], err)
		}
		pointerY, err = strconv.Atoi(args[3])
		if err != nil {
			return fmt.Errorf("invalid pointer Y %q: %w", args[3], err)
		}
		if pointerX < 0 || pointerX > 32767 || pointerY < 0 || pointerY > 32767 {
			return errors.New("QMP pointer coordinates must be between 0 and 32767")
		}
		if args[4] != "" && args[4] != "left" && args[4] != "middle" && args[4] != "right" {
			return errors.New("QMP pointer button must be left, middle, or right")
		}
		pointerDown, err = strconv.ParseBool(args[5])
		if err != nil {
			return fmt.Errorf("invalid pointer DOWN %q: %w", args[5], err)
		}
	}
	if args[0] == "screenshot" && len(args) == 3 {
		if err := requireAbsolutePath(args[2], "QMP screenshot path"); err != nil {
			return err
		}
	}
	client, err := qemu.Dial(ctx, qemu.Config{SocketPath: socket, Timeout: vmCommandTimeout})
	if err != nil {
		return fmt.Errorf("connect QMP: %w", err)
	}
	defer client.Close()
	switch args[0] {
	case "status":
		status, err := client.QueryStatus(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, status)
	case "screenshot":
		if err := client.Screenshot(ctx, filepath.Clean(args[2])); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "captured", "backend": "qemu", "path": filepath.Clean(args[2])})
	case "key":
		if err := client.SendKey(ctx, args[2]); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "qemu", "key": args[2]})
	case "pointer":
		if err := client.SendPointer(ctx, pointerX, pointerY, args[4], pointerDown); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "qemu", "x": pointerX, "y": pointerY, "button": args[4], "down": pointerDown})
	default:
		panic("validated qemu command disappeared")
	}
}

func runQEMUSnapshot(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: vm qemu snapshot list|take|restore|delete SOCKET ...")
	}
	action, socket := args[0], args[1]
	client, err := qemu.Dial(ctx, qemu.Config{SocketPath: socket, Timeout: vmCommandTimeout})
	if err != nil {
		return fmt.Errorf("connect QMP: %w", err)
	}
	defer client.Close()
	switch action {
	case "list":
		if len(args) != 2 {
			return errors.New("usage: vm qemu snapshot list SOCKET")
		}
		items, err := client.ListSnapshots(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, items)
	case "take", "restore":
		if len(args) < 4 {
			return fmt.Errorf("usage: vm qemu snapshot %s SOCKET TAG VMSTATE [DEVICE...]", action)
		}
		spec := qemu.SnapshotSpec{Tag: args[2], JobID: "bifrost-" + action + "-" + args[2], VMState: args[3], Devices: append([]string(nil), args[4:]...)}
		if action == "take" {
			err = client.SaveSnapshot(ctx, spec)
		} else {
			err = client.LoadSnapshot(ctx, spec)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "qemu", "action": action, "tag": spec.Tag})
	case "delete":
		if len(args) != 3 {
			return errors.New("usage: vm qemu snapshot delete SOCKET TAG")
		}
		if err := client.DeleteSnapshot(ctx, args[2], "bifrost-delete-"+args[2]); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "qemu", "action": action, "tag": args[2]})
	default:
		return fmt.Errorf("unknown QEMU snapshot action %q", action)
	}
}

func runVirtualBox(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("vm virtualbox requires list, show, start, lifecycle, snapshot, screenshot, text, or scancodes")
	}
	if args[0] == "screenshot" && len(args) == 3 {
		if err := requireAbsolutePath(args[2], "VirtualBox screenshot path"); err != nil {
			return err
		}
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("usage: vm virtualbox list")
		}
	case "show":
		if len(args) != 2 {
			return errors.New("usage: vm virtualbox show ID")
		}
	case "screenshot":
		if len(args) != 3 {
			return errors.New("usage: vm virtualbox screenshot ID ABS_PATH")
		}
	case "text":
		if len(args) < 3 {
			return errors.New("usage: vm virtualbox text ID TEXT")
		}
	case "scancodes":
		if len(args) < 3 {
			return errors.New("usage: vm virtualbox scancodes ID HEX...")
		}
	case "start", "pause", "resume", "reset", "acpi-shutdown", "poweroff":
		if len(args) != 2 {
			return fmt.Errorf("usage: vm virtualbox %s ID", args[0])
		}
	case "snapshot":
		if len(args) < 3 {
			return errors.New("usage: vm virtualbox snapshot list|take|restore|delete ID ...")
		}
	default:
		return fmt.Errorf("unknown vm virtualbox command %q", args[0])
	}
	manager, err := virtualbox.New(virtualbox.Config{Timeout: vmCommandTimeout})
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		machines, err := manager.List(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, machines)
	case "start":
		if err := manager.Start(ctx, args[1], true); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "started", "backend": "virtualbox", "id": args[1], "headless": true})
	case "pause", "resume", "reset", "acpi-shutdown", "poweroff":
		if err := manager.Control(ctx, args[1], args[0]); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "virtualbox", "id": args[1], "action": args[0]})
	case "snapshot":
		return runVirtualBoxSnapshot(ctx, manager, args[1:], stdout)
	case "show":
		machine, err := manager.Show(ctx, args[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, machine)
	case "screenshot":
		path := filepath.Clean(args[2])
		if err := manager.Screenshot(ctx, args[1], path); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "captured", "backend": "virtualbox", "id": args[1], "path": path})
	case "text":
		text := strings.Join(args[2:], " ")
		if err := manager.SendText(ctx, args[1], text); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "virtualbox", "id": args[1], "text": text})
	case "scancodes":
		codes := append([]string(nil), args[2:]...)
		if err := manager.SendScancodes(ctx, args[1], codes); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "sent", "backend": "virtualbox", "id": args[1], "scancodes": codes})
	default:
		panic("validated VirtualBox command disappeared")
	}
}

func runVirtualBoxSnapshot(ctx context.Context, manager *virtualbox.Manager, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: vm virtualbox snapshot list|take|restore|delete ID ...")
	}
	action, id := args[0], args[1]
	switch action {
	case "list":
		if len(args) != 2 {
			return errors.New("usage: vm virtualbox snapshot list ID")
		}
		items, err := manager.ListSnapshots(ctx, id)
		if err != nil {
			return err
		}
		return writeJSON(stdout, items)
	case "take":
		if len(args) < 3 || len(args) > 4 {
			return errors.New("usage: vm virtualbox snapshot take ID NAME [DESCRIPTION]")
		}
		description := ""
		if len(args) == 4 {
			description = args[3]
		}
		if err := manager.TakeSnapshot(ctx, id, args[2], description, false); err != nil {
			return err
		}
	case "restore", "delete":
		if len(args) != 3 {
			return fmt.Errorf("usage: vm virtualbox snapshot %s ID NAME", action)
		}
		var err error
		if action == "restore" {
			err = manager.RestoreSnapshot(ctx, id, args[2])
		} else {
			err = manager.DeleteSnapshot(ctx, id, args[2])
		}
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown VirtualBox snapshot action %q", action)
	}
	return writeJSON(stdout, map[string]any{"status": "sent", "backend": "virtualbox", "action": action, "id": id})
}

func readQEMULaunchConfig(path string) (qemu.LaunchConfig, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return qemu.LaunchConfig{}, errors.New("QEMU launch config path must be absolute")
	}
	file, err := os.Open(path)
	if err != nil {
		return qemu.LaunchConfig{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var cfg qemu.LaunchConfig
	if err := decoder.Decode(&cfg); err != nil {
		return qemu.LaunchConfig{}, fmt.Errorf("decode QEMU launch config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return qemu.LaunchConfig{}, errors.New("QEMU launch config contains trailing JSON")
		}
		return qemu.LaunchConfig{}, fmt.Errorf("decode trailing QEMU launch config: %w", err)
	}
	return cfg, nil
}

func requireAbsolutePath(path, label string) error {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return fmt.Errorf("%s must be absolute", label)
	}
	return nil
}

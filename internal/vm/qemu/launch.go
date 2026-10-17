package qemu

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var launchToken = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)

// LaunchConfig describes a managed QEMU process. Disk and firmware paths are
// existing files; socket and pid paths must be new files. The launch code does
// not delete disks and does not accept arbitrary QEMU argv fragments.
type LaunchConfig struct {
	Binary       string `json:"binary,omitempty"`
	DiskPath     string `json:"disk_path"`
	DiskFormat   string `json:"disk_format,omitempty"`
	FirmwarePath string `json:"firmware_path,omitempty"`
	SocketPath   string `json:"socket_path"`
	PIDPath      string `json:"pid_path"`
	Machine      string `json:"machine,omitempty"`
	Architecture string `json:"architecture,omitempty"`
	Accelerator  string `json:"accelerator,omitempty"`
	CPUModel     string `json:"cpu_model,omitempty"`
	MemoryMB     int    `json:"memory_mb,omitempty"`
	CPUs         int    `json:"cpus,omitempty"`
	NetworkNAT   bool   `json:"network_nat,omitempty"`
	Display      string `json:"display,omitempty"`
}

type Instance struct {
	cmd        *exec.Cmd
	cfg        LaunchConfig
	pidPath    string
	socketPath string
	mu         sync.Mutex
	done       chan error
	waitOnce   sync.Once
	waitErr    error
	closed     bool
}

func (i *Instance) PID() int {
	if i == nil || i.cmd == nil || i.cmd.Process == nil {
		return 0
	}
	return i.cmd.Process.Pid
}

// BuildLaunchArgs validates the managed configuration and returns explicit
// argv suitable for exec.Command. It performs no filesystem mutation.
func BuildLaunchArgs(cfg LaunchConfig) ([]string, error) {
	if err := validateLaunchConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.DiskFormat == "" {
		cfg.DiskFormat = "qcow2"
	}
	binary := cfg.Binary
	if binary == "" {
		binary = qemuBinary(cfg.Architecture)
	}
	args := []string{binary, "-nodefaults", "-drive", "file=" + cfg.DiskPath + ",if=virtio,format=" + cfg.DiskFormat}
	if cfg.Machine != "" {
		args = append(args, "-machine", cfg.Machine)
	}
	if cfg.CPUModel != "" {
		args = append(args, "-cpu", cfg.CPUModel)
	}
	if cfg.MemoryMB > 0 {
		args = append(args, "-m", strconv.Itoa(cfg.MemoryMB))
	}
	if cfg.CPUs > 0 {
		args = append(args, "-smp", strconv.Itoa(cfg.CPUs))
	}
	if cfg.FirmwarePath != "" {
		args = append(args, "-bios", cfg.FirmwarePath)
	}
	if cfg.Accelerator != "" {
		args = append(args, "-accel", cfg.Accelerator)
	}
	if cfg.NetworkNAT {
		args = append(args, "-netdev", "user,id=bifrostnet", "-device", "virtio-net-pci,netdev=bifrostnet")
	}
	if cfg.Display == "" {
		cfg.Display = "none"
	}
	args = append(args, "-display", cfg.Display, "-qmp", "unix:"+cfg.SocketPath+",server=on,wait=off", "-pidfile", cfg.PIDPath)
	return args, nil
}

func qemuBinary(architecture string) string {
	switch architecture {
	case "aarch64":
		return "qemu-system-aarch64"
	case "arm":
		return "qemu-system-arm"
	case "ppc64le":
		return "qemu-system-ppc64"
	case "riscv64":
		return "qemu-system-riscv64"
	case "s390x":
		return "qemu-system-s390x"
	default:
		return "qemu-system-x86_64"
	}
}

func validateLaunchConfig(cfg LaunchConfig) error {
	if strings.TrimSpace(cfg.DiskPath) == "" || !filepath.IsAbs(cfg.DiskPath) {
		return errors.New("QEMU disk_path must be absolute")
	}
	if err := validateQEMUOptionPath("disk_path", cfg.DiskPath); err != nil {
		return err
	}
	info, err := os.Stat(cfg.DiskPath)
	if err != nil {
		return fmt.Errorf("QEMU disk: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("QEMU disk_path is not a regular file")
	}
	if cfg.DiskFormat == "" {
		cfg.DiskFormat = "qcow2"
	}
	if cfg.DiskFormat != "qcow2" && cfg.DiskFormat != "raw" {
		return errors.New("QEMU disk_format must be qcow2 or raw")
	}
	for label, path := range map[string]string{"socket_path": cfg.SocketPath, "pid_path": cfg.PIDPath} {
		if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
			return fmt.Errorf("QEMU %s must be absolute", label)
		}
		if err := validateQEMUOptionPath(label, path); err != nil {
			return err
		}
		parent, err := os.Stat(filepath.Dir(path))
		if err != nil || !parent.IsDir() {
			return fmt.Errorf("QEMU %s parent is not a directory", label)
		}
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("QEMU %s already exists", label)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("QEMU %s: %w", label, err)
		}
	}
	if filepath.Clean(cfg.SocketPath) == filepath.Clean(cfg.PIDPath) {
		return errors.New("QEMU socket_path and pid_path must differ")
	}
	if cfg.FirmwarePath != "" {
		if !filepath.IsAbs(cfg.FirmwarePath) {
			return errors.New("QEMU firmware_path must be absolute")
		}
		firmware, err := os.Stat(cfg.FirmwarePath)
		if err != nil || !firmware.Mode().IsRegular() {
			return errors.New("QEMU firmware_path is not a regular file")
		}
	}
	for label, value := range map[string]string{"machine": cfg.Machine, "architecture": cfg.Architecture, "cpu_model": cfg.CPUModel} {
		if value != "" && !launchToken.MatchString(value) {
			return fmt.Errorf("QEMU %s contains unsupported characters", label)
		}
	}
	if cfg.Architecture != "" {
		switch cfg.Architecture {
		case "x86_64", "aarch64", "arm", "ppc64le", "riscv64", "s390x":
		default:
			return errors.New("QEMU architecture is unsupported")
		}
	}
	if cfg.Accelerator != "" && (cfg.Accelerator != "kvm" && cfg.Accelerator != "tcg" && cfg.Accelerator != "hvf" && cfg.Accelerator != "whpx" && cfg.Accelerator != "nvmm" && cfg.Accelerator != "xen") {
		return errors.New("QEMU accelerator is unsupported")
	}
	if cfg.MemoryMB < 0 || cfg.MemoryMB > 1<<20 || cfg.CPUs < 0 || cfg.CPUs > 256 {
		return errors.New("QEMU resources are outside supported bounds")
	}
	if cfg.Display != "" && !launchToken.MatchString(cfg.Display) {
		return errors.New("QEMU display contains unsupported characters")
	}
	return nil
}

// QEMU's -drive and -qmp values are comma-separated option strings. Keep
// paths out of that grammar rather than attempting to implement its escaping
// rules; the standalone pidfile argument remains subject to the same control
// character check for consistent config validation.
func validateQEMUOptionPath(label, path string) error {
	if strings.ContainsAny(path, ",\\") {
		return fmt.Errorf("QEMU %s contains an unsupported option character", label)
	}
	for _, r := range path {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("QEMU %s contains control characters", label)
		}
	}
	return nil
}

// Launch starts a managed QEMU process with explicit argv. The process owns
// the configured QMP socket and pid file; Close terminates only this child and
// removes those files when they still identify this process.
func Launch(ctx context.Context, cfg LaunchConfig) (*Instance, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	args, err := BuildLaunchArgs(cfg)
	if err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(commandCtx, args[0], args[1:]...)
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	instance := &Instance{cmd: cmd, cfg: cfg, pidPath: cfg.PIDPath, socketPath: cfg.SocketPath}
	instance.done = make(chan error, 1)
	go func() {
		instance.done <- cmd.Wait()
		cancel()
	}()
	if err := waitForControlFiles(ctx, instance); err != nil {
		_ = instance.Close()
		return nil, err
	}
	return instance, nil
}

func waitForControlFiles(ctx context.Context, instance *Instance) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		pidReady := false
		if contents, err := os.ReadFile(instance.pidPath); err == nil {
			pidReady = strings.TrimSpace(string(contents)) == strconv.Itoa(instance.PID())
		}
		socketReady := false
		if info, err := os.Lstat(instance.socketPath); err == nil {
			socketReady = info.Mode()&os.ModeSocket != 0
		}
		if pidReady && socketReady {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("QEMU did not publish owned QMP socket and PID files")
		case <-ticker.C:
		}
	}
}

// Wait waits for the managed child without changing its state.
func (i *Instance) Wait() error {
	if i == nil || i.cmd == nil {
		return errors.New("QEMU instance is nil")
	}
	i.waitOnce.Do(func() { i.waitErr = <-i.done })
	return i.waitErr
}

// Close terminates the managed process and removes only socket/pid files
// that still belong to the recorded child. It never removes disk or firmware.
func (i *Instance) Close() error {
	if i == nil || i.cmd == nil {
		return nil
	}
	i.mu.Lock()
	if i.closed {
		i.mu.Unlock()
		return nil
	}
	i.closed = true
	i.mu.Unlock()
	if i.cmd.Process != nil {
		_ = i.cmd.Process.Kill()
	}
	removeOwned := func(path string) error {
		if path == "" {
			return nil
		}
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		if path == i.pidPath {
			contents, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(contents)) != strconv.Itoa(i.cmd.Process.Pid) {
				return errors.New("QEMU pid file ownership changed")
			}
		}
		return os.Remove(path)
	}
	if i.done != nil {
		_ = i.Wait()
	}
	return errors.Join(removeOwned(i.socketPath), removeOwned(i.pidPath))
}

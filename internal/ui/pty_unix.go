//go:build !windows

package ui

import (
	"errors"
	"github.com/creack/pty"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

func terminatePTYProcess(cmd *exec.Cmd, foregroundPGID int) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if err := termGroup(foregroundPGID, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Kill()
	}
	if err := termGroup(pid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		_ = cmd.Process.Kill()
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var waitErr error
	select {
	case waitErr = <-done:
		// The shell can exit before a job-controlled foreground process. Kill
		// both groups even after Wait reports the leader's exit.
		_ = termGroup(foregroundPGID, syscall.SIGKILL)
		_ = termGroup(pid, syscall.SIGKILL)
		return normalizeWaitError(waitErr)
	case <-time.After(250 * time.Millisecond):
		_ = termGroup(foregroundPGID, syscall.SIGKILL)
		_ = termGroup(pid, syscall.SIGKILL)
		waitErr = <-done
		return normalizeWaitError(waitErr)
	}
}

func termGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 {
		return nil
	}
	return syscall.Kill(-pgid, signal)
}

func normalizeWaitError(err error) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

// A pollable nonblocking master lets Close interrupt a kernel write even when
// the slave is not consuming input. Rewrap a duplicate so os.File records the
// nonblocking state at construction. Never use File.Fd after this point.
func preparePTY(file *os.File) (*os.File, error) {
	fd, err := syscall.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	syscall.CloseOnExec(fd)
	if err := syscall.SetNonblock(fd, true); err != nil {
		syscall.Close(fd)
		return nil, err
	}
	wrapped := os.NewFile(uintptr(fd), file.Name())
	file.Close()
	return wrapped, nil
}

func resizePTY(file *os.File, width, height int) error {
	raw, err := file.SyscallConn()
	if err != nil {
		return err
	}
	size := pty.Winsize{Cols: uint16(width), Rows: uint16(height)}
	var ioctlErr error
	err = raw.Control(func(fd uintptr) {
		_, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCSWINSZ), uintptr(unsafe.Pointer(&size)))
		if e != 0 {
			ioctlErr = e
		}
	})
	if err != nil {
		return err
	}
	return ioctlErr
}

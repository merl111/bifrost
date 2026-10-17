//go:build windows

package ui

import (
	"errors"
	"os"
	"os/exec"
)

func terminatePTYProcess(cmd *exec.Cmd, _ int) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = cmd.Process.Kill()
	return normalizeWaitError(cmd.Wait())
}

func normalizeWaitError(err error) error { return err }

func preparePTY(file *os.File) (*os.File, error) { return file, nil }
func resizePTY(*os.File, int, int) error         { return errors.New("native Windows PTY is not supported") }

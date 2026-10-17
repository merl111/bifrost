//go:build windows

package cli

import "os/exec"

func configureDetachedCommand(cmd *exec.Cmd) {}

func terminateDetachedCommand(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

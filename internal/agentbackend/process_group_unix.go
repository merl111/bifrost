//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agentbackend

import (
	"os/exec"
	"syscall"
)

func configureChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func terminateChild(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// CLIs may launch shells and tool subprocesses. Kill the process group so
	// cancellation does not leave a vendor child running in the workspace.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

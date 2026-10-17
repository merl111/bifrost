//go:build windows || plan9 || js || wasip1

package agentbackend

import "os/exec"

func configureChild(*exec.Cmd) {}

func terminateChild(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

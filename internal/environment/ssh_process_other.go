//go:build !unix

package environment

import "os/exec"

func configureSSHCommand(*exec.Cmd) {}
func killSSHCommand(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

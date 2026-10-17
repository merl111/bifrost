//go:build windows || plan9 || js || wasip1

package process

import "os/exec"

func configureProcess(*exec.Cmd) {}

func terminateProcess(cmd *exec.Cmd, _ bool) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

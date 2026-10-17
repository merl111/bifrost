//go:build !unix

package lsp

import "os/exec"

func configure(*exec.Cmd) {}
func kill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

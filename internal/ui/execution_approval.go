package ui

import (
	"bifrost/internal/execpolicy"
	"encoding/json"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) pendingExecutionApproval() *execpolicy.Request {
	for len(m.executionApprovals) > 0 {
		request := m.executionApprovals[0]
		select {
		case <-request.Done:
			m.executionApprovals = m.executionApprovals[1:]
			continue
		default:
			return request
		}
	}
	return nil
}
func (m *Model) handleExecutionApproval(key string) bool {
	request := m.pendingExecutionApproval()
	if request == nil {
		return false
	}
	allowed := key == "ctrl+y"
	if allowed || key == "ctrl+n" || key == "esc" || key == "ctrl+c" {
		select {
		case request.Reply <- allowed:
		default:
		}
		m.executionApprovals = m.executionApprovals[1:]
	}
	return true
}
func (m *Model) renderExecutionApproval(request *execpolicy.Request) string {
	argv, _ := json.Marshal(request.Argv)
	text := "Command needs approval\n\nWorkspace: " + request.Root + "\nDirectory: " + request.CWD + "\n\n" + string(argv) + "\n\nCtrl+Y  Allow once    Ctrl+N / Esc  Deny\n\nApproval applies only to this exact command."
	return ansi.Hardwrap(sanitizeDisplay(text), maxInt(20, m.width-4), true)
}

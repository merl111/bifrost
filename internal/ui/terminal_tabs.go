package ui

import (
	"encoding/json"
	"fmt"
	"strings"
)

type terminalTab struct {
	ID       string
	Title    string
	Command  string
	Output   string
	Running  bool
	ExitCode int
	HasExit  bool
}

const maxAgentTerminalTabs = 10

func (m *Model) recordTerminalToolEvent(event ChatEvent) {
	if !strings.EqualFold(strings.TrimSpace(event.Kind), "tool") {
		return
	}
	textLower := strings.ToLower(strings.TrimSpace(event.Text))
	if !strings.HasPrefix(textLower, "exec ") && textLower != "exec" {
		return
	}
	id := strings.TrimSpace(event.ID)
	if id == "" {
		id = fmt.Sprintf("exec-%d", len(m.terminalTabs)+1)
	}
	cmdStr, outStr, exitCode, hasExit := parseExecToolDetail(event.Detail)
	title := shortCommandLabel(cmdStr)
	if title == "" {
		title = "agent-exec"
	}
	running := strings.Contains(textLower, "started")

	idx := -1
	for i := range m.terminalTabs {
		if m.terminalTabs[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		if len(m.terminalTabs) >= maxAgentTerminalTabs {
			copy(m.terminalTabs, m.terminalTabs[1:])
			m.terminalTabs = m.terminalTabs[:maxAgentTerminalTabs-1]
			if m.viewerTab > m.viewerIndexForExec(0) {
				m.viewerTab--
			}
		}
		m.terminalTabs = append(m.terminalTabs, terminalTab{
			ID:      id,
			Title:   title,
			Command: cmdStr,
			Running: running,
		})
		idx = len(m.terminalTabs) - 1
		// Show the command in the viewer unless the user is typing in a shell.
		if running && m.mode != ModeTerminal && m.mode != ModeInline {
			m.viewerTab = m.viewerIndexForExec(idx)
		}
	}

	tab := &m.terminalTabs[idx]
	if cmdStr != "" {
		tab.Command = cmdStr
		tab.Title = title
	}
	tab.Running = running
	if hasExit {
		tab.ExitCode = exitCode
		tab.HasExit = true
	}
	if outStr != "" || !running {
		tab.Output = outStr
	}
}

func parseExecToolDetail(detail string) (cmdStr, output string, exitCode int, hasExit bool) {
	if strings.TrimSpace(detail) == "" {
		return "", "", 0, false
	}
	lines := strings.Split(detail, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "args:") {
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "args:"))
			var payload struct {
				Argv []string `json:"argv"`
			}
			if json.Unmarshal([]byte(raw), &payload) == nil && len(payload.Argv) > 0 {
				cmdStr = strings.Join(payload.Argv, " ")
			}
		}
		if strings.HasPrefix(trimmed, "result:") {
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "result:"))
			var res struct {
				Argv      []string `json:"argv"`
				ExitCode  int      `json:"exit_code"`
				Stdout    string   `json:"stdout"`
				Stderr    string   `json:"stderr"`
				Cancelled bool     `json:"cancelled"`
			}
			if json.Unmarshal([]byte(raw), &res) == nil {
				if cmdStr == "" && len(res.Argv) > 0 {
					cmdStr = strings.Join(res.Argv, " ")
				}
				exitCode = res.ExitCode
				hasExit = true
				var b strings.Builder
				if res.Stdout != "" {
					b.WriteString(res.Stdout)
					if !strings.HasSuffix(res.Stdout, "\n") {
						b.WriteByte('\n')
					}
				}
				if res.Stderr != "" {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(res.Stderr)
					if !strings.HasSuffix(res.Stderr, "\n") {
						b.WriteByte('\n')
					}
				}
				output = strings.TrimRight(b.String(), "\n")
			} else if raw != "" {
				output = raw
			}
		}
	}
	return cmdStr, output, exitCode, hasExit
}

func shortCommandLabel(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	base := fields[0]
	if idx := strings.LastIndex(base, "/"); idx >= 0 && idx+1 < len(base) {
		base = base[idx+1:]
	}
	if len(fields) > 1 {
		base += " " + fields[1]
	}
	runes := []rune(base)
	if len(runes) > 18 {
		return string(runes[:17]) + "…"
	}
	return base
}

func (m *Model) terminalTabCount() int {
	return 1 + len(m.terminalTabs)
}

func (m *Model) clampTerminalTab() {
	total := m.terminalTabCount()
	if m.terminalTab < 0 {
		m.terminalTab = 0
	}
	if m.terminalTab >= total {
		m.terminalTab = total - 1
	}
}

func (m *Model) switchTerminalTab(delta int) {
	total := m.terminalTabCount()
	if total <= 1 {
		m.terminalTab = 0
		return
	}
	m.terminalTab = (m.terminalTab + delta + total) % total
	m.terminalScroll = 0
	if m.terminalTab != 0 && m.mode == ModeTerminal {
		m.mode = ModeNormal
	}
}

func (m *Model) terminalPanelTitle() string {
	m.clampTerminalTab()
	if len(m.terminalTabs) == 0 {
		return "TERMINAL  ·  PTY"
	}
	var parts []string
	if m.terminalTab == 0 {
		parts = append(parts, "[1:shell ●]")
	} else {
		parts = append(parts, "1:shell")
	}
	for i, tab := range m.terminalTabs {
		idx := i + 2
		status := ""
		if tab.Running {
			status = " ⟳"
		} else if tab.HasExit && tab.ExitCode != 0 {
			status = fmt.Sprintf(" !%d", tab.ExitCode)
		}
		label := fmt.Sprintf("%d:%s%s", idx, sanitizeDisplay(tab.Title), status)
		if m.terminalTab == i+1 {
			label = "[" + label + "]"
		}
		parts = append(parts, label)
	}
	return "TERMINAL  ·  " + strings.Join(parts, "  ") + "  ·  [/] tabs"
}

func (m *Model) renderActiveTerminalTab() string {
	m.clampTerminalTab()
	if m.terminalTab == 0 {
		return m.renderPTYTerminal()
	}
	tab := m.terminalTabs[m.terminalTab-1]
	p := m.colors()
	var b strings.Builder
	cmdLine := "$ " + sanitizeDisplay(valueOr(tab.Command, tab.Title))
	statusBadge := p.accentText("● running")
	if !tab.Running {
		if tab.HasExit && tab.ExitCode != 0 {
			statusBadge = p.dangerText(fmt.Sprintf("✗ exit %d", tab.ExitCode))
		} else {
			statusBadge = p.okText("✓ exit 0")
		}
	}
	b.WriteString(p.cyanText(cmdLine) + "   " + statusBadge + "   " + p.quietText("([ / ] switch tab · j/k scroll · t hide)"))
	b.WriteByte('\n')
	if strings.TrimSpace(tab.Output) == "" {
		if tab.Running {
			b.WriteString(p.mutedText("Running command in workspace…"))
		} else {
			b.WriteString(p.quietText("(no stdout/stderr output)"))
		}
		return b.String()
	}
	allLines := strings.Split(sanitizeDisplay(tab.Output), "\n")
	viewport := maxInt(m.terminalHeight()-2, 1)
	maxOffset := maxInt(len(allLines)-viewport, 0)
	if m.terminalScroll > maxOffset {
		m.terminalScroll = maxOffset
	}
	if m.terminalScroll < 0 {
		m.terminalScroll = 0
	}
	visible := allLines[m.terminalScroll:minInt(m.terminalScroll+viewport, len(allLines))]
	b.WriteString(strings.Join(visible, "\n"))
	return b.String()
}

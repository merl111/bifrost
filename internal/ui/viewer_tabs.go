package ui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type viewerTabHit struct {
	x, w  int
	index int
}

func (m *Model) shellCount() int {
	if m == nil || !m.shellOpen {
		return 0
	}
	return 1 + len(m.extraShells)
}

func (m *Model) viewerTabCount() int {
	if m == nil {
		return 1
	}
	return 1 + m.shellCount() + len(m.terminalTabs)
}

func (m *Model) clampViewerTab() {
	if m.viewerTab < 0 {
		m.viewerTab = 0
	}
	if m.viewerTab >= m.viewerTabCount() {
		m.viewerTab = m.viewerTabCount() - 1
	}
}

func (m *Model) viewerTabIsFile() bool {
	m.clampViewerTab()
	return m.viewerTab == 0
}

func (m *Model) viewerTabIsShell() bool {
	m.clampViewerTab()
	return m.viewerTab > 0 && m.viewerTab <= m.shellCount()
}

func (m *Model) viewerTabIsExec() bool {
	m.clampViewerTab()
	return m.viewerTab > m.shellCount()
}

func (m *Model) execIndex() int {
	if !m.viewerTabIsExec() {
		return -1
	}
	return m.viewerTab - m.shellCount() - 1
}

func (m *Model) viewerIndexForExec(exec int) int {
	if exec < 0 {
		return 0
	}
	return 1 + m.shellCount() + exec
}

func (m *Model) activeShell() *ptySession {
	if m == nil || !m.viewerTabIsShell() {
		return m.pty
	}
	if m.viewerTab == 1 {
		return m.pty
	}
	index := m.viewerTab - 2
	if index < 0 || index >= len(m.extraShells) {
		return m.pty
	}
	return m.extraShells[index]
}

func (m *Model) ownsShell(shell *ptySession) bool {
	if shell == nil {
		return false
	}
	if shell == m.pty {
		return true
	}
	for _, extra := range m.extraShells {
		if extra == shell {
			return true
		}
	}
	return false
}

func (m *Model) resizeShells() {
	if m == nil {
		return
	}
	width, height := m.terminalWidth(), m.terminalHeight()
	if m.pty != nil {
		_ = m.pty.resize(width, height)
	}
	for _, shell := range m.extraShells {
		_ = shell.resize(width, height)
	}
}

func (m *Model) selectViewerTab(index int) {
	if index < 0 || index >= m.viewerTabCount() {
		return
	}
	m.viewerTab = index
	m.terminalScroll = 0
	if !m.viewerTabIsShell() && m.mode == ModeTerminal {
		m.mode = ModeNormal
	}
}

func (m *Model) switchViewerTab(delta int) {
	total := m.viewerTabCount()
	if total <= 1 {
		m.viewerTab = 0
		return
	}
	m.selectViewerTab((m.viewerTab + delta + total) % total)
}

func (m *Model) beginShellInput() {
	if m == nil || !m.viewerTabIsShell() {
		return
	}
	m.focus = focusViewer
	m.chatEditing = false
	m.prefix = false
	m.mode = ModeTerminal
	m.status = "shell"
}

func (m *Model) stopShellInput() {
	if m == nil || m.mode != ModeTerminal {
		return
	}
	m.mode = ModeNormal
	m.prefix = false
}

func (m *Model) leaveTerminalInput() {
	m.stopShellInput()
	if m == nil {
		return
	}
	m.focus = focusChat
	m.chatEditing = true
	m.status = "message"
}

func (m *Model) shellBodyContains(mouse tea.Mouse) bool {
	if m == nil || !m.viewerTabIsShell() || mouse.Y < 3 {
		return false
	}
	if mouse.Y >= 1+m.mainPaneHeight() {
		return false
	}
	x, width := m.viewerContentOrigin()
	return width > 0 && mouse.X >= x && mouse.X < x+width
}

func (m *Model) openShellTab() {
	m.shellOpen = true
	m.viewerTab = 1
	m.focus = focusViewer
	m.stopShellInput()
	m.chatEditing = false
	m.status = "shell"
	m.resizeShells()
}

func (m *Model) addShellTab() tea.Cmd {
	root := m.root
	if m.terminalDir != "" {
		root = m.terminalDir
	}
	var shell *ptySession
	var err error
	if len(m.terminalArgv) > 0 {
		shell, err = startPTYArgv(root, m.terminalWidth(), m.terminalHeight(), m.terminalArgv)
	} else {
		shell, err = startPTY(root, m.terminalWidth(), m.terminalHeight(), m.shell)
	}
	if err != nil {
		m.status = "terminal unavailable: " + err.Error()
		return nil
	}
	m.shellOpen = true
	m.extraShells = append(m.extraShells, shell)
	m.viewerTab = 1 + len(m.extraShells)
	m.focus = focusViewer
	m.stopShellInput()
	m.chatEditing = false
	m.status = fmt.Sprintf("shell tab %d", len(m.extraShells)+1)
	return readPTY(shell)
}

func (m *Model) viewerPaneTitle() string {
	switch {
	case m.accountsOpen:
		return "ACCOUNTS"
	case m.marketplaceOpen:
		return "MARKETPLACE"
	case m.paletteOpen:
		return "COMMAND PALETTE"
	case m.mode == ModeInline:
		return "EDITOR"
	case m.viewerTabIsShell() || m.viewerTabIsExec():
		return "VIEWER  ·  TERMINAL"
	default:
		return "VIEWER"
	}
}

func (m *Model) viewerPaneBody() string {
	if m.accountsOpen || m.marketplaceOpen || m.paletteOpen || m.mode == ModeInline {
		return m.renderViewer()
	}
	strip := m.renderViewerTabStrip()
	var body string
	switch {
	case m.viewerTabIsShell():
		body = m.renderShellTab(m.activeShell())
	case m.viewerTabIsExec():
		body = m.renderExecTab(m.execIndex())
	default:
		body = m.renderViewer()
	}
	return strip + "\n" + body
}

func (m *Model) viewerTabLabel(index int) string {
	if index == 0 {
		switch m.viewPath {
		case "", "tasks":
			return "view"
		case "review":
			return "review"
		default:
			return filepath.Base(m.viewPath)
		}
	}
	if index <= m.shellCount() {
		if index == 1 {
			return "shell"
		}
		return fmt.Sprintf("shell %d", index)
	}
	exec := index - m.shellCount() - 1
	if exec >= 0 && exec < len(m.terminalTabs) {
		return m.terminalTabs[exec].Title
	}
	return "tab"
}

func (m *Model) viewerContentOrigin() (x, width int) {
	if m.width < 60 {
		return 1, maxInt(m.width-2, 1)
	}
	widths := m.paneWidths(m.width)
	order := paneVisualOrder(m.activeLayout())
	cursor := 0
	for i, index := range order {
		if index == 1 && widths[1] > 0 {
			return cursor + 1, maxInt(widths[1]-2, 1)
		}
		cursor += widths[index]
		if i < len(order)-1 {
			cursor++
		}
	}
	return 1, maxInt(m.width-2, 1)
}

func (m *Model) renderViewerTabStrip() string {
	originX, width := m.viewerContentOrigin()
	m.viewerTabHits = m.viewerTabHits[:0]
	m.clampViewerTab()
	p := m.colors()
	var b strings.Builder
	x := originX
	for i := 0; i < m.viewerTabCount(); i++ {
		raw := " " + sanitizeDisplay(m.viewerTabLabel(i)) + " "
		label := raw
		active := i == m.viewerTab
		if !p.enabled() {
			if active {
				label = "[" + strings.TrimSpace(raw) + "]"
			}
		} else if active {
			label = lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(raw)
		} else {
			label = lipgloss.NewStyle().Foreground(p.muted).Background(lipgloss.Color("#1f2437")).Render(raw)
		}
		cellWidth := ansi.StringWidth(label)
		if x > originX && x+cellWidth > originX+width {
			break
		}
		if x > originX {
			b.WriteByte(' ')
			x++
		}
		m.viewerTabHits = append(m.viewerTabHits, viewerTabHit{x: x, w: cellWidth, index: i})
		b.WriteString(label)
		x += cellWidth
	}
	plus := " +"
	if p.enabled() {
		plus = lipgloss.NewStyle().Foreground(p.cyan).Render(" +")
	}
	plusWidth := ansi.StringWidth(plus)
	if x+plusWidth <= originX+width {
		m.viewerTabHits = append(m.viewerTabHits, viewerTabHit{x: x, w: plusWidth, index: -1})
		b.WriteString(plus)
	}
	return b.String()
}

func (m *Model) clickViewerTab(mouse tea.Mouse) (tea.Cmd, bool) {
	if mouse.Button != tea.MouseLeft || m == nil || m.mode == ModeTerminal || mouse.Y != 2 {
		return nil, false
	}
	if m.accountsOpen || m.marketplaceOpen || m.helpOpen || m.paletteOpen || m.mode == ModeInline || m.settingsOpen {
		return nil, false
	}
	for _, hit := range m.viewerTabHits {
		if mouse.X < hit.x || mouse.X >= hit.x+hit.w {
			continue
		}
		m.focus = focusViewer
		if hit.index < 0 {
			return m.addShellTab(), true
		}
		m.selectViewerTab(hit.index)
		m.status = m.viewerTabLabel(hit.index)
		return nil, true
	}
	return nil, false
}

func (m *Model) renderShellTab(shell *ptySession) string {
	if m.ptyErr != "" && (shell == nil || shell == m.pty) {
		return m.colors().dangerText("terminal unavailable: " + m.ptyErr)
	}
	if shell == nil {
		return m.colors().mutedText("Shell is open. Click it, or press i, to type. Esc returns to the message box.")
	}
	text := shell.snapshot()
	if text == "" {
		return m.colors().mutedText("Click here or press i to type. Esc returns to the message box.")
	}
	return text
}

func (m *Model) renderExecTab(index int) string {
	if index < 0 || index >= len(m.terminalTabs) {
		return ""
	}
	tab := m.terminalTabs[index]
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
	b.WriteString(p.cyanText(cmdLine) + "   " + statusBadge + "   " + p.quietText("([ / ] tabs · j/k scroll · t shell)"))
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
	end := minInt(m.terminalScroll+viewport, len(allLines))
	for _, line := range allLines[m.terminalScroll:end] {
		b.WriteByte('\n')
		b.WriteString(line)
	}
	return b.String()
}

package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type chatFoldRow struct {
	line int
	row  int // transcript row, before scrolling
}

func chatActivityKind(kind string) bool {
	switch kind {
	case "tool", "thinking", "reasoning", "summary", "decision", "status":
		return true
	}
	return false
}

func chatFoldable(kind string) bool { return kind == "assistant" || chatActivityKind(kind) }

func foldMark(collapsed bool) string {
	if collapsed {
		return "▸ "
	}
	return "▾ "
}

func (m *Model) renderChatActivity(line *chatLine, width int) []string {
	p := m.colors()
	title := strings.TrimSpace(strings.SplitN(sanitizeDisplay(line.text), "\n", 2)[0])
	if title == "" {
		title = line.kind
	}
	header := ansi.Truncate(foldMark(line.collapsed)+title, width, "…")
	lines := []string{p.mutedText(header)}
	if line.collapsed {
		return lines
	}
	body := line.detail
	if body == "" {
		body = line.text
	}
	if line.kind == "tool" {
		// Output is literal. Tool results may contain Markdown fences themselves.
		for _, row := range wrapChatText(body, maxInt(width-2, 1)) {
			lines = append(lines, ansi.Truncate(p.quietText("│ ")+row, width, "…"))
		}
	} else {
		for _, row := range renderChatMarkdown(body, maxInt(width-2, 1), p) {
			lines = append(lines, ansi.Truncate(p.quietText("│ ")+row, width, "…"))
		}
	}
	return lines
}

func (m *Model) toggleChatFold(index int) {
	if index < 0 || index >= len(m.chatLines) || !chatFoldable(m.chatLines[index].kind) {
		return
	}
	m.chatLines[index].collapsed = !m.chatLines[index].collapsed
	m.chatFoldSelected = index
	m.clampChatScroll()
}

func (m *Model) handleChatFoldKey(key string) bool {
	_, _, visible := m.chatPaneBounds()
	if m.focus != focusChat || m.chatEditing || !visible {
		return false
	}
	switch key {
	case "tab", "shift+tab", "enter", "return", "space":
	default:
		return false
	}
	m.chatTranscriptLines()
	if len(m.chatFolds) == 0 {
		return false
	}
	selected := -1
	for i, fold := range m.chatFolds {
		if fold.line == m.chatFoldSelected {
			selected = i
			break
		}
	}
	if key == "tab" {
		selected = (selected + 1) % len(m.chatFolds)
	} else if key == "shift+tab" {
		if selected < 0 {
			selected = 0
		}
		selected = (selected - 1 + len(m.chatFolds)) % len(m.chatFolds)
	} else {
		if selected < 0 {
			selected = 0
			for i, fold := range m.chatFolds {
				if fold.row >= m.chatScroll {
					selected = i
					break
				}
			}
		}
		m.toggleChatFold(m.chatFolds[selected].line)
		return true
	}
	fold := m.chatFolds[selected]
	m.chatFoldSelected = fold.line
	if fold.row < m.chatScroll || fold.row >= m.chatScroll+m.chatViewport() {
		m.chatScroll = fold.row
		m.clampChatScroll()
	}
	return true
}

func (m *Model) chatPaneBounds() (x, width int, visible bool) {
	if m.settingsOpen || m.accountsOpen || m.marketplaceOpen || m.helpOpen || m.paletteOpen || m.mode != ModeNormal {
		return 0, 0, false
	}
	if m.width < 60 {
		return 0, m.width, m.focus == focusChat
	}
	if m.activeLayout().name == LayoutFocus {
		return 0, m.width, !m.layoutOverlayVisible()
	}
	w := m.paneWidths(m.width)
	if m.activeLayout().name == LayoutConversation {
		return w[0] + 1, w[2], true
	}
	return w[0] + w[1] + 2, w[2], true
}

func (m *Model) handleChatMouse(mouse tea.Mouse, wheel bool) tea.Cmd {
	x, width, visible := m.chatPaneBounds()
	if !visible || mouse.X < x || mouse.X >= x+width || mouse.Y < 1 || mouse.Y >= 1+m.mainPaneHeight() {
		return nil
	}
	if wheel {
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.chatScroll -= 3
		case tea.MouseWheelDown:
			m.chatScroll += 3
		default:
			return nil
		}
		m.clampChatScroll()
		return nil
	}
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	m.focus = focusChat
	startX := x + 1 + len(m.chatMargin())
	innerHeight := maxInt(m.mainPaneHeight()-2, 1)
	topRows := 0
	if m.chatTopBarVisible(innerHeight) {
		topRows = len(m.renderChatTopBar(m.chatPaneInnerWidth()))
	}
	contentRow := mouse.Y - 2
	if contentRow == 0 && topRows > 0 {
		// Clicked on the chat top bar: right third opens quick model switcher, left switches tabs
		relX := mouse.X - (x + 1)
		if relX >= maxInt(m.chatPaneInnerWidth()-36, m.chatPaneInnerWidth()*2/3) {
			if m.chatModelPicker.open {
				m.chatModelPicker.open = false
				return nil
			}
			return m.openChatModelPicker()
		}
		if len(m.chatTabs) > 1 {
			m.chatTab = (m.chatTab + 1) % len(m.chatTabs)
			m.loadActiveChatTab()
		}
		return nil
	}
	if mouse.X < startX || mouse.X >= startX+m.chatReadingWidth() {
		return nil
	}
	m.chatTranscriptLines()
	row := contentRow - topRows
	if row >= 0 && row < m.chatViewport() {
		relCol := maxInt(mouse.X-startX, 0)
		tRow := row + m.chatScroll
		m.chatSelection = chatSelectionState{
			dragging: true,
			active:   false,
			startRow: tRow,
			startCol: relCol,
			endRow:   tRow,
			endCol:   relCol,
		}
		return m.activateChatClickAt(tRow, relCol)
	} else if row >= m.chatViewport() && contentRow < innerHeight {
		m.chatEditing = true
	}
	return nil
}

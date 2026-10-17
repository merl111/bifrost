package ui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
)

type paneDragKind uint8

const (
	paneDragVertical paneDragKind = iota + 1
	paneDragHorizontal
)

// paneDragState contains only the reversible preview state. Persistence is
// deliberately deferred until mouse release so a canceled drag never writes
// a partial layout to the embedding application's settings.
type paneDragState struct {
	kind      paneDragKind
	split     int
	startX    int
	startY    int
	boundaryX int
	boundaryY int
	initial   layoutState
	atBottom  bool
	status    string
}

func (m *Model) paneDragAllowed() bool {
	return m != nil && !m.settingsOpen && m.mode == ModeNormal && !m.layoutOverlayVisible()
}

func paneVisualOrder(layout layoutState) [3]int {
	if layout.name == LayoutConversation {
		return [3]int{0, 2, 1}
	}
	return [3]int{0, 1, 2}
}

func nearPaneBoundary(value, boundary int) bool {
	delta := value - boundary
	return delta >= -1 && delta <= 1
}

// focusPaneAt moves keyboard focus to the workspace pane under a left click.
// Gutter clicks are claimed earlier by startPaneDrag, and a click does not
// enter terminal input mode.
func (m *Model) focusPaneAt(mouse tea.Mouse) bool {
	if mouse.Button != tea.MouseLeft || m == nil || m.mode != ModeNormal {
		return false
	}
	if m.settingsOpen || m.marketplaceOpen || m.paletteOpen || m.projectOpen || m.sessionsOpen || m.projectDetailsOpen {
		return false
	}
	if mouse.Y < 1 {
		return false
	}
	mainBottom := 1 + m.mainPaneHeight()
	if mouse.Y >= mainBottom {
		return false
	}
	if m.width < 60 || (m.activeLayout().name == LayoutFocus && !m.layoutOverlayVisible()) {
		if m.focusVisible(focusChat) {
			m.focus = focusChat
			return true
		}
		return false
	}
	if m.activeLayout().name == LayoutFocus {
		return false
	}
	widths := m.paneWidths(m.width)
	order := paneVisualOrder(m.activeLayout())
	x := 0
	for i, index := range order {
		width := widths[index]
		if width <= 0 {
			continue
		}
		if mouse.X >= x && mouse.X < x+width {
			target := focusFiles
			switch index {
			case 1:
				target = focusViewer
			case 2:
				target = focusChat
			}
			if !m.focusVisible(target) {
				return false
			}
			m.focus = target
			return true
		}
		x += width
		if i < len(order)-1 {
			x++
		}
	}
	return false
}

// startPaneDrag claims a left click on a visible pane gutter or the
// main/terminal border. The caller invokes chat hit testing only when this
// returns false.
func (m *Model) startPaneDrag(mouse tea.Mouse) bool {
	if mouse.Button != tea.MouseLeft || !m.paneDragAllowed() {
		return false
	}

	mainBottom := 1 + m.mainPaneHeight()
	if m.width < 60 || m.activeLayout().name == LayoutFocus || mouse.Y < 1 || mouse.Y >= mainBottom {
		return false
	}
	widths := m.paneWidths(m.width)
	order := paneVisualOrder(m.activeLayout())
	x := 0
	for split := 0; split < len(order)-1; split++ {
		x += widths[order[split]]
		if nearPaneBoundary(mouse.X, x) {
			m.paneDrag = &paneDragState{
				kind:      paneDragVertical,
				split:     split,
				startX:    mouse.X,
				boundaryX: x,
				initial:   m.layout,
				atBottom:  m.chatAtBottom(),
				status:    m.status,
			}
			m.status = "dragging pane split · Esc cancels"
			return true
		}
		x++ // hjoin inserts one cell between panels.
	}
	return false
}

func (m *Model) movePaneDrag(mouse tea.Mouse) {
	drag := m.paneDrag
	if drag == nil {
		return
	}
	previous := m.layout
	if drag.kind == paneDragHorizontal {
		m.layout.terminalPercent = m.paneDragTerminalPercent(mouse.Y)
		m.layout.terminalVisible = true
		if m.layout.terminalPercent == previous.terminalPercent && previous.terminalVisible {
			return
		}
		m.status = fmt.Sprintf("dragging terminal split · %d%% · Esc cancels", m.layout.terminalPercent)
	} else {
		m.paneDragVerticalDelta(drag, mouse.X)
		if m.layout.proportions == previous.proportions {
			return
		}
		m.status = "dragging pane split · Esc cancels"
	}

	m.resizeShells()
	m.paneDragKeepChatPosition(drag.atBottom)
}

func (m *Model) paneDragVerticalDelta(drag *paneDragState, x int) {
	order := paneVisualOrder(drag.initial)
	left, right := order[drag.split], order[drag.split+1]
	initialWidths := m.paneWidthsForLayout(drag.initial)
	pairPixels := initialWidths[left] + initialWidths[right]
	if pairPixels < 1 {
		return
	}
	firstPixels := initialWidths[left] + x - drag.startX
	if firstPixels < 1 {
		firstPixels = 1
	}
	if firstPixels > pairPixels-1 {
		firstPixels = pairPixels - 1
	}
	pairPercent := drag.initial.proportions[left] + drag.initial.proportions[right]
	firstPercent := (firstPixels*pairPercent + pairPixels/2) / pairPixels
	minPercent := maxInt(10, pairPercent-80)
	maxPercent := minInt(80, pairPercent-10)
	firstPercent = maxInt(minInt(firstPercent, maxPercent), minPercent)
	secondPercent := pairPercent - firstPercent
	proportions := drag.initial.proportions
	proportions[left], proportions[right] = firstPercent, secondPercent
	if validProportions(proportions) {
		m.layout.proportions = proportions
	}
}

func (m *Model) paneWidthsForLayout(layout layoutState) [3]int {
	previous := m.layout
	m.layout = layout
	widths := m.paneWidths(m.width)
	m.layout = previous
	return widths
}

func (m *Model) paneDragTerminalPercent(y int) int {
	available := maxInt(m.height-1-boolInt(m.footerVisible()), 1)
	terminalHeight := available - (y - 1)
	percent := (terminalHeight*100 + available/2) / available
	return maxInt(minInt(percent, 60), 10)
}

func (m *Model) paneDragKeepChatPosition(atBottom bool) {
	if atBottom {
		m.chatScroll = m.chatScrollMax()
		return
	}
	m.clampChatScroll()
}

func (m *Model) finishPaneDrag() {
	drag := m.paneDrag
	if drag == nil {
		return
	}
	m.paneDrag = nil
	if m.layout == drag.initial {
		m.status = drag.status
		return
	}
	if err := m.persistLayoutSettings(); err != nil {
		m.layout = drag.initial
		m.paneDragKeepChatPosition(drag.atBottom)
		m.status = "pane resize failed: " + err.Error()
		m.resizeShells()
		return
	}
	m.status = "pane resized"
}

// joinWorkspacePanels is hjoin with one-cell dividers that can expose the
// active draggable split without changing the layout's measured width.
// hjoin's separator is already one cell; keeping that shape preserves the
// existing panel geometry while allowing an active divider to be highlighted.
func (m *Model) joinWorkspacePanels(parts ...string) string {
	if m == nil {
		return hjoin(parts...)
	}
	p := m.colors()
	if !p.enabled() && (m.paneDrag == nil || m.paneDrag.kind != paneDragVertical) {
		return hjoin(parts...)
	}
	defaultSep := " "
	if p.enabled() {
		defaultSep = surfaceText(" ", p.ink, p.chrome)
	}
	separators := make([]string, maxInt(len(parts)-1, 0))
	for i := range separators {
		separators[i] = defaultSep
		if m.paneDrag != nil && m.paneDrag.kind == paneDragVertical && i == m.paneDrag.split {
			if p.enabled() {
				separators[i] = surfaceText(p.cyanText("│"), p.ink, p.chrome)
			} else {
				separators[i] = p.cyanText("│")
			}
		}
	}
	return hjoinWithSeparators(separators, parts...)
}

func (m *Model) cancelPaneDrag() {
	drag := m.paneDrag
	if drag == nil {
		return
	}
	m.paneDrag = nil
	m.layout = drag.initial
	m.paneDragKeepChatPosition(drag.atBottom)
	m.status = drag.status
	m.resizeShells()
}

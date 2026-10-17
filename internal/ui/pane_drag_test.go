package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestPaneDragVerticalPreviewReleasePersistsOnce(t *testing.T) {
	var saved []LayoutSettings
	m := New(t.TempDir(), WithLayoutSettingsChange(func(settings LayoutSettings) error {
		saved = append(saved, settings)
		return nil
	}))
	m.width, m.height = 120, 36
	widths := m.paneWidths(m.width)
	old := m.layout.proportions
	_, _ = m.Update(tea.MouseClickMsg{X: widths[0], Y: 4, Button: tea.MouseLeft})
	if m.paneDrag == nil {
		t.Fatal("vertical gutter did not start drag through Update")
	}
	_, _ = m.Update(tea.MouseMotionMsg{X: widths[0] + 12, Y: 4, Button: tea.MouseLeft})
	if m.layout.proportions == old {
		t.Fatal("vertical drag did not preview a changed split")
	}
	if len(saved) != 0 {
		t.Fatalf("preview persisted early: %d callbacks", len(saved))
	}
	_, _ = m.Update(tea.MouseReleaseMsg{X: widths[0] + 12, Y: 4, Button: tea.MouseLeft})
	if len(saved) != 1 {
		t.Fatalf("release callbacks=%d, want 1", len(saved))
	}
	if saved[0].Files != m.layout.proportions[0] || saved[0].Viewer != m.layout.proportions[1] || saved[0].Chat != m.layout.proportions[2] {
		t.Fatalf("saved layout=%+v current=%v", saved[0], m.layout.proportions)
	}
}

func TestPaneDragVerticalWorksWhenTerminalIsHidden(t *testing.T) {
	m := New(t.TempDir(), WithLayout("review"))
	m.width, m.height = 120, 36
	widths := m.paneWidths(m.width)
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("vertical drag did not start with terminal hidden")
	}
	m.movePaneDrag(tea.Mouse{X: widths[0] + 10, Y: 4, Button: tea.MouseLeft})
	if m.layout.proportions[0] == 16 {
		t.Fatal("vertical preview did not change review layout")
	}
	m.cancelPaneDrag()
}

func TestPaneDragConversationUsesVisualOrder(t *testing.T) {
	m := New(t.TempDir(), WithLayout("conversation"))
	m.width, m.height = 120, 36
	widths := m.paneWidths(m.width)
	old := m.layout.proportions
	secondBoundary := widths[0] + 1 + widths[2]
	if !m.startPaneDrag(tea.Mouse{X: secondBoundary, Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("conversation second gutter did not start drag")
	}
	m.movePaneDrag(tea.Mouse{X: secondBoundary - 10, Y: 4, Button: tea.MouseLeft})
	if m.layout.proportions[0] != old[0] {
		t.Fatalf("files pane changed in conversation drag: old=%d new=%d", old[0], m.layout.proportions[0])
	}
	if m.layout.proportions[1] == old[1] || m.layout.proportions[2] == old[2] {
		t.Fatalf("conversation chat/viewer pair did not change: old=%v new=%v", old, m.layout.proportions)
	}
	m.cancelPaneDrag()
}

func TestPaneDragHorizontalTerminalAndBounds(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 120, 36
	old := m.layout.terminalPercent
	boundary := 1 + m.mainPaneHeight()
	if m.startPaneDrag(tea.Mouse{X: 40, Y: boundary, Button: tea.MouseLeft}) {
		t.Fatal("bottom edge still starts a terminal drag")
	}
	if m.layout.terminalPercent != old {
		t.Fatalf("terminal percent changed without a bottom pane: %d", m.layout.terminalPercent)
	}

	widths := m.paneWidths(m.width)
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("vertical gutter did not start second drag")
	}
	m.movePaneDrag(tea.Mouse{X: -1000, Y: 4, Button: tea.MouseLeft})
	for _, value := range m.layout.proportions {
		if value < 10 || value > 80 {
			t.Fatalf("invalid constrained proportion=%v", m.layout.proportions)
		}
	}
	m.movePaneDrag(tea.Mouse{X: 1000, Y: 4, Button: tea.MouseLeft})
	for _, value := range m.layout.proportions {
		if value < 10 || value > 80 {
			t.Fatalf("invalid constrained proportion=%v", m.layout.proportions)
		}
	}
	m.cancelPaneDrag()
}

func TestPaneDragCancelAndPersistenceFailureRollback(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 120, 36
	widths := m.paneWidths(m.width)
	old := m.layout
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("gutter did not start drag")
	}
	m.movePaneDrag(tea.Mouse{X: widths[0] + 15, Y: 4, Button: tea.MouseLeft})
	m.cancelPaneDrag()
	if m.layout != old || m.paneDrag != nil {
		t.Fatalf("cancel did not restore layout=%+v drag=%v", m.layout, m.paneDrag)
	}

	m = New(t.TempDir(), WithLayoutSettingsChange(func(LayoutSettings) error {
		return errors.New("read only")
	}))
	m.width, m.height = 120, 36
	widths = m.paneWidths(m.width)
	old = m.layout
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("gutter did not start drag")
	}
	m.movePaneDrag(tea.Mouse{X: widths[0] + 15, Y: 4, Button: tea.MouseLeft})
	m.finishPaneDrag()
	if m.layout != old || m.paneDrag != nil {
		t.Fatalf("failed persistence did not rollback layout=%+v drag=%v", m.layout, m.paneDrag)
	}
}

func TestPaneDragClickWithoutMovementDoesNotPersist(t *testing.T) {
	saved := 0
	m := New(t.TempDir(), WithLayoutSettingsChange(func(LayoutSettings) error {
		saved++
		return nil
	}))
	m.width, m.height = 120, 36
	widths := m.paneWidths(m.width)
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("gutter did not start drag")
	}
	m.finishPaneDrag()
	if saved != 0 {
		t.Fatalf("unchanged drag persisted %d times", saved)
	}
}

func TestPaneDragBlockedByNarrowFocusAndOverlays(t *testing.T) {
	for _, tc := range []struct {
		name string
		make func(*Model)
	}{
		{name: "narrow", make: func(m *Model) { m.width, m.height = 50, 24 }},
		{name: "focus", make: func(m *Model) { m.width, m.height = 120, 36; m.layout = mustLayout(LayoutFocus) }},
		{name: "overlay", make: func(m *Model) { m.width, m.height = 120, 36; m.accountsOpen = true }},
		{name: "settings", make: func(m *Model) { m.width, m.height = 120, 36; m.settingsOpen = true }},
		{name: "inline", make: func(m *Model) { m.width, m.height = 120, 36; m.mode = ModeInline }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := New(t.TempDir())
			tc.make(m)
			widths := m.paneWidths(m.width)
			if m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
				t.Fatal("vertical drag started when blocked")
			}
			if m.startPaneDrag(tea.Mouse{X: 4, Y: 2 + m.mainPaneHeight(), Button: tea.MouseLeft}) && tc.name != "narrow" {
				t.Fatal("horizontal drag started when blocked")
			}
		})
	}
}

func TestPaneDragPreservesChatBottomAnchor(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 120, 36
	for i := 0; i < 30; i++ {
		m.chatLines = append(m.chatLines, chatLine{kind: "assistant", text: strings.Repeat("chat content ", 8)})
	}
	m.chatScroll = m.chatScrollMax()
	widths := m.paneWidths(m.width)
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("gutter did not start drag")
	}
	m.movePaneDrag(tea.Mouse{X: widths[0] + 20, Y: 4, Button: tea.MouseLeft})
	if m.chatScroll != m.chatScrollMax() {
		t.Fatalf("chat left bottom while resizing: scroll=%d max=%d", m.chatScroll, m.chatScrollMax())
	}
	m.cancelPaneDrag()
}

func TestMouseClickFocusesPaneAndHeavyBorder(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 120, 40
	m.mode = ModeNormal
	m.focus = focusChat
	widths := m.paneWidths(m.width)

	_, _ = m.Update(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft})
	if m.focus != focusFiles || m.mode != ModeNormal {
		t.Fatalf("files click focus=%v mode=%v", m.focus, m.mode)
	}

	_, _ = m.Update(tea.MouseClickMsg{X: widths[0] + 3, Y: 4, Button: tea.MouseLeft})
	if m.focus != focusViewer {
		t.Fatalf("viewer click focus=%v", m.focus)
	}

	m.layout, _ = layoutFor(string(LayoutConversation))
	widths = m.paneWidths(m.width)
	_, _ = m.Update(tea.MouseClickMsg{X: widths[0] + 3, Y: 4, Button: tea.MouseLeft})
	if m.focus != focusChat {
		t.Fatalf("conversation chat click focus=%v", m.focus)
	}

	m.layout = studioLayout()
	before := m.focus
	y := 1 + m.mainPaneHeight() + 2
	_, _ = m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
	if m.focus != before || m.mode == ModeTerminal || m.terminalPaneHeight() != 0 {
		t.Fatalf("bottom click focus=%v mode=%v height=%d", m.focus, m.mode, m.terminalPaneHeight())
	}

	before = m.focus
	widths = m.paneWidths(m.width)
	if !m.startPaneDrag(tea.Mouse{X: widths[0], Y: 4, Button: tea.MouseLeft}) {
		t.Fatal("gutter did not start drag")
	}
	if m.focus != before {
		t.Fatal("gutter drag changed focus")
	}
	m.cancelPaneDrag()

	focused := renderPanelTheme("midnight", "CHAT", "hi", 24, true, 6)
	unfocused := renderPanelTheme("midnight", "FILES", "hi", 24, false, 6)
	plain := renderPanelTheme("plain", "CHAT", "hi", 24, true, 6)
	if !strings.Contains(focused, "┏") || strings.Contains(unfocused, "┏") || !strings.Contains(plain, "┃") {
		t.Fatalf("focus chrome focused=%q unfocused=%q plain=%q", focused, unfocused, plain)
	}
}

func mustLayout(name LayoutName) layoutState {
	layout, ok := layoutFor(string(name))
	if !ok {
		panic("invalid test layout")
	}
	return layout
}

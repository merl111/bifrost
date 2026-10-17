package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestHelpOpensAsModalOverTheWorkspace(t *testing.T) {
	m := New(t.TempDir(), WithLayout("studio"))
	m.width, m.height = 120, 20
	before := m.focus
	m.Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if !m.helpOpen || m.focus != before {
		t.Fatalf("help open=%v focus=%v", m.helpOpen, m.focus)
	}
	view := ansi.Strip(m.render())
	if !strings.Contains(view, "HELP") || !strings.Contains(view, "Navigate") || !strings.Contains(view, "BIFROST") {
		t.Fatalf("help modal did not sit over the workspace:\n%s", view)
	}
	if m.helpModalW < m.width-8 || m.helpModalH < m.height-4 {
		t.Fatalf("help modal is still small: %dx%d in %dx%d", m.helpModalW, m.helpModalH, m.width, m.height)
	}
	if strings.Contains(view, "VIEWER  ·  HELP") || strings.Count(view, "KEYBOARD SHORTCUTS") > 0 {
		t.Fatal("help replaced the viewer")
	}
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if m.helpScroll == 0 && m.helpLineCount > m.helpInnerRows {
		t.Fatal("help did not scroll")
	}
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.helpOpen {
		t.Fatal("escape left help open")
	}
	closed := ansi.Strip(m.render())
	if strings.Contains(closed, "\nNavigate\n") {
		t.Fatal("help stayed on screen after close")
	}
}

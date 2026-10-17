package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChatFoldMouseAcrossLayouts(t *testing.T) {
	for _, layout := range []string{"studio", "conversation", "review", "focus"} {
		for _, width := range []int{50, 120, 160} {
			m := New(t.TempDir(), WithLayout(layout))
			m.width, m.height, m.focus = width, 36, focusChat
			m.appendChatEvent(ChatEvent{Kind: "tool", ID: "call-1", Text: "read_file finished", Detail: "package example\n\nfunc main() {}"})
			m.render()
			if !m.chatLines[0].collapsed || strings.Contains(ansi.Strip(m.renderChat()), "package example") {
				t.Fatal("tool details were not collapsed")
			}
			x, _, _ := m.chatPaneBounds()
			click := tea.MouseClickMsg{X: x + 1 + len(m.chatMargin()), Y: 2, Button: tea.MouseLeft}
			m.Update(click)
			if m.chatLines[0].collapsed || !strings.Contains(ansi.Strip(m.renderChat()), "package example") {
				t.Fatalf("click did not expand tool in %s at width %d", layout, width)
			}
			m.Update(click)
			if !m.chatLines[0].collapsed {
				t.Fatal("second click did not collapse")
			}
			m.paletteOpen = true
			m.Update(click)
			if !m.chatLines[0].collapsed {
				t.Fatal("overlay click changed hidden fold")
			}
		}
	}
}

func TestChatFoldTracksUpdatesScrollAndKeyboard(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.width, m.height, m.focus = 80, 18, focusChat
	m.appendChatEvent(ChatEvent{Kind: "tool", ID: "call-1", Text: "exec started", Detail: "go test ./..."})
	m.handleNormalKey("tab", "")
	m.handleNormalKey("enter", "")
	if m.chatLines[0].collapsed {
		t.Fatal("keyboard did not open selected fold")
	}
	m.appendChatEvent(ChatEvent{Kind: "tool", ID: "call-1", Text: "exec finished", Detail: strings.Repeat("test passed\n", 30)})
	if len(m.chatLines) != 1 || m.chatLines[0].collapsed {
		t.Fatal("tool update duplicated the call or discarded fold state")
	}
	m.Update(tea.MouseWheelMsg{X: 40, Y: 4, Button: tea.MouseWheelDown})
	if m.chatScroll != 3 {
		t.Fatalf("wheel scroll=%d", m.chatScroll)
	}
	m.appendChatEvent(ChatEvent{Kind: "thinking", ID: "thought-1", Text: "Thinking summary", Detail: "Compare the two approaches."})
	m.handleNormalKey("tab", "")
	if m.chatFoldSelected != 1 {
		t.Fatal("Tab did not select thinking fold")
	}
	m.handleNormalKey("space", "")
	if m.chatLines[1].collapsed {
		t.Fatal("Space did not expand thinking")
	}
	m.appendChatEvent(ChatEvent{Kind: "user", Text: "Another turn"})
	m.appendChatEvent(ChatEvent{Kind: "thinking", ID: "thought-1", Text: "Thinking summary", Detail: "A new decision."})
	if len(m.chatLines) != 4 || m.chatLines[1].detail != "Compare the two approaches." {
		t.Fatal("reused event ID overwrote a previous turn")
	}
}

func TestChatFoldScrolledHitTesting(t *testing.T) {
	m := New(t.TempDir(), WithLayout("conversation"))
	m.width, m.height, m.focus = 120, 36, focusChat
	for i := 0; i < 40; i++ {
		m.appendChatEvent(ChatEvent{Kind: "summary", Text: "Summary", Detail: "Expanded contents"})
	}
	m.chatScroll = 10
	m.render()
	x, _, _ := m.chatPaneBounds()
	m.Update(tea.MouseClickMsg{X: x + 1 + len(m.chatMargin()), Y: 2, Button: tea.MouseLeft})
	if m.chatLines[10].collapsed || !m.chatLines[0].collapsed {
		t.Fatal("scrolled click toggled wrong message")
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse events are not enabled")
	}
}

func TestChatComposerPaste(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.focus, m.chatEditing = focusChat, true
	m.Update(tea.PasteMsg{Content: "hello 👩🏽‍💻\r\nnext\x1b[31m"})
	if m.chatInput != "hello 👩🏽‍💻\nnext" {
		t.Fatalf("paste lost multiline content: %q", m.chatInput)
	}
	for _, emoji := range []string{"👩🏽‍💻", "❤️", "🇦🇹"} {
		m.chatInput = "hello " + emoji
		m.chatInputKey("backspace", "", 0)
		if m.chatInput != "hello " {
			t.Fatalf("backspace split emoji %q: %q", emoji, m.chatInput)
		}
	}
}

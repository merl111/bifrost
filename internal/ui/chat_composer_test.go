package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChatComposerPrimaryAndFallbackNewlines(t *testing.T) {
	tests := []struct {
		name string
		opt  Option
		key  string
		mod  tea.KeyMod
	}{
		{name: "shift primary", key: "shift+enter", mod: tea.ModShift},
		{name: "alt fallback", key: "alt+enter", mod: tea.ModAlt},
		{name: "ctrl j fallback", key: "ctrl+j", mod: tea.ModCtrl},
		{name: "alt modifier fallback", opt: WithComposer(8, "ctrl+j"), key: "enter", mod: tea.ModAlt},
		{name: "ctrl modifier fallback", opt: WithComposer(8, "alt+enter"), key: "j", mod: tea.ModCtrl},
		{name: "configured alt primary", opt: WithComposer(8, "alt+enter"), key: "enter", mod: tea.ModAlt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(t.TempDir(), tt.opt)
			m.chatEditing = true
			m.chatInput = "before"
			if cmd := m.chatInputKey(tt.key, "", tt.mod); cmd != nil {
				t.Fatal("newline unexpectedly submitted the prompt")
			}
			if m.chatInput != "before\n" {
				t.Fatalf("newline input = %q", m.chatInput)
			}
		})
	}
}

func TestChatComposerConfiguredPrimaryLeavesEnterAsSubmit(t *testing.T) {
	m := New(t.TempDir(), WithComposer(8, "alt+enter"), WithChatRunner(func(context.Context, string, func(ChatEvent)) error {
		return nil
	}))
	m.chatEditing = true
	m.chatInput = "send me"
	if cmd := m.chatInputKey("enter", "", 0); cmd == nil || !m.chatRunning {
		t.Fatal("plain Enter did not submit with Alt+Enter configured")
	}
}

func TestChatComposerGrowsForWrappingAndExplicitNewlines(t *testing.T) {
	m := New(t.TempDir(), WithComposer(8, "shift+enter"))
	m.width, m.height, m.chatEditing = 48, 24, true
	baseRows := len(m.chatFooterLines())
	m.chatInput = strings.Repeat("wrapped text ", 20)
	wrappedRows := len(m.chatFooterLines())
	if wrappedRows <= baseRows {
		t.Fatalf("wrapped composer did not grow: base=%d wrapped=%d", baseRows, wrappedRows)
	}
	m.chatInput = "first line\n\nthird line"
	view := ansi.Strip(strings.Join(m.chatFooterLines(), "\n"))
	if !strings.Contains(view, "first line") || !strings.Contains(view, "third line▏") {
		t.Fatalf("explicit lines or cursor missing: %q", view)
	}
	for _, row := range m.chatFooterLines() {
		if got := ansi.StringWidth(row); got > m.chatPaneInnerWidth() {
			t.Fatalf("composer row overflow: width=%d inner=%d row=%q", got, m.chatPaneInnerWidth(), row)
		}
	}
}

func TestChatComposerMaxRowsAndBottomAnchor(t *testing.T) {
	m := New(t.TempDir(), WithComposer(3, "shift+enter"))
	m.width, m.height, m.chatEditing = 48, 18, true
	for i := 0; i < 40; i++ {
		m.chatLines = append(m.chatLines, chatLine{kind: "assistant", text: "history"})
	}
	m.chatScroll = m.chatScrollMax()
	m.chatInput = strings.Repeat("line ", 3)
	m.chatInputChanged(true)
	footer := m.chatFooterLines()
	if len(footer) > maxInt(m.mainPaneHeight()-3, 1) {
		t.Fatalf("composer exceeded pane: rows=%d limit=%d", len(footer), maxInt(m.mainPaneHeight()-3, 1))
	}
	if m.chatScroll != m.chatScrollMax() {
		t.Fatalf("bottom anchor moved: scroll=%d max=%d", m.chatScroll, m.chatScrollMax())
	}

	// A reader who scrolled up keeps their position while the composer grows.
	m.chatScroll = maxInt(m.chatScrollMax()-2, 0)
	old := m.chatScroll
	m.chatInput = strings.Repeat("line ", 100)
	m.chatInputChanged(false)
	if m.chatScroll != old {
		t.Fatalf("reader position changed: got=%d want=%d", m.chatScroll, old)
	}
}

func TestChatComposerShiftEnterOnVTETerminalsAndKeyPressMsg(t *testing.T) {
	submitted := false
	m := New(
		t.TempDir(),
		WithShiftDetector(func() bool { return true }),
		WithChatRunner(func(context.Context, string, func(ChatEvent)) error {
			submitted = true
			return nil
		}),
	)
	m.focus, m.chatEditing = focusChat, true
	m.chatInput = "line one"

	// Simulate VTE / GNOME Console (kgx) sending an unmodified Enter key press
	// while the physical Shift key is held down on the keyboard.
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil || submitted {
		t.Fatal("Shift+Enter on VTE terminal submitted the prompt instead of inserting a newline")
	}
	if m.chatInput != "line one\n" {
		t.Fatalf("chatInput = %q, want %q", m.chatInput, "line one\n")
	}

	// Simulate a terminal where Key.Text is set alongside ModShift (where
	// Key.String() would return Text instead of "shift+enter").
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift, Text: "\r"})
	if cmd != nil || submitted {
		t.Fatal("KeyPressMsg with ModShift and Text submitted the prompt")
	}
	if m.chatInput != "line one\n\n" {
		t.Fatalf("chatInput = %q, want %q", m.chatInput, "line one\n\n")
	}

	// Simulate NumLock / CapsLock active with Shift+Enter.
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift | tea.ModNumLock})
	if cmd != nil || submitted {
		t.Fatal("Shift+Enter with NumLock submitted the prompt")
	}
	if m.chatInput != "line one\n\n\n" {
		t.Fatalf("chatInput = %q, want %q", m.chatInput, "line one\n\n\n")
	}

	// When Shift is released, plain Enter submits the message.
	m.shiftHeld = func() bool { return false }
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !m.chatRunning {
		t.Fatal("plain Enter without Shift did not submit the prompt")
	}
}

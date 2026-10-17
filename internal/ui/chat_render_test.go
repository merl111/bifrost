package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRichChatPresentationAndCache(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.width, m.height = 120, 36
	source := "# Ready 🧪\n\nA **clear** answer with `code`.\n\n```diff\n+done\n-old\n```"
	m.appendChatEvent(ChatEvent{Kind: "assistant", Text: source})
	first := strings.Join(m.chatTranscriptLines(), "\n")
	cache := m.chatLines[0].rendered
	plain := ansi.Strip(first)
	for _, want := range []string{"Ready 🧪", "A clear answer with code.", "+done", "-old"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("render missing %q: %s", want, plain)
		}
	}
	for _, marker := range []string{"# Ready", "**clear**", "```", "assistant:"} {
		if strings.Contains(plain, marker) {
			t.Fatalf("raw markup leaked: %s", plain)
		}
	}
	if m.chatLines[0].text != source {
		t.Fatal("presentation mutated the portable conversation")
	}
	m.chatTranscriptLines()
	if m.chatLines[0].rendered != cache {
		t.Fatal("unchanged message was reparsed")
	}
	m.width = 50
	m.chatTranscriptLines()
	if m.chatLines[0].rendered == cache {
		t.Fatal("resize reused stale wrapping")
	}
	cache = m.chatLines[0].rendered
	m.theme = "plain"
	if view := m.renderChat(); strings.Contains(view, "\x1b") || m.chatLines[0].rendered == cache {
		t.Fatal("theme change reused styled cache")
	}
}

func TestComposerKeepsSpaceAboveTheMessageBox(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.width, m.height, m.chatEditing = 120, 36, true
	m.appendChatEvent(ChatEvent{Kind: "assistant", Text: "Which repository should I test?"})
	footer := m.chatFooterLines()
	if len(footer) < 3 {
		t.Fatalf("footer = %#v", footer)
	}
	for i := 0; i < 2; i++ {
		if strings.TrimSpace(ansi.Strip(footer[i])) != "" {
			t.Fatalf("gap row %d = %q", i, ansi.Strip(footer[i]))
		}
	}
	box := ansi.Strip(footer[2])
	if !strings.Contains(box, "Message") {
		t.Fatalf("composer starts at %q", box)
	}
}

func TestComposerShowsMultilineEmojiAndEndOfLongPrompt(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.width, m.height, m.chatEditing = 80, 24, true
	m.chatInput = "First 👩🏽‍💻\nSecond ❤️\nLast 🧪"
	view := ansi.Strip(strings.Join(m.chatFooterLines(), "\n"))
	for _, want := range []string{"First 👩🏽‍💻", "Second ❤️", "Last 🧪▏"} {
		if !strings.Contains(view, want) {
			t.Fatalf("multiline composer lost %q: %s", want, view)
		}
	}
	m.chatInput = strings.Repeat("long prompt\n", 30) + "visible tail"
	if view = ansi.Strip(strings.Join(m.chatFooterLines(), "\n")); !strings.Contains(view, "visible tail▏") {
		t.Fatal("composer hid the insertion point")
	}
	for _, size := range [][2]int{{1, 4}, {20, 8}, {80, 24}, {160, 48}} {
		m.width, m.height = size[0], size[1]
		for _, row := range m.chatFooterLines() {
			if ansi.StringWidth(row) > m.chatPaneInnerWidth() {
				t.Fatalf("composer overflow at %v: %q", size, row)
			}
		}
	}
}

func TestChatCompletionKeepsHistoryPosition(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.width, m.height = 80, 18
	m.chatLines = []chatLine{{kind: "assistant", text: strings.Repeat("paragraph\n\n", 80)}}
	m.chatRunning, m.chatID, m.chatStream = true, 7, &chatStream{}
	m.chatScroll = 2
	m.handleChatStream(chatStreamMsg{id: 7, stream: m.chatStream, item: chatStreamItem{done: true}})
	if m.chatScroll != 2 {
		t.Fatalf("completion jumped away from history: %d", m.chatScroll)
	}
	m.chatScroll = m.chatScrollMax()
	m.updateWindowSize(tea.WindowSizeMsg{Width: 40, Height: 12})
	if m.chatScroll != m.chatScrollMax() {
		t.Fatal("resize lost bottom anchoring")
	}
}

func TestStreamingProviderLabelsDoNotRewriteHistory(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"))
	m.appendChatEvent(ChatEvent{Kind: "assistant", Text: "Imported history"})
	m.appendChatEvent(ChatEvent{Kind: "user", Text: "Next turn"})
	m.appendChatEvent(ChatEvent{Kind: "connection", Connection: &ConnectionInfo{Provider: "claude", Model: "sonnet"}})
	m.appendChatEvent(ChatEvent{Kind: "delta", Text: "New reply"})
	m.appendChatEvent(ChatEvent{Kind: "connection", Connection: &ConnectionInfo{Provider: "openai", Model: "gpt"}})
	if m.chatLines[0].provider != "" || m.chatLines[2].provider != "claude" {
		t.Fatal("changing accounts reassigned message authorship")
	}
	view := ansi.Strip(strings.Join(m.chatTranscriptLines(), "\n"))
	if !strings.Contains(view, "Bifrost") || !strings.Contains(view, "Claude") {
		t.Fatalf("missing message identity: %s", view)
	}
}

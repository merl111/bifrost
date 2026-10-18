package ui

import (
	"bifrost/internal/agent"
	"bifrost/internal/provider"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestChatComposerStreamsAndDeduplicatesFinal(t *testing.T) {
	runner := ChatRunner(func(_ context.Context, prompt string, emit func(ChatEvent)) error {
		if prompt != "hello" {
			t.Errorf("prompt = %q", prompt)
		}
		emit(ChatEvent{Kind: "tool", Text: "lookup"})
		emit(ChatEvent{Kind: "delta", Text: "hel"})
		emit(ChatEvent{Kind: "delta", Text: "lo"})
		emit(ChatEvent{Kind: "final", Text: "hello"})
		return nil
	})
	m := New(t.TempDir(), WithChatRunner(runner))
	m.width, m.height = 80, 16
	m.handleNormalKey("c", "")
	if m.focus != focusChat || !m.chatEditing {
		t.Fatalf("compose focus = %v editing=%v", m.focus, m.chatEditing)
	}
	m.chatInputKey("h", "h", 0)
	m.chatInputKey("e", "e", 0)
	m.chatInputKey("l", "l", 0)
	m.chatInputKey("l", "l", 0)
	m.chatInputKey("o", "o", 0)
	cmd := m.chatInputKey("enter", "", 0)
	for cmd != nil {
		msg := cmd()
		var next tea.Cmd
		_, next = m.Update(msg)
		cmd = next
		if !m.chatRunning {
			break
		}
	}
	if m.chatRunning || len(m.chatLines) != 3 {
		t.Fatalf("chat state running=%v lines=%#v", m.chatRunning, m.chatLines)
	}
	if got := m.chatLines[2].text; got != "hello" {
		t.Fatalf("final text = %q", got)
	}
	if m.focus != focusChat || !m.chatEditing {
		t.Fatalf("composer lost focus after send: focus=%v editing=%v", m.focus, m.chatEditing)
	}
	visible := ansi.Strip(strings.Join(m.chatTranscriptLines(), "\n"))
	if strings.Count(visible, "hello") != 2 { // user prompt and one assistant reply
		t.Fatalf("chat did not render a single reply: %q", visible)
	}
}

func TestChatHistoryPreservesOrderAndSanitizes(t *testing.T) {
	history := []ChatEvent{
		{Kind: "user", Text: "first\x1b[31m secret\x1b[0m"},
		{Kind: "assistant", Text: "second\nline"},
		{Kind: "tool", Text: "read_file finished"},
	}
	m := New(t.TempDir(), WithChatHistory(history))
	history[0].Text = "mutated"
	if len(m.chatLines) != len(history) {
		t.Fatalf("history lines = %#v", m.chatLines)
	}
	if m.chatLines[0].kind != "user" || m.chatLines[0].text != "first secret" {
		t.Fatalf("first history line = %#v", m.chatLines[0])
	}
	if m.chatLines[1].kind != "assistant" || m.chatLines[1].text != "second\nline" || m.chatLines[2].kind != "tool" {
		t.Fatalf("history order = %#v", m.chatLines)
	}
	if m.chatScroll != m.chatScrollMax() {
		t.Fatalf("history scroll = %d max=%d", m.chatScroll, m.chatScrollMax())
	}
}

func TestChatHistoryIsBoundedAndWraps(t *testing.T) {
	events := make([]ChatEvent, 0, maxChatLines+40)
	for i := 0; i < maxChatLines+40; i++ {
		events = append(events, ChatEvent{Kind: "status", Text: fmt.Sprintf("line-%d", i)})
	}
	events = append(events, ChatEvent{Kind: "assistant", Text: strings.Repeat("wide 世界 ", maxChatRunes)})
	events = append(events, ChatEvent{Kind: "assistant", Text: "tail"})
	m := New(t.TempDir(), WithChatHistory(events))
	if len(m.chatLines) > maxChatLines || chatRuneCount(m.chatLines) > maxChatRunes {
		t.Fatalf("history limits lines=%d runes=%d", len(m.chatLines), chatRuneCount(m.chatLines))
	}
	if m.chatLines[len(m.chatLines)-1].text != "tail" {
		t.Fatalf("bounded history lost newest event: %#v", m.chatLines[len(m.chatLines)-1])
	}
	m.width, m.height = 40, 12
	for i, line := range strings.Split(m.renderChat(), "\n") {
		if got := ansi.StringWidth(line); got > m.chatPaneInnerWidth() {
			t.Fatalf("wrapped history line %d width=%d inner=%d", i, got, m.chatPaneInnerWidth())
		}
	}
}

func TestChatComposerNewlineAndCancel(t *testing.T) {
	canceled := make(chan struct{})
	runner := ChatRunner(func(ctx context.Context, _ string, _ func(ChatEvent)) error {
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	})
	m := New(t.TempDir(), WithChatRunner(runner))
	m.handleNormalKey("ctrl+w", "")
	m.handleNormalKey("l", "")
	m.handleNormalKey("i", "")
	m.chatInputKey("a", "a", 0)
	m.chatInputKey("alt+enter", "", tea.ModAlt)
	m.chatInputKey("b", "b", 0)
	if m.chatInput != "a\nb" {
		t.Fatalf("multiline input = %q", m.chatInput)
	}
	cmd := m.chatInputKey("enter", "", 0)
	if cmd == nil || !m.chatRunning {
		t.Fatal("chat did not start")
	}
	m.chatInputKey("ctrl+c", "", tea.ModCtrl)
	if m.chatRunning || m.status != "chat canceled" {
		t.Fatalf("cancel state running=%v status=%q", m.chatRunning, m.status)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}
}

func TestReviewLoaderUpdatesViewerAndEscapesOutput(t *testing.T) {
	m := New(t.TempDir(), WithReviewLoader(func(context.Context) (string, error) {
		return "status\n\x1b[31msecret\x1b[0m", nil
	}))
	cmd := m.handleNormalKey("r", "")
	if cmd == nil || m.status != "loading review" {
		t.Fatalf("review command=%v status=%q", cmd != nil, m.status)
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if m.focus != focusViewer || m.viewPath != "review" || m.viewerOffset != 0 {
		t.Fatalf("review state focus=%v path=%q offset=%d", m.focus, m.viewPath, m.viewerOffset)
	}
	if strings.Contains(m.renderViewer(), "\x1b") {
		t.Fatal("review renderer exposed escape sequence")
	}

	failed := New(t.TempDir(), WithReviewLoader(func(context.Context) (string, error) {
		return "", errors.New("git unavailable")
	}))
	cmd = failed.handleNormalKey("r", "")
	_, _ = failed.Update(cmd())
	if !strings.Contains(failed.status, "review failed") {
		t.Fatalf("review error status = %q", failed.status)
	}
}

func TestChatRunnerAgentProviderSSEIntegration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("provider method = %s", r.Method)
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("provider request: %v", err)
		}
		if !request.Stream {
			t.Fatal("agent did not use streaming provider")
		}
		if requests.Add(1) == 1 {
			writeTestSSE(w, `{"choices":[{"index":0,"delta":{"role":"assistant","content":"before "}}]}`)
			writeTestSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_","type":"function","function":{"name":"search","arguments":"{\"query\":"}}]}}]}`)
			writeTestSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hello\"}"}}]}}]}`)
			writeTestSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
			writeTestSSE(w, "[DONE]")
			return
		}
		writeTestSSE(w, `{"choices":[{"index":0,"delta":{"role":"assistant","content":"after"}}]}`)
		writeTestSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`)
		writeTestSSE(w, "[DONE]")
	}))
	defer server.Close()
	client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: server.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	state := t.TempDir()
	runner := ChatRunner(func(ctx context.Context, prompt string, emit func(ChatEvent)) error {
		_, runErr := agent.Run(ctx, agent.Config{
			Provider: client,
			Root:     root,
			StateDir: state,
			OnEvent:  func(event agent.Event) { emit(ChatEvent{Kind: event.Kind, Text: event.Text}) },
		}, prompt)
		return runErr
	})
	m := New(root, WithChatRunner(runner))
	m.width, m.height = 100, 20
	m.handleNormalKey("c", "")
	for _, r := range "hello" {
		text := string(r)
		m.chatInputKey(text, text, 0)
	}
	cmd := m.chatInputKey("enter", "", 0)
	for cmd != nil {
		msg := cmd()
		_, cmd = m.Update(msg)
		if !m.chatRunning {
			break
		}
	}
	if m.chatRunning {
		t.Fatal("chat runner did not complete")
	}
	if requests.Load() != 2 {
		t.Fatalf("provider requests = %d, want tool loop of 2; status=%q lines=%#v", requests.Load(), m.status, m.chatLines)
	}
	joined := ansi.Strip(strings.Join(m.chatTranscriptLines(), "\n"))
	for _, want := range []string{"before", "after", "search started", "search finished"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("chat view missing %q: %q", want, joined)
		}
	}
}

func TestChatCloseCancelsBlockedCallback(t *testing.T) {
	entered := make(chan struct{})
	returned := make(chan struct{})
	runner := ChatRunner(func(ctx context.Context, _ string, emit func(ChatEvent)) error {
		close(entered)
		for i := 0; i < maxChatLines*2; i++ {
			emit(ChatEvent{Kind: "delta", Text: fmt.Sprintf("line-%d", i)})
		}
		<-ctx.Done()
		close(returned)
		return ctx.Err()
	})
	m := New(t.TempDir(), WithChatRunner(runner))
	m.handleNormalKey("c", "")
	m.chatInput = "cancel me"
	if cmd := m.submitChat(); cmd == nil {
		t.Fatal("chat did not start")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runner did not start")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("chat callback remained blocked after Close")
	}
}

func TestChatWrapsToPaneCellsAndAnchorsScroll(t *testing.T) {
	m := New(t.TempDir(), WithChatRunner(func(context.Context, string, func(ChatEvent)) error { return nil }))
	m.width, m.height = 40, 12
	m.chatLines = []chatLine{{kind: "assistant", text: strings.Repeat("wide 世界 paragraph ", 12)}}
	wrapped := strings.Split(m.renderChat(), "\n")
	for i, line := range wrapped {
		if got := ansi.StringWidth(line); got > m.chatPaneInnerWidth() {
			t.Fatalf("chat line %d width=%d inner=%d: %q", i, got, m.chatPaneInnerWidth(), line)
		}
	}
	if len(wrapped) < 3 {
		t.Fatalf("long chat paragraph was not wrapped: %d rows", len(wrapped))
	}
	m.chatEditing = true
	m.chatInput = strings.Repeat("compose 世界 ", 200)
	footer := m.chatFooterLines()
	if len(footer) > maxInt(m.mainPaneHeight()-3, 1) {
		t.Fatalf("composer footer exceeded pane: %d rows", len(footer))
	}
	for i, line := range strings.Split(m.renderChat(), "\n") {
		if got := ansi.StringWidth(line); got > m.chatPaneInnerWidth() {
			t.Fatalf("composer line %d width=%d inner=%d", i, got, m.chatPaneInnerWidth())
		}
	}
	m.chatEditing = false

	m.chatLines = nil
	for i := 0; i < 20; i++ {
		m.chatLines = append(m.chatLines, chatLine{kind: "status", text: fmt.Sprintf("line-%d", i)})
	}
	m.chatRunning = true
	m.chatID = 1
	m.chatStream = &chatStream{}
	m.chatScroll = 0
	m.handleChatStream(chatStreamMsg{id: 1, stream: m.chatStream, item: chatStreamItem{event: ChatEvent{Kind: "delta", Text: "new"}}})
	if m.chatScroll != 0 {
		t.Fatalf("user scroll moved while reading history: %d", m.chatScroll)
	}
	m.chatScroll = m.chatScrollMax()
	oldMax := m.chatScroll
	m.handleChatStream(chatStreamMsg{id: 1, stream: m.chatStream, item: chatStreamItem{event: ChatEvent{Kind: "tool", Text: "bottom"}}})
	if m.chatScroll <= oldMax {
		t.Fatalf("bottom scroll did not follow new delta: old=%d new=%d", oldMax, m.chatScroll)
	}
}

func writeTestSSE(w http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func TestNormalizedCursorEventsKeepTUIRepliesAndActivity(t *testing.T) {
	m := New(t.TempDir())
	for _, ev := range []ChatEvent{
		{Kind: "user", Text: "Explain the flow"},
		{Kind: "thinking", ID: "thinking-1", Text: "Read "},
		{Kind: "thinking", ID: "thinking-1", Text: "Read the entry point."},
		{Kind: "delta", Text: "I will inspect it."},
		{Kind: "tool", ID: "read-1", Text: "Running · read · main.go"},
		{Kind: "tool", ID: "read-1", Text: "Completed · read · main.go", Detail: "package main"},
		{Kind: "delta", Text: "Here is the explanation."},
	} {
		m.appendChatEvent(ev)
	}
	if len(m.chatLines) != 5 {
		t.Fatalf("duplicated normalized activity: %+v", m.chatLines)
	}
	if m.chatLines[1].text != "Read the entry point." || m.chatLines[2].text != "I will inspect it." || m.chatLines[4].text != "Here is the explanation." {
		t.Fatalf("lost streamed text: %+v", m.chatLines)
	}
}

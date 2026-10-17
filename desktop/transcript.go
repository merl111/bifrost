package main

import (
	"strings"
	"unicode/utf8"

	"bifrost/internal/ui"
)

const maxMessageBytes = 32 << 10
const maxVisibleMessages = 256

type message struct {
	ID                         uint64
	Author, Text               string
	Raw, Kind, EventID, Detail string
	Collapsed                  bool
}
type transcript struct {
	messages  []message
	next      uint64
	streaming uint64
}

// Only the presentation is bounded. Complete history remains in the shared
// session store and can be resumed from either frontend.
func safeText(text string) string {
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", "�"), "�")
	if len(text) <= maxMessageBytes {
		return text
	}
	n := maxMessageBytes
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n] + "\n… Display truncated; full content remains in the workspace/session."
}

func (t *transcript) append(author, text string) {
	t.next++
	t.messages = append(t.messages, message{ID: t.next, Author: author, Text: safeText(text), Raw: cleanText(text)})
	if len(t.messages) > maxVisibleMessages {
		t.messages = append([]message(nil), t.messages[len(t.messages)-maxVisibleMessages:]...)
	}
}

func (t *transcript) apply(e ui.ChatEvent) {
	defer t.trim()
	switch e.Kind {
	case "user":
		t.streaming = 0
		t.append("You", e.Text)
	case "assistant":
		t.streaming = 0
		t.append("Bifrost", e.Text)
	case "tool", "thinking", "reasoning", "summary", "decision", "status":
		// A tool execution separates model steps. Status and reasoning updates
		// can arrive between tokens of the same reply and must not split it.
		if e.Kind == "tool" {
			t.streaming = 0
		}
		for i := len(t.messages) - 1; i >= 0; i-- {
			m := &t.messages[i]
			if m.Author == "You" {
				break
			}
			if e.ID != "" && m.EventID == e.ID && m.Kind == e.Kind {
				m.Text = safeText(e.Text)
				m.Raw = cleanText(e.Text)
				if e.Detail != "" {
					m.Detail = cleanText(e.Detail)
				}
				return
			}
		}
		t.append(e.Kind, e.Text)
		m := &t.messages[len(t.messages)-1]
		m.Kind = e.Kind
		m.EventID = e.ID
		m.Detail = cleanText(e.Detail)
		m.Collapsed = true
	case "delta":
		i := t.streamingIndex()
		if i < 0 {
			t.append("Bifrost", "")
			t.streaming = t.next
			i = len(t.messages) - 1
		}
		t.messages[i].Raw = cleanText(t.messages[i].Raw + e.Text)
		t.messages[i].Text = safeText(t.messages[i].Raw)
	case "final":
		if i := t.streamingIndex(); i >= 0 {
			if e.Text != "" {
				t.messages[i].Text = safeText(e.Text)
				t.messages[i].Raw = cleanText(e.Text)
			}
		} else if e.Text != "" {
			t.append("Bifrost", e.Text)
		}
		t.streaming = 0
	case "error":
		t.streaming = 0
		t.append("Needs attention", e.Text)
	}
}

func (t *transcript) streamingIndex() int {
	if t.streaming != 0 {
		for i := len(t.messages) - 1; i >= 0; i-- {
			if t.messages[i].ID == t.streaming {
				return i
			}
		}
	}
	return -1
}

func cleanText(text string) string {
	text = strings.ToValidUTF8(strings.ReplaceAll(text, "\x00", "�"), "�")
	if len(text) > 2<<20 {
		text = text[:2<<20]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

func (t *transcript) trim() {
	total := 0
	for _, m := range t.messages {
		total += utf8.RuneCountInString(m.Raw) + utf8.RuneCountInString(m.Detail)
	}
	for total > 128<<10 && len(t.messages) > 1 {
		m := t.messages[0]
		total -= utf8.RuneCountInString(m.Raw) + utf8.RuneCountInString(m.Detail)
		t.messages = t.messages[1:]
	}
}

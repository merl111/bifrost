package main

import (
	"bifrost/internal/ui"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTranscriptFinalReplacesStreamingAndBoundsDisplay(t *testing.T) {
	var transcript transcript
	for _, event := range []ui.ChatEvent{{Kind: "user", Text: "hello"}, {Kind: "delta", Text: "par"}, {Kind: "delta", Text: "tial"}, {Kind: "final", Text: "complete"}} {
		transcript.apply(event)
	}
	if len(transcript.messages) != 2 || transcript.messages[1].Text != "complete" {
		t.Fatalf("duplicated final: %+v", transcript.messages)
	}
	for i := 0; i < maxVisibleMessages+10; i++ {
		transcript.apply(ui.ChatEvent{Kind: "assistant", Text: "next"})
	}
	if len(transcript.messages) != maxVisibleMessages {
		t.Fatal("unbounded transcript")
	}
	text := safeText(strings.Repeat("🌉", maxMessageBytes) + "\x00")
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > maxMessageBytes+150 {
		t.Fatal("unsafe native text")
	}
	if len(messageSpans(strings.Repeat("```go\nx\n```", 200))) > 128 {
		t.Fatal("exceeded native span limit")
	}
}

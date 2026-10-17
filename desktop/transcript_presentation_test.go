package main

import (
	"bifrost/internal/ui"
	cui "cui.local/cui"
	"strings"
	"testing"
)

func TestConversationPresentationKeepsPromptsAndGroupsActivity(t *testing.T) {
	var tr transcript
	for _, e := range []ui.ChatEvent{
		{Kind: "user", Text: "Find my messages"},
		{Kind: "status", Text: "task private-id"},
		{Kind: "tool", ID: "read", Text: "Reading source", Detail: "original detail"},
		{Kind: "thinking", Text: "Inspect the message model"},
		{Kind: "assistant", Text: "Your messages now stand out."},
		{Kind: "user", Text: "Thanks"},
		{Kind: "tool", ID: "read", Text: "Second turn"},
		{Kind: "final", Text: "Done"},
	} {
		tr.apply(e)
	}
	items := tr.native()
	if len(items) != 6 || items[0].Flags&cui.ChatOutgoing == 0 || items[3].Flags&cui.ChatOutgoing == 0 || items[2].Flags&cui.ChatOutgoing != 0 {
		t.Fatalf("speaker grouping: %+v", items)
	}
	if items[1].ID != tr.messages[1].ID || len(items[1].Spans) != 1 || !strings.Contains(items[1].Spans[0].Text, "1 tool event") {
		t.Fatal("activity summary lost stable identity or counts")
	}
	for _, m := range items {
		if m.ThreadCount != 0 {
			t.Fatal("duplicate Details cards")
		}
	}
	tr.messages[1].Collapsed = false
	tr.apply(ui.ChatEvent{Kind: "tool", ID: "read", Text: "Second turn complete", Detail: "new detail"})
	items = tr.native()
	if !strings.Contains(items[1].Spans[1].Text, "original detail") || strings.Contains(items[1].Spans[1].Text, "new detail") {
		t.Fatal("updates crossed turn boundary")
	}
	if !strings.Contains(tr.activityBody(6, 7), "new detail") {
		t.Fatal("full activity lost updated content")
	}
}

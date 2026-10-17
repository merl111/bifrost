package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bifrost/internal/provider"
)

type summaryProvider struct{}

func (summaryProvider) Complete(context.Context, provider.Request) (provider.Response, error) {
	return provider.Response{
		Message:         provider.Message{Role: provider.RoleAssistant, Content: "done"},
		ThinkingSummary: "Compared the available options.",
	}, nil
}

func TestRunEmitsProviderThinkingSummary(t *testing.T) {
	var events []Event
	result, err := Run(context.Background(), Config{
		Provider: summaryProvider{},
		Root:     t.TempDir(),
		StateDir: t.TempDir(),
		OnEvent:  func(event Event) { events = append(events, event) },
	}, "finish the task")
	if err != nil || result.Text != "done" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for _, event := range events {
		if event.Kind == "thinking" {
			if event.ID != "thinking-1" || event.Text != "Thinking summary" || event.Detail != "Compared the available options." {
				t.Fatalf("thinking event = %#v", event)
			}
			return
		}
	}
	t.Fatalf("thinking event missing: %#v", events)
}

func TestBoundedEventDetailIsValidUTF8(t *testing.T) {
	got := BoundedEventDetail(strings.Repeat("界", MaxEventDetailBytes))
	if len([]byte(got)) > MaxEventDetailBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("detail bytes=%d suffix=%q", len([]byte(got)), got[len(got)-3:])
	}
}

func TestEmitToolCarriesStableIDAndBoundedDetails(t *testing.T) {
	var events []Event
	cfg := Config{OnEvent: func(event Event) { events = append(events, event) }}
	call := provider.ToolCall{ID: "call-1", Name: "read_file", Arguments: json.RawMessage(`{"path":"note.txt"}`)}
	emitTool(cfg, call, "started", "args: {\"path\":\"note.txt\"}")
	if len(events) != 1 || events[0].ID != "call-1" || events[0].Text != "read_file started" || events[0].Detail == "" {
		t.Fatalf("tool event = %#v", events)
	}
}

func TestToolFinishedDetailKeepsArgumentsAndResult(t *testing.T) {
	detail := toolFinishedDetail(json.RawMessage(`{"path":"note.txt"}`), "old value")
	if !strings.Contains(detail, `args: {"path":"note.txt"}`) || !strings.Contains(detail, "result: old value") {
		t.Fatalf("finished detail = %q", detail)
	}
}

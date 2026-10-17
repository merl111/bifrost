package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/ui"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestViewerFlowToolRejectsInvalidAndEmitsMixedSteps(t *testing.T) {
	cfg := agent.Config{}
	var events []ui.ChatEvent
	attachWorkspaceViewerTools(&cfg, func(e ui.ChatEvent) { events = append(events, e) })
	for _, tool := range cfg.ExtraTools {
		if tool.Definition.Name != "viewer_flow" {
			continue
		}
		for _, raw := range []string{`{"steps":[]}`, `{"steps":[{"path":"../outside"}]}`, `{"steps":[{"kind":"diff","note":"missing"}]}`} {
			if _, err := tool.Call(context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("accepted invalid flow")
			}
		}
		if len(events) != 0 {
			t.Fatal("invalid flow mutated viewer")
		}
		raw := json.RawMessage(`{"title":"Flow","steps":[{"kind":"concept","explanation":"Context"},{"path":"main.go","start_line":3,"explanation":"Entry"},{"kind":"diff","content":"-old\n+new","explanation":"Changed behavior"}]}`)
		if _, err := tool.Call(context.Background(), raw); err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 || !strings.Contains(events[0].Detail, "Changed behavior") {
			t.Fatal("flow detail lost")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := tool.Call(ctx, raw); err == nil || len(events) != 1 {
			t.Fatal("cancelled flow emitted")
		}
		return
	}
	t.Fatal("viewer_flow missing")
}

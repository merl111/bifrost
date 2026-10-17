package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
)

func TestMonitoredAgentActuallySpawnsAndWaits(t *testing.T) {
	opts := options{stateDir: t.TempDir()}
	root := t.TempDir()
	st, err := sessionStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Create(root, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	client := runtimeProvider(func(_ context.Context, r provider.Request) (provider.Response, error) {
		if r.Messages[len(r.Messages)-1].Content == "child job" {
			return provider.Response{Message: provider.Message{Content: "child finished"}}, nil
		}
		last := r.Messages[len(r.Messages)-1]
		if last.Role == provider.RoleTool && last.ToolName == "spawn_agent" {
			var child tasks.Task
			if err := json.Unmarshal([]byte(last.Content), &child); err != nil {
				t.Fatal(err)
			}
			if child.ID == "" {
				t.Fatalf("no child task: %s", last.Content)
			}
			raw, _ := json.Marshal(map[string]string{"id": string(child.ID)})
			return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "wait", Name: "task_wait", Arguments: raw}}}}, nil
		}
		if last.Role == provider.RoleTool && last.ToolName == "task_wait" {
			return provider.Response{Message: provider.Message{Content: "parent finished"}}, nil
		}
		return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "spawn", Name: "spawn_agent", Arguments: json.RawMessage(`{"prompt":"child job","mode":"readonly"}`)}}}}, nil
	})
	cfg := agent.Config{Root: root, StateDir: t.TempDir(), Provider: client, MaxSteps: 4}
	_, result, err := runMonitoredSessionTurn(context.Background(), opts, st, current, cfg, "parent job", nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "parent finished" || result.ToolCalls != 2 {
		t.Fatal(result)
	}
	monitor, err := tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := monitor.List()
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	for _, row := range rows {
		if row.Status != tasks.StatusCompleted {
			t.Fatalf("unfinished: %+v", row)
		}
	}
}

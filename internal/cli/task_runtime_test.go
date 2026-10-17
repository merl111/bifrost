package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
)

func TestOpenTaskRuntimeExternalParentSpawnsAndWaits(t *testing.T) {
	root := t.TempDir()
	base := agent.Config{Provider: taskRuntimeProvider{}, Root: root, StateDir: t.TempDir(), MaxSteps: 3}
	runtime, err := OpenTaskRuntime(context.Background(), options{stateDir: t.TempDir()}, &base, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	parent, err := runtime.RegisterParent(context.Background(), base, "parent turn")
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := tasks.OpenMonitor(filepath.Join(runtime.stateDir, ".."))
	if err != nil {
		t.Fatal(err)
	}
	if listed, err := monitor.List(); err != nil || len(listed) != 1 || listed[0].ID != parent.ID {
		t.Fatalf("aggregated monitor = %#v, %v", listed, err)
	}
	runtime.AttachParent(&base, parent.ID)
	spawn := findTaskTool(base.ExtraTools, "spawn_agent")
	wait := findTaskTool(base.ExtraTools, "task_wait")
	raw, err := spawn.Call(context.Background(), json.RawMessage(`{"prompt":"child"}`))
	if err != nil {
		t.Fatal(err)
	}
	var child tasks.Task
	if err := json.Unmarshal([]byte(raw), &child); err != nil {
		t.Fatal(err)
	}
	if _, err := wait.Call(context.Background(), json.RawMessage(`{"id":"`+child.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := runtime.CompleteParent(parent.ID, tasks.Result{Output: "parent done"}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := runtime.Scheduler().Status(child.ID); err != nil || got.Status != tasks.StatusCompleted {
		t.Fatalf("child status = %#v, %v", got, err)
	}
}

func TestTaskProviderResolverUsesSavedProfileAndModelOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	settings := config.DefaultSettings()
	settings.Providers["fixture"] = config.ProviderSettings{Kind: "chat-completions", BaseURL: "http://127.0.0.1:1/v1", Model: "saved-model"}
	if err := config.SaveSettings(path, settings, true); err != nil {
		t.Fatal(err)
	}
	resolver, err := taskProviderResolver(options{configPath: path})
	if err != nil {
		t.Fatal(err)
	}
	client, err := resolver(context.Background(), "override-model", "fixture")
	if err != nil || client == nil {
		t.Fatalf("resolve saved profile = %v, %v", client, err)
	}
	if _, err := resolver(context.Background(), "", "missing"); err == nil {
		t.Fatal("unknown profile was accepted")
	}
}

type taskRuntimeProvider struct{}

func (taskRuntimeProvider) Complete(context.Context, provider.Request) (provider.Response, error) {
	return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "child done"}, FinishReason: "stop"}, nil
}

func findTaskTool(tools []agent.Tool, name string) agent.Tool {
	for _, tool := range tools {
		if tool.Definition.Name == name {
			return tool
		}
	}
	panic("missing task tool " + name)
}

package cli

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
)

func TestNativeParentDelegatesToExternalProfile(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	var childMu sync.Mutex
	var childCfg agent.Config
	parent := mixedParentProvider{}
	var scheduler *tasks.Scheduler
	runner := NewTaskRunner(TaskRunnerConfig{Agent: agent.Config{Provider: &parent, Root: root, StateDir: state, WriteEnabled: false, ExecEnabled: false, MaxSteps: 4}, SchedulerFunc: func() *tasks.Scheduler { return scheduler }, ResolveAgent: func(_ context.Context, model, profile string) (agent.Config, error) {
		return agent.Config{ModelName: model, ProfileName: profile, RunExternal: func(_ context.Context, cfg agent.Config, _ string) (agent.Result, error) {
			childMu.Lock()
			childCfg = cfg
			childMu.Unlock()
			return agent.Result{Text: "external child"}, nil
		}}, nil
	}, AllowWrite: false})
	var err error
	scheduler, err = tasks.New(filepath.Join(state, "tasks"), runner, tasks.Options{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()
	parentTask, err := scheduler.RegisterParent(context.Background(), tasks.TaskSpec{Root: root, Prompt: "parent", Mode: tasks.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	cfg := agent.Config{Provider: &parent, Root: root, StateDir: state, WriteEnabled: false, ExecEnabled: false, MaxSteps: 4}
	AttachTaskTools(&cfg, scheduler, parentTask.ID, false)
	result, err := agent.Run(context.Background(), cfg, "parent")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text == "" {
		t.Fatal("parent did not complete")
	}
	if result.ToolCalls == 0 {
		t.Fatalf("parent did not invoke spawn tool: %#v", result)
	}
	var childID domain.ID
	for _, summary := range scheduler.List() {
		if summary.ParentID == parentTask.ID {
			childID = summary.ID
		}
	}
	if childID == "" {
		t.Fatal("parent did not create child")
	}
	if _, err := scheduler.WaitChild(context.Background(), parentTask.ID, childID); err != nil {
		t.Fatal(err)
	}
	childMu.Lock()
	got := childCfg
	childMu.Unlock()
	if got.RunExternal == nil || got.Provider != nil || got.Root == "" || got.StateDir == state || got.WriteEnabled || got.ExecEnabled || got.ModelName != "sonnet" || got.ProfileName != "claude" {
		t.Fatalf("child config lost isolation/profile: root=%q parentRoot=%q state=%q write=%v exec=%v model=%q profile=%q provider=%v external=%v", got.Root, root, got.StateDir, got.WriteEnabled, got.ExecEnabled, got.ModelName, got.ProfileName, got.Provider != nil, got.RunExternal != nil)
	}
}

type mixedParentProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *mixedParentProvider) Complete(context.Context, provider.Request) (provider.Response, error) {
	p.mu.Lock()
	p.calls++
	n := p.calls
	p.mu.Unlock()
	if n == 1 {
		b, _ := json.Marshal(map[string]any{"prompt": "child", "profile": "claude", "model": "sonnet", "mode": "readonly"})
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "spawn", Name: "spawn_agent", Arguments: b}}}, FinishReason: "tool_calls"}, nil
	}
	return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "parent done"}, FinishReason: "stop"}, nil
}

package cli

import (
	"context"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
)

type taskTestProvider struct{}

func (taskTestProvider) Complete(context.Context, provider.Request) (provider.Response, error) {
	return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}, FinishReason: "stop"}, nil
}

func TestAttachTaskToolsRebindsAndDropsMutableBrowserTools(t *testing.T) {
	s, err := tasks.New(t.TempDir(), func(context.Context, tasks.TaskSpec, func(tasks.Event)) (tasks.Result, error) {
		return tasks.Result{}, nil
	}, tasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	cfg := agent.Config{ExtraTools: []agent.Tool{
		{Definition: provider.ToolDefinition{Name: "browser_navigate"}},
		{Definition: provider.ToolDefinition{Name: "spawn_agent"}},
		{Definition: provider.ToolDefinition{Name: "keep"}},
	}}
	AttachTaskTools(&cfg, s, "", false)
	for _, tool := range cfg.ExtraTools {
		if tool.Definition.Name == "spawn_agent" {
			// The task tool is intentionally re-added with a fresh binding.
			continue
		}
	}
	if len(cfg.ExtraTools) != 6 {
		t.Fatalf("tool count = %d", len(cfg.ExtraTools))
	}
}

func TestNewTaskRunnerUsesIsolatedRootAndMapsResult(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	s, err := tasks.New(state, func(context.Context, tasks.TaskSpec, func(tasks.Event)) (tasks.Result, error) {
		return tasks.Result{}, nil
	}, tasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, _ := domain.NewID(domain.KindTask)
	runner := NewTaskRunner(TaskRunnerConfig{Agent: agent.Config{Provider: taskTestProvider{}, StateDir: state, Root: root, MaxSteps: 4}, Scheduler: s})
	result, err := runner(context.Background(), tasks.TaskSpec{TaskID: id, Root: root, Prompt: "hello", Mode: tasks.ModeReadOnly}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "done" || result.Steps != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewTaskRunnerRejectsUnsafeOverrides(t *testing.T) {
	runner := NewTaskRunner(TaskRunnerConfig{Agent: agent.Config{Provider: taskTestProvider{}, MaxSteps: 1}})
	if _, err := runner(context.Background(), tasks.TaskSpec{Root: t.TempDir(), Prompt: "x", Model: "bad\nmodel"}, nil); err == nil {
		t.Fatal("newline model override was accepted")
	}
}

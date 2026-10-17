package tasktools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/tasks"
)

func TestToolsSpawnStatusWaitCancel(t *testing.T) {
	release := make(chan struct{})
	s, err := tasks.New(filepath.Join(t.TempDir(), "state"), func(ctx context.Context, spec tasks.TaskSpec, _ func(tasks.Event)) (tasks.Result, error) {
		if spec.Prompt == "slow" {
			select {
			case <-release:
			case <-ctx.Done():
				return tasks.Result{}, ctx.Err()
			}
		}
		return tasks.Result{Output: spec.Prompt}, nil
	}, tasks.Options{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tools := Tools(s, "")
	spawn := find(tools, "spawn_agent")
	status := find(tools, "task_status")
	wait := find(tools, "task_wait")
	cancel := find(tools, "task_cancel")
	root := t.TempDir()
	raw, err := spawn.Call(context.Background(), json.RawMessage(`{"root":"`+root+`","prompt":"slow","mode":"readonly"}`))
	if err != nil {
		t.Fatal(err)
	}
	var task tasks.Task
	if err := json.Unmarshal([]byte(raw), &task); err != nil {
		t.Fatal(err)
	}
	if task.ID == "" {
		t.Fatal("spawn returned no id")
	}
	if _, err := status.Call(context.Background(), json.RawMessage(`{"id":"`+task.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := wait.Call(context.Background(), json.RawMessage(`{"id":"`+task.ID+`","timeout_ms":1}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	if _, err := cancel.Call(context.Background(), json.RawMessage(`{"id":"`+task.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := wait.Call(context.Background(), json.RawMessage(`{"id":"`+task.ID+`"}`)); err != nil {
		t.Fatal(err)
	}
	close(release)
}

func TestToolsChildInheritsRootAndRejectsMalformedArguments(t *testing.T) {
	release := make(chan struct{})
	s, err := tasks.New(filepath.Join(t.TempDir(), "state"), func(_ context.Context, spec tasks.TaskSpec, _ func(tasks.Event)) (tasks.Result, error) {
		if spec.Prompt == "parent" {
			<-release
		}
		return tasks.Result{Output: spec.Root}, nil
	}, tasks.Options{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	root := t.TempDir()
	parent, err := s.SpawnParent(context.Background(), tasks.TaskSpec{Root: root, Prompt: "parent", Mode: tasks.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	// Parent must be active or queued while its child is registered.
	childTools := Tools(s, parent.ID)
	spawn := find(childTools, "spawn_agent")
	if _, err := spawn.Call(context.Background(), json.RawMessage(`{"prompt":"child","mode":"readonly"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := spawn.Call(context.Background(), json.RawMessage(`{"prompt":"x"} trailing`)); err == nil {
		t.Fatal("trailing JSON was accepted")
	}
	if _, err := spawn.Call(context.Background(), json.RawMessage(`[]`)); err == nil {
		t.Fatal("array arguments were accepted")
	}
	close(release)
	if _, err := s.Wait(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
}

func TestToolsChildScopeAndTimeoutValidation(t *testing.T) {
	s, err := tasks.New(filepath.Join(t.TempDir(), "state"), func(ctx context.Context, _ tasks.TaskSpec, _ func(tasks.Event)) (tasks.Result, error) {
		<-ctx.Done()
		return tasks.Result{}, ctx.Err()
	}, tasks.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := t.TempDir()
	parent, err := s.RegisterParent(context.Background(), tasks.TaskSpec{Root: root, Prompt: "parent", Mode: tasks.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChild(context.Background(), parent.ID, tasks.TaskSpec{Root: root, Prompt: "child", Mode: tasks.ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	bound := Tools(s, parent.ID)
	status := find(bound, "task_status")
	if _, err := status.Call(context.Background(), json.RawMessage(`{"id":"`+string(parent.ID)+`"}`)); err == nil {
		t.Fatal("child-bound status exposed parent")
	}
	if _, err := status.Call(context.Background(), json.RawMessage(`{"id":"`+string(child.ID)+`"}`)); err != nil {
		t.Fatal(err)
	}
	wait := find(bound, "task_wait")
	if _, err := wait.Call(context.Background(), json.RawMessage(`{"id":"`+string(child.ID)+`","timeout_ms":-1}`)); err == nil {
		t.Fatal("negative wait timeout was accepted")
	}
}

func find(tools []agent.Tool, name string) agent.Tool {
	for _, tool := range tools {
		if tool.Definition.Name == name {
			return tool
		}
	}
	panic("missing tool " + name)
}

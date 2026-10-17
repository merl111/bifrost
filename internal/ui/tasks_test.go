package ui

import (
	"context"
	"strings"
	"testing"

	"bifrost/internal/domain"
	"bifrost/internal/tasks"
)

func TestTaskMonitorRefreshAndRender(t *testing.T) {
	id, err := domain.NewID(domain.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	m := New(t.TempDir(), WithFilesystem(func(context.Context, string) ([]FileEntry, error) { return nil, nil }, func(context.Context, string) (string, error) { return "", nil }), WithTaskMonitor(TaskMonitorCallbacks{
		Refresh: func(context.Context) ([]tasks.Summary, error) {
			called = true
			return []tasks.Summary{{Task: tasks.Task{ID: id, Status: tasks.StatusActive, Result: tasks.Result{Steps: 2, ToolCalls: 1}}}}, nil
		},
		Events: func(context.Context, uint64, int) ([]tasks.Event, uint64, error) {
			return []tasks.Event{{Sequence: 1, TaskID: id, Type: "task.active", Message: "started"}}, 1, nil
		},
	}))
	cmd := m.startTaskMonitor()
	if cmd == nil {
		t.Fatal("monitor command is nil")
	}
	msg := cmd()
	updated, _ := m.Update(msg)
	if updated != m || !called || m.viewPath != "tasks" {
		t.Fatalf("monitor state: called=%v path=%q", called, m.viewPath)
	}
	rendered := m.renderTasks()
	if !strings.Contains(rendered, string(id)) || !strings.Contains(rendered, "task.active") {
		t.Fatalf("rendered task monitor = %q", rendered)
	}
	_ = m.Close()
}

func TestTaskMonitorCancelPalette(t *testing.T) {
	id, err := domain.NewID(domain.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	var canceled domain.ID
	m := New(t.TempDir(), WithFilesystem(func(context.Context, string) ([]FileEntry, error) { return nil, nil }, func(context.Context, string) (string, error) { return "", nil }), WithTaskMonitor(TaskMonitorCallbacks{
		Refresh: func(context.Context) ([]tasks.Summary, error) { return nil, nil },
		Cancel:  func(_ context.Context, got domain.ID) error { canceled = got; return nil },
	}))
	m.paletteInput = "tasks cancel " + string(id)
	_ = m.paletteKey("enter", "")
	if canceled != id {
		t.Fatalf("canceled %q, want %q", canceled, id)
	}
	_ = m.Close()
}

package main

import (
	"bifrost/internal/tasks"
	"testing"
)

func TestCompactWorkspaceTitles(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  Review\n changes  <attached_context>secret path and source", "Review changes"},
		{"  Native  UI  ", "Native UI"},
	} {
		if got := compactText(tc.in, 60); got != tc.want {
			t.Errorf("got %q, want %q", got, tc.want)
		}
	}
}
func TestTaskFilters(t *testing.T) {
	task := tasks.Summary{Task: tasks.Task{ID: "task-1", Status: tasks.StatusQueued, Prompt: "Review changes", Model: "Model A"}}
	if !taskMatches(task, "review", 1) || !taskMatches(task, "model a", 0) || taskMatches(task, "missing", 0) || taskMatches(task, "", 2) || taskMatches(task, "", 3) {
		t.Fatal("task filtering mixes status or query matches")
	}
	task.Status = tasks.StatusFailed
	if !taskMatches(task, "", 3) || taskMatches(task, "", 1) {
		t.Fatal("failed task status filter")
	}
}

package tasks

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bifrost/internal/domain"
	"bifrost/internal/store"
)

func TestAggregateMonitorReturnsLatestEventsWithoutCursor(t *testing.T) {
	root := filepath.Join(t.TempDir(), "tasks")
	base := time.Now().UTC().Add(-2 * time.Hour)
	for runIndex := 0; runIndex < 2; runIndex++ {
		runDir := filepath.Join(root, fmt.Sprintf("run-%d", runIndex))
		if err := os.MkdirAll(filepath.Join(runDir, "snapshots"), 0o700); err != nil {
			t.Fatal(err)
		}
		journal, err := store.OpenJournal(filepath.Join(runDir, "events.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		taskID, err := domain.NewID(domain.KindTask)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 1200; i++ {
			payload, _ := json.Marshal(Event{TaskID: taskID, Type: "task.progress", Message: fmt.Sprintf("run%d-%d", runIndex, i)})
			if _, err := journal.Append(store.Event{Type: "task.progress", Payload: payload, Timestamp: base.Add(time.Duration(runIndex*500+i) * time.Millisecond)}); err != nil {
				t.Fatal(err)
			}
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	monitor, err := OpenMonitor(root)
	if err != nil {
		t.Fatal(err)
	}
	events, cursor, err := monitor.Events(0, 64)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 0 || len(events) != 64 {
		t.Fatalf("aggregate page = %d events, cursor %d", len(events), cursor)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Time.Before(events[i-1].Time) {
			t.Fatal("aggregate events are not chronological")
		}
	}
	if events[0].Message != "run1-1136" || events[len(events)-1].Message != "run1-1199" {
		t.Fatalf("aggregate did not return latest events: first=%q last=%q", events[0].Message, events[len(events)-1].Message)
	}
	if _, _, err := monitor.Events(1, 64); err == nil {
		t.Fatal("aggregate cursor was silently accepted")
	}
	run, err := OpenMonitor(filepath.Join(root, "run-0"))
	if err != nil {
		t.Fatal(err)
	}
	page, next, err := run.Events(0, 10)
	if err != nil || len(page) != 10 || next != 10 {
		t.Fatalf("per-run first page = %d, cursor %d, err %v", len(page), next, err)
	}
	page, next2, err := run.Events(next, 10)
	if err != nil || len(page) != 10 || next2 != 20 {
		t.Fatalf("per-run second page = %d, cursor %d, err %v", len(page), next2, err)
	}
}

func TestMonitorRejectsNegativeLimitAndOversizedLine(t *testing.T) {
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "snapshots"), 0o700); err != nil {
		t.Fatal(err)
	}
	monitor, err := OpenMonitor(state)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := monitor.Events(0, -1); err == nil {
		t.Fatal("negative monitor limit was accepted")
	}
	if err := os.WriteFile(filepath.Join(state, "events.jsonl"), []byte(`{"`+string(make([]byte, maxMonitorLine))+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := monitor.Events(0, 1); err == nil {
		t.Fatal("oversized monitor line was accepted")
	}
}

func TestOpenMonitorEmptyStateIsReadable(t *testing.T) {
	monitor, err := OpenMonitor(filepath.Join(t.TempDir(), "missing", "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	items, err := monitor.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("empty monitor list = %#v, %v", items, err)
	}
	events, cursor, err := monitor.Events(0, 64)
	if err != nil || len(events) != 0 || cursor != 0 {
		t.Fatalf("empty monitor events = %#v, %d, %v", events, cursor, err)
	}
}

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalReplayRestartAndTruncatedFinalRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Append(Event{Type: "project.created"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema_version":1,"sequence":2,"event_id":"evt_deadbeef`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	j, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	events, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != first.ID {
		t.Fatalf("replay = %+v", events)
	}
	if _, err := j.Append(Event{Type: "project.updated"}); err != nil {
		t.Fatal(err)
	}
	_ = j.Close()
}

func TestJournalInteriorCorruptionIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Type: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Type: "two"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range b {
		if b[i] == '{' {
			b[i] = 'x'
			break
		}
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(path); err == nil || errors.Is(err, ErrStoreLocked) {
		t.Fatalf("corruption error = %v", err)
	}
}

func TestJournalRestartAppendsSeparatorAfterUnterminatedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := j.Append(Event{Type: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 || b[len(b)-1] != '\n' {
		t.Fatal("expected initial record newline")
	}
	if err := os.WriteFile(path, b[:len(b)-1], 0o600); err != nil {
		t.Fatal(err)
	}
	j, err = OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := j.Append(Event{Type: "two"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Sequence != first.Sequence+1 {
		t.Fatalf("second sequence = %d", second.Sequence)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := func() ([]Event, error) {
		j, err := OpenJournal(path)
		if err != nil {
			return nil, err
		}
		defer j.Close()
		return j.Replay()
	}()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Sequence != 1 || events[1].Sequence != 2 {
		t.Fatalf("replay = %+v", events)
	}
}

func TestJournalCompleteMalformedFinalRecordIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Type: "one"}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema_version":}`); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := OpenJournal(path); err == nil || errors.Is(err, ErrStoreLocked) {
		t.Fatalf("complete malformed record error = %v", err)
	}
}

func TestJournalWriteFailureRequiresReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.jsonl")
	j, err := OpenJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Append(Event{Type: "one"}); err == nil {
		t.Fatal("append after closed file unexpectedly succeeded")
	}
	if _, err := j.Append(Event{Type: "two"}); err == nil || !strings.Contains(err.Error(), "reopen required") {
		t.Fatalf("second append error = %v", err)
	}
	_ = j.Close()
}

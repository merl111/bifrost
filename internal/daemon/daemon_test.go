package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testHandler(block <-chan struct{}) Handler {
	return func(ctx context.Context, req AgentRequest, emit func(Event) error) (json.RawMessage, error) {
		if err := emit(Event{Type: "message", Payload: json.RawMessage(`{"text":"started"}`)}); err != nil {
			return nil, err
		}
		if req.Prompt == "block" {
			select {
			case <-block:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return json.RawMessage(`{"ok":true}`), nil
	}
}

func waitStatus(t *testing.T, c *Client, id string, want ...string) Job {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		j, err := c.Status(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range want {
			if j.Status == s {
				return j
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not reach %v", id, want)
	return Job{}
}

func TestSubmitReplayAttachCancelAndAuth(t *testing.T) {
	state := t.TempDir()
	block := make(chan struct{})
	s, err := Start(context.Background(), Config{StateDir: state}, testHandler(block))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := Dial(context.Background(), Config{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Submit(context.Background(), AgentRequest{Prompt: "done"})
	if err != nil {
		t.Fatal(err)
	}
	j := waitStatus(t, c, id, "completed")
	if len(j.Result) == 0 || j.LastCursor == 0 {
		t.Fatalf("job=%+v", j)
	}
	ev, err := c.Events(context.Background(), id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ev) < 3 {
		t.Fatalf("events=%+v", ev)
	}
	var attached []Event
	if err := c.Attach(context.Background(), id, 0, func(e Event) error { attached = append(attached, e); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(attached) != len(ev) {
		t.Fatalf("attach=%d replay=%d", len(attached), len(ev))
	}
	blocked, err := c.Submit(context.Background(), AgentRequest{Prompt: "block"})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, blocked, "running")
	if err := c.Cancel(context.Background(), blocked); err != nil {
		t.Fatal(err)
	}
	j = waitStatus(t, c, blocked, "cancelled")
	if j.Error == "" {
		t.Fatal("missing cancellation error")
	}
	bad, err := Dial(context.Background(), Config{StateDir: state, Token: "wrong"})
	if err == nil || bad != nil || !errors.Is(err, ErrUnauthorized) && !strings.Contains(err.Error(), "401") {
		t.Fatalf("auth err=%v", err)
	}
}

func TestSingletonStaleAndRestartRecovery(t *testing.T) {
	state := t.TempDir()
	s, err := Start(context.Background(), Config{StateDir: state}, func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{StateDir: state}, func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error) { return nil, nil }); err == nil {
		t.Fatal("second daemon started")
	}
	_ = s.Close()
	// A regular file at the socket path is never removed as stale state.
	if err := os.WriteFile(filepath.Join(state, "daemon.sock"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Config{StateDir: state}, func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error) { return nil, nil }); err == nil {
		t.Fatal("regular socket path accepted")
	}
	if b, _ := os.ReadFile(filepath.Join(state, "daemon.sock")); string(b) != "keep" {
		t.Fatal("regular path was removed")
	}
	_ = os.Remove(filepath.Join(state, "daemon.sock"))
	// Running jobs in a recovered snapshot are marked interrupted and never rerun.
	jobs := stateFile{Schema: 1, Jobs: []Job{{ID: "job-old", Status: "running", CreatedAt: time.Now().UTC()}}}
	b, _ := json.Marshal(jobs)
	if err := os.WriteFile(filepath.Join(state, "jobs.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = Start(context.Background(), Config{StateDir: state}, func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error) {
		t.Fatal("replayed handler")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := Dial(context.Background(), Config{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	j, err := c.Status(context.Background(), "job-old")
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != "interrupted" {
		t.Fatalf("status=%s", j.Status)
	}
}

func TestEventCursorExpiryAndBounds(t *testing.T) {
	state := t.TempDir()
	s, err := Start(context.Background(), Config{StateDir: state, MaxEvents: 2, MaxOutputBytes: 32}, func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error) {
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := Dial(context.Background(), Config{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Submit(context.Background(), AgentRequest{})
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, c, id, "completed")
	ev, err := c.Events(context.Background(), id, 0)
	if err != nil || len(ev) > 2 {
		t.Fatalf("bounded events=%v err=%v", ev, err)
	}
}

func TestMaxConcurrentBoundsHandlers(t *testing.T) {
	state := t.TempDir()
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var mu sync.Mutex
	active, maximum := 0, 0
	s, err := Start(context.Background(), Config{StateDir: state, MaxConcurrent: 2}, func(ctx context.Context, _ AgentRequest, _ func(Event) error) (json.RawMessage, error) {
		mu.Lock()
		active++
		if active > maximum {
			maximum = active
		}
		mu.Unlock()
		started <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
		mu.Lock()
		active--
		mu.Unlock()
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c, err := Dial(context.Background(), Config{StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 4)
	for i := range ids {
		ids[i], err = c.Submit(context.Background(), AgentRequest{Prompt: "block"})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("handlers did not start")
		}
	}
	select {
	case <-started:
		t.Fatal("third handler exceeded MaxConcurrent")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	for _, id := range ids {
		waitStatus(t, c, id, "completed")
	}
	mu.Lock()
	got := maximum
	mu.Unlock()
	if got > 2 {
		t.Fatalf("maximum active handlers=%d", got)
	}
}

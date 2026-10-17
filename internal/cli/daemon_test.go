package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"bifrost/internal/daemon"
	"bifrost/internal/ui"
)

func TestRunDaemonStatusJobsAndStop(t *testing.T) {
	state := t.TempDir()
	s, err := daemon.Start(context.Background(), daemon.Config{StateDir: state}, func(context.Context, daemon.AgentRequest, func(daemon.Event) error) (json.RawMessage, error) {
		return json.RawMessage(`{}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	opts := options{stateDir: state, json: true}
	var out strings.Builder
	if err := runDaemon(context.Background(), opts, []string{"jobs"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out.String()), "[") {
		t.Fatalf("jobs output=%q", out.String())
	}
	out.Reset()
	if err := runDaemon(context.Background(), opts, []string{"status", "missing"}, &out, io.Discard); err == nil {
		t.Fatal("missing daemon job accepted")
	}
	if err := runDaemon(context.Background(), opts, []string{"stop"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestRunDaemonServeStopsThroughServerShutdown(t *testing.T) {
	state := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- runDaemon(ctx, options{stateDir: state}, []string{"serve"}, io.Discard, io.Discard)
	}()

	var client *daemon.Client
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		client, err = daemon.Dial(context.Background(), daemon.Config{StateDir: state})
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if client == nil {
		t.Fatalf("daemon did not start: %v", err)
	}
	defer client.Close()
	if err := client.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not observe daemon shutdown")
	}
}

func TestDaemonChatRunnerPreservesArgsAndSession(t *testing.T) {
	state := t.TempDir()
	var mu sync.Mutex
	var got daemon.AgentRequest
	s, err := daemon.Start(context.Background(), daemon.Config{StateDir: state}, func(_ context.Context, req daemon.AgentRequest, emit func(daemon.Event) error) (json.RawMessage, error) {
		mu.Lock()
		got = req
		mu.Unlock()
		payload, _ := json.Marshal(map[string]any{"kind": "tool", "text": "read_file finished", "id": "call-1", "detail": "result: ok", "step": 2})
		if err := emit(daemon.Event{Type: "agent.delta", Payload: payload}); err != nil {
			return nil, err
		}
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	runner, cleanup, err := daemonChatRunner(context.Background(), options{stateDir: state}, []string{"--profile", "local", "--allow-write"}, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var events []ui.ChatEvent
	if err := runner(context.Background(), "prompt", func(event ui.ChatEvent) { events = append(events, event) }); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.SessionID != "session-1" || got.Prompt != "prompt" || len(got.Args) != 3 || got.Args[0] != "--profile" {
		t.Fatalf("request=%+v", got)
	}
	if len(events) != 1 || events[0].Text != "read_file finished" || events[0].ID != "call-1" || events[0].Detail != "result: ok" {
		t.Fatalf("events=%+v", events)
	}
}

func TestDaemonPromptOnlyConsumesLeadingDaemonFlag(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
		left []string
	}{
		{name: "leading flag", args: []string{"--daemon", "--profile", "local", "hello"}, want: true, left: []string{"--profile", "local", "hello"}},
		{name: "equals flag", args: []string{"--daemon=true", "hello"}, want: true, left: []string{"hello"}},
		{name: "prompt literal", args: []string{"hello", "--daemon"}, want: false, left: []string{"hello", "--daemon"}},
		{name: "separator literal", args: []string{"--", "--daemon", "hello"}, want: false, left: []string{"--", "--daemon", "hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, left, err := daemonPrompt(tt.args)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want || strings.Join(left, "\x00") != strings.Join(tt.left, "\x00") {
				t.Fatalf("got daemon=%v args=%q", got, left)
			}
		})
	}
}

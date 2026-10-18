package cli

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"bifrost/internal/provider"
	"bifrost/internal/ui"
)

func TestDesktopParallelConversations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := OpenDesktopWorkspace(ctx, []string{"--root", t.TempDir(), "--state-dir", t.TempDir(), "--base-url", "http://localhost:1", "--model", "fixture", "--agents", "2"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	started := make(chan string, 4)
	release := make(chan struct{})
	d.chat.setup.cfg.Provider = runtimeProvider(func(ctx context.Context, req provider.Request) (provider.Response, error) {
		prompt := req.Messages[len(req.Messages)-1].Content
		started <- prompt
		if prompt == "follow up" {
			found := false
			for _, m := range req.Messages {
				if m.Content == "answer second" {
					found = true
				}
				if m.Content == "first" {
					t.Error("another tab's history reached the provider")
				}
			}
			if !found {
				t.Error("follow-up lost its conversation history")
			}
		} else {
			select {
			case <-ctx.Done():
				return provider.Response{}, ctx.Err()
			case <-release:
			}
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "answer " + prompt}}, nil
	})
	type result struct {
		snapshot DesktopSnapshot
		err      error
	}
	first, second := make(chan result, 1), make(chan result, 1)
	firstCtx, stopFirst := context.WithCancel(ctx)
	defer stopFirst()
	go func() { s, e := d.RunConversation(firstCtx, "", "first", func(ui.ChatEvent) {}); first <- result{s, e} }()
	go func() { s, e := d.RunConversation(ctx, "", "second", func(ui.ChatEvent) {}); second <- result{s, e} }()
	for range 2 {
		select {
		case <-started:
		case r := <-first:
			t.Fatalf("first exited before parallel start: %v", r.err)
		case r := <-second:
			t.Fatalf("second exited before parallel start: %v", r.err)
		case <-ctx.Done():
			t.Fatal("turns did not start concurrently")
		}
	}
	stopFirst()
	var a, b result
	select {
	case a = <-first:
	case <-ctx.Done():
		t.Fatal("first did not stop")
	}
	if !errors.Is(a.err, context.Canceled) {
		t.Fatalf("first cancellation: %v", a.err)
	}
	select {
	case <-second:
		t.Fatal("stopping first also stopped second")
	default:
	}
	close(release)
	select {
	case b = <-second:
	case <-ctx.Done():
		t.Fatal("second did not finish")
	}
	if b.err != nil {
		t.Fatal(b.err)
	}
	activeID := func(s DesktopSnapshot) string {
		for _, item := range s.Sessions {
			if item.Active {
				return item.ID
			}
		}
		return ""
	}
	aID, bID := activeID(a.snapshot), activeID(b.snapshot)
	if aID == "" || bID == "" || aID == bID {
		t.Fatal("tabs did not persist separate sessions")
	}
	if len(b.snapshot.History) != 2 || b.snapshot.History[0].Text != "second" {
		t.Fatalf("second history: %+v", b.snapshot.History)
	}
	if _, err := d.RunConversation(ctx, bID, "follow up", func(ui.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
	// The shared workspace can resume the completed tab for TUI-compatible reads.
	if err := d.Resume(ctx, bID); err != nil {
		t.Fatal(err)
	}
	s, err := d.Snapshot(ctx)
	if err != nil || len(s.History) != 4 {
		t.Fatalf("persisted history: %+v, %v", s.History, err)
	}
}

func TestDesktopParallelCloseAndSessionLock(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := OpenDesktopWorkspace(ctx, []string{"--root", t.TempDir(), "--state-dir", t.TempDir(), "--base-url", "http://localhost:1", "--model", "fixture", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	started := make(chan struct{}, 2)
	d.chat.setup.cfg.Provider = runtimeProvider(func(ctx context.Context, _ provider.Request) (provider.Response, error) {
		started <- struct{}{}
		<-ctx.Done()
		return provider.Response{}, ctx.Err()
	})
	id := string(d.chat.current.ID)
	done := make(chan error, 2)
	go func() { _, err := d.RunConversation(ctx, id, "first", func(ui.ChatEvent) {}); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("turn failed to start")
	}
	if _, err := d.RunConversation(ctx, id, "duplicate", func(ui.ChatEvent) {}); err == nil {
		t.Fatal("same session ran twice")
	}
	go func() { _, err := d.RunConversation(ctx, "", "second", func(ui.ChatEvent) {}); done <- err }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("second failed to start")
	}
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("workspace close did not cancel all turns")
	}
	for range 2 {
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("close result: %v", err)
		}
	}
	if _, err := d.RunConversation(ctx, "", "after close", func(ui.ChatEvent) {}); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed workspace started turn: %v", err)
	}
}

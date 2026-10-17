package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/provider"
	"bifrost/internal/session"
)

func testSessionConfig(t *testing.T, root, state string, p provider.Client) agent.Config {
	t.Helper()
	workspaceState, err := (options{stateDir: state}).workspaceStatePath(root)
	if err != nil {
		t.Fatal(err)
	}
	return agent.Config{Provider: p, Root: root, StateDir: workspaceState, MaxSteps: 2}
}

func TestStartupResumesTheLatestConversationWithMessages(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	fp := "provider-sha256:test"
	older, err := st.Create(root, fp)
	if err != nil {
		t.Fatal(err)
	}
	older.Messages = []provider.Message{{Role: provider.RoleUser, Content: "remember this"}, {Role: provider.RoleAssistant, Content: "kept"}}
	older.Status = session.StatusCompleted
	if older, err = st.Save(older, older.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(root, fp); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create(root, "provider-sha256:other"); err != nil {
		t.Fatal(err)
	}

	resumed, err := loadOrCreateSession(st, "", root, fp)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != older.ID || len(resumed.Messages) != 2 || resumed.Messages[0].Content != "remember this" {
		t.Fatalf("startup resumed %+v", resumed)
	}
	explicit, err := loadOrCreateSession(st, resumed.ID.String(), root, fp)
	if err != nil || explicit.ID != resumed.ID {
		t.Fatalf("explicit resume = %+v err=%v", explicit, err)
	}
}

func TestRunSessionTurnPersistsAndResumesCompleteHistory(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	var requests []provider.Request
	p := runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		requests = append(requests, req)
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "answer"}}, nil
	})
	current, err := loadOrCreateSession(st, "", root, "provider-sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	cfg := testSessionConfig(t, root, state, p)
	current, _, err = runSessionTurn(context.Background(), state, st, current, cfg, "first")
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != session.StatusCompleted || current.Revision != 3 {
		t.Fatalf("unexpected first turn: %+v", current)
	}

	// Reopen from disk to model a restart and make sure the next request sees
	// only the complete prior turn, followed by the new user prompt.
	restarted, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSessionTurn(context.Background(), state, restarted, loaded, cfg, "second"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("provider calls = %d", len(requests))
	}
	if len(requests[1].Messages) != 4 || requests[1].Messages[1].Role != provider.RoleUser || requests[1].Messages[1].Content != "first" || requests[1].Messages[3].Content != "second" {
		t.Fatalf("resume history was not complete-turn bounded: %+v", requests[1].Messages)
	}
}

func TestRunSessionTurnRetainsHistoryOnErrorAndChecksBinding(t *testing.T) {
	root, otherRoot, state := t.TempDir(), t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Create(root, "provider-sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateSession(st, current.ID.String(), otherRoot, "provider-sha256:test"); !errors.Is(err, session.ErrBindingMismatch) {
		t.Fatalf("binding mismatch = %v", err)
	}
	if _, err := loadOrCreateSession(st, current.ID.String(), root, "provider-sha256:other"); !errors.Is(err, session.ErrBindingMismatch) {
		t.Fatalf("provider mismatch = %v", err)
	}
	working := runtimeProvider(func(_ context.Context, _ provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "ok"}}, nil
	})
	cfg := testSessionConfig(t, root, state, working)
	current, _, err = runSessionTurn(context.Background(), state, st, current, cfg, "keep")
	if err != nil {
		t.Fatal(err)
	}
	previousMessages := len(current.Messages)
	failing := runtimeProvider(func(_ context.Context, _ provider.Request) (provider.Response, error) {
		return provider.Response{}, errors.New("fixture failure")
	})
	cfg.Provider = failing
	updated, _, err := runSessionTurn(context.Background(), state, st, current, cfg, "do not persist")
	if err == nil || !strings.Contains(err.Error(), "fixture failure") {
		t.Fatalf("expected provider failure, got %v", err)
	}
	if updated.Status != session.StatusError || len(updated.Messages) != previousMessages {
		t.Fatalf("error status/history = %+v", updated)
	}
	loaded, err := st.Load(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Status != session.StatusError || len(loaded.Messages) != previousMessages {
		t.Fatalf("persisted error status/history = %+v", loaded)
	}
}

func TestRunSessionTurnContentionAndCancellation(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Create(root, "provider-sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	p := runtimeProvider(func(_ context.Context, _ provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "ok"}}, nil
	})
	cfg := testSessionConfig(t, root, state, p)
	release, err := acquireSessionTurn(state, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runSessionTurn(context.Background(), state, st, current, cfg, "blocked"); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("contention error = %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	updated, _, err := runSessionTurn(cancelled, state, st, current, cfg, "cancelled")
	if !errors.Is(err, context.Canceled) || updated.Status != session.StatusInterrupted {
		t.Fatalf("cancellation = status %s err %v", updated.Status, err)
	}
}

func TestRunSessionTurnMarksInterruptedBeforeProviderAndRejectsStale(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.Create(root, "provider-sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	var observed session.Status
	var observedRevision uint64
	p := runtimeProvider(func(_ context.Context, _ provider.Request) (provider.Response, error) {
		loaded, err := st.Load(current.ID)
		if err != nil {
			t.Fatal(err)
		}
		observed = loaded.Status
		observedRevision = loaded.Revision
		return provider.Response{}, errors.New("stop after marker")
	})
	cfg := testSessionConfig(t, root, state, p)
	updated, _, err := runSessionTurn(context.Background(), state, st, current, cfg, "marker")
	if err == nil || observed != session.StatusInterrupted || observedRevision != 2 || updated.Status != session.StatusError {
		t.Fatalf("marker status=%s revision=%d updated=%s err=%v", observed, observedRevision, updated.Status, err)
	}
	resumed, err := st.Load(current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resumeNotice(resumed) == "" || len(resumed.Messages) != 0 {
		t.Fatalf("first-turn crash was not marked uncertain: rev=%d status=%s messages=%d", resumed.Revision, resumed.Status, len(resumed.Messages))
	}
	var noticeSeen bool
	resumeCfg := testSessionConfig(t, root, state, runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		if !strings.Contains(req.Messages[0].Content, sessionResumeNotice) {
			t.Fatal("resume notice missing from model instructions")
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "recovered"}}, nil
	}))
	resumeCfg.OnEvent = func(event agent.Event) {
		if event.Kind == "status" && strings.Contains(event.Text, "Do not replay tools") {
			noticeSeen = true
		}
	}
	if _, _, err := runSessionTurn(context.Background(), state, st, resumed, resumeCfg, "recover"); err != nil {
		t.Fatal(err)
	}
	if !noticeSeen {
		t.Fatal("resume notice status event was not emitted")
	}

	// A caller that loaded before another writer committed must be rejected
	// before the provider is allowed to perform side effects.
	stale, err := st.Create(root, "provider-sha256:stale")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := st.Save(stale, stale.Revision)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	staleCfg := testSessionConfig(t, root, state, runtimeProvider(func(_ context.Context, _ provider.Request) (provider.Response, error) {
		called = true
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "must not run"}}, nil
	}))
	_, _, err = runSessionTurn(context.Background(), state, st, stale, staleCfg, "stale")
	if !errors.Is(err, ErrSessionStale) || called {
		t.Fatalf("stale turn err=%v called=%v newer=%d", err, called, newer.Revision)
	}
}

func TestSessionListAndShowOutput(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	st, err := session.New(state)
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.Create(root, "provider-sha256:test")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runSession(options{stateDir: state, root: root}, []string{"list"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), created.ID.String()) {
		t.Fatalf("list output = %q", out.String())
	}
	out.Reset()
	if err := runSession(options{stateDir: state, root: root}, []string{"show", created.ID.String()}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), created.ID.String()) || !strings.Contains(out.String(), "provider-sha256:test") {
		t.Fatalf("show output = %q", out.String())
	}
}

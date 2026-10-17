package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"bifrost/internal/config"
	"bifrost/internal/provider"
	"bifrost/internal/ui"
)

func TestDesktopSharesRuntimeAndPersistentSessions(t *testing.T) {
	ctx := context.Background()
	root, state := t.TempDir(), t.TempDir()
	settings := config.DefaultSettings()
	settings.ActiveProvider = "alpha"
	settings.Providers = map[string]config.ProviderSettings{
		"alpha": {Kind: "chat-completions", BaseURL: "http://localhost:1", Model: "fixture"},
		"beta":  {Kind: "chat-completions", BaseURL: "http://localhost:1", Model: "fixture"},
	}
	if err := config.SaveSettings(filepath.Join(state, "config.json"), settings, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", state, "--base-url", "http://localhost:1", "--model", "fixture", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		found := false
		for _, tool := range req.Tools {
			if tool.Name == "viewer_open" {
				found = true
			}
		}
		if !found {
			t.Error("desktop is missing the shared viewer tools")
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "Shared answer"}}, nil
	})
	if err := d.Run(ctx, "Remember this", func(ui.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := d.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.WriteEnabled || snapshot.ExecEnabled {
		t.Fatal("desktop enabled permissions")
	}
	if len(snapshot.Sessions) != 1 || len(snapshot.History) != 2 {
		t.Fatalf("missing saved history: %+v", snapshot)
	}
	id := snapshot.Sessions[0].ID
	if err := d.SwitchAccount(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	switched, err := d.Snapshot(ctx)
	if err != nil || switched.Connection.Account != "beta" || len(switched.History) != 2 || d.chat.current.ID.String() != id {
		t.Fatalf("account switching lost conversation: %+v %v", switched, err)
	}
	if err = d.NewSession(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := d.Snapshot(ctx)
	if err != nil || len(fresh.History) != 0 || d.chat.current.ID.String() == id {
		t.Fatalf("new conversation resumed old history: %+v %v", fresh, err)
	}
	if err = d.Resume(ctx, id); err != nil {
		t.Fatal(err)
	}
	snapshot, err = d.Snapshot(ctx)
	if err != nil || snapshot.History[1].Text != "Shared answer" {
		t.Fatalf("resume: %+v %v", snapshot, err)
	}
	if _, err := d.Backend.List(ctx, ".."); err == nil {
		t.Fatal("root traversal accepted")
	}
	d.Close()
	// The unchanged CLI can read the same session after the GUI releases it.
	if _, _, err := invoke(t, "--root", root, "--state-dir", state, "session", "export", id); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopFailureReleasesWorkspace(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	args := []string{"--root", root, "--state-dir", state}
	if _, err := OpenDesktopWorkspace(context.Background(), append(args, "unexpected prompt"), io.Discard); err == nil {
		t.Fatal("prompt accepted")
	}
	d, err := OpenDesktopWorkspace(context.Background(), args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := os.WriteFile(filepath.Join(root, "example.txt"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	text, _, err := d.Backend.Read(context.Background(), "example.txt")
	if err != nil || text != "hello" {
		t.Fatalf("read %q %v", text, err)
	}
}

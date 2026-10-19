package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"bifrost/internal/config"
	"bifrost/internal/provider"
	"bifrost/internal/session"
)

func TestModelSwitchIndependentFromProviderAndPreservesHistory(t *testing.T) {
	ctx := context.Background()
	root, state := t.TempDir(), t.TempDir()
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "alpha"
	cfg.Providers = map[string]config.ProviderSettings{
		"alpha": {Kind: "chat-completions", BaseURL: "http://127.0.0.1:1", Model: "a-one"},
		"beta":  {Kind: "chat-completions", BaseURL: "http://127.0.0.1:1", Model: "b-one"},
	}
	path := filepath.Join(state, "config.json")
	if err := config.SaveSettings(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", state, "--profile", "alpha", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.chat.current.Messages = []provider.Message{{Role: provider.RoleUser, Content: "Explain this code"}, {Role: provider.RoleAssistant, Content: "Saved explanation"}}
	d.chat.current.Status = session.StatusCompleted
	d.chat.current, err = d.chat.store.Save(d.chat.current, d.chat.current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	id := d.chat.current.ID
	for _, step := range []struct{ account, model, want string }{
		{"alpha", "a-two", "a-two"}, {"beta", "", "b-one"}, {"beta", "b-two", "b-two"}, {"alpha", "", "a-two"},
	} {
		if step.model == "" {
			err = d.SwitchAccount(ctx, step.account)
		} else {
			err = d.SwitchModel(ctx, step.account, step.model)
		}
		if err != nil {
			t.Fatal(err)
		}
		if d.chat.setup.cfg.ProfileName != step.account || d.chat.setup.cfg.ModelName != step.want || d.chat.current.ID != id || len(d.chat.current.Messages) != 2 {
			t.Fatalf("switch lost provider/model/history: %+v, %s, %s", step, d.chat.setup.cfg.ModelName, d.chat.current.ID)
		}
	}
	// Reject before persistence: previously the model was saved even though
	// switching could fail, so returning to this provider silently selected it.
	d.chat.setup.sshName = "fixture"
	err = d.SwitchModel(ctx, "alpha", "must-not-save")
	d.chat.setup.sshName = ""
	if err == nil {
		t.Fatal("unsupported switch succeeded")
	}
	for _, step := range []struct{ account, model string }{{"missing", "bad"}, {"alpha", " "}} {
		if err := d.SwitchModel(ctx, step.account, step.model); err == nil {
			t.Fatal("invalid switch succeeded")
		}
	}
	saved, err := config.LoadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Providers["alpha"].Model != "a-two" || saved.Providers["beta"].Model != "b-two" || d.chat.setup.cfg.ModelName != "a-two" || d.chat.current.ID != id {
		t.Fatal("failed switch changed saved models or live conversation")
	}
}

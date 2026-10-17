package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bifrost/internal/accounts"
	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/provider"
	"bifrost/internal/session"
)

func isolatedAccountOptions(t *testing.T) options {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return options{root: root, stateDir: filepath.Join(base, "state")}
}

func TestAccountProfilesSwitchPreservesOneConversation(t *testing.T) {
	opts := isolatedAccountOptions(t)
	t.Setenv("FIRST_ROUTER_KEY", "first-secret")
	t.Setenv("SECOND_ROUTER_KEY", "second-secret")
	for _, entry := range []struct{ name, env string }{{"personal", "FIRST_ROUTER_KEY"}, {"work", "SECOND_ROUTER_KEY"}} {
		if err := loginAccount(context.Background(), opts, []string{"openrouter", entry.name, "--api-key-env", entry.env}, io.Discard, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	// Generic environment overrides must not hijack an explicitly saved account.
	t.Setenv("BIFROST_PROVIDER", "anthropic")
	t.Setenv("BIFROST_BASE_URL", "https://wrong.invalid")
	t.Setenv("BIFROST_MODEL", "wrong-model")
	setup, err := configureAgentSetup(context.Background(), opts, []string{"--profile", "openrouter-personal"}, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.cleanup()
	if setup.cfg.ModelName != "openrouter/auto" {
		t.Fatalf("environment replaced saved model: %s", setup.cfg.ModelName)
	}
	chat, err := newWorkspaceChat(context.Background(), opts, []string{"--profile", "openrouter-personal"}, setup, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer chat.close()
	personalID := chat.current.ID
	chat.current.Messages = []provider.Message{{Role: provider.RoleUser, Content: "private personal history"}, {Role: provider.RoleAssistant, Content: "personal answer"}}
	chat.current.Status = session.StatusCompleted
	chat.current, err = chat.store.Save(chat.current, chat.current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	history, err := chat.switchAccount(context.Background(), "openrouter-work")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || chat.current.ID != personalID {
		t.Fatal("account switch lost conversation")
	}
	workID := chat.current.ID
	history, err = chat.switchAccount(context.Background(), "openrouter-personal")
	if err != nil {
		t.Fatal(err)
	}
	if chat.current.ID != personalID || len(history) != 2 || history[0].Text != "private personal history" {
		t.Fatalf("did not restore account conversation: %#v", history)
	}
	if _, err := chat.switchAccount(context.Background(), "missing"); err == nil || chat.current.ID != personalID {
		t.Fatal("failed switch changed session")
	}
	if _, err := chat.switchAccount(context.Background(), "openrouter-work"); err != nil || chat.current.ID != workID {
		t.Fatalf("work conversation lost: %v", err)
	}
	if _, err := loadOrCreateSession(chat.store, personalID.String(), opts.root, setup.fingerprint); err == nil {
		t.Fatal("resumed conversation under different account")
	}
	t.Setenv("FIRST_ROUTER_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "wrong-account-secret")
	if _, err := chat.switchAccount(context.Background(), "openrouter-personal"); err != nil {
		t.Fatal("saved OpenRouter key should be used after its environment variable is unset")
	}
	store, err := accountStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := store.GetByName(accounts.KindOpenRouter, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClearCredentials(personal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chat.switchAccount(context.Background(), "openrouter-personal"); err == nil {
		t.Fatal("explicit account fell back to unrelated API key")
	}
}

func TestExternalLoginAndSessionBinding(t *testing.T) {
	opts := isolatedAccountOptions(t)
	script := filepath.Join(t.TempDir(), "fake-claude")
	const body = `#!/bin/sh
if [ "$1" = "auth" ]; then exit 0; fi
cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","session_id":"fixture-session","result":"fixture answer","usage":{"input_tokens":2,"output_tokens":3}}'
`
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := loginAccount(context.Background(), opts, []string{"claude", "work", "--executable", script}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	setup, err := configureAgentSetup(context.Background(), opts, nil, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.cleanup()
	if setup.cfg.RunExternal == nil || setup.cfg.Provider != nil {
		t.Fatal("external profile not wired")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := agent.Run(ctx, setup.cfg, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "fixture answer" || len(result.Messages) != 2 || len(result.Messages[1].Opaque) == 0 {
		t.Fatalf("missing external session result: %#v", result)
	}
	store, _ := accountStore(opts)
	a, err := store.GetByName(accounts.KindClaude, "work")
	if err != nil {
		t.Fatal(err)
	}
	a.Generation = "replacement-login"
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Run(ctx, setup.cfg, "stale login"); err == nil || !strings.Contains(err.Error(), "login changed") {
		t.Fatalf("replaced login reused: %v", err)
	}
}

func TestChatGPTTokenSourceRejectsReplacementAndLogout(t *testing.T) {
	store, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := accounts.Account{ID: "chatgpt-work", Name: "work", Kind: accounts.KindChatGPT, Subject: "subject", Issuer: "issuer", ClientID: "client"}
	if err := store.Save(a, accounts.Credentials{AccessToken: "test-token", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	a, err = store.Get(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	source := chatGPTTokenSource(store, a)
	if token, err := source(context.Background()); err != nil || token != "test-token" {
		t.Fatalf("token = %q, %v", token, err)
	}
	a.Generation = "replacement"
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	if _, err := source(context.Background()); err == nil {
		t.Fatal("accepted replacement login")
	}
	if err := store.ClearCredentials(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := chatGPTTokenSource(store, a)(context.Background()); err == nil {
		t.Fatal("accepted logged out credentials")
	}
}

func TestTUIStartsWhenOpenRouterKeyIsMissing(t *testing.T) {
	opts := isolatedAccountOptions(t)
	settings := config.DefaultSettings()
	settings.ActiveProvider = "openrouter-openrouter"
	settings.Defaults.Provider = settings.ActiveProvider
	settings.Providers = map[string]config.ProviderSettings{
		"openrouter-openrouter": {Kind: "openrouter", Model: "openrouter/auto", Account: "openrouter", APIKeyEnv: "OPENROUTER_API_KEY"},
	}
	path, err := opts.configFile()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.SaveSettings(path, settings, true); err != nil {
		t.Fatal(err)
	}
	store, err := accountStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(accounts.Account{ID: "openrouter-openrouter", Name: "openrouter", Kind: accounts.KindOpenRouter, APIKeyEnv: "OPENROUTER_API_KEY", Generation: "gen"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "")
	setup, err := configureAgentSetup(context.Background(), opts, nil, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.cleanup()
	if setup.cfg.Provider != nil || setup.startupNotice == "" {
		t.Fatalf("provider=%v notice=%q", setup.cfg.Provider != nil, setup.startupNotice)
	}
	if _, err := configureAgentSetup(context.Background(), opts, []string{"hello"}, io.Discard, true); err == nil {
		t.Fatal("headless run started without a key")
	}
}

func TestInvalidLoginDoesNotSaveAccount(t *testing.T) {
	opts := isolatedAccountOptions(t)
	t.Setenv("OPENROUTER_API_KEY", "fixture")
	if err := loginAccount(context.Background(), opts, []string{"openrouter", "work", "--executable", "invalid"}, io.Discard, io.Discard); err == nil {
		t.Fatal("accepted unsupported executable")
	}
	store, _ := accountStore(opts)
	items, err := store.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("invalid login saved accounts: %v %v", items, err)
	}
}

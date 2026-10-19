package cli

import (
	"bifrost/internal/accounts"
	"bifrost/internal/config"
	"bifrost/internal/ui"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestModelDiscoveryUsesSavedOpenRouterAccount(t *testing.T) {
	opts := isolatedAccountOptions(t)
	store, err := accountStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	a := accounts.Account{ID: "openrouter-personal", Name: "personal", Kind: accounts.KindOpenRouter, APIKeyEnv: "BIFROST_TEST_ROUTER_KEY"}
	if err := store.Save(a, accounts.Credentials{APIKey: "saved-fixture-key"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIFROST_TEST_ROUTER_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "unrelated-key")
	t.Setenv("BIFROST_API_KEY", "unrelated-key")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/models" || r.Header.Get("Authorization") != "Bearer saved-fixture-key" {
			t.Error("catalog did not use the saved account endpoint/credential")
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"vendor/model","name":"Available model","context_length":32768}]}`)
	}))
	defer server.Close()
	settings := config.DefaultSettings()
	settings.Providers = map[string]config.ProviderSettings{"renamed-profile": {Kind: "openrouter", Account: "personal", APIKeyEnv: a.APIKeyEnv, Model: "remembered-model", BaseURL: server.URL + "/api/v1"}}
	if err := config.SaveSettings(filepath.Join(opts.stateDir, "config.json"), settings, false); err != nil {
		t.Fatal(err)
	}
	chat := &workspaceChat{ctx: context.Background(), opts: opts}
	models, current, err := chat.discoverAccountModels(context.Background(), ui.AccountAddRequest{Name: "renamed-profile", Kind: "openrouter"}, false)
	if err != nil || len(models) != 1 || models[0].Slug != "vendor/model" || models[0].ContextLength != 32768 || current != "remembered-model" {
		t.Fatalf("models=%v current=%s err=%v", models, current, err)
	}
	if err := store.ClearCredentials(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := chat.discoverAccountModels(context.Background(), ui.AccountAddRequest{Name: "renamed-profile", Kind: "openrouter"}, false); err == nil {
		t.Fatal("missing account credential silently fell back")
	}
	if calls != 1 {
		t.Fatalf("unexpected catalog calls: %d", calls)
	}
}

func TestDiscoveryAccountResolvesProfileAliasesWithoutDoublePrefix(t *testing.T) {
	store, err := accounts.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []accounts.Kind{accounts.KindCursorNative, accounts.KindChatGPT, accounts.KindClaude, accounts.KindCursor} {
		id, _ := accounts.StableID(kind, "work")
		if err := store.Save(accounts.Account{ID: id, Name: "work", Kind: kind}); err != nil {
			t.Fatal(err)
		}
		for _, req := range []struct{ profile, account string }{{id, ""}, {"work", ""}, {"renamed-profile", "work"}, {id, "work"}} {
			got, err := discoveryAccount(store, string(kind), req.profile, req.account)
			if err != nil || got.ID != id {
				t.Fatalf("%s %v: %v %v", kind, req, got, err)
			}
		}
		if _, err := discoveryAccount(store, string(kind), id, "missing"); err == nil {
			t.Fatal("explicit missing account fell back to another identity")
		}
	}
}

func TestModelDiscoveryUsesDefaultEnvironmentWithoutChangingIt(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "default-fixture-key")
	t.Setenv("BIFROST_API_KEY", "")
	opts := isolatedAccountOptions(t)
	chat := &workspaceChat{ctx: context.Background(), opts: opts}
	key, err := chat.catalogAPIKey(ui.AccountAddRequest{}, "responses", "")
	if err != nil || key != "default-fixture-key" {
		t.Fatal("default provider environment ignored")
	}
	key, err = chat.catalogAPIKey(ui.AccountAddRequest{RawAPIKey: "form-fixture-key"}, "responses", "")
	if err != nil || key != "form-fixture-key" {
		t.Fatal("explicit form key ignored")
	}
	key, err = chat.catalogAPIKey(ui.AccountAddRequest{}, "responses", "")
	if err != nil || key != "default-fixture-key" {
		t.Fatal("discovery replaced shared environment")
	}
}

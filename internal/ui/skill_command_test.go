package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestSkillInstallPreviewsBeforeInstallingWithDefaultAndExplicitScopes(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
		scope string
	}{
		{name: "default", input: "skill install catalog/one", scope: "project"},
		{name: "project", input: "skill install catalog/one --scope project", scope: "project"},
		{name: "global", input: "skill install catalog/one --scope global", scope: "global"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
				Preview: func(_ context.Context, id string) (string, error) {
					calls = append(calls, "preview:"+id)
					return "reviewed", nil
				},
				Install: func(_ context.Context, id, scope string) (string, error) {
					calls = append(calls, "install:"+id+":"+scope)
					return "catalog/one", nil
				},
			}))
			cmd := m.executePaletteCommand(test.input)
			if cmd == nil {
				t.Fatal("valid skill install did not schedule a command")
			}
			if got := cmd(); got == nil {
				t.Fatal("skill install command returned no message")
			} else {
				_, _ = m.Update(got)
			}
			want := []string{"preview:catalog/one", "install:catalog/one:" + test.scope}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("callback order=%v, want %v", calls, want)
			}
			if m.marketplacePreviewID != "catalog/one" || !m.marketplaceOpen {
				// A successful install intentionally keeps the marketplace preview
				// open so its result remains reviewable.
				t.Fatalf("skill install modal state: open=%v preview=%q", m.marketplaceOpen, m.marketplacePreviewID)
			}
		})
	}
}

func TestSkillInstallPreviewFailurePreventsInstall(t *testing.T) {
	installed := false
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		Preview: func(context.Context, string) (string, error) { return "", errors.New("catalog unavailable") },
		Install: func(context.Context, string, string) (string, error) {
			installed = true
			return "", nil
		},
	}))
	cmd := m.executePaletteCommand("skill install catalog/one")
	if cmd == nil {
		t.Fatal("valid skill install did not schedule a command")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if installed {
		t.Fatal("install ran after preview failed")
	}
	if !strings.Contains(m.marketplaceError, "preview") || !strings.Contains(m.marketplaceError, "catalog unavailable") {
		t.Fatalf("preview failure not shown: %q", m.marketplaceError)
	}
}

func TestSkillInstallCancellationBetweenPreviewAndInstall(t *testing.T) {
	installed := false
	var m *Model
	m = New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		Preview: func(context.Context, string) (string, error) {
			// Simulate a provider that ignores cancellation and returns a
			// successful preview after the user closes/replaces the request.
			m.marketplaceCancel()
			return "reviewed", nil
		},
		Install: func(context.Context, string, string) (string, error) {
			installed = true
			return "", nil
		},
	}))
	cmd := m.executePaletteCommand("skill install catalog/one")
	if cmd == nil {
		t.Fatal("valid skill install did not schedule a command")
	}
	msg := cmd()
	_, _ = m.Update(msg)
	if installed {
		t.Fatal("install ran after cancellation between preview and install")
	}
	if !strings.Contains(m.marketplaceError, "preview canceled") {
		t.Fatalf("cancellation error not shown: %q", m.marketplaceError)
	}
}

func TestSkillInstallMalformedScopeDoesNotCallMarketplace(t *testing.T) {
	called := false
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		Preview: func(context.Context, string) (string, error) { called = true; return "", nil },
		Install: func(context.Context, string, string) (string, error) { called = true; return "", nil },
	}))
	for _, input := range []string{
		"skill install catalog/one --scope workspace",
		"skill install catalog/one --scope=global",
		"skill install catalog/one --unknown flag",
	} {
		if cmd := m.executePaletteCommand(input); cmd != nil {
			t.Fatalf("malformed command %q scheduled a command", input)
		}
	}
	if called {
		t.Fatal("malformed scope called marketplace")
	}
	if m.status != skillCommandUsage {
		t.Fatalf("malformed scope status=%q, want %q", m.status, skillCommandUsage)
	}
}

func TestSkillCompletionUsesLoadedIDsAndScopes(t *testing.T) {
	m := New(t.TempDir())
	m.marketplaceEntries = []MarketplaceEntry{{ID: "catalog/one"}, {ID: "other/two"}}
	if got := suggestionTexts(m.paletteSuggestions("skill install ")); !reflect.DeepEqual(got, []string{"skill install catalog/one", "skill install other/two"}) {
		t.Fatalf("skill ID completions=%v", got)
	}
	if got := suggestionTexts(m.paletteSuggestions("skill install cat")); !reflect.DeepEqual(got, []string{"skill install catalog/one"}) {
		t.Fatalf("filtered skill ID completions=%v", got)
	}
	if got := suggestionTexts(m.paletteSuggestions("skill install catalog/one --scope ")); !reflect.DeepEqual(got, []string{"skill install catalog/one --scope project", "skill install catalog/one --scope global"}) {
		t.Fatalf("scope completions=%v", got)
	}
	for _, prefix := range []string{"--s", "--sc"} {
		input := "skill install catalog/one " + prefix
		want := []string{"skill install catalog/one --scope "}
		if got := suggestionTexts(m.paletteSuggestions(input)); !reflect.DeepEqual(got, want) {
			t.Fatalf("partial scope completion for %q=%v, want %v", prefix, got, want)
		}
	}
	if got := suggestionTexts(m.paletteSuggestions("skill install catalog/one --scope p")); !reflect.DeepEqual(got, []string{"skill install catalog/one --scope project"}) {
		t.Fatalf("partial scope value completion=%v", got)
	}
	if got := suggestionTexts(m.paletteSuggestions("skill browse")); !reflect.DeepEqual(got, []string{"skill browse"}) {
		t.Fatalf("browse completion=%v", got)
	}
}

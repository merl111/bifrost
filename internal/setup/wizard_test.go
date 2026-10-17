package setup

import (
	"bifrost/internal/config"
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
)

func TestWizardOfflineReviewAndSave(t *testing.T) {
	w := New(config.DefaultSettings(), t.TempDir())
	w.project = "demo"
	for i := 0; i < 4; i++ {
		w.next()
	}
	if w.step != 5 || w.done {
		t.Fatalf("premature finish: %+v", w)
	}
	if !strings.Contains(w.View().Content, "Ready to build") {
		t.Fatal("missing review")
	}
	w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !w.done || w.result.Settings.Defaults.AllowExec || w.result.Settings.ActiveProvider != "" {
		t.Fatal("unsafe offline defaults")
	}
}
func TestWizardPreservesCustomProviderAndCancellation(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "work"
	cfg.Providers["work"] = config.ProviderSettings{Kind: "responses", Model: "chosen", BaseURL: "https://example.com/api", APIKeyEnv: "WORK_KEY"}
	w := New(cfg, t.TempDir())
	w.next()
	if w.url != "https://example.com/api" || w.model != "chosen" || w.keyEnv != "WORK_KEY" {
		t.Fatal("overwrote configured provider")
	}
	w.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if !w.cancelled || w.done {
		t.Fatal("escape saved configuration")
	}
}

func TestWizardKeepsActiveCustomProfileOnFinish(t *testing.T) {
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "work"
	cfg.Providers["work"] = config.ProviderSettings{Kind: "responses", Model: "chosen", BaseURL: "https://example.com/api", APIKeyEnv: "WORK_KEY"}
	w := New(cfg, t.TempDir())
	for w.step < 5 {
		w.next()
	}
	if err := w.finish(); err != nil {
		t.Fatal(err)
	}
	if w.result.Settings.ActiveProvider != "work" {
		t.Fatalf("custom profile was renamed to %q", w.result.Settings.ActiveProvider)
	}
	if _, ok := w.result.Settings.Providers["work"]; !ok {
		t.Fatal("custom profile disappeared")
	}
}
func TestWizardRejectsCredentialsAndFiltersControls(t *testing.T) {
	w := New(config.DefaultSettings(), t.TempDir())
	w.step = 1
	w.kind = "responses"
	w.model = "m"
	w.keyEnv = "sk-secret!"
	w.next()
	if w.step != 1 || w.problem == "" {
		t.Fatal("accepted key literal")
	}
	w.field = 0
	w.model = ""
	w.insert("safe\x1b\ntext")
	if w.model != "safetext" {
		t.Fatal(w.model)
	}
}

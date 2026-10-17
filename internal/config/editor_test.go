package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditFieldsReflectDefaultsAndProfiles(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Providers["work.local"] = ProviderSettings{Kind: "responses", Model: "model"}
	fields := EditFields(cfg)
	have := make(map[string]EditField, len(fields))
	for _, field := range fields {
		have[field.Key] = field
		if field.Description == "" {
			t.Fatalf("empty description for %s", field.Key)
		}
	}
	for _, key := range []string{"active_provider", "defaults.max_steps", "defaults.allow_exec", "providers", "providers.work.local", "providers.work.local.model", "hooks", "ui.theme"} {
		if _, ok := have[key]; !ok {
			t.Fatalf("missing editable field %q", key)
		}
	}
	if _, ok := have["schema_version"]; ok {
		t.Fatal("schema_version must not be editable")
	}
	if have["defaults.max_steps"].Value != "8" || have["defaults.max_steps"].Kind != EditKindInt {
		t.Fatalf("unexpected max_steps field: %#v", have["defaults.max_steps"])
	}
	if have["providers"].Kind != EditKindJSON || have["providers.work.local.model"].Kind != EditKindString {
		t.Fatalf("unexpected provider field kinds: %#v %#v", have["providers"], have["providers.work.local.model"])
	}
	if !strings.Contains(have["providers.work.local.api_key_env"].Description, "never paste") {
		t.Fatalf("credential guidance missing: %q", have["providers.work.local.api_key_env"].Description)
	}
	if !strings.Contains(have["ui.composer_max_rows"].Description, "1..20") || !strings.Contains(have["ui.newline_key"].Description, "shift+enter") {
		t.Fatalf("UI field guidance missing: %#v %#v", have["ui.composer_max_rows"], have["ui.newline_key"])
	}
}

func TestSetValueResolvesExactDottedMapKeyBeforeNestedLeaf(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Providers["foo"] = ProviderSettings{Kind: "responses", Model: "short"}
	cfg.Providers["foo.bar"] = ProviderSettings{Kind: "responses", Model: "dotted"}
	if err := SetValue(&cfg, "providers.foo.bar", `{"kind":"responses","model":"exact"}`); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["foo"].Model != "short" || cfg.Providers["foo.bar"].Model != "exact" {
		t.Fatalf("exact dotted key resolution failed: %#v", cfg.Providers)
	}
	if err := SetValue(&cfg, "providers.foo.bar.model", "nested"); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["foo.bar"].Model != "nested" || cfg.Providers["foo"].Model != "short" {
		t.Fatalf("nested dotted key resolution failed: %#v", cfg.Providers)
	}
}

func TestSetValueStrictAtomicAndLayoutReset(t *testing.T) {
	cfg := DefaultSettings()
	cfg.UI.PaneProportions = [3]int{20, 30, 50}
	visible := true
	cfg.UI.TerminalVisible = &visible
	cfg.UI.TerminalPercent = 25
	before := cfg
	if err := SetValue(&cfg, "providers.local", `{"kind":"responses","model":"m"}`); err != nil {
		t.Fatal(err)
	}
	if err := SetValue(&cfg, "providers.local.model", "next"); err != nil {
		t.Fatal(err)
	}
	if cfg.Providers["local"].Model != "next" {
		t.Fatalf("model=%q", cfg.Providers["local"].Model)
	}
	if err := SetValue(&cfg, "providers.local", `{"kind":"responses","model":"m","unknown":true}`); err == nil {
		t.Fatal("unknown structured field accepted")
	}
	if cfg.Providers["local"].Model != "next" {
		t.Fatal("failed SetValue mutated the config")
	}
	if err := SetValue(&cfg, "unknown.setting", "value"); err == nil {
		t.Fatal("unknown key accepted")
	}
	if cfg.UI.PaneProportions != before.UI.PaneProportions || cfg.UI.TerminalVisible == nil || cfg.UI.TerminalPercent != before.UI.TerminalPercent {
		t.Fatal("failed edits changed unrelated layout state")
	}
	if err := SetValue(&cfg, "ui.layout", "conversation"); err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Layout != "conversation" || cfg.UI.PaneProportions != [3]int{} || cfg.UI.TerminalVisible != nil || cfg.UI.TerminalPercent != 0 {
		t.Fatalf("layout overrides not cleared: %#v", cfg.UI)
	}
}

func TestRevisionLoadSaveAndConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg, revision, err := LoadSettingsRevision(path)
	if err != nil || revision != MissingRevision || cfg.SchemaVersion != 1 {
		t.Fatalf("missing load cfg=%#v revision=%q err=%v", cfg, revision, err)
	}
	if err := SaveSettingsRevision(path, cfg, revision); err != nil {
		t.Fatal(err)
	}
	loaded, revision2, err := LoadSettingsRevision(path)
	if err != nil || revision2 == revision || loaded.SchemaVersion != 1 {
		t.Fatalf("saved load revision=%q old=%q err=%v", revision2, revision, err)
	}
	if err := SaveSettingsRevision(path, cfg, revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale save err=%v", err)
	}
	if err := SaveSettingsRevision(path, cfg, revision2); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("saved data err=%v data=%q", err, data)
	}
}

func TestLoadSettingsRevisionBoundsAndRejectsNonRegular(t *testing.T) {
	root := t.TempDir()
	large := filepath.Join(root, "large.json")
	if err := os.WriteFile(large, []byte(strings.Repeat("x", (1<<20)+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSettingsRevision(large); err == nil || !strings.Contains(err.Error(), "exceeds 1 MiB") {
		t.Fatalf("oversized config err=%v", err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadSettingsRevision(directory); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory config err=%v", err)
	}
}

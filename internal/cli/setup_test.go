package cli

import (
	"bifrost/internal/config"
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCreatesOfflineProjectAndRefusesOverwrite(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	var out bytes.Buffer
	args := []string{"--state-dir", state, "--root", root, "setup", "--non-interactive", "--provider", "offline", "--name", "demo"}
	if err := Run(context.Background(), args, &out, &out); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadSettings(filepath.Join(state, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Defaults.Project != "demo" || cfg.Defaults.AllowExec {
		t.Fatal(cfg)
	}
	if err := Run(context.Background(), args, &out, &out); err == nil {
		t.Fatal("setup overwrote config")
	}
	opts, err := applyProjectDefault(options{stateDir: state})
	if err != nil || opts.project != "demo" {
		t.Fatal(opts, err)
	}
}
func TestConfigSetStrictProfileAndNoLiteralKey(t *testing.T) {
	cfg := config.DefaultSettings()
	if err := setConfigValue(&cfg, "providers.my.local", `{"kind":"responses","model":"m","api_key_env":"WORK_KEY"}`); err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Providers["my.local"]; !ok {
		t.Fatal("dotted profile lost")
	}
	if err := setConfigValue(&cfg, "providers.bad", `{"kind":"responses","model":"m","api_key":"secret"}`); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatal(err)
	}
}

func TestConfigSetProviderIconMode(t *testing.T) {
	cfg := config.DefaultSettings()
	if err := setConfigValue(&cfg, "ui.icons", "ascii"); err != nil {
		t.Fatal(err)
	}
	if cfg.UI.Icons != "ascii" {
		t.Fatalf("ui.icons = %q", cfg.UI.Icons)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/config"
)

func TestSettingsUIStagesPersistsAndRejectsConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	callbacks, err := settingsUICallbacks(options{configPath: path})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := callbacks.Load()
	if err != nil {
		t.Fatal(err)
	}
	doc, err = callbacks.Edit(doc, "ui.newline_key", "shift+enter")
	if err != nil {
		t.Fatal(err)
	}
	doc, err = callbacks.Edit(doc, "ui.composer_max_rows", "12")
	if err != nil {
		t.Fatal(err)
	}
	doc, apply, err := callbacks.Save(doc)
	if err != nil || len(apply) == 0 {
		t.Fatalf("save: %v", err)
	}
	saved, err := config.LoadSettings(path)
	if err != nil || saved.UI.ComposerMaxRows != 12 || saved.UI.NewlineKey != "shift+enter" {
		t.Fatalf("saved: %+v %v", saved.UI, err)
	}
	oldDoc := doc
	doc, err = callbacks.Edit(doc, "ui.theme", "light")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = callbacks.Save(doc); err != nil {
		t.Fatal(err)
	}
	if _, _, err = callbacks.Save(oldDoc); err == nil || !strings.Contains(err.Error(), "changed on disk") {
		t.Fatalf("stale save: %v", err)
	}
	saved, _ = config.LoadSettings(path)
	if saved.UI.Theme != "light" {
		t.Fatal("stale draft overwrote saved theme")
	}
}

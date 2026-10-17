package ui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func settingsTestModel(t *testing.T) *Model {
	t.Helper()
	doc := SettingsDocument{Path: "/tmp/config.json", Revision: "first", Fields: []SettingsField{
		{Key: "defaults.allow_exec", Value: "false", Kind: "bool", Description: "Allow commands after restart."},
		{Key: "ui.theme", Value: "midnight", Kind: "string", Description: "Choose midnight, light, or plain. Applies on save."},
	}}
	m := New(t.TempDir(), WithSettings(SettingsCallbacks{
		Load: func() (SettingsDocument, error) { return doc, nil },
		Edit: func(d SettingsDocument, key, value string) (SettingsDocument, error) {
			if key == "ui.theme" && value != "light" && value != "midnight" {
				return d, errors.New("theme must be midnight or light")
			}
			d.Fields = append([]SettingsField(nil), d.Fields...)
			for i := range d.Fields {
				if d.Fields[i].Key == key {
					d.Fields[i].Value = value
				}
			}
			return d, nil
		},
		Save: func(d SettingsDocument) (SettingsDocument, []Option, error) {
			d.Revision = "saved"
			return d, []Option{WithTheme("light")}, nil
		},
	}))
	cmd := m.openSettings()
	m.Update(cmd())
	return m
}

func TestSettingsStagingValidationSaveAndDiscard(t *testing.T) {
	m := settingsTestModel(t)
	m.settingsKey("/", "")
	m.settingsKey("", "ui.theme")
	m.settingsKey("enter", "")
	m.settingsKey("enter", "")
	m.settingsKey("ctrl+u", "")
	m.settingsPaste("wrong")
	m.settingsKey("enter", "")
	if !m.settingsEditor.editing || m.settingsEditor.err == "" || m.settingsEditor.dirty {
		t.Fatal("invalid edit should stay open without changing draft")
	}
	m.settingsKey("ctrl+u", "")
	m.settingsPaste("light")
	m.settingsKey("enter", "")
	if !m.settingsEditor.dirty || m.theme != "midnight" {
		t.Fatal("staging applied before save")
	}
	m.settingsKey("esc", "")
	if !m.settingsOpen || !m.settingsEditor.discard {
		t.Fatal("dirty close needs discard confirmation")
	}
	cmd := m.settingsKey("ctrl+s", "")
	if cmd == nil {
		t.Fatal("save not scheduled")
	}
	m.Update(cmd())
	if m.settingsEditor.dirty || m.theme != "light" || m.settingsEditor.document.Revision != "saved" {
		t.Fatal("save did not apply appearance")
	}
	m.settingsKey("esc", "")
	if m.settingsOpen {
		t.Fatal("clean settings did not close")
	}
}

func TestSettingsModalPasteAndStaleSave(t *testing.T) {
	m := settingsTestModel(t)
	m.chatInput, m.chatEditing, m.focus = "chat draft", true, focusChat
	m.Update(tea.PasteMsg{Content: "must not enter chat"})
	if m.chatInput != "chat draft" {
		t.Fatal("settings leaked paste into composer")
	}
	m.settingsKey("space", "")
	if !m.settingsEditor.dirty || m.settingsEditor.document.Fields[0].Value != "true" {
		t.Fatal("boolean toggle failed")
	}
	m.updateSettingsSaved(settingsSavedMsg{id: 999, apply: []Option{WithTheme("light")}})
	if m.theme != "midnight" {
		t.Fatal("stale save changed appearance")
	}
	m.settingsKey("esc", "")
	m.settingsKey("esc", "")
	if m.settingsOpen || m.chatInput != "chat draft" {
		t.Fatal("discard lost chat or failed to close")
	}
}

func TestSettingsSaveFailureRetainsDraftAndFits(t *testing.T) {
	m := settingsTestModel(t)
	m.settingsKey("space", "")
	m.updateSettingsSaved(settingsSavedMsg{id: m.settingsEditor.id, err: errors.New("Config changed on disk")})
	if !m.settingsEditor.dirty || m.settingsEditor.err == "" {
		t.Fatal("failed save lost draft")
	}
	for _, size := range [][2]int{{40, 16}, {80, 24}, {120, 36}} {
		m.width, m.height = size[0], size[1]
		for _, editing := range []bool{false, true} {
			m.settingsEditor.editing = editing
			m.settingsEditor.input = strings.Repeat("世界 ✨ field value ", 40)
			m.settingsEditor.cursor = len([]rune(m.settingsEditor.input))
			rows := strings.Split(m.render(), "\n")
			if len(rows) != m.height {
				t.Fatalf("height %dx%d: %d", m.width, m.height, len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) != m.width {
					t.Fatalf("width %dx%d: %d", m.width, m.height, ansi.StringWidth(row))
				}
			}
		}
	}
}

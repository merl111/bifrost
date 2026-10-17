package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/config"
)

func TestWriteUXSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_UI_SNAPSHOTS") != "1" {
		t.Skip("opt-in render evidence")
	}
	m := New(t.TempDir(), WithLayout("focus"), WithConnectionInfo(ConnectionInfo{Provider: "chatgpt", Account: "personal", Model: "dev-model", Connected: true, Location: "local"}))
	m.width, m.height = 120, 36
	m.focus, m.chatEditing = focusChat, true
	m.chatInput = "Please update the settings screen so I can change every option without leaving Bifrost.\n\nKeep the existing project configuration, add validation messages, and make the composer grow as I type. ✨"
	m.appendChatEvent(ChatEvent{Kind: "assistant", Text: "## Ready to build\n\nDescribe the change below. **Shift+Enter** adds a new line; the message box grows with your draft."})
	outputs := map[string]string{"composer-expanded-120x36.ansi": m.render()}
	m.chatEditing = false
	m.paletteOpen, m.paletteInput = true, "layout "
	outputs["commands-120x36.ansi"] = m.render()
	m.paletteOpen = false
	m.settingsOpen = true
	m.settingsEditor.document.Path = "~/.config/bifrost/config.json"
	cfg := config.DefaultSettings()
	cfg.UI.ComposerMaxRows = 8
	cfg.UI.NewlineKey = "shift+enter"
	for _, f := range config.EditFields(cfg) {
		m.settingsEditor.document.Fields = append(m.settingsEditor.document.Fields, SettingsField{f.Key, f.Value, f.Kind, f.Description})
	}
	m.settingsEditor.query = "ui."
	for i, f := range m.settingsFields() {
		if f.Key == "ui.composer_max_rows" {
			m.settingsEditor.selected = i
		}
	}
	outputs["settings-120x36.ansi"] = m.render()
	m.settingsOpen = false
	m.marketplace = MarketplaceCallbacks{List: func(context.Context, string) ([]MarketplaceEntry, error) {
		return []MarketplaceEntry{
			{ID: "openai-skill-creator", Name: "Skill Creator", Description: "Create reusable agent skills with focused instructions, supporting resources, and validation.", Source: "github.com/openai/skills · pinned revision"},
			{ID: "openai-cli-creator", Name: "CLI Creator", Description: "Create robust command-line interfaces and CLI tools.", Source: "github.com/openai/skills · pinned revision"},
			{ID: "openai-define-goal", Name: "Define Goal", Description: "Turn an ambiguous request into a concrete, testable goal.", Source: "github.com/openai/skills · pinned revision"},
		}, nil
	}}
	m.Update(m.startMarketplace()())
	outputs["marketplace-120x36.ansi"] = m.render()
	m.marketplaceSearching = true
	m.marketplaceQuery = "skill"
	outputs["marketplace-search-120x36.ansi"] = m.render()
	m.marketplaceQuery = ""
	m.marketplaceSearching = false
	m.marketplacePreviewID = "openai-skill-creator"
	m.marketplaceInstallScope = "preview-ready"
	m.marketplacePreview = "Create focused instructions that agents can reuse across projects.\n\n## Before installing\n\nReview the repository and pinned revision. Installation adds the skill to your library; activation is a separate choice.\n\n## Included\n\n- A reusable SKILL.md template\n- Validation guidance\n- Supporting-resource conventions"
	outputs["marketplace-preview-120x36.ansi"] = m.render()
	for _, width := range []int{80, 36} {
		m.width, m.height = width, 24
		m.marketplacePreviewID = ""
		outputs[fmt.Sprintf("marketplace-%dx24.ansi", width)] = m.render()
		m.marketplacePreviewID = "openai-skill-creator"
		outputs[fmt.Sprintf("marketplace-preview-%dx24.ansi", width)] = m.render()
	}
	dir := filepath.Join("testdata", "snapshots")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range outputs {
		wantRows := 35
		if strings.HasSuffix(name, "x24.ansi") {
			wantRows = 23
		}
		if strings.Count(content, "\n") != wantRows {
			t.Fatalf("%s wrong height", name)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

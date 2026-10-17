package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bifrost/internal/config"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type settingsView struct {
	query, list, name, description, value, save, stage, state cui.Widget
	scalar, structured, toggle, spacer                        cui.Widget
	document                                                  ui.SettingsDocument
	fields                                                    []ui.SettingsField
	key                                                       string
	pending                                                   map[string]string
	loaded, busy, dirty                                       bool
}

func (g *desktop) buildSettingsPanel(parent cui.Widget) {
	s := &g.workbench.settings
	s.pending = make(map[string]string)
	bar := parent.Box(cui.Horizontal, 8)
	s.state = label(bar, "Settings use the same schema and conflict checks as the TUI.", 11, muted)
	s.state.Expand(true)
	button(bar, "Discard & reload", func() {
		if s.busy {
			return
		}
		if !s.dirty && len(s.pending) == 0 {
			g.loadSettings()
			return
		}
		g.window.Alert("Discard staged settings?", "Your saved configuration will stay unchanged.", "Discard", func(result int, _ string) {
			if result == cui.DialogAccepted {
				g.loadSettings()
			}
		})
	})
	s.save = button(bar, "Save settings · Ctrl+S", g.saveSettings)
	style(s.save, violet, background, 8, false, 8)
	split := parent.Split(cui.Horizontal, .35)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(8)
	right.Padding(12)
	s.query = left.Search("")
	s.query.Placeholder("Search settings…")
	s.query.OnAction(func(cui.Widget) { g.filterSettings() })
	s.list = left.List(nil)
	s.list.Expand(true)
	s.list.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i < 0 || i >= len(s.fields) {
			return
		}
		g.selectSetting(s.fields[i])
	})
	s.name = label(right, "Select a setting", 15, foreground)
	s.description = label(right, "Choose a setting to edit its value.", 11, muted)
	s.scalar = right.Entry("")
	s.structured = right.Code("")
	s.structured.ReadOnly(false)
	s.structured.Expand(true)
	s.toggle = right.Checkbox("Enabled", false)
	s.toggle.OnAction(func(w cui.Widget) {
		s.scalar.SetText(strconv.FormatBool(w.Checked()))
		g.rememberSetting()
	})
	for _, field := range []cui.Widget{s.scalar, s.structured} {
		field.OnAction(func(cui.Widget) { g.rememberSetting() })
		style(field, surface, foreground, 8, true, 10)
	}
	s.value = s.scalar
	s.scalar.SetVisible(false)
	s.structured.SetVisible(false)
	s.toggle.SetVisible(false)
	s.stage = button(right, "Stage this value", g.stageSetting)
	label(right, "Save validates and applies all edited values. Staging is optional. Runtime settings take effect after restart.", 10, muted)
	s.spacer = right.Box(cui.Vertical, 0)
	s.spacer.Expand(true)
}

func (g *desktop) rememberSetting() {
	s := &g.workbench.settings
	if s.busy || s.key == "" {
		return
	}
	s.pending[s.key] = s.value.Text()
	for _, field := range s.document.Fields {
		if field.Key == s.key && field.Value == s.value.Text() {
			delete(s.pending, s.key)
			break
		}
	}
	s.save.SetEnabled(s.dirty || len(s.pending) > 0)
	if len(s.pending) > 0 {
		s.state.SetText("Unsaved changes · Ctrl+S validates and saves all edits")
	} else if s.dirty {
		s.state.SetText("Staged changes · save to apply")
	} else {
		s.state.SetText("Saved configuration · " + s.document.Path)
	}
}

func (g *desktop) setSettingsBusy(busy bool) {
	s := &g.workbench.settings
	s.busy = busy
	s.scalar.SetEnabled(!busy)
	s.structured.ReadOnly(busy)
	s.toggle.SetEnabled(!busy)
	s.stage.SetEnabled(!busy)
	s.save.SetEnabled(!busy && (s.dirty || len(s.pending) > 0))
}

func (g *desktop) selectSetting(f ui.SettingsField) {
	s := &g.workbench.settings
	g.rememberSetting()
	s.key = f.Key
	s.name.SetText(f.Key + " · " + f.Kind)
	s.description.SetText(f.Description)
	value := f.Value
	if pending, ok := s.pending[f.Key]; ok {
		value = pending
	}
	structured := f.Kind == config.EditKindJSON || strings.Contains(value, "\n")
	boolean := f.Kind == config.EditKindBool
	s.value = s.scalar
	if structured {
		s.value = s.structured
	}
	s.scalar.SetVisible(!structured && !boolean)
	s.structured.SetVisible(structured)
	s.toggle.SetVisible(boolean)
	s.spacer.SetVisible(!structured)
	s.toggle.SetChecked(value == "true")
	s.value.SetText(value)
}

func (g *desktop) loadSettings() {
	s := &g.workbench.settings
	if s.busy {
		return
	}
	g.setSettingsBusy(true)
	callbacks := g.ws.Settings
	g.work(func(context.Context) func() {
		doc, err := callbacks.Load()
		return func() {
			g.setSettingsBusy(false)
			if err != nil {
				g.workbenchNotice("Could not load settings", err)
				return
			}
			s.document = doc
			s.loaded = true
			s.dirty = false
			clear(s.pending)
			s.key = ""
			s.state.SetText("Saved configuration · " + doc.Path)
			s.value.SetText("")
			s.scalar.SetVisible(false)
			s.structured.SetVisible(false)
			s.toggle.SetVisible(false)
			g.filterSettings()
		}
	})
}

func (g *desktop) filterSettings() {
	s := &g.workbench.settings
	query := strings.ToLower(s.query.Text())
	s.fields = nil
	var names []string
	for _, f := range s.document.Fields {
		if strings.Contains(strings.ToLower(f.Key+" "+f.Description), query) {
			s.fields = append(s.fields, f)
			names = append(names, f.Key)
		}
	}
	s.list.Items(names)
	s.save.SetEnabled((s.dirty || len(s.pending) > 0) && !s.busy)
}

func (g *desktop) stageSetting() {
	s := &g.workbench.settings
	if s.busy || s.key == "" {
		return
	}
	doc, err := g.ws.Settings.Edit(s.document, s.key, s.value.Text())
	if err != nil {
		g.workbenchNotice("Invalid setting — value retained", err)
		return
	}
	s.document = doc
	delete(s.pending, s.key)
	s.dirty = true
	s.state.SetText("Staged changes · save to apply")
	g.filterSettings()
}

func (g *desktop) saveSettings() {
	s := &g.workbench.settings
	if s.busy {
		return
	}
	g.rememberSetting()
	if !s.dirty && len(s.pending) == 0 {
		return
	}
	doc, err := stageSettingsEdits(s.document, s.pending, g.ws.Settings.Edit)
	if err != nil {
		g.workbenchNotice("Invalid settings — all edits retained", err)
		return
	}
	s.document = doc
	s.dirty = true
	clear(s.pending)
	g.setSettingsBusy(true)
	callbacks := g.ws.Settings
	g.work(func(context.Context) func() {
		saved, _, err := callbacks.Save(doc)
		return func() {
			g.setSettingsBusy(false)
			if err == nil {
				s.document = saved
				s.dirty = false
				s.state.SetText("Settings saved · runtime changes require restart")
				var cfg config.Settings
				if json.Unmarshal([]byte(saved.Data), &cfg) == nil {
					g.applyAppearance(cfg.UI)
				}
				for _, field := range saved.Fields {
					if field.Key == s.key {
						s.key = ""
						g.selectSetting(field)
						break
					}
				}
			}
			g.filterSettings()
			g.workbenchNotice("Settings saved", err)
		}
	})
}

// Validate every pending value before replacing the staged document or saving.
func stageSettingsEdits(doc ui.SettingsDocument, pending map[string]string, edit func(ui.SettingsDocument, string, string) (ui.SettingsDocument, error)) (ui.SettingsDocument, error) {
	keys := make([]string, 0, len(pending))
	for key := range pending {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		next, err := edit(doc, key, pending[key])
		if err != nil {
			return ui.SettingsDocument{}, fmt.Errorf("%s: %w", key, err)
		}
		doc = next
	}
	return doc, nil
}

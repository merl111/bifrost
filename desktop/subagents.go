package main

import (
	"bifrost/internal/config"
	"bifrost/internal/ui"
	"context"
	cui "cui.local/cui"
	"encoding/json"
	"strings"
	"time"
)

type subagentView struct {
	provider, model, catalog, save, discover, state cui.Widget
	profiles                                        []ui.AccountChoice
	models                                          []ui.AccountModelOption
	document                                        ui.SettingsDocument
	busy                                            bool
	request                                         uint64
}

func (g *desktop) buildSubagentPanel(parent cui.Widget) {
	s := &g.workbench.subagents
	label(parent, "Choose defaults for spawned tasks", 18, foreground)
	label(parent, "The main conversation keeps its own model. A task can explicitly override these defaults. Changes apply to new parent turns and their children.", 11, muted)
	label(parent, "Provider / account", 11, foreground)
	s.provider = parent.Select(nil)
	s.provider.OnAction(func(cui.Widget) {
		s.request++
		s.models = nil
		s.catalog.Items(nil)
		s.catalog.SetVisible(false)
		s.model.SetText("")
		s.state.SetText("Choose a model or leave blank to use the profile default.")
	})
	label(parent, "Model ID", 11, foreground)
	s.model = parent.Entry("")
	s.model.Placeholder("Blank uses the provider default or parent model")
	s.discover = button(parent, "Load available models", g.discoverSubagentModels)
	s.catalog = parent.Select(nil)
	s.catalog.SetVisible(false)
	s.catalog.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i >= 0 && i < len(s.models) {
			s.model.SetText(s.models[i].Slug)
		}
	})
	s.save = button(parent, "Save sub-agent defaults", g.saveSubagents)
	style(s.save, violet, background, 8, false, 8)
	s.state = label(parent, "", 11, muted)
	label(parent, "Concurrency and budgets remain controlled by defaults.max_agents and defaults.max_steps. Read-only child tasks cannot execute commands. Vendor CLI tool restrictions still apply.", 11, muted)
	button(parent, "Concurrency & budget settings", func() {
		g.openWorkbench("Settings")
		g.workbench.settings.query.SetText("defaults.max")
		g.filterSettings()
	})
	parent.Box(cui.Vertical, 0).Expand(true)
}
func (g *desktop) loadSubagents() {
	s := &g.workbench.subagents
	if s.busy {
		return
	}
	s.busy = true
	s.save.SetEnabled(false)
	g.work(func(context.Context) func() {
		doc, err := g.ws.Settings.Load()
		var cfg config.Settings
		if err == nil {
			err = json.Unmarshal([]byte(doc.Data), &cfg)
		}
		return func() {
			s.busy = false
			s.save.SetEnabled(err == nil)
			if err != nil {
				g.workbenchNotice("Could not load sub-agent defaults", err)
				return
			}
			s.document = doc
			s.profiles = append([]ui.AccountChoice(nil), g.accounts...)
			labels := []string{"Inherit parent provider"}
			selected := 0
			for i, p := range s.profiles {
				labels = append(labels, p.Label)
				if p.ID == cfg.Subagents.Provider {
					selected = i + 1
				}
			}
			s.provider.Items(labels)
			s.provider.SetSelected(selected)
			s.model.SetText(cfg.Subagents.Model)
			s.catalog.Items(nil)
			s.catalog.SetVisible(false)
			s.state.SetText("Saved defaults · explicit task choices take precedence")
		}
	})
}
func (g *desktop) saveSubagents() {
	s := &g.workbench.subagents
	if s.busy {
		return
	}
	profile := ""
	i := s.provider.Selected() - 1
	if i >= 0 && i < len(s.profiles) {
		profile = s.profiles[i].ID
	}
	raw, _ := json.Marshal(config.SubagentSettings{Provider: profile, Model: strings.TrimSpace(s.model.Text())})
	doc := s.document
	s.busy = true
	s.save.SetEnabled(false)
	g.work(func(context.Context) func() {
		updated, err := g.ws.Settings.Edit(doc, "subagents", string(raw))
		if err == nil {
			updated, _, err = g.ws.Settings.Save(updated)
		}
		return func() {
			s.busy = false
			s.save.SetEnabled(true)
			if err == nil {
				s.document = updated
				s.state.SetText("Saved · applies to new turns without changing the main model")
			}
			g.workbenchNotice("Sub-agent defaults saved", err)
		}
	})
}
func (g *desktop) discoverSubagentModels() {
	s := &g.workbench.subagents
	i := s.provider.Selected() - 1
	account, ok := g.selectedProvider()
	if i >= 0 && i < len(s.profiles) {
		account = s.profiles[i]
		ok = true
	}
	if !ok {
		return
	}
	s.request++
	request := s.request
	s.discover.SetEnabled(false)
	s.state.SetText("Loading models…")
	g.work(func(ctx context.Context) func() {
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		models, _, err := g.ws.DiscoverModels(ctx, ui.AccountAddRequest{Name: account.ID, Kind: account.Provider}, false)
		return func() {
			s.discover.SetEnabled(true)
			if request != s.request || g.workbench.open != "Sub-agents" {
				return
			}
			if err != nil {
				g.workbenchNotice("Could not load models; enter a model ID", err)
				return
			}
			s.models = models
			labels := []string{}
			for _, model := range models {
				labels = append(labels, model.Slug)
			}
			s.catalog.Items(labels)
			s.catalog.SetVisible(len(labels) > 0)
			s.state.SetText("Choose a model, then save the defaults")
		}
	})
}

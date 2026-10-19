package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"bifrost/internal/cli"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type quickSelection struct {
	kind    string
	request uint64
	cancel  context.CancelFunc
	models  map[string][]ui.AccountModelOption
}

func (g *desktop) selectedProvider() (ui.AccountChoice, bool) {
	for _, choice := range g.accounts {
		if choice.Active {
			return choice, true
		}
	}
	return ui.AccountChoice{}, false
}

func (g *desktop) updateQuickSelection(snapshot cli.DesktopSnapshot) {
	provider := "Connect provider"
	if choice, ok := g.selectedProvider(); ok {
		provider = choice.Label
	}
	model := snapshot.Connection.Model
	if model == "" {
		model = "Choose model"
	}
	g.profiles.SetText("Provider · " + shortText(provider, 18))
	g.profiles.Tooltip("Switch provider account · " + provider)
	g.profiles.Accessibility("Switch provider", provider)
	g.modelName.SetText("Model · " + shortText(model, 24))
	g.modelName.Tooltip("Switch model independently · " + model)
	g.modelName.Accessibility("Switch model", model)
}

func (g *desktop) cancelQuickSelection() {
	g.quick.request++
	if g.quick.cancel != nil {
		g.quick.cancel()
		g.quick.cancel = nil
	}
	g.quick.kind = ""
}

func (g *desktop) openQuickSelection(kind string) {
	if g.busy {
		return
	}
	g.closePalette()
	g.openPalette()
	g.quick.kind = kind
	placeholder := "Search providers and accounts…"
	if kind == "model" {
		placeholder = "Search models for this provider…"
	}
	if input, ok := g.palette.PickerPart(cui.PickerInput); ok {
		input.Placeholder(placeholder)
	}
	if kind == "provider" {
		g.showProviderChoices()
		return
	}
	choice, ok := g.selectedProvider()
	if !ok {
		g.showProviderChoices()
		return
	}
	if models, cached := g.quick.models[choice.ID]; cached {
		g.showModelChoices(choice, models, "")
		return
	}
	g.showModelChoices(choice, nil, "Loading available models…")
	g.loadQuickModels(choice)
}

func (g *desktop) showProviderChoices() {
	g.paletteActions = make(map[uint64]func())
	items := []cui.Choice{}
	for _, account := range g.accounts {
		id := uint64(len(items) + 1)
		detail := account.Provider
		if account.Active {
			detail += " · Current provider"
		}
		items = append(items, cui.Choice{ID: id, Label: account.Label, Detail: detail})
		g.paletteActions[id] = func() {
			if !account.Active {
				g.switchAccount(account.ID)
			}
		}
	}
	id := uint64(len(items) + 1)
	items = append(items, cui.Choice{ID: id, Label: "Connect or manage providers…", Detail: "Accounts and credentials"})
	g.paletteActions[id] = func() { g.openWorkbench("Accounts") }
	g.palette.PickerItems(items)
}

func (g *desktop) showModelChoices(account ui.AccountChoice, models []ui.AccountModelOption, notice string) {
	g.paletteActions = make(map[uint64]func())
	items := []cui.Choice{}
	add := func(label, detail string, disabled bool, action func()) {
		id := uint64(len(items) + 1)
		items = append(items, cui.Choice{ID: id, Label: label, Detail: detail, Disabled: disabled})
		g.paletteActions[id] = action
	}
	current := g.activeChat().snapshot.Connection.Model
	seen := map[string]bool{}
	for _, model := range append([]ui.AccountModelOption{{Slug: current, DisplayName: "Current model"}}, models...) {
		if model.Slug == "" || seen[model.Slug] {
			continue
		}
		seen[model.Slug] = true
		detail := model.DisplayName
		if model.ContextLength > 0 {
			detail += fmt.Sprintf(" · %d context tokens", model.ContextLength)
		}
		add(model.Slug, detail, false, func() { g.selectQuickModel(account.ID, model.Slug) })
	}
	if notice != "" {
		add(notice, "Provider: "+account.Label, true, nil)
	}
	add("Enter model ID…", "Use an unlisted model on this provider", false, func() { g.openCommand("model ") })
	add("Refresh model list", "Reload from "+account.Label, false, func() {
		delete(g.quick.models, account.ID)
		g.openQuickSelection("model")
	})
	g.palette.PickerItems(items)
}

func (g *desktop) loadQuickModels(account ui.AccountChoice) {
	g.quick.request++
	request := g.quick.request
	ctx, cancel := context.WithTimeout(g.ctx, 20*time.Second)
	g.quick.cancel = cancel
	g.work(func(context.Context) func() {
		models, _, err := g.ws.DiscoverModels(ctx, ui.AccountAddRequest{Name: account.ID, Kind: account.Provider}, false)
		cancel()
		return func() {
			active, ok := g.selectedProvider()
			if request != g.quick.request || !g.paletteOpen || g.quick.kind != "model" || !ok || active.ID != account.ID {
				return
			}
			g.quick.cancel = nil
			if err != nil {
				g.showModelChoices(account, nil, "Could not load models: "+shortText(err.Error(), 140))
				return
			}
			if len(models) == 0 {
				g.showModelChoices(account, nil, "No models returned — check the account or refresh")
				return
			}
			if g.quick.models == nil {
				g.quick.models = make(map[string][]ui.AccountModelOption)
			}
			g.quick.models[account.ID] = models
			g.updateContextMeter(g.activeChat().snapshot.Connection)
			g.showModelChoices(account, models, "")
		}
	})
}

func (g *desktop) selectQuickModel(account, model string) {
	active, ok := g.selectedProvider()
	if g.busy || !ok || active.ID != account || strings.TrimSpace(model) == "" {
		return
	}
	if model == g.activeChat().snapshot.Connection.Model {
		return
	}
	g.changeConversation(func(ctx context.Context) error { return g.ws.SwitchModel(ctx, account, model) })
}

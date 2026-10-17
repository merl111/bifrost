package main

import (
	"context"
	"fmt"
	"strings"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

var accountKinds = []string{"responses", "anthropic", "gemini", "openrouter", "compatible", "chatgpt", "cursor-native", "cursor", "claude"}

type accountsView struct {
	list, kind, name, key, env, endpoint, executable, model, models, form cui.Widget
	choices                                                               []ui.AccountModelOption
	selected                                                              string
	cancel                                                                context.CancelFunc
}

func labeledEntry(parent cui.Widget, title string) cui.Widget {
	label(parent, title, 10, muted)
	w := parent.Entry("")
	style(w, surface, foreground, 7, true, 7)
	return w
}

func (g *desktop) buildAccountsPanel(parent cui.Widget) {
	a := &g.workbench.accounts
	label(parent, "Manage provider profiles · switching retains this conversation", 12, muted)
	split := parent.Split(cui.Horizontal, .32)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(8)
	right.Padding(12)
	a.list = left.List(nil)
	a.list.Expand(true)
	a.list.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i < 0 || i >= len(g.accounts) || g.busy {
			return
		}
		choice := g.accounts[i]
		a.selected = choice.ID
		a.model.SetText(choice.Model)
	})
	button(left, "Connect selected profile", func() {
		if id := a.selected; id != "" {
			g.accountOperation(func(ctx context.Context) error { return g.ws.SwitchAccount(ctx, id) })
		}
	})
	button(left, "Remove selected profile", g.removeSelectedAccount)
	button(left, "Load selected profile models", g.discoverSelectedModels)
	button(left, "Compact conversation", func() { g.accountOperation(g.ws.Compact) })
	a.form = right.Box(cui.Vertical, 5)
	label(a.form, "Create a new profile · select a saved profile to connect or change its model", 11, muted)
	row := a.form.Wrap(12)
	first, second := row.Box(cui.Vertical, 5), row.Box(cui.Vertical, 5)
	first.MinSize(230, 0)
	second.MinSize(230, 0)
	label(first, "Provider", 10, muted)
	a.kind = first.Select(accountKinds)
	a.kind.SetSelected(0)
	a.name = labeledEntry(first, "Profile name")
	a.env = labeledEntry(first, "API key environment variable (optional)")
	label(first, "API key (optional, this process)", 10, muted)
	a.key = first.Password("")
	a.endpoint = labeledEntry(second, "Base URL (optional / compatible providers)")
	a.executable = labeledEntry(second, "Vendor CLI executable (optional)")
	a.model = labeledEntry(second, "Model ID (or load models below)")
	a.models = a.form.List(nil)
	a.models.MinSize(0, 120)
	a.models.Expand(true)
	a.models.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i >= 0 && i < len(a.choices) {
			a.model.SetText(a.choices[i].Slug)
		}
	})
	actions := a.form.Wrap(8)
	button(actions, "Load models", func() { g.discoverModels(false) })
	button(actions, "Browser sign-in", func() { g.discoverModels(true) })
	button(actions, "Save & connect", func() {
		request := g.accountRequest()
		for _, existing := range g.accounts {
			if existing.ID == strings.TrimSpace(request.Name) {
				g.workbenchNotice("Choose a new profile name", fmt.Errorf("use the selected profile controls to connect or change its model"))
				return
			}
		}
		g.accountOperation(func(ctx context.Context) error { return g.ws.AddAccount(ctx, request) })
	})
	button(actions, "Use model on selected profile", func() {
		id, model := a.selected, a.model.Text()
		if id != "" {
			g.accountOperation(func(ctx context.Context) error { return g.ws.SwitchModel(ctx, id, model) })
		}
	})
	button(right, "Cancel account operation", func() {
		if a.cancel != nil {
			a.cancel()
		}
	})
	label(right, "ChatGPT / Cursor Native use browser sign-in. Vendor CLI profiles use their installed clients. Secrets are never written into settings JSON.", 10, muted)
}

func (g *desktop) refreshAccountPanel() {
	var names []string
	for _, a := range g.accounts {
		mark := ""
		if a.Active {
			mark = "● "
		}
		names = append(names, mark+a.Label+"\n"+a.Provider+" · "+a.Model)
	}
	g.workbench.accounts.list.Items(names)
}

func (g *desktop) accountRequest() ui.AccountAddRequest {
	a := &g.workbench.accounts
	i := a.kind.Selected()
	if i < 0 || i >= len(accountKinds) {
		i = 0
	}
	return ui.AccountAddRequest{Kind: accountKinds[i], Name: a.name.Text(), Model: a.model.Text(), RawAPIKey: a.key.Text(), APIKeyEnv: a.env.Text(), BaseURL: a.endpoint.Text(), Executable: a.executable.Text()}
}

func (g *desktop) accountOperation(action func(context.Context) error) {
	if g.busy {
		return
	}
	a := &g.workbench.accounts
	ctx, cancel := context.WithCancel(g.ctx)
	a.cancel = cancel
	a.form.SetEnabled(false)
	sessionID := snapshotSession(g.activeChat().snapshot)
	connected := g.connected
	g.setBusy(true)
	g.work(func(context.Context) func() {
		err := g.activateDisplayedSession(ctx, sessionID, connected)
		if err == nil {
			err = action(ctx)
		}
		cancel()
		snapshot, snapErr := g.ws.Snapshot(g.ctx)
		return func() {
			a.cancel = nil
			a.form.SetEnabled(true)
			a.key.SetText("")
			if snapErr == nil {
				g.applySnapshot(snapshot)
			}
			g.setBusy(false)
			g.refreshAccountPanel()
			if err == nil {
				err = snapErr
			}
			g.workbenchNotice("Account operation completed", err)
		}
	})
}

func (g *desktop) discoverModels(login bool) {
	if g.busy {
		return
	}
	request := g.accountRequest()
	if login && request.Kind != "chatgpt" && request.Kind != "cursor-native" {
		g.workbenchNotice("Choose a browser sign-in provider", fmt.Errorf("browser sign-in is available for ChatGPT and Cursor Native profiles"))
		return
	}
	g.discoverModelRequest(request, login)
}
func (g *desktop) discoverSelectedModels() {
	if g.busy {
		return
	}
	for _, choice := range g.accounts {
		if choice.ID == g.workbench.accounts.selected {
			g.discoverModelRequest(ui.AccountAddRequest{Name: choice.ID, Kind: choice.Provider, Model: choice.Model}, false)
			return
		}
	}
	g.workbenchNotice("Select a saved profile first", fmt.Errorf("choose a profile in the left list"))
}
func (g *desktop) discoverModelRequest(request ui.AccountAddRequest, login bool) {
	a := &g.workbench.accounts
	ctx, cancel := context.WithCancel(g.ctx)
	a.cancel = cancel
	g.setBusy(true)
	a.form.SetEnabled(false)
	g.work(func(context.Context) func() {
		models, selected, err := g.ws.DiscoverModels(ctx, request, login)
		cancel()
		return func() {
			a.cancel = nil
			a.form.SetEnabled(true)
			g.setBusy(false)
			if err != nil {
				g.workbenchNotice("Could not load models", err)
				return
			}
			a.choices = models
			var names []string
			for _, m := range models {
				names = append(names, m.Slug+" · "+m.DisplayName)
			}
			a.models.Items(names)
			if strings.TrimSpace(a.model.Text()) == "" {
				a.model.SetText(selected)
			}
			g.workbenchNotice(fmt.Sprintf("Loaded %d models", len(models)), nil)
		}
	})
}

func (g *desktop) removeSelectedAccount() {
	a := &g.workbench.accounts
	id := a.selected
	if id == "" || g.busy {
		return
	}
	for _, profile := range g.accounts {
		if profile.ID == id && profile.Active {
			g.workbenchNotice("Profile is connected", fmt.Errorf("connect another profile before removing this one"))
			return
		}
	}
	g.window.Alert("Remove profile?", id+" will be removed from your configuration. Conversation history is retained.", "Remove", func(result int, _ string) {
		if result == cui.DialogAccepted {
			g.accountOperation(func(ctx context.Context) error { return g.ws.RemoveAccount(ctx, id) })
		}
	})
}

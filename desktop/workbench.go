package main

import (
	"context"

	"bifrost/internal/domain"
	"bifrost/internal/tasks"
	cui "cui.local/cui"
)

type workbenchView struct {
	rail, brand, navTitle, back, reading, left, right, subtitle cui.Widget
	compact                                                     bool
	inset                                                       int
	taskUI                                                      taskPresentation
	navigation                                                  map[string]cui.Widget
	root, body, heading, notice                                 cui.Widget
	pages                                                       map[string]cui.Widget
	open                                                        string
	settings                                                    settingsView
	market                                                      marketView
	accounts                                                    accountsView
	projects                                                    projectsView
	taskList, taskDetail, taskCancel                            cui.Widget
	tasks                                                       []tasks.Summary
	events                                                      []tasks.Event
	selectedTask                                                domain.ID
	taskLoading                                                 bool
}

func (g *desktop) buildWorkbench(stack cui.Widget) {
	w := &workbenchView{pages: make(map[string]cui.Widget), navigation: make(map[string]cui.Widget)}
	g.workbench = w
	w.root = stack.StackLayer(cui.LayerFill, 0, 0, 0)
	style(w.root, background, foreground, 0, false)
	layout := w.root.Box(cui.Horizontal, 0)
	layout.Expand(true)
	w.rail = layout.Box(cui.Vertical, 8)
	w.rail.MinSize(186, 0)
	style(w.rail, rail, foreground, 0, false, 12)
	w.brand = label(w.rail, "◈  Bifrost", 16, foreground)
	w.back = navigationButton(w.rail, "Conversation", cui.SymbolThread, g.closeWorkbench)
	w.navTitle = label(w.rail, "WORKSPACE", 9, muted)
	for _, dest := range []struct {
		name string
		icon int
	}{
		{"Project", cui.SymbolHome}, {"Tasks", cui.SymbolPoll},
		{"Skills", cui.SymbolFile}, {"MCP servers", cui.SymbolPanel},
		{"Accounts", cui.SymbolPerson}, {"Settings", cui.SymbolSettings},
	} {
		name := dest.name
		w.navigation[name] = navigationButton(w.rail, name, dest.icon, func() { g.openWorkbench(name) })
	}
	w.rail.Box(cui.Vertical, 0).Expand(true)
	w.reading = layout.Box(cui.Horizontal, 0)
	w.reading.Expand(true)
	w.left = w.reading.Box(cui.Vertical, 0)
	main := w.reading.Box(cui.Vertical, 16)
	main.Expand(true)
	main.Padding(24)
	w.right = w.reading.Box(cui.Vertical, 0)
	w.heading = label(main, "Workspace", 22, foreground)
	w.heading.Font("", 22, 600)
	w.subtitle = label(main, "", 11, muted)
	w.notice = main.Feedback(cui.Banner)
	w.body = main.Box(cui.Vertical, 12)
	w.body.Expand(true)
	g.buildTaskPanel(g.workbenchPage("Tasks"))
	g.buildSettingsPanel(g.workbenchPage("Settings"))
	g.buildAccountsPanel(g.workbenchPage("Accounts"))
	g.buildMarketPanel(g.workbenchPage("Marketplace"))
	g.buildProjectPanel(g.workbenchPage("Project"))
	g.buildProjectDetails(g.workbenchPage("Project details"))
	w.root.SetVisible(false)
	g.app.Every(1000, func() {
		if w.open == "Tasks" {
			g.refreshTasks()
		}
	})
}

func (g *desktop) workbenchPage(name string) cui.Widget {
	p := g.workbench.body.Box(cui.Vertical, 12)
	p.Expand(true)
	p.SetVisible(false)
	g.workbench.pages[name] = p
	return p
}

func (g *desktop) openWorkbench(name string) {
	g.closePalette()
	w := g.workbench
	if w.open != name {
		w.notice.FeedbackDismiss()
	}
	if name == "Marketplace" {
		name = "Skills"
	}
	if w.market.installing && ((name == "Skills" && w.market.kind.Selected() != 0) || (name == "MCP servers" && w.market.kind.Selected() != 1)) {
		g.workbenchNotice("Installation in progress; switch catalogs when it finishes", nil)
		return
	}
	w.open = name
	w.root.SetVisible(true)
	g.shell.SetEnabled(false)
	g.shell.SetVisible(false)
	w.heading.SetText(name)
	w.subtitle.SetText(map[string]string{
		"Project":         "Repositories, connections and the context your agents work with.",
		"Tasks":           "Follow active work, inspect results and recover from failures.",
		"Skills":          "Find reusable instructions. Review their source before adding them.",
		"MCP servers":     "Connect tools and data sources to your workspace.",
		"Accounts":        "Manage provider profiles and choose the models you use.",
		"Settings":        "Configure your workspace. Runtime changes apply after restart.",
		"Project details": "Documentation, pull requests, issues and connected sources.",
	}[name])
	for key, b := range w.navigation {
		selected := key == name || (key == "Project" && name == "Project details")
		if selected {
			style(b, accentSoft, violet, 6, false, 8)
		} else {
			style(b, 0, muted, 6, false, 8)
		}
	}
	for key, page := range w.pages {
		visible := key == name || (key == "Marketplace" && (name == "Skills" || name == "MCP servers"))
		page.SetVisible(visible)
	}
	switch name {
	case "Tasks":
		g.refreshTasks()
	case "Settings":
		if !w.settings.loaded {
			g.loadSettings()
		}
	case "Accounts":
		g.refreshAccountPanel()
	case "Skills", "MCP servers":
		kind := 0
		if name == "MCP servers" {
			kind = 1
		}
		changed := w.market.kind.Selected() != kind
		w.market.kind.SetSelected(kind)
		if !w.market.loaded || changed {
			g.searchMarket()
		}
	case "Project":
		if !w.projects.loaded {
			g.refreshProject()
		}
	}
}

func (g *desktop) closeWorkbench() {
	w := g.workbench
	if w.open == "Project details" && g.projectDetails.loading {
		g.projectDetails.request++
		if g.projectDetails.cancel != nil {
			g.projectDetails.cancel()
		}
		g.projectDetails.loading = false
	}
	if (w.open == "Skills" || w.open == "MCP servers") && !w.market.installing {
		w.market.request++
		if w.market.cancel != nil {
			w.market.cancel()
		}
		w.market.loaded = false
	}
	w.open = ""
	w.root.SetVisible(false)
	g.shell.SetVisible(true)
	g.shell.SetEnabled(true)
	g.composer.Focus()
}

func (g *desktop) workbenchNotice(title string, err error) {
	if err != nil {
		g.workbench.notice.FeedbackShow(title, safeText(err.Error()), cui.Danger, "", 0)
	} else {
		g.workbench.notice.FeedbackShow(title, "", cui.Success, "", 0)
	}
}

func (g *desktop) refreshTasks() {
	w := g.workbench
	if w.taskLoading {
		return
	}
	w.taskLoading = true
	callbacks := g.ws.Tasks
	g.work(func(ctx context.Context) func() {
		items, err := callbacks.Refresh(ctx)
		var events []tasks.Event
		if err == nil {
			events, _, err = callbacks.Events(ctx, 0, 64)
		}
		return func() {
			w.taskLoading = false
			if err != nil {
				g.workbenchNotice("Could not load tasks", err)
				return
			}
			w.tasks, w.events = items, events
			g.filterTasks()
			g.showTaskDetails()
		}
	})
}

func (g *desktop) cancelSelectedTask() {
	id := g.workbench.selectedTask
	if id == "" {
		return
	}
	callbacks := g.ws.Tasks
	g.workbench.taskCancel.SetEnabled(false)
	g.work(func(ctx context.Context) func() {
		err := callbacks.Cancel(ctx, id)
		return func() { g.workbenchNotice("Cancellation requested", err); g.refreshTasks() }
	})
}

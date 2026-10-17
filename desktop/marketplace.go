package main

import (
	"context"
	"fmt"
	"time"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type marketView struct {
	title, description, source, installNote, empty, preview cui.Widget
	installed                                               map[string]string
	searchDue                                               time.Time
	query, kind, list, detail, scope, install, state        cui.Widget
	entries                                                 []ui.MarketplaceEntry
	selected                                                string
	request                                                 uint64
	loaded, installing                                      bool
	cancel                                                  context.CancelFunc
}

func (g *desktop) buildMarketPanel(parent cui.Widget) {
	m := &g.workbench.market
	m.installed = make(map[string]string)
	bar := parent.Box(cui.Horizontal, 8)
	m.kind = bar.Select([]string{"Skills", "MCP servers"})
	m.kind.SetSelected(0)
	m.kind.SetVisible(false)
	m.query = bar.Search("")
	m.query.Placeholder("Search by name or description…")
	m.query.Expand(true)
	button(bar, "Search", g.searchMarket)
	iconButton(bar, cui.SymbolSettings, "Configure catalog sources", func() {
		g.openWorkbench("Settings")
		g.workbench.settings.query.SetText("marketplace")
		g.filterSettings()
	})
	m.query.OnAction(func(cui.Widget) { m.searchDue = time.Now().Add(300 * time.Millisecond) })
	m.query.OnKey(func(key int, _ uint) bool {
		if key == cui.KeyEnter {
			g.searchMarket()
			return true
		}
		return false
	})
	g.app.Every(150, func() {
		if !m.searchDue.IsZero() && time.Now().After(m.searchDue) {
			m.searchDue = time.Time{}
			if g.workbench.open == "Skills" || g.workbench.open == "MCP servers" {
				g.searchMarket()
			}
		}
	})
	m.state = label(parent, "Search the catalog, then select an item to inspect its source.", 10, muted)
	split := parent.Split(cui.Horizontal, .38)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(4)
	right.Padding(20)
	m.list = left.List(nil)
	m.list.Expand(true)
	style(m.list, background, foreground, 6, false)
	style(right, surface, foreground, 10, false, 20)
	m.empty = label(right, "Discover something useful.\n\nSelect a result to review what it adds and where it comes from.", 14, muted)
	m.preview = right.Box(cui.Vertical, 14)
	m.preview.Expand(true)
	m.title = label(m.preview, "", 20, foreground)
	m.title.Font("", 20, 600)
	m.description = label(m.preview, "", 12, foreground)
	m.source = label(m.preview, "", 10, muted)
	actions := m.preview.Box(cui.Horizontal, 8)
	m.scope = actions.Select([]string{"This project", "All projects"})
	m.scope.SetSelected(0)
	m.install = button(actions, "Install", g.installMarket)
	style(m.install, accentSoft, violet, 6, false, 8)
	m.install.SetEnabled(false)
	actions.Box(cui.Horizontal, 0).Expand(true)
	m.installNote = label(m.preview, "Preview the source before installation.", 10, muted)
	label(m.preview, "SOURCE & CONFIGURATION", 9, muted)
	m.detail = m.preview.Code("")
	m.detail.ReadOnly(true)
	m.detail.Expand(true)
	style(m.detail, background, foreground, 8, false, 12)
	m.preview.SetVisible(false)
	m.list.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i >= 0 && i < len(m.entries) {
			g.previewMarket(m.entries[i].ID)
		}
	})
}

func (g *desktop) searchMarket() {
	m := &g.workbench.market
	if m.installing {
		return
	}
	m.searchDue = time.Time{}
	m.loaded = false
	m.request++
	request := m.request
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(g.ctx)
	m.cancel = cancel
	query := m.query.Text()
	isMCP := m.kind.Selected() == 1
	m.selected = ""
	m.entries = nil
	m.list.Items(nil)
	m.install.SetEnabled(false)
	m.state.SetText("Searching catalog…")
	m.preview.SetVisible(false)
	m.empty.SetVisible(true)
	callbacks := g.ws.Marketplace
	g.work(func(context.Context) func() {
		defer cancel()
		var items []ui.MarketplaceEntry
		var err error
		if isMCP {
			items, err = callbacks.ListMCP(ctx, query)
		} else {
			items, err = callbacks.List(ctx, query)
		}
		return func() {
			if request != m.request {
				return
			}
			if err != nil {
				g.workbenchNotice("Catalog search failed", err)
				m.state.SetText("Search failed; retry when connected")
				return
			}
			m.entries = items
			m.loaded = true
			var names []string
			for _, e := range items {
				names = append(names, compactText(e.Name, 42)+"\n"+compactText(e.Description, 76)+"\n"+compactText(e.Source, 55))
			}
			m.list.Items(names)
			m.state.SetText(fmt.Sprintf("%d results · source preview required before install", len(items)))
			if len(items) == 0 {
				m.empty.SetText("No results found. Try another search or configure a catalog source.")
			} else {
				m.empty.SetText("Select an item to review its capabilities, source and installation details.")
			}
		}
	})
}

func (g *desktop) previewMarket(id string) {
	m := &g.workbench.market
	if m.installing {
		return
	}
	m.request++
	request := m.request
	if m.cancel != nil {
		m.cancel()
	}
	ctx, cancel := context.WithCancel(g.ctx)
	m.cancel = cancel
	m.selected = ""
	m.install.SetEnabled(false)
	m.detail.SetText("Loading source details…")
	m.preview.SetVisible(true)
	m.empty.SetVisible(false)
	m.title.SetText(id)
	m.description.SetText("")
	m.source.SetText("")
	for _, entry := range m.entries {
		if entry.ID == id {
			m.title.SetText(entry.Name)
			m.description.SetText(entry.Description)
			source := entry.Source
			if source == "" {
				source = "See the source preview below"
			}
			m.source.SetText("Source  ·  " + source)
		}
	}
	m.installNote.SetText("Skills add instructions; MCP installs server configuration. Review the source below.")
	if scope, ok := m.installed[id]; ok {
		m.installNote.SetText("Installed in " + scope + " during this session. You can reinstall or choose another scope.")
	}
	callbacks := g.ws.Marketplace
	g.work(func(context.Context) func() {
		defer cancel()
		detail, err := callbacks.Preview(ctx, id)
		return func() {
			if request != m.request {
				return
			}
			if err != nil {
				g.workbenchNotice("Preview failed", err)
				return
			}
			m.selected = id
			m.detail.SetText(safeText(detail))
			m.install.SetEnabled(true)
		}
	})
}

func (g *desktop) installMarket() {
	m := &g.workbench.market
	if m.installing || m.selected == "" {
		return
	}
	id, scope := m.selected, "project"
	if m.scope.Selected() == 1 {
		scope = "global"
	}
	m.installing = true
	m.install.SetEnabled(false)
	callbacks := g.ws.Marketplace
	g.work(func(ctx context.Context) func() {
		result, err := callbacks.Install(ctx, id, scope)
		return func() {
			m.installing = false
			m.install.SetEnabled(true)
			if err == nil {
				m.installed[id] = scope
				m.state.SetText(safeText(result))
				m.installNote.SetText("Installed in " + scope + ". Skills must be activated separately; server configuration applies on reconnect.")
			}
			g.workbenchNotice("Installation complete", err)
		}
	})
}

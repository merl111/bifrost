package main

import (
	"bifrost/internal/ui"
	cui "cui.local/cui"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

func (g *desktop) selectNavigation(files bool) {
	g.filesMode = files
	g.conversationPage.SetVisible(!files)
	g.filesPage.SetVisible(files)
	style(g.conversationsTab, 0, muted, 7, false, 6)
	style(g.filesTab, 0, muted, 7, false, 6)
	active := g.conversationsTab
	placeholder := "Filter conversations…"
	if files {
		active = g.filesTab
		placeholder = "Filter this directory…"
	}
	style(active, elevated, foreground, 7, false, 6)
	g.sideSearch.Placeholder(placeholder)
	g.sideSearch.SetText("")
	g.filterNavigation()
}

func (g *desktop) filterNavigation() {
	query := strings.ToLower(strings.TrimSpace(g.sideSearch.Text()))
	g.visibleSessions = nil
	g.visibleFiles = nil
	var titles []string
	selected := -1
	for _, s := range g.sessions {
		if query != "" && !strings.Contains(strings.ToLower(s.Title), query) {
			continue
		}
		if s.Active {
			selected = len(titles)
		}
		title := s.Title
		if title == "(empty conversation)" {
			title = "New conversation"
		}
		titles = append(titles, compactText(title, 38)+"\n"+sessionSubtitle(s))
		g.visibleSessions = append(g.visibleSessions, s)
	}
	g.renderFiles(query)

	g.sessionList.Items(titles)
	g.sessionList.SetSelected(selected)
	count := len(titles)
	empty := "No conversations yet"
	if g.filesMode {
		count = len(g.visibleFiles)
		empty = "This directory is empty"
	}
	if query != "" {
		empty = "No matches — try another search"
	}
	g.sideEmpty.SetText(empty)
	g.sideEmpty.SetVisible(count == 0)
}

func (g *desktop) showFiles() {
	if !g.sidebarVisible {
		g.toggleSidebar()
	}
	g.selectNavigation(true)
	g.sideSearch.Focus()
}

func (g *desktop) updateLayout() {
	width, height, ok := g.window.Size()
	if !ok {
		return
	}
	if ok && width < g.lastWidth {
		if width < 1000 && g.sidebarVisible {
			g.toggleSidebar()
		}
		if width < 850 && g.inspectorVisible {
			g.toggleInspector()
		}
	}
	if width != g.lastWidth && g.sidebarVisible {
		// A navigation rail stays readable on ultrawide monitors instead of
		// consuming a fixed percentage of thousands of pixels.
		fraction := float64(g.commands.layout.Files) / 100
		if fraction <= 0 {
			fraction = .18
		}
		g.body.SetSplitPosition(float64(min(340, max(230, int(float64(width)*fraction)))) / float64(width))
	}
	g.lastWidth = width
	g.updateWorkbenchLayout(width)
	compact := float64(width) < 1000*g.textScale
	if compact != g.compact {
		g.compact = compact
		g.commandButton.IconOnly(compact)
		if compact {
			g.content.Padding(16)
		} else {
			g.content.Padding(28)
		}
	}
	g.resizeChatTabs()
	empty := len(g.transcript.messages) == 0
	tiny := width < 700 || height < 560
	g.welcome.SetVisible(empty && !tiny)
	g.welcomeCompact.SetVisible(empty && tiny)
	// Keep the primary input reachable on short windows and with enlarged text.
	compactWelcome := width < 900 || float64(height) < 760*g.textScale
	if compactWelcome != g.compactWelcome {
		g.compactWelcome = compactWelcome
		g.starters.SetVisible(!compactWelcome)
		g.welcomeMark.SetVisible(!compactWelcome)
	}
	if w, h, ok := g.readingHost.AllocatedSize(); ok {
		w = min(900, w)
		if w != g.readingWidth || h != g.readingHeight {
			g.readingWidth, g.readingHeight = w, h
			g.readingLayer.MinSize(w, h)
		}
	}

	if g.paletteOpen && !g.palette.PickerIsOpen() {
		g.closePalette()
	}
}

func (g *desktop) buildPalette(stack cui.Widget) {
	g.backdrop = stack.StackBackdrop("Close command palette")
	g.backdrop.SetVisible(false)
	g.backdrop.OnAction(func(cui.Widget) { g.closePalette() })
	g.paletteLayer = stack.StackLayer(cui.LayerTop, 560, 0, 32)
	style(g.paletteLayer, surface, foreground, 14, true, 10)
	g.palette = g.paletteLayer.Picker(cui.CommandPalette, "Search commands, conversations, files…")
	g.palette.PickerSetChrome(false, true, false)
	if results, ok := g.palette.PickerPart(cui.PickerResults); ok {
		results.MinSize(0, 300)
	}
	g.paletteLayer.SetVisible(false)
	g.palette.OnAction(func(w cui.Widget) {
		if w.PickerEvent() == cui.PickerSelect {
			action := g.paletteActions[w.PickerSelected()]
			g.closePalette()
			if action != nil {
				if g.workbench.open != "" {
					g.closeWorkbench()
				}
				action()
			}
		} else if w.PickerEvent() == cui.PickerCancel {
			g.closePalette()
		}
	})
}

func (g *desktop) openPalette() {
	if g.paletteOpen {
		return
	}
	if input, ok := g.palette.PickerPart(cui.PickerInput); ok {
		input.Placeholder("Search commands, conversations, files…")
	}
	g.paletteActions = make(map[uint64]func())
	items := []cui.Choice{}
	add := func(title, detail string, disabled bool, action func()) {
		id := uint64(len(items) + 1)
		items = append(items, cui.Choice{ID: id, Label: shortText(title, 80), Detail: shortText(detail, 100), Disabled: disabled})
		g.paletteActions[id] = action
	}
	add("Run a colon command", "Same commands as the TUI · Tab completes", false, func() { g.openCommand("") })
	for _, spec := range ui.Commands() {
		add(":"+spec.Usage, spec.Help, false, func() { g.openCommand(spec.Name + " ") })
	}
	add("Switch provider", "Choose a connected provider account", g.busy, func() { g.openQuickSelection("provider") })
	add("Switch model", "Choose a model for the current provider", g.busy, func() { g.openQuickSelection("model") })
	add("New conversation", "Ctrl+N", g.operationBusy || !g.connected, func() { g.addChat("") })
	add("Browse workspace files", "Ctrl+P", false, g.showFiles)
	add("Review working tree changes", "Ctrl+R", false, g.loadReview)
	add("Focus composer", "Ctrl+L", false, func() { g.composer.Focus() })
	add("Toggle sidebar", "Ctrl+B", false, g.toggleSidebar)
	add("Toggle source pane", "Ctrl+J", false, g.toggleInspector)
	add("Stop generation", "Escape", g.turnCancel == nil, g.cancelTurn)
	add("Open terminal", "Ctrl+T", false, g.showTerminal)
	add("Save source file", "Ctrl+S", false, g.saveEditor)
	for _, name := range []string{"Project", "Tasks", "Skills", "MCP servers", "Accounts", "Settings"} {
		add("Workspace: "+name, "Manage "+name, false, func() { g.openWorkbench(name) })
	}
	for _, s := range g.sessions {
		if s.Active {
			continue
		}
		add(s.Title, "Conversation · "+s.UpdatedAt, g.operationBusy, func() { g.resume(s.ID) })
	}
	for _, f := range g.files {
		if !f.IsDir {
			add(f.Name, "File · "+filepath.Dir(f.Path), false, func() { g.loadFile(f.Path) })
		}
	}
	g.palette.PickerItems(items)
	g.palette.PickerSetQuery("")
	g.shell.SetEnabled(false)
	g.workbench.root.SetEnabled(false)
	g.backdrop.SetVisible(true)
	g.paletteLayer.SetVisible(true)
	g.paletteOpen = true
	focus := g.composer
	switch g.workbench.open {
	case "Settings":
		focus = g.workbench.settings.query
	case "Accounts":
		focus = g.workbench.accounts.name
	case "Skills", "MCP servers":
		focus = g.workbench.market.query
	case "Project":
		focus = g.workbench.projects.overviewTab
	case "Tasks":
		focus = g.workbench.taskList
	}
	g.palette.PickerOpen(&focus)
}

func (g *desktop) closePalette() {
	if !g.paletteOpen {
		return
	}
	g.paletteOpen = false
	g.cancelQuickSelection()
	g.workbench.root.SetEnabled(true)
	g.shell.SetEnabled(g.workbench.open == "")
	g.palette.PickerClose()
	g.paletteLayer.SetVisible(false)
	g.backdrop.SetVisible(false)
	if g.workbench.open == "" {
		g.composer.Focus()
	}
}

func (g *desktop) toggleActivity() {
	if i := g.transcript.latestActivity(); i >= 0 {
		g.transcript.messages[i].Collapsed = !g.transcript.messages[i].Collapsed
		g.dirty = true
		g.timeline.ScrollTo(g.transcript.messages[i].ID)
	}
}

func (g *desktop) mentionSource() {
	path := g.sourcePath()
	if path == "" {
		return
	}
	g.composer.InsertText(" @" + path + " ")
	g.composer.Focus()
	g.status.SetText("File reference added to your draft")
}

func sessionSubtitle(s ui.SessionSummary) string {
	count := fmt.Sprintf("%d messages", s.MessageCount)
	if s.MessageCount == 0 {
		count = "No messages yet"
	}
	if stamp, err := time.Parse(time.RFC3339, s.UpdatedAt); err == nil {
		return count + " · " + stamp.Local().Format("Jan 2")
	}
	return count
}

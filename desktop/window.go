package main

import (
	"bifrost/internal/cli"
	"bifrost/internal/ui"
	"bifrost/internal/workspace"
	"context"
	cui "cui.local/cui"
	"path/filepath"
	"sync"
)

type desktop struct {
	editorToolbar, editorSearchRow, editorUndoButton, editorRedoButton             cui.Widget
	editorMenu                                                                     cui.Menu
	welcomeCompact                                                                 cui.Widget
	composerMetrics                                                                composerMetrics
	closing, closeFlushing                                                         bool
	mentions                                                                       mentionView
	editorVim                                                                      bool
	editorSearch                                                                   cui.Widget
	projectDetails                                                                 projectDetailsView
	commands                                                                       commandState
	chats                                                                          chatTabs
	terminalTabs                                                                   []*terminalView
	terminalHint, terminalInterrupt                                                cui.Widget
	terminalSelect                                                                 cui.Widget
	terminalIndex                                                                  int
	ctx                                                                            context.Context
	cancel                                                                         context.CancelFunc
	workers                                                                        sync.WaitGroup
	queue                                                                          chan func()
	app                                                                            *cui.App
	window                                                                         cui.Window
	ws                                                                             *cli.DesktopWorkspace
	shell, sidebar, inspector, body, content, contextSplit                         cui.Widget
	status, connection, permissions, heading, location                             cui.Widget
	composer, send, stop, newButton, profiles, sessionList, fileList               cui.Widget
	preview, previewTitle, directory, activity, welcome                            cui.Widget
	readingHost, readingLayer, upperSpace, lowerSpace                              cui.Widget
	sideSearch, conversationPage, filesPage, conversationsTab, filesTab, sideEmpty cui.Widget
	activityToggle, feedback, mention                                              cui.Widget
	palette, paletteLayer, backdrop                                                cui.Widget
	paletteActions                                                                 map[uint64]func()
	paletteOpen, filesMode                                                         bool
	visibleSessions                                                                []ui.SessionSummary
	visibleFiles                                                                   []workspace.Entry
	accounts                                                                       []ui.AccountChoice
	sessions                                                                       []ui.SessionSummary
	files                                                                          []workspace.Entry
	cwd, previewPath, activityLog                                                  string
	viewRequest, dirRequest                                                        uint64
	commandRun                                                                     uint64
	busy, connected, sidebarVisible, inspectorVisible                              bool
	turnCancel                                                                     context.CancelFunc
	transcript                                                                     transcript
	timeline                                                                       cui.ChatComponent
	dirty                                                                          bool
	lastWidth, readingWidth, readingHeight                                         int
	commandButton, modelName, activityRow, starters, welcomeMark                   cui.Widget
	compact, compactWelcome                                                        bool
	textScale                                                                      float64
	drafts                                                                         map[string]*editorDraft
	previewLoading, closeDialog                                                    bool
	saveSource, editorState, sourcePage, terminalPage                              cui.Widget
	viewer                                                                         viewerState
	terminal                                                                       *terminalView
	workbench                                                                      *workbenchView
}

func newDesktop(ctx context.Context, app *cui.App, ws *cli.DesktopWorkspace, width, height int) *desktop {
	ctx, cancel := context.WithCancel(ctx)
	g := &desktop{ctx: ctx, cancel: cancel, app: app, ws: ws, queue: make(chan func(), 256), sidebarVisible: width >= 1000, lastWidth: width, textScale: 1}
	widgetStyles = make(map[cui.Widget]cui.WidgetStyle)
	currentTheme = "midnight"
	g.window = app.Window("Bifrost — "+filepath.Base(ws.Root), width, height)
	g.drafts = make(map[string]*editorDraft)
	g.window.SetCloseDeferred(true)
	root := g.window.Root()
	root.Padding(0)
	root.Font("", 11, 400)
	style(root, background, foreground, 0, false)
	stack := root.Stack()
	stack.Expand(true)
	g.shell = stack.StackLayer(cui.LayerFill, 0, 0, 0)
	g.body = g.shell.Split(cui.Horizontal, .18)
	g.body.SetSplitScrollable(0, false)
	g.body.SetSplitScrollable(1, false)
	g.sidebar = g.body.Pane(0)
	g.buildSidebar()
	main := g.body.Pane(1)
	g.buildToolbar(main)
	g.contextSplit = main.Split(cui.Horizontal, .61)
	g.contextSplit.SetSplitScrollable(0, false)
	g.contextSplit.SetSplitScrollable(1, false)
	// The centered overlay does not contribute fixed gutters to the window's
	// minimum width. It follows the available pane allocation on every resize.
	g.readingHost = g.contextSplit.Pane(0).Stack()
	g.readingHost.StackLayer(cui.LayerFill, 0, 0, 0).MinSize(180, 80)
	g.readingLayer = g.readingHost.StackLayer(cui.LayerCenter, 0, 0, 0)
	g.content = g.readingLayer.Box(cui.Vertical, 14)
	g.content.Padding(28)
	g.content.Expand(true)
	g.buildConversation()
	g.inspector = g.contextSplit.Pane(1)
	g.buildInspector()
	g.buildStatus(g.shell)
	g.buildWorkbench(stack)
	g.buildPalette(stack)
	g.bindCommands()
	g.applyAppearance(ws.Appearance)
	g.restoreLayout(ws.Appearance)
	g.body.OnAction(func(cui.Widget) { g.queueLayoutSave() })
	g.contextSplit.OnAction(func(cui.Widget) { g.queueLayoutSave() })
	g.app.Every(33, g.drain)
	g.app.Every(500, g.flushEditorDrafts)
	g.updateLayout()
	return g
}

func (g *desktop) buildToolbar(parent cui.Widget) {
	bar := parent.Box(cui.Horizontal, 12)
	style(bar, background, foreground, 0, false, 12)
	iconButton(bar, cui.SymbolPanel, "Toggle sidebar · Ctrl+B", g.toggleSidebar)
	g.heading = label(bar, "New conversation", 12, foreground)
	g.heading.Expand(true)
	search := button(bar, "Search & commands    Ctrl+K", g.openPalette)
	g.commandButton = search
	search.SetSymbol(cui.SymbolSearch)
	search.Tooltip("Search commands, conversations and files · Ctrl+K")
	style(search, surface, muted, 8, true, 7)
	iconButton(bar, cui.SymbolFile, "Browse files · Ctrl+P", g.showFiles)
	iconButton(bar, cui.SymbolPanel, "Terminal · Ctrl+T", g.showTerminal)
	iconButton(bar, cui.SymbolReply, "Review changes · Ctrl+R", g.loadReview)
	iconButton(bar, cui.SymbolMore, "Workspace actions · Ctrl+K", g.openPalette)
}

func (g *desktop) buildSidebar() {
	style(g.sidebar, rail, foreground, 0, false, 16)
	g.sidebar.MinSize(230, 0)
	g.sidebar.SetVisible(g.sidebarVisible)
	brand := g.sidebar.Box(cui.Horizontal, 10)
	label(brand, "◈", 24, violet).Accessibility("Bifrost", "")
	name := brand.Box(cui.Vertical, 2)
	label(name, "Bifrost", 16, foreground).Font("", 16, 600)
	label(name, shortText(filepath.Base(g.ws.Root), 24), 10, muted).Tooltip(g.ws.Root)
	g.newButton = button(g.sidebar, "New conversation", g.newSession)
	g.newButton.SetSymbol(cui.SymbolPlus)
	style(g.newButton, accentSoft, foreground, 9, false, 9)
	nav := g.sidebar.Box(cui.Horizontal, 4)
	g.conversationsTab = button(nav, "Chats", func() { g.selectNavigation(false) })
	g.conversationsTab.SetSymbol(cui.SymbolThread)
	g.conversationsTab.Expand(true)
	g.filesTab = button(nav, "Files", func() { g.selectNavigation(true) })
	g.filesTab.SetSymbol(cui.SymbolFile)
	g.filesTab.Expand(true)
	g.sideSearch = g.sidebar.Search("")
	g.sideSearch.Placeholder("Search conversations…")
	style(g.sideSearch, surface, foreground, 8, true, 6)
	g.sideSearch.OnAction(func(cui.Widget) { g.filterNavigation() })
	g.sideEmpty = label(g.sidebar, "No conversations yet", 10, muted)
	g.conversationPage = g.sidebar.Box(cui.Vertical, 8)
	g.conversationPage.Expand(true)
	g.sessionList = g.conversationPage.List(nil)
	style(g.sessionList, rail, foreground, 6, false)
	g.sessionList.Expand(true)
	g.sessionList.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i >= 0 && i < len(g.visibleSessions) {
			g.resume(g.visibleSessions[i].ID)
		}
	})
	g.filesPage = g.sidebar.Box(cui.Vertical, 8)
	g.filesPage.Expand(true)
	header := g.filesPage.Box(cui.Horizontal, 6)
	g.directory = label(header, "/", 10, muted)
	g.directory.Expand(true)
	iconButton(header, cui.SymbolUp, "Parent directory", func() { g.loadDirectory(filepath.Dir(g.cwd)) })
	iconButton(header, cui.SymbolRepeat, "Refresh directory", func() { g.loadDirectory(g.cwd) })
	g.fileList = g.filesPage.List(nil)
	style(g.fileList, rail, foreground, 6, false)
	g.fileList.Expand(true)
	g.fileList.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i < 0 || i >= len(g.visibleFiles) {
			return
		}
		e := g.visibleFiles[i]
		if e.IsDir {
			g.loadDirectory(e.Path)
		} else {
			g.loadFile(e.Path)
		}
	})
	footer := g.sidebar.Box(cui.Vertical, 3)
	label(footer, "WORKSPACE", 9, muted)
	for _, dest := range []struct {
		name string
		icon int
	}{{"Project", cui.SymbolHome}, {"Tasks", cui.SymbolPoll}, {"Skills", cui.SymbolFile}, {"MCP servers", cui.SymbolPanel}} {
		name := dest.name
		navigationButton(footer, name, dest.icon, func() { g.openWorkbench(name) })
	}
	utility := footer.Box(cui.Horizontal, 4)
	navigationButton(utility, "Settings", cui.SymbolSettings, func() { g.openWorkbench("Settings") })
	iconButton(utility, cui.SymbolPerson, "Accounts", func() { g.openWorkbench("Accounts") })
	iconButton(utility, cui.SymbolReply, "Review changes", g.loadReview)
	g.selectNavigation(false)
}

func (g *desktop) buildConversation() {
	g.buildChatTabs()
	g.upperSpace = g.content.Box(cui.Vertical, 0)
	g.upperSpace.Expand(true)
	g.welcomeCompact = label(g.content, "What are we building?", 16, foreground)
	g.welcomeCompact.SetVisible(false)
	g.buildWelcome()
	g.timeline = g.content.Chat(cui.ChatTimeline, cui.ChatNebula)
	g.timeline.SetTheme(chatTheme())
	p, _ := cui.ChatPresentationPreset(cui.ChatNebula)
	p.Messages = cui.ChatDocument
	p.ShowSender = true
	g.timeline.SetPresentation(p)
	g.timeline.SetCommands([]cui.ChatCommand{{ID: 1, Label: "Copy message", Action: cui.ChatCopy}, {ID: 2, Label: "Expand / collapse", Action: cui.ChatThread}, {ID: 3, Label: "Read full message", Action: cui.ChatMore}})
	g.timeline.Root.Expand(true)
	g.timeline.Root.MinSize(180, 80)
	g.timeline.Root.OnAction(func(cui.Widget) {
		e, ok := g.timeline.Event()
		if !ok {
			return
		}
		g.handleTimelineAction(e)
	})

	row := g.content.Box(cui.Horizontal, 8)
	g.activityRow = row
	g.activity = label(row, "Ready when you are", 10, muted)
	g.activity.Expand(true)
	g.activityToggle = button(row, "Latest activity", g.toggleActivity)
	g.activityToggle.SetSymbol(cui.SymbolThread)
	iconButton(row, cui.SymbolDown, "Jump to latest message", func() { g.timeline.Scroll(-1) })
	g.feedback = g.content.Feedback(cui.Banner)
	g.buildComposer()
	g.buildCommandLine(g.content)
	g.lowerSpace = g.content.Box(cui.Vertical, 0)
	g.lowerSpace.Expand(true)
}

func (g *desktop) buildWelcome() {
	g.welcome = g.content.Box(cui.Vertical, 16)
	mark := g.welcome.Box(cui.Horizontal, 0)
	g.welcomeMark = mark
	mark.Role(cui.Ambient)
	mark.Padding(14)
	label(mark, "◈   SPACE TO BUILD", 12, violet)
	label(g.welcome, "What are we building?", 28, foreground).Font("", 28, 500)
	label(g.welcome, "Start with a question. Bring the code into focus.", 12, muted)
	row := g.welcome.Wrap(10)
	g.starters = row
	for _, item := range []struct {
		symbol                int
		title, detail, prompt string
	}{
		{cui.SymbolSearch, "Understand", "Map the codebase", "Explain this codebase: its main components, entry points, and how they fit together."},
		{cui.SymbolReply, "Review", "Find what needs attention", "Review my local changes for correctness and missing tests. Do not modify files."},
		{cui.SymbolEdit, "Plan", "Shape the next change", "Help me plan a feature for this project. First ask what I want to build."},
	} {
		card := row.Box(cui.Vertical, 8)
		style(card, surface, foreground, 12, true, 14)
		card.MinSize(170, 0)
		b := button(card, item.title, func() { g.setPrompt(item.prompt) })
		b.SetSymbol(item.symbol)
		style(b, 0, foreground, 6, false, 4)
		label(card, item.detail, 10, muted)
	}
}

func (g *desktop) buildComposer() {
	compose := g.content.Box(cui.Vertical, 10)
	style(compose, surface, foreground, 14, true, 16)
	g.composer = compose.Textarea("")
	g.composer.Placeholder("Ask anything, or describe a change…")
	g.composer.MinSize(180, 82)
	style(g.composer, surface, foreground, 8, false)
	g.buildMentions(compose)
	g.composer.Accessibility("Message Bifrost", "Enter sends. Shift Enter or Alt Enter inserts a new line.")
	row := compose.Box(cui.Horizontal, 8)
	iconButton(row, cui.SymbolAttach, "Choose a file to mention · Ctrl+P", g.showFiles)
	g.profiles = row.Select([]string{"Offline"})
	g.modelName = label(row, "Offline", 10, muted)
	g.modelName.Expand(true)
	g.modelName.MinSize(140, 0)
	style(g.profiles, elevated, foreground, 8, false, 6)
	g.profiles.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i >= 0 && i < len(g.accounts) {
			g.switchAccount(g.accounts[i].ID)
		}
	})
	row.Box(cui.Horizontal, 0).Expand(true)
	g.stop = button(row, "Stop", g.cancelTurn)
	g.stop.SetSymbol(cui.SymbolPause)
	g.stop.SetVisible(false)
	g.send = button(row, "Send", g.sendPrompt)
	g.send.SetSymbol(cui.SymbolArrowRight)
	g.send.IconTrailing(true)
	style(g.send, violet, background, 9, false, 9)
	g.location = label(g.content, "Enter to send   ·   Shift+Enter for a new line", 10, muted)
	g.composer.OnKey(func(key int, mods uint) bool {
		if mods == 0 && g.mentionKey(key) {
			return true
		}
		if key == cui.KeyEnter && mods&(cui.ModShift|cui.ModAlt|cui.ModControl) != 0 {
			g.composer.InsertText("\n")
			return true
		}
		if key == cui.KeyEnter && mods&(cui.ModShift|cui.ModAlt|cui.ModControl) == 0 {
			g.sendPrompt()
			return true
		}
		return false
	})
}

func (g *desktop) buildInspector() {
	style(g.inspector, surface, foreground, 0, false, 12)
	g.inspector.MinSize(290, 0)
	g.inspector.SetVisible(false)
	row := g.inspector.Box(cui.Horizontal, 8)
	button(row, "Source", g.showSourcePage)
	button(row, "Terminal", g.showTerminal)
	row.Box(cui.Horizontal, 0).Expand(true)
	iconButton(row, cui.SymbolClose, "Close source · Ctrl+J", g.toggleInspector)
	g.sourcePage = g.inspector.Box(cui.Vertical, 10)
	g.sourcePage.Expand(true)
	g.buildViewerControls()
	g.buildEditorControls()
	g.preview = g.sourcePage.Code("Choose a file from the sidebar.\n\nSource opens here alongside\nyour conversation.")
	g.preview.ReadOnly(true)
	g.preview.Expand(true)
	g.preview.Font("monospace", 11, 400)
	g.preview.SetTextSpacing(3, 0)
	style(g.preview, background, foreground, 8, false, 8)
	g.preview.OnAction(func(cui.Widget) { g.rememberEditor() })
	g.preview.OnKey(g.editorKey)
	row = g.sourcePage.Box(cui.Horizontal, 8)
	g.mention = iconButton(row, cui.SymbolAttach, "Mention this file in your prompt", g.mentionSource)
	g.mention.SetSymbol(cui.SymbolAttach)
	g.mention.SetEnabled(false)
	row.Box(cui.Horizontal, 0).Expand(true)
	g.saveSource = button(row, "Save changes", g.saveEditor)
	style(g.saveSource, accentSoft, foreground, 8, false, 6)
	g.editorState = label(g.sourcePage, "Read-only preview", 9, muted)
	g.updateEditorActions()
	g.buildTerminal()
}

func (g *desktop) buildStatus(root cui.Widget) {
	bar := root.Box(cui.Horizontal, 14)
	style(bar, rail, muted, 0, false, 8)
	g.connection = label(bar, "○  Offline", 9, cyan)
	g.status = label(bar, "Ready", 9, muted)
	g.status.Expand(true)
	g.permissions = label(bar, "", 9, muted)
}

func (g *desktop) bindCommands() {
	g.primaryCommand("Focus composer", 'l', func() { g.composer.Focus() })
	g.primaryCommand("Search & commands", 'k', g.openPalette)
	g.primaryCommand("Find files", 'p', g.showFiles)
	g.primaryCommand("Toggle sidebar", 'b', g.toggleSidebar)
	g.primaryCommand("Newline or source", 'j', func() {
		if g.composer.HasFocus() {
			g.composer.InsertText("\n")
		} else {
			g.toggleInspector()
		}
	})
	g.primaryCommand("New conversation", 'n', g.newSession)
	g.primaryCommand("Review changes", 'r', func() {
		if g.preview.HasFocus() {
			g.editorUndo(true)
		} else {
			g.loadReview()
		}
	})
	g.primaryCommand("Terminal", 't', g.showTerminal)
	g.primaryCommand("Save", 's', func() {
		if g.workbench.open == "Settings" {
			g.saveSettings()
		} else {
			g.saveEditor()
		}
	})
	g.app.Command("Dismiss or stop", cui.KeyEscape, 0, func() {
		if g.mentionKey(cui.KeyEscape) {
		} else if g.paletteOpen {
			g.closePalette()
		} else if g.workbench.open != "" {
			g.closeWorkbench()
		} else if g.editorSearch.HasFocus() {
			g.closeEditorSearch()
		} else if g.preview.HasFocus() && g.editorKey(cui.KeyEscape, 0) {
		} else if g.terminal.input.HasFocus() {
			g.composer.Focus()
		} else {
			g.cancelTurn()
		}
	})
}

func (g *desktop) showSourcePage() {
	g.sourcePage.SetVisible(true)
	g.terminalPage.SetVisible(false)
	g.terminal.visible = false
}

func (g *desktop) toggleSidebar() {
	g.sidebarVisible = !g.sidebarVisible
	g.sidebar.SetVisible(g.sidebarVisible)
}
func (g *desktop) toggleInspector() {
	g.inspectorVisible = !g.inspectorVisible
	g.inspector.SetVisible(g.inspectorVisible)
}
func (g *desktop) setPrompt(text string) { g.replacePrompt(text); g.composer.Focus() }

func (g *desktop) primaryCommand(name string, key uint, action func()) {
	g.app.Command(name, key, cui.ModPrimary, func() {
		if g.paletteOpen && key != 'k' {
			return
		}
		if g.terminal.input.HasFocus() {
			g.terminalKey(int(key)-int('a')+int('A'), cui.ModControl)
			return
		}
		if g.workbench.open != "" && key != 'k' && key != 's' {
			g.closeWorkbench()
		}
		if key == 's' && g.workbench.open != "" && g.workbench.open != "Settings" {
			return
		}
		action()
	})
}

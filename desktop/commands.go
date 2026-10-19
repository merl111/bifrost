package main

import (
	"bifrost/internal/config"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bifrost/internal/domain"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type commandState struct {
	completion        ui.CommandCompletion
	pendingLayout     *ui.LayoutSettings
	layoutDue         time.Time
	root, input, hint cui.Widget
	history           []string
	index             int
	layout            ui.LayoutSettings
	layoutBusy        bool
}

func (g *desktop) buildCommandLine(parent cui.Widget) {
	c := &g.commands
	c.layout, _ = ui.LayoutPreset("studio")
	c.root = parent.Box(cui.Vertical, 5)
	style(c.root, surface, foreground, 8, true, 10)
	row := c.root.Box(cui.Horizontal, 6)
	label(row, ":", 13, violet)
	c.input = row.Entry("")
	c.input.Expand(true)
	c.input.Placeholder("Command · Tab completes · Enter runs · Escape closes")
	button(row, "Run", g.runCommandLine)
	button(row, "Close", func() { c.root.SetVisible(false); g.composer.Focus() })
	c.hint = label(c.root, "", 10, muted)
	c.input.OnAction(func(cui.Widget) {
		c.hint.SetText(strings.Join(ui.CommandSuggestions(c.input.Text(), g.commandContext()), "   ·   "))
	})
	c.input.OnKey(func(key int, mods uint) bool {
		switch key {
		case cui.KeyEnter:
			g.runCommandLine()
			return true
		case cui.KeyEscape:
			c.root.SetVisible(false)
			g.composer.Focus()
			return true
		case cui.KeyTab:
			text := c.completion.Complete(c.input.Text(), mods&cui.ModShift == 0, g.commandContext())
			c.input.SetText(text)
			c.input.SetTextRange(uint(len(text)), uint(len(text)))
			return true
		case cui.KeyUp, cui.KeyDown:
			if len(c.history) == 0 {
				return true
			}
			d := 1
			if key == cui.KeyUp {
				d = -1
			}
			c.index = max(0, min(len(c.history), c.index+d))
			text := ""
			if c.index < len(c.history) {
				text = c.history[c.index]
			}
			c.input.SetText(text)
			return true
		}
		return false
	})
	c.root.SetVisible(false)
}
func (g *desktop) openCommand(input string) {
	g.closePalette()
	g.closeWorkbench()
	g.commands.root.SetVisible(true)
	g.commands.input.SetText(input)
	g.commands.completion = ui.CommandCompletion{}
	g.commands.input.Focus()
}
func (g *desktop) runCommandLine() {
	c := &g.commands
	input := c.input.Text()
	if err := g.executeCommand(input); err != nil {
		c.hint.SetText(err.Error())
		return
	}
	c.history = append(c.history, input)
	if len(c.history) > 100 {
		c.history = c.history[len(c.history)-100:]
	}
	c.index = len(c.history)
	c.input.SetText("")
	c.root.SetVisible(false)
}
func (g *desktop) executeCommand(input string) error {
	fields, err := ui.CommandFields(input)
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		g.commandHelp("")
		return nil
	}
	spec, ok := ui.Command(fields[0])
	if !ok {
		return fmt.Errorf("unknown command %q · use :help", fields[0])
	}
	args := fields[1:]
	usage := func() error { return fmt.Errorf("usage: :%s", spec.Usage) }
	if len(args) == 1 && strings.EqualFold(args[0], "--help") {
		g.commandHelp(spec.Name)
		return nil
	}
	if request, custom, err := ui.ParseExtensionCommand(spec.Name, args); custom {
		if err != nil {
			return err
		}
		return g.commandExtension(request)
	}
	handlers := g.commandHandlers()
	handler := handlers[spec.Name]
	if handler == nil {
		return fmt.Errorf("command %s has no desktop handler", spec.Name)
	}
	if err := handler(args); err != nil {
		return fmt.Errorf("%w · %s", err, usage())
	}
	return nil
}
func noArguments(args []string, action func()) error {
	if len(args) > 0 {
		return fmt.Errorf("unexpected arguments")
	}
	action()
	return nil
}
func (g *desktop) commandHandlers() map[string]func([]string) error {
	simple := func(action func()) func([]string) error {
		return func(a []string) error { return noArguments(a, action) }
	}
	return map[string]func([]string) error{
		"explain": g.commandExplain, "walkthrough": g.commandWalkthrough,
		"save": simple(g.saveEditor), "discard": simple(g.reloadEditor), "review": simple(g.loadReview), "external": simple(g.externalEditor),
		"editor":   simple(g.editViewedSource),
		"settings": simple(func() { g.openWorkbench("Settings") }), "config": simple(func() { g.openWorkbench("Settings") }),
		"new": simple(g.newSession), "quit": simple(g.confirmClose),
		"compact": simple(func() { g.changeConversation(g.ws.Compact) }),
		"help": func(a []string) error {
			if len(a) > 1 {
				return fmt.Errorf("expected a command name")
			}
			name := ""
			if len(a) == 1 {
				name = a[0]
				if _, ok := ui.Command(name); !ok {
					return fmt.Errorf("unknown command %q", name)
				}
			}
			g.commandHelp(name)
			return nil
		},
		"accounts": func(a []string) error {
			if len(a) > 1 || (len(a) == 1 && a[0] != "add") {
				return fmt.Errorf("expected add")
			}
			g.openWorkbench("Accounts")
			return nil
		},
		"history": func(a []string) error {
			if len(a) > 1 {
				return fmt.Errorf("expected session ID")
			}
			if len(a) == 0 {
				g.selectNavigation(false)
				if !g.sidebarVisible {
					g.toggleSidebar()
				}
			} else if a[0] == "new" {
				g.newSession()
			} else {
				g.resume(a[0])
			}
			return nil
		},
		"model": g.commandModel, "terminal": g.commandTerminal, "layout": g.commandLayout, "resize": g.commandResize,
		"marketplace": g.commandMarketplace, "mcp": func(a []string) error {
			if len(a) > 1 || (len(a) == 1 && a[0] != "browse") {
				return fmt.Errorf("expected browse")
			}
			return g.commandMarketplace([]string{"mcp"})
		},
		"skill": g.commandSkill, "tasks": g.commandTasks, "project": g.commandProject, "chat": g.commandChat,
	}
}
func (g *desktop) commandHelp(name string) {
	var b strings.Builder
	if name != "" {
		s, _ := ui.Command(name)
		fmt.Fprintf(&b, ":%s\n\n%s\nAliases: %s", s.Usage, s.Help, strings.Join(s.Aliases, ", "))
	} else {
		b.WriteString("Bifrost commands\n\nEnter a /command or :command in the composer, or select one from Search & commands.\n\n")
		for _, s := range ui.Commands() {
			fmt.Fprintf(&b, ":%s\n  %s\n\n", s.Usage, s.Help)
		}
		b.WriteString("Keyboard\nCtrl+K commands · Ctrl+P files · Ctrl+S save · Ctrl+T terminal\nCtrl+B sidebar · Ctrl+J inspector · Ctrl+R review · Ctrl+L composer\nEnter sends · Shift+Enter inserts a newline · Escape stops generation\n")
	}
	text := b.String()
	g.loadSource("Command reference", func(context.Context) (string, error) { return text, nil })
}
func (g *desktop) commandModel(a []string) error {
	if len(a) > 1 {
		return fmt.Errorf("expected one model slug")
	}
	if len(a) == 0 {
		g.openQuickSelection("model")
		return nil
	}
	if g.busy {
		return fmt.Errorf("wait for the active operation before switching models")
	}
	for _, account := range g.accounts {
		if account.Active {
			id := account.ID
			g.changeConversation(func(ctx context.Context) error { return g.ws.SwitchModel(ctx, id, a[0]) })
			return nil
		}
	}
	return fmt.Errorf("connect an account first")
}
func (g *desktop) commandTerminal(a []string) error {
	if len(a) > 1 {
		return fmt.Errorf("expected one terminal action")
	}
	sub := "on"
	if len(a) == 1 {
		sub = strings.ToLower(a[0])
	}
	switch sub {
	case "on":
		g.showTerminal()
		if len(a) > 0 {
			l := g.commands.layout
			l.TerminalVisible = true
			if l.TerminalPercent == 0 {
				l.TerminalPercent = 25
			}
			g.saveLayout(l)
		}
	case "off":
		g.showSourcePage()
		l := g.commands.layout
		l.TerminalVisible = false
		g.saveLayout(l)
	case "new":
		g.newTerminal(nil, "")
	case "next":
		g.cycleTerminal(1)
	case "prev":
		g.cycleTerminal(-1)
	default:
		return fmt.Errorf("unknown terminal action")
	}
	return nil
}
func (g *desktop) commandLayout(a []string) error {
	if len(a) != 1 {
		return fmt.Errorf("expected layout name")
	}
	l, ok := ui.LayoutPreset(a[0])
	if !ok {
		return fmt.Errorf("unknown layout")
	}
	g.saveLayout(l)
	return nil
}
func (g *desktop) commandResize(a []string) error {
	if len(a) != 2 {
		return fmt.Errorf("expected pane and percentage")
	}
	n, err := strconv.Atoi(a[1])
	if err != nil {
		return err
	}
	l, err := ui.ResizeLayout(g.commands.layout, a[0], n)
	if err == nil {
		g.saveLayout(l)
	}
	return err
}
func (g *desktop) saveLayout(l ui.LayoutSettings) {
	if g.commands.layoutBusy {
		g.showError(fmt.Errorf("layout save is already in progress"))
		return
	}
	g.commands.layoutBusy = true
	g.work(func(context.Context) func() {
		err := g.ws.SaveLayout(l)
		return func() {
			g.commands.layoutBusy = false
			if err != nil {
				g.showError(err)
				g.applyLayout(g.commands.layout)
				return
			}
			g.commands.layout = l
			g.applyLayout(l)
		}
	})
}
func (g *desktop) applyLayout(l ui.LayoutSettings) {
	g.sidebarVisible = l.Files > 0
	g.inspectorVisible = l.Viewer > 0
	g.sidebar.SetVisible(g.sidebarVisible)
	g.inspector.SetVisible(g.inspectorVisible)
	if l.Files > 0 {
		g.body.SetSplitPosition(float64(l.Files) / 100)
	}
	if l.Viewer+l.Chat > 0 {
		g.contextSplit.SetSplitPosition(float64(l.Chat) / float64(l.Viewer+l.Chat))
	}
}
func (g *desktop) commandMarketplace(a []string) error {
	if len(a) > 1 {
		return fmt.Errorf("expected skills or mcp")
	}
	kind := 0
	if len(a) == 1 {
		switch strings.ToLower(a[0]) {
		case "skills":
		case "mcp", "servers":
			kind = 1
		default:
			return fmt.Errorf("expected skills or mcp")
		}
	}
	name := "Skills"
	if kind == 1 {
		name = "MCP servers"
	}
	g.openWorkbench(name)
	return nil
}
func (g *desktop) commandSkill(a []string) error {
	if len(a) == 1 && a[0] == "browse" {
		return g.commandMarketplace(nil)
	}
	if len(a) != 2 && len(a) != 4 {
		return fmt.Errorf("expected browse or install ID [--scope project|global]")
	}
	if a[0] != "install" {
		return fmt.Errorf("expected install")
	}
	scope := "project"
	if len(a) == 4 {
		if a[2] != "--scope" || (a[3] != "project" && a[3] != "global") {
			return fmt.Errorf("invalid scope")
		}
		scope = a[3]
	}
	m := &g.workbench.market
	if m.installing {
		return fmt.Errorf("installation already in progress")
	}
	m.installing = true
	id := a[1]
	g.work(func(ctx context.Context) func() {
		_, err := g.ws.Marketplace.Preview(ctx, id)
		result := ""
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			result, err = g.ws.Marketplace.Install(ctx, id, scope)
		}
		return func() {
			m.installing = false
			if err != nil {
				g.showError(err)
			} else {
				g.status.SetText(safeText(result))
			}
		}
	})
	return nil
}
func (g *desktop) commandTasks(a []string) error {
	if len(a) == 0 || (len(a) == 1 && a[0] == "refresh") {
		g.openWorkbench("Tasks")
		return nil
	}
	if len(a) != 2 || a[0] != "cancel" {
		return fmt.Errorf("expected refresh or cancel ID")
	}
	id, err := domain.ParseID(a[1])
	if err != nil {
		return err
	}
	g.work(func(ctx context.Context) func() {
		err := g.ws.Tasks.Cancel(ctx, id)
		return func() {
			if err != nil {
				g.showError(err)
			} else {
				g.refreshTasks()
			}
		}
	})
	return nil
}

func (g *desktop) restoreLayout(settings config.UISettings) {
	if settings.Layout == "" {
		return
	}
	l, ok := ui.LayoutPreset(settings.Layout)
	if !ok {
		return
	}
	p := settings.PaneProportions
	if p[0] >= 10 && p[1] >= 10 && p[2] >= 10 && p[0]+p[1]+p[2] == 100 {
		l.Files, l.Viewer, l.Chat = p[0], p[1], p[2]
	}
	if settings.TerminalVisible != nil {
		l.TerminalVisible = *settings.TerminalVisible
	}
	if settings.TerminalPercent > 0 {
		l.TerminalPercent = settings.TerminalPercent
	}
	g.commands.layout = l
	g.applyLayout(l)
}
func (g *desktop) queueLayoutSave() {
	l := g.commands.layout
	if g.sidebarVisible {
		l.Files = max(10, min(80, int(g.body.SplitPosition()*100)))
	}
	remaining := 100 - l.Files
	if g.inspectorVisible {
		l.Chat = max(10, min(remaining-10, int(g.contextSplit.SplitPosition()*float64(remaining))))
	}
	l.Viewer = remaining - l.Chat
	if l.Viewer < 10 {
		l.Viewer = 10
		l.Chat = remaining - 10
	}
	if l.Name == "focus" {
		l.Name = "studio"
	}
	g.commands.pendingLayout = &l
	g.commands.layoutDue = time.Now().Add(400 * time.Millisecond)
}
func (g *desktop) updateLayoutSave() {
	c := &g.commands
	if c.pendingLayout == nil || c.layoutBusy || time.Now().Before(c.layoutDue) {
		return
	}
	l := *c.pendingLayout
	c.pendingLayout = nil
	g.saveLayout(l)
}

func (g *desktop) commandContext() ui.CommandContext {
	w := g.workbench
	return ui.CommandContext{Tasks: w.tasks, Project: w.projects.status, Catalog: w.market.entries}
}

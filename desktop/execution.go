package main

import (
	"bifrost/internal/cli"
	"bifrost/internal/execpolicy"
	"context"
	cui "cui.local/cui"
	"encoding/json"
)

type executionView struct {
	mode    cui.Widget
	pending []*execpolicy.Request
	showing bool
}

func (g *desktop) buildExecutionControl(parent cui.Widget) {
	g.execution.mode = button(parent, "Commands: disabled", g.showExecutionModes)
	g.execution.mode.SetSymbol(cui.SymbolDown)
	g.execution.mode.IconTrailing(true)
	g.execution.mode.IconSize(12)
	style(g.execution.mode, elevated, foreground, 7, false, 6)
	g.execution.mode.Tooltip("Command approval policy. Terminal input is user controlled; vendor CLI tools use vendor permissions.")
}
func (g *desktop) showExecutionModes() {
	if g.busy {
		return
	}
	g.closePalette()
	g.openPalette()
	if input, ok := g.palette.PickerPart(cui.PickerInput); ok {
		input.Placeholder("Command permissions…")
	}
	g.paletteActions = make(map[uint64]func())
	items := []cui.Choice{}
	for i, choice := range []struct{ mode, label, detail string }{
		{"disabled", "Commands disabled", "Do not expose command execution to the agent"},
		{"ask", "Ask every time", "Review each exact command before it runs"},
		{"rules", "Use command rules", "Allow exact configured argument lists; ask about everything else"},
		{"allow", "Allow all commands", "Execute enabled agent commands without confirmation"},
	} {
		id := uint64(i + 1)
		items = append(items, cui.Choice{ID: id, Label: choice.label, Detail: choice.detail})
		g.paletteActions[id] = func() {
			g.changeConversation(func(ctx context.Context) error { return g.ws.SetExecutionMode(ctx, choice.mode) })
		}
	}
	items = append(items, cui.Choice{ID: 5, Label: "Edit command rules…", Detail: "Configure exact allowed argv arrays"})
	g.paletteActions[5] = func() {
		g.openWorkbench("Settings")
		g.workbench.settings.query.SetText("execution")
		g.filterSettings()
	}
	g.palette.PickerItems(items)
}

func (g *desktop) updateExecutionControl(s cli.DesktopSnapshot) {
	index := 0
	if s.ExecEnabled {
		index = 3
		switch s.Execution.Mode {
		case "ask":
			index = 1
		case "rules":
			index = 2
		}
	}
	g.execution.mode.SetText([]string{"Commands: disabled", "Commands: ask", "Commands: rules", "Commands: allow all"}[index])
}
func (g *desktop) queueExecutionApproval(request *execpolicy.Request) {
	g.execution.pending = append(g.execution.pending, request)
	g.showExecutionApproval()
}
func (g *desktop) showExecutionApproval() {
	e := &g.execution
	if e.showing {
		return
	}
	for len(e.pending) > 0 {
		request := e.pending[0]
		e.pending = e.pending[1:]
		select {
		case <-request.Done:
			continue
		default:
		}
		argv, _ := json.Marshal(request.Argv)
		e.showing = true
		g.window.Alert("Allow this command?", safeText("Workspace: "+request.Root+"\nDirectory: "+request.CWD+"\n\n"+string(argv)+"\n\nOnly this command will be approved. Cancel denies it."), "Allow once", func(result int, _ string) {
			select {
			case request.Reply <- result == cui.DialogAccepted:
			default:
			}
			e.showing = false
			g.showExecutionApproval()
		})
		return
	}
}

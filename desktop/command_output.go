package main

import (
	"fmt"
	"strings"

	"bifrost/internal/ui"
)

func (g *desktop) recordCommandOutput(e ui.ChatEvent) {
	if e.Kind != "tool" || !strings.HasPrefix(strings.ToLower(e.Text), "exec") {
		return
	}
	id := e.ID
	if id == "" {
		id = e.Text
	}
	id = fmt.Sprintf("%d:%s", g.commandRun, id)
	command, output, exit, done := ui.ExecToolDetail(e.Detail)
	var target *terminalView
	for _, t := range g.terminalTabs {
		if t.toolID == id {
			target = t
			break
		}
	}
	if target == nil {
		count := 0
		oldest := -1
		for i, t := range g.terminalTabs {
			if t.readonly {
				count++
				if oldest < 0 {
					oldest = i
				}
			}
		}
		if count >= 10 && oldest >= 0 {
			selected := oldest == g.terminalIndex
			g.terminalTabs = append(g.terminalTabs[:oldest], g.terminalTabs[oldest+1:]...)
			if selected {
				g.selectTerminal(min(oldest, len(g.terminalTabs)-1))
			} else if g.terminalIndex > oldest {
				g.terminalIndex--
			}
		}
		base := g.terminal
		target = &terminalView{toolID: id, readonly: true, title: "Agent command", output: base.output, input: base.input, state: base.state, start: base.start}
		g.terminalTabs = append(g.terminalTabs, target)
	}
	if command != "" {
		target.title = shortText(command, 32)
		target.command = command
	}
	state := "Running"
	if done {
		state = fmt.Sprintf("Exit %d", exit)
	}
	target.frame = ui.TerminalFrame{Text: "$ " + target.command + "\n" + state + "\n\n" + cleanText(output)}
	target.last = target.frame.Text
	if g.terminal == target {
		g.renderTerminal(target)
	}
	g.refreshTerminalChoices()
}

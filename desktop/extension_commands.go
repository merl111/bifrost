package main

import (
	"context"
	cui "cui.local/cui"
	"fmt"
	"strings"

	"bifrost/internal/ui"
)

func (g *desktop) commandExtension(request ui.ExtensionRequest) error {
	if g.ws.Extensions == nil {
		return fmt.Errorf("custom extensions unavailable")
	}
	m := &g.workbench.market
	if m.installing {
		return fmt.Errorf("an extension operation is already running")
	}
	m.installing = true
	g.status.SetText("Configuring extension…")
	run := g.ws.Extensions
	g.work(func(ctx context.Context) func() {
		result, err := run(ctx, request)
		return func() {
			m.installing = false
			if err != nil {
				g.showError(err)
				return
			}
			g.status.SetText(safeText(result))
			g.loadSource("Extension result", func(context.Context) (string, error) { return result, nil })
		}
	})
	return nil
}

// The picker prepares an editable command so scope and source remain visible.
func (g *desktop) chooseExtensionPath(skill bool) {
	kind, title, command := cui.DialogOpen, "Choose an MCP config file", "/mcp config "
	if skill {
		kind, title, command = cui.DialogFolder, "Choose a folder containing SKILL.md", "/skill install "
	}
	g.window.FileDialog(kind, title, g.ws.Root, func(result int, path string) {
		if result != cui.DialogAccepted || path == "" {
			return
		}
		g.closeWorkbench()
		g.replacePrompt(command + "'" + strings.ReplaceAll(path, "'", "'\"'\"'") + "' --scope project")
		g.composer.Focus()
	})
}

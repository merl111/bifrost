package main

import (
	"bifrost/internal/cli"
	"bifrost/internal/ui"
	"context"
	cui "cui.local/cui"
	"fmt"
)

func contextLabel(info ui.ConnectionInfo) (string, string) {
	label := "Context —"
	detail := fmt.Sprintf("Approximately %d tokens. Model capacity is unknown.", info.ContextTokens)
	if info.ContextMaxTokens > 0 {
		percent := float64(info.ContextTokens) * 100 / float64(info.ContextMaxTokens)
		label = fmt.Sprintf("Context ~%.0f%%", percent)
		detail = fmt.Sprintf("Estimated context: %d / %d tokens (%.1f%%).", info.ContextTokens, info.ContextMaxTokens, percent)
	}
	return label, detail + " Click to summarize. The original conversation stays in History. Automatic summarization runs before a turn at 80% of known capacity or 400 KiB of text."
}
func (g *desktop) updateContextMeter(info ui.ConnectionInfo) {
	for _, model := range g.quick.models[info.Account] {
		if model.Slug == info.Model && model.ContextLength > 0 {
			info.ContextMaxTokens = model.ContextLength
			break
		}
	}
	text, tip := contextLabel(info)
	g.contextMeter.SetText(text)
	g.contextMeter.Tooltip(tip)
	g.contextMeter.Accessibility(text, tip)
}
func (g *desktop) summarizeConversation() {
	if g.busy || snapshotSession(g.activeChat().snapshot) == "" {
		return
	}
	origin := g.activeChat()
	before := origin.snapshot.Connection.ContextTokens
	id := snapshotSession(origin.snapshot)
	g.setBusy(true)
	g.activity.SetText("Summarizing conversation…")
	g.work(func(ctx context.Context) func() {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		err := g.ws.Resume(ctx, id)
		if err == nil {
			err = g.ws.Compact(ctx)
		}
		var snapshot cli.DesktopSnapshot
		if err == nil {
			snapshot, err = g.ws.Snapshot(ctx)
		}
		return func() {
			if err == nil {
				g.applySnapshot(snapshot)
				origin.status = fmt.Sprintf("Summarized · ~%d → ~%d tokens · original saved in History", before, snapshot.Connection.ContextTokens)
				g.feedback.FeedbackShow("Conversation summarized", origin.status, cui.Success, "", 0)
			}
			g.setBusy(false)
			if err != nil {
				g.showError(err)
			}
		}
	})
}
func (g *desktop) showHistory() {
	if !g.sidebarVisible {
		g.toggleSidebar()
	}
	g.selectNavigation(false)
	g.sideSearch.Focus()
	g.work(func(ctx context.Context) func() {
		snapshot, err := g.ws.Snapshot(ctx)
		return func() {
			if err != nil {
				g.showError(err)
				return
			}
			g.syncRuntimeSnapshot(snapshot)
			g.sessions = g.activeChat().snapshot.Sessions
			g.filterNavigation()
		}
	})
}

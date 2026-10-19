package main

import (
	"context"
	"errors"
	"strings"

	"bifrost/internal/cli"
	"bifrost/internal/ui"
)

func (g *desktop) sendPrompt() {
	prompt := strings.TrimSpace(g.composer.Text())
	if strings.HasPrefix(prompt, ":") {
		if err := g.executeCommand(prompt); err != nil {
			g.showError(err)
		} else if strings.TrimSpace(g.composer.Text()) == prompt {
			g.replacePrompt("")
		}
		return
	}
	origin := g.activeChat()
	if g.operationBusy || origin.cancel != nil || !g.connected || prompt == "" {
		return
	}
	if len([]rune(prompt)) > 16<<10 {
		g.status.SetText("Message is too long (maximum 16,384 characters, matching the TUI).")
		return
	}
	g.commandRun++
	origin.runID = g.commandRun
	sessionID := snapshotSession(origin.snapshot)
	terminal := g.terminal.session
	ctx, cancel := context.WithCancel(g.ctx)
	origin.cancel = cancel
	origin.unread = false
	if len(g.transcript.messages) == 0 && origin.title == "" {
		origin.title = shortText(prompt, 64)
	}
	g.transcript.apply(ui.ChatEvent{Kind: "user", Text: prompt})
	g.heading.SetText(shortText(prompt, 42))
	g.activityLog = ""
	g.feedback.FeedbackDismiss()
	g.dirty = true
	g.replacePrompt("")
	g.refreshBusy()
	g.refreshChatTabs()
	g.work(func(context.Context) func() {
		emit := func(event ui.ChatEvent) {
			g.post(func() { g.receiveChatEvent(origin, event) })
		}
		expanded := prompt
		if strings.Contains(prompt, "@") {
			var refs []string
			expanded, refs = g.ws.MentionContext(ctx, terminal).Expand(prompt)
			if len(refs) > 0 {
				emit(ui.ChatEvent{Kind: "status", Text: "Attached context: " + strings.Join(refs, ", ")})
			}
		}
		snapshot, err := g.ws.RunConversation(ctx, sessionID, expanded, emit)
		cancel()
		return func() { g.completeChatTurn(origin, snapshot, err) }
	})
}

func (g *desktop) receiveChatEvent(origin *chatTab, event ui.ChatEvent) {
	if event.Approval != nil {
		g.queueExecutionApproval(event.Approval)
		return
	}
	if event.Kind == "session" {
		found := false
		for i := range origin.snapshot.Sessions {
			active := origin.snapshot.Sessions[i].ID == event.ID
			origin.snapshot.Sessions[i].Active = active
			found = found || active
		}
		if !found {
			origin.snapshot.Sessions = append(origin.snapshot.Sessions, ui.SessionSummary{ID: event.ID, Title: origin.title, Active: true})
		}
		return
	}
	if event.Connection != nil {
		origin.snapshot.Connection = *event.Connection
	}
	if g.activeChat() == origin {
		g.receive(event)
		return
	}
	if event.Kind == "viewer" {
		// A background conversation cannot replace the source being inspected.
		origin.viewer = &event
	} else {
		origin.transcript.apply(event)
	}
	g.recordCommandOutputForRun(event, origin.runID)
	switch event.Kind {
	case "tool", "thinking", "reasoning", "status", "summary", "decision":
		origin.activity = boundedLog(origin.activity, event.Text+"\n"+event.Detail)
	}
}

func (g *desktop) completeChatTurn(origin *chatTab, snapshot cli.DesktopSnapshot, err error) {
	origin.cancel = nil
	origin.status = "Conversation saved"
	if snapshotSession(snapshot) != "" {
		g.syncRuntimeSnapshot(snapshot)
		origin.snapshot = snapshot
		if g.activeChat() == origin {
			live := g.transcript
			g.displaySnapshot(snapshot)
			g.transcript = live
			origin.transcript = live
			g.dirty = true
		}
	}
	if errors.Is(err, context.Canceled) {
		origin.status = "Stopped · partial turn retained"
	} else if err != nil {
		g.receiveChatEvent(origin, ui.ChatEvent{Kind: "error", Text: err.Error()})
		origin.status = "Turn failed · details in conversation"
	}
	origin.unread = g.activeChat() != origin
	g.refreshBusy()
	g.refreshChatTabs()
}

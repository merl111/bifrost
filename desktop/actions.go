package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"bifrost/internal/cli"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

// Workers return closures containing copied results. Native calls happen only
// while drain runs on CUI's owning OS thread. The queue is bounded and closing
// the app unblocks producers even when the event loop no longer consumes.
func (g *desktop) post(fn func()) {
	select {
	case g.queue <- fn:
	case <-g.ctx.Done():
	}
}

func (g *desktop) work(job func(context.Context) func()) {
	g.workers.Add(1)
	go func() {
		defer g.workers.Done()
		result := job(g.ctx)
		if result != nil {
			g.post(result)
		}
	}()
}

func (g *desktop) drain() {
	if g.ctx.Err() != nil {
		g.cancel()
		g.app.Quit()
		return
	}
	if g.window.CloseRequested() {
		g.window.SetCloseDeferred(true)
		g.confirmClose()
	}
	for i := 0; i < 128; i++ {
		select {
		case fn := <-g.queue:
			fn()
		default:
			i = 128
		}
	}
	if g.dirty {
		if !g.timeline.SetMessages(g.transcript.native()) {
			g.status.SetText("The conversation could not be rendered. Saved history is retained.")
		}
		empty := len(g.transcript.messages) == 0
		g.welcome.SetVisible(empty)
		g.upperSpace.SetVisible(empty)
		g.lowerSpace.SetVisible(empty)
		g.timeline.Root.SetVisible(!empty)
		g.activityRow.SetVisible(!empty)
		g.activityToggle.SetEnabled(g.transcript.latestActivity() >= 0)
		g.dirty = false
	}
	// CUI caches unchanged frames, but must observe the current allocation after
	// a native resize; otherwise the old surface is scaled like an image.
	g.timeline.Refresh(g.window.Scale())
	g.updateLayout()
	g.refreshTerminal()
	g.updateMentions()
	g.updateComposer()
	g.updateLayoutSave()
	g.finishClose()
}

func (g *desktop) setBusy(busy bool) {
	g.busy = busy
	g.send.SetEnabled(!busy && g.connected)
	g.stop.SetEnabled(busy && g.turnCancel != nil)
	g.stop.SetVisible(busy && g.turnCancel != nil)
	g.activityToggle.SetEnabled(g.transcript.latestActivity() >= 0)
	g.newButton.SetEnabled(!busy && g.connected)
	g.profiles.SetEnabled(!busy && len(g.accounts) > 0)
	g.sessionList.SetEnabled(!busy && g.connected)
}

func (g *desktop) applySnapshot(s cli.DesktopSnapshot) {
	g.syncRuntimeSnapshot(s)
	g.displaySnapshot(s)
}

// Profiles belong to the shared runtime, not to a saved chat tab. A tab retains
// its history, but must advertise the account that will actually run its turn.
func (g *desktop) syncRuntimeSnapshot(s cli.DesktopSnapshot) {
	for _, tab := range g.chats.items {
		selected := snapshotSession(tab.snapshot)
		tab.snapshot.Sessions = append([]ui.SessionSummary(nil), s.Sessions...)
		for i := range tab.snapshot.Sessions {
			tab.snapshot.Sessions[i].Active = tab.snapshot.Sessions[i].ID == selected
		}
		tab.snapshot.Accounts = s.Accounts
		tab.snapshot.Connection = s.Connection
		tab.snapshot.WriteEnabled = s.WriteEnabled
		tab.snapshot.ExecEnabled = s.ExecEnabled
	}
}

func (g *desktop) displaySnapshot(s cli.DesktopSnapshot) {
	g.activeChat().snapshot = s
	g.refreshChatTabs()
	g.accounts, g.sessions, g.connected = s.Accounts, s.Sessions, s.Connection.Connected
	profiles := make([]string, 0, len(s.Accounts))
	selected := -1
	for i, a := range s.Accounts {
		profiles = append(profiles, shortText(a.Label+" · "+a.Model, 32))
		if a.Active {
			selected = i
		}
	}
	if len(profiles) == 0 {
		profiles = []string{"Offline workspace"}
		if s.Connection.Connected {
			profiles[0] = shortText(s.Connection.Model, 30)
		}
		selected = 0
	}
	g.profiles.Items(profiles)
	g.profiles.SetSelected(selected)
	g.profiles.SetVisible(len(g.accounts) > 1)
	g.modelName.SetVisible(len(g.accounts) <= 1)
	model := s.Connection.Model
	if model == "" {
		model = "Offline workspace"
	}
	g.modelName.SetText(shortText(model, 30))
	g.modelName.Tooltip(s.Connection.Account + " · " + model)
	g.filterNavigation()
	g.transcript = transcript{}
	for _, event := range s.History {
		g.transcript.apply(event)
	}
	g.dirty = true
	g.updateConnection(s.Connection)
	g.permissions.SetText(fmt.Sprintf("Agent writes: %s   ·   Commands: %s", enabledText(s.WriteEnabled), enabledText(s.ExecEnabled)))
	title := "New conversation"
	for _, s := range s.Sessions {
		if s.Active && s.Title != "(empty conversation)" {
			title = s.Title
		}
	}
	g.heading.SetText(shortText(title, 42))
	g.heading.Tooltip(title)
	g.setBusy(g.busy)
}

func enabledText(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func (g *desktop) updateConnection(info ui.ConnectionInfo) {
	g.connected = info.Connected
	if info.Connected {
		g.connection.SetText(shortText("●  "+info.Model, 32))
		g.connection.Tooltip(safeText(info.Account + " · " + g.ws.Root))
		if info.ContextMaxTokens > 0 {
			g.connection.SetText(shortText(info.Provider+" · "+info.Model, 30) + fmt.Sprintf(" · %d/%d tokens", info.ContextTokens, info.ContextMaxTokens))
		}
		g.location.SetText("Enter to send   ·   Shift+Enter for a new line")
	} else {
		g.connection.SetText("○  Offline workspace")
		g.location.SetText("Offline · files and review are available")
		g.feedback.FeedbackShow("Connect an account to start chatting", "Open Workspace → Accounts to add a provider or sign in.", cui.Warning, "", 0)
	}
}

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
	if g.busy || !g.connected || prompt == "" {
		return
	}
	if len([]rune(prompt)) > 16<<10 {
		g.status.SetText("Message is too long (maximum 16,384 characters, matching the TUI).")
		return
	}
	origin := g.activeChat()
	g.commandRun++
	sessionID := snapshotSession(origin.snapshot)
	terminal := g.terminal.session
	ctx, cancel := context.WithCancel(g.ctx)
	g.turnCancel = cancel
	g.setBusy(true)
	g.transcript.apply(ui.ChatEvent{Kind: "user", Text: prompt})
	g.heading.SetText(shortText(prompt, 42))
	g.activityLog = ""
	g.feedback.FeedbackDismiss()
	g.dirty = true
	g.replacePrompt("")
	g.activity.SetText("Bifrost is working…")
	g.status.SetText("Generating · Escape to stop")
	g.work(func(context.Context) func() {
		var err error
		if sessionID == "" {
			err = g.ws.NewSession(ctx)
		} else {
			err = g.ws.Resume(ctx, sessionID)
		}
		if err == nil {
			expanded := prompt
			if strings.Contains(prompt, "@") {
				var refs []string
				expanded, refs = g.ws.MentionContext(ctx, terminal).Expand(prompt)
				if len(refs) > 0 {
					g.post(func() {
						if g.activeChat() == origin {
							g.receive(ui.ChatEvent{Kind: "status", Text: "Attached context: " + strings.Join(refs, ", ")})
						}
					})
				}
			}
			err = g.ws.Run(ctx, expanded, func(event ui.ChatEvent) {
				g.post(func() {
					if g.activeChat() == origin {
						g.receive(event)
					} else {
						origin.transcript.apply(event)
						g.recordCommandOutput(event)
						if event.Kind == "viewer" {
							g.receiveViewer(event)
						}
						if event.Kind == "tool" || event.Kind == "thinking" || event.Kind == "status" {
							origin.activity = boundedLog(origin.activity, event.Text+"\n"+event.Detail)
						}
					}
				})
			})
		}
		cancel()
		snapshot, snapErr := g.ws.Snapshot(g.ctx)
		return func() {
			g.turnCancel = nil
			if snapErr == nil {
				g.syncRuntimeSnapshot(snapshot)
				origin.snapshot = snapshot
				if g.activeChat() == origin {
					live := g.transcript
					g.applySnapshot(snapshot)
					g.transcript = live
					origin.transcript = live
					g.dirty = true
				}
				g.refreshChatTabs()
			}
			g.setBusy(false)
			g.activity.SetText("Ready for the next step")
			g.status.SetText("Conversation saved")
			if errors.Is(err, context.Canceled) {
				g.status.SetText("Stopped · partial turn retained")
			} else if err != nil {
				if g.activeChat() == origin {
					g.receive(ui.ChatEvent{Kind: "error", Text: err.Error()})
				} else {
					origin.transcript.apply(ui.ChatEvent{Kind: "error", Text: err.Error()})
				}
				g.status.SetText("Turn failed · details in conversation")
			}
			if snapErr != nil {
				g.showError(snapErr)
			}
		}
	})
}

func (g *desktop) receive(e ui.ChatEvent) {
	if e.Kind == "viewer" {
		g.receiveViewer(e)
		return
	}
	if e.Connection != nil {
		g.updateConnection(*e.Connection)
	}
	g.recordCommandOutput(e)
	g.transcript.apply(e)
	g.dirty = true
	switch e.Kind {
	case "tool", "status", "thinking", "reasoning", "summary", "decision":
		detail := e.Text
		if e.Detail != "" {
			detail += "\n" + e.Detail
		}
		g.activityLog = boundedLog(g.activityLog, detail)
		g.activityToggle.SetEnabled(true)
		g.activity.SetText("Activity updated")
		if g.busy {
			g.activity.SetText("Bifrost is working…")
		}
	}
}

func (g *desktop) cancelTurn() {
	if g.turnCancel != nil {
		g.turnCancel()
		g.stop.SetEnabled(false)
		g.status.SetText("Stopping…")
	}
}

func (g *desktop) changeConversation(action func(context.Context) error) {
	if g.busy {
		return
	}
	sessionID := snapshotSession(g.activeChat().snapshot)
	connected := g.connected
	g.setBusy(true)
	g.status.SetText("Loading conversation…")
	g.work(func(ctx context.Context) func() {
		err := g.activateDisplayedSession(ctx, sessionID, connected)
		if err == nil {
			err = action(ctx)
		}
		var snapshot cli.DesktopSnapshot
		if err == nil {
			snapshot, err = g.ws.Snapshot(ctx)
		}
		return func() {
			if err != nil {
				g.showError(err)
			} else {
				g.activityLog = ""
				g.applySnapshot(snapshot)
				g.status.SetText("Ready")
			}
			g.setBusy(false)
		}
	})
}

func (g *desktop) newSession() { g.changeConversation(g.ws.NewSession) }
func (g *desktop) resume(id string) {
	g.changeConversation(func(ctx context.Context) error { return g.ws.Resume(ctx, id) })
}
func (g *desktop) switchAccount(id string) {
	g.changeConversation(func(ctx context.Context) error { return g.ws.SwitchAccount(ctx, id) })
}

func (g *desktop) showError(err error) {
	g.status.SetText(shortText(err.Error(), 100))
	g.status.Tooltip(safeText(err.Error()))
	g.feedback.FeedbackShow("Something needs attention", safeText(err.Error()), cui.Danger, "", 0)
}

func (g *desktop) loadDirectory(path string) {
	g.dirRequest++
	request := g.dirRequest
	g.work(func(ctx context.Context) func() {
		entries, err := g.ws.Backend.List(ctx, path)
		if len(entries) > 1000 {
			entries = entries[:1000]
		}
		return func() {
			if request != g.dirRequest {
				return
			}
			if err != nil {
				g.showError(err)
				return
			}
			g.cwd, g.files = path, entries
			g.directory.SetText(shortText(path, 22))
			g.directory.Tooltip(path)
			g.filterNavigation()
			if len(entries) == 1000 {
				g.status.SetText("Showing the first 1,000 directory entries")
			}
		}
	})
}

func (g *desktop) loadSource(title string, read func(context.Context) (string, error)) {
	g.rememberEditor()
	g.viewer.bar.SetVisible(false)
	g.viewer.path = ""
	g.showSourcePage()
	g.viewRequest++
	g.previewPath = ""
	g.previewLoading = false
	g.updateEditorActions()
	g.mention.SetEnabled(false)
	request := g.viewRequest
	if !g.inspectorVisible {
		g.toggleInspector()
	}
	g.previewTitle.SetText(shortText(title, 40))
	g.previewTitle.Tooltip(title)
	g.setEditorText("Loading…")
	g.work(func(ctx context.Context) func() {
		content, err := read(ctx)
		content = cleanText(content)
		return func() {
			if request != g.viewRequest {
				return
			}
			if err != nil {
				g.setEditorText(safeText(err.Error()))
				return
			}
			g.setEditorText(content)
			g.decorateSource(title, content)
			g.mention.SetEnabled(g.previewPath != "")
		}
	})
}

func (g *desktop) loadFile(path string) {
	g.openEditor(path)
}

func (g *desktop) loadReview() {
	g.loadSource("Working tree changes", func(ctx context.Context) (string, error) {
		review, err := g.ws.Backend.Review(ctx)
		return review.Text(), err
	})
}

func (g *desktop) activateDisplayedSession(ctx context.Context, id string, connected bool) error {
	if id != "" {
		return g.ws.Resume(ctx, id)
	}
	if connected {
		return g.ws.NewSession(ctx)
	}
	return nil
}

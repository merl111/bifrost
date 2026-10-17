package main

import (
	"bifrost/internal/ui"
	cui "cui.local/cui"
	"fmt"
	"strings"
)

func (p *nativeParity) chatPresentationParity(g *desktop) (bool, error) {
	if p.step >= 160 {
		return p.reviewParity(g)
	}
	fail := func(text string) (bool, error) { return false, fmt.Errorf("chat presentation %d: %s", p.step, text) }
	switch p.step {
	case 150:
		g.closeWorkbench()
		g.window.SetSize(1440, 920)
		g.inspectorVisible = false
		g.inspector.SetVisible(false)
		g.feedback.FeedbackDismiss()
		g.status.SetText("Ready")
		g.activeChat().title = "Chat organization"
		g.refreshChatTabs()
		g.transcript = transcript{}
		for _, e := range []ui.ChatEvent{
			{Kind: "user", Text: "What tools can you use in this project?"},
			{Kind: "status", Text: "Task fixture · monitoring available"},
			{Kind: "tool", ID: "context", Text: "Inspect project capabilities", Detail: "Files, Git review and local terminal are available."},
			{Kind: "thinking", Text: "Check which tools are enabled for this workspace."},
			{Kind: "assistant", Text: "I can inspect and edit files, review Git changes, and run commands when permissions allow.\n\nWhat would you like to work on?"},
			{Kind: "user", Text: "Make my messages easier to find."},
			{Kind: "tool", ID: "editor", Text: "Inspect the chat renderer", Detail: "Preserve message identity and source details."},
			{Kind: "thinking", Text: "Separate prompts from replies and group background events."},
			{Kind: "final", Text: "Your messages now appear in distinct bubbles on the right. My replies stay on the left, with background activity tucked into expandable rows."},
		} {
			g.transcript.apply(e)
		}
		g.dirty = true
		g.setPrompt("")
	case 151:
		g.timeline.Scroll(0)
	case 152:
		p.capture("desktop-chat-organized")
		event := cui.ChatEvent{Action: cui.ChatLink, ID: 2, Text: activityLink}
		region := g.timeline.ActionRegion(event)
		if region == 0 || !g.timeline.Part(0).CanvasActivateRegion(region) {
			return fail("activity disclosure has no native hit target")
		}
		if g.transcript.messages[1].Collapsed {
			return fail("native disclosure did not expand")
		}
	case 153:
		p.capture("desktop-chat-activity")
		if !strings.Contains(g.transcript.native()[1].Spans[1].Text, "Files, Git review") {
			return fail("expanded activity lost detail")
		}
		region := g.timeline.ActionRegion(cui.ChatEvent{Action: cui.ChatLink, ID: 2, Text: activityLink})
		if region == 0 || !g.timeline.Part(0).CanvasActivateRegion(region) {
			return fail("activity could not collapse")
		}
		g.window.SetSize(800, 720)
	case 154:
		g.timeline.Scroll(0)
	case 155:
		if w, h, _ := g.window.Size(); w > 800 || h > 720 {
			return fail(fmt.Sprintf("narrow window cannot shrink: %dx%d", w, h))
		}
		p.capture("desktop-chat-organized-narrow")
		g.window.SetSize(1200, 900)
		g.app.TextScale(1.5)
		g.textScale = 1.5
	case 156:
		g.timeline.Scroll(0)
	case 157:
		p.capture("desktop-chat-organized-large-text")
		p.step = 160
		return false, nil
	}
	p.step++
	return false, nil
}

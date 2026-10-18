package main

import (
	"bifrost/internal/ui"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (p *nativeParity) walkthroughParity(g *desktop) (bool, error) {
	if p.step >= 180 {
		return p.chatTabsParity(g)
	}
	fail := func(s string) (bool, error) { return false, fmt.Errorf("walkthrough phase %d: %s", p.step, s) }
	switch p.step {
	case 170:
		g.setPrompt("Keep this draft")
		if err := g.executeCommand("explain routing"); err == nil || g.composer.Text() != "Keep this draft" {
			return fail("draft overwritten")
		}
		g.setPrompt(":explain request routing")
		g.sendPrompt()
		if !strings.Contains(g.composer.Text(), "Back/Next") || g.busy {
			return fail("request not prepared or sent prematurely")
		}
		g.setPrompt("")
		g.window.SetSize(1600, 1000)
		appearance := g.ws.Appearance
		appearance.Theme = "midnight"
		g.applyAppearance(appearance)
		g.feedback.FeedbackDismiss()
		g.activeChat().title = "Understanding a request"
		g.refreshChatTabs()
		g.transcript = transcript{}
		g.transcript.apply(ui.ChatEvent{Kind: "user", Text: "Walk me through the request flow, then review the change."})
		g.transcript.apply(ui.ChatEvent{Kind: "assistant", Text: "Follow the explanation beside the code. Use Back and Next to move between context, the entry point, and the diff."})
		g.dirty = true
		g.contextSplit.SetSplitPosition(.48)
		code := "package handlers\n\nfunc Handle(input string) error {\n    return process(input)\n}\n"
		if err := os.WriteFile(filepath.Join(p.root, "handler.go"), []byte(code), 0600); err != nil {
			return fail(err.Error())
		}
		payload, _ := json.Marshal(ui.ViewerCommandPayload{Action: "flow", Title: "Understanding a request", Steps: []ui.ViewerStep{
			{Kind: "concept", Title: "Start with the contract", Explanation: "A request crosses three boundaries: input, processing, and output.\n\nWe will follow the entry point, then review a change to its behavior. Back and Next let you revisit any step without changing files."},
			{Kind: "code", Path: "handler.go", Title: "The entry point", StartLine: 3, EndLine: 5, Explanation: "This file defines the entry point in our fixture workspace. The highlighted lines show where processing starts.\n\nCheck its callers before changing the interface: a small signature change can affect several flows."},
			{Kind: "diff", Title: "Review the behavior change", Content: "--- a/handler.go\n+++ b/handler.go\n@@ -1,3 +1,6 @@\n func Handle(input string) error {\n+    if input == \"\" {\n+        return ErrEmptyInput\n+    }\n     return process(input)\n }", Explanation: "The change rejects empty input before processing.\n\nReview question: do existing callers rely on accepting empty strings? Verify both the error case and an ordinary request. This illustrative fixture is not a change to your repository."},
		}})
		g.receiveViewer(ui.ChatEvent{Kind: "viewer", Detail: string(payload)})
	case 171:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if g.viewer.index != 0 || !strings.Contains(g.viewer.explanation.Text(), "three boundaries") {
			return fail("concept step missing")
		}
		g.viewerStep(-1)
		if g.viewer.index != 0 {
			return fail("back wrapped")
		}
		g.viewer.next.Activate()
	case 172:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if g.viewer.index != 1 || !strings.Contains(g.preview.Text(), "func Handle") || !strings.Contains(g.viewer.explanation.Text(), "callers") {
			return fail("code step missing")
		}
		p.capture("desktop-explanation-code")
		g.viewer.next.Activate()
	case 173:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if g.viewer.index != 2 || !strings.Contains(g.preview.Text(), "ErrEmptyInput") || g.viewer.path != "" {
			return fail("diff editable or missing")
		}
		g.viewerStep(1)
		if g.viewer.index != 2 {
			return fail("next wrapped")
		}
	case 174:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		p.capture("desktop-explanation-review")
		g.viewer.previous.Activate()
	case 175:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if g.viewer.index != 1 || !strings.Contains(g.preview.Text(), "func Handle") {
			return fail("back did not restore code")
		}
		g.viewer.close.Activate()
		if len(g.viewer.steps) != 0 {
			return fail("end walkthrough failed")
		}
		p.step = 180
		return false, nil
	}
	p.step++
	return false, nil
}

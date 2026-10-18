package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

// These scenarios use the real native event loop, buffers, PTYs and runtime.
// Remote project details are fixtures so no user account or network is touched.
func (p *nativeParity) fullParity(g *desktop) (bool, error) {
	if p.step >= 108 && p.step <= 111 {
		return p.parallelChatParity(g)
	}
	if p.step >= 130 {
		return p.redesignParity(g)
	}
	fail := func(message string) (bool, error) {
		return false, fmt.Errorf("extended parity %d: %s", p.step, message)
	}
	switch p.step {
	case 100:
		g.window.SetSize(1440, 920)
		g.feedback.FeedbackDismiss()
		g.openEditor("second.go")
	case 101:
		if g.previewLoading {
			return false, nil
		}
		g.editorVim = true
		g.drafts["second.go"].buffer.SetInsertMode(false)
		g.updateEditorActions()
		g.preview.Focus()
		if err := injectKeys("g", "i", "x"); err != nil {
			return fail(err.Error())
		}
	case 102:
		if !strings.HasPrefix(g.preview.Text(), "xpackage") {
			return fail("native Vim normal/insert keys did not edit source: " + g.preview.Text())
		}
		if err := injectKeys("Escape", "u"); err != nil {
			return fail(err.Error())
		}
	case 103:
		if g.preview.Text() != "package second\n" {
			return fail("native Vim undo: " + g.preview.Text())
		}
		g.editorVim = false
		g.drafts["second.go"].buffer.SetInsertMode(true)
		g.updateEditorActions()
		g.preview.SetTextRange(0, 0)
		g.preview.InsertText("// recovered\n")
		g.rememberEditor()
		g.flushEditorDrafts()
	case 104:
		d := g.drafts["second.go"]
		if d.writing {
			return false, nil
		}
		restored, err := ui.ReadEditorDraft(filepath.Join(g.ws.StateDir, "drafts"), d.document)
		if err != nil || restored.Content != g.preview.Text() {
			return fail(fmt.Sprintf("durable draft: %v %q", err, restored.Content))
		}
		g.previewPath = ""
		delete(g.drafts, "second.go")
		g.openEditor("second.go")
	case 105:
		if g.previewLoading {
			return false, nil
		}
		if !strings.HasPrefix(g.preview.Text(), "// recovered") {
			return fail("draft was not restored after reopening")
		}
		g.editorSearch.SetText("second")
		g.findInEditor()
		a, b, ok := g.preview.TextRange()
		if !ok || g.preview.Text()[a:b] != "second" {
			return fail("native editor search selection")
		}
		p.capture("desktop-editor-vim")
		g.composer.Focus()
		payload, _ := json.Marshal(ui.ViewerCommandPayload{Action: "flow", Title: "Workspace walkthrough", Steps: []ui.ViewerStep{{Path: "second.go", StartLine: 1, Note: "First stop"}, {Path: "workspace.go", StartLine: 1, Note: "Second stop"}}})
		g.receive(ui.ChatEvent{Kind: "viewer", Detail: string(payload)})
	case 106:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if !g.composer.HasFocus() || !strings.Contains(g.preview.Text(), "package second") || !strings.Contains(g.viewer.note.Text(), "Step 1/2") {
			return fail("viewer flow content/focus")
		}
		g.viewer.next.Activate()
	case 107:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if !strings.Contains(g.preview.Text(), "External edit") || !strings.Contains(g.viewer.note.Text(), "Step 2/2") {
			return fail("next viewer flow step")
		}
		p.capture("desktop-walkthrough")
		if g.sourcePath() != "workspace.go" {
			return fail("viewer lost editable file identity")
		}
		g.viewer.edit.Activate()
		if g.previewPath != "workspace.go" {
			return fail("viewer Edit file did not open rooted editor")
		}
		g.setPrompt("original draft")
		g.addChat("Second tab")
		g.setPrompt("other draft")
		g.selectChat(0)
		if g.composer.Text() != "original draft" {
			return fail("chat draft lost on tab switch")
		}
		p.parallelRequests = p.requests.Load()
		g.setPrompt("Background turn")
		g.sendPrompt()
	case 112:
		if g.terminal.session == nil {
			return false, nil
		}
		if len(g.terminalTabs) != 2 || g.terminalTabs[0].session == nil {
			return fail("new shell replaced old PTY")
		}
		g.enqueueTerminal(func(t *ui.NativeTerminal) error { return t.Paste("echo second-shell") })
		g.terminalKey(cui.KeyEnter, 0)
	case 113:
		if !strings.Contains(g.terminal.output.Text(), "\nsecond-shell") {
			return false, nil
		}
		g.cycleTerminal(-1)
		if !strings.Contains(g.terminal.output.Text(), "survives") {
			return fail("previous shell state lost")
		}
		g.cycleTerminal(1)
		g.closeTerminal()
		if len(g.terminalTabs) != 1 || g.terminal.session == nil {
			return fail("closing shell closed other PTY")
		}
		g.showSourcePage()
		g.setPrompt("Inspect @sec")
		g.composer.SetTextRange(uint(len(g.composer.Text())), uint(len(g.composer.Text())))
		g.queueMentions()
	case 114:
		if !g.mentions.open {
			return false, nil
		}
		if !g.acceptMention(0) || !strings.Contains(g.composer.Text(), "@second.go") {
			return fail("mention completion did not insert workspace token")
		}
		// Verify bounded expansion through the same adapter without sending inference.
		g.work(func(ctx context.Context) func() {
			expanded, _ := g.ws.MentionContext(ctx, nil).Expand("Inspect @second.go")
			return func() { p.expanded = expanded }
		})
	case 115:
		if p.expanded == "" {
			return false, nil
		}
		if !strings.Contains(p.expanded, "<attached_context>") || !strings.Contains(p.expanded, "package second") {
			return fail("file mention did not attach context")
		}
		g.ws.Projects.FetchDetails = func(context.Context) (ui.ProjectDetails, error) {
			return ui.ProjectDetails{ReadmeTitle: "README.md", ReadmeContent: "# Fixture documentation", GitHubConnected: true, GitHubUser: "fixture", OpenPRs: []ui.ProjectGitHubPR{{Number: 7, Title: "Parity PR", URL: "https://example.com/pull/7"}}, OpenIssues: []ui.ProjectGitHubIssue{{Number: 8, Title: "Parity issue"}}, NotionConnected: true, NotionPageBody: "Fixture Notion content"}, nil
		}
		g.openProjectDetails(1)
	case 116:
		if g.projectDetails.loading {
			return false, nil
		}
		if !strings.Contains(g.projectDetails.body.Text(), "Fixture documentation") {
			return fail("README detail view")
		}
		g.projectDetails.tab.SetSelected(2)
		g.renderProjectDetails()
		if !strings.Contains(g.projectDetails.body.Text(), "Parity PR") || len(g.projectDetails.urls) != 1 {
			return fail("PR details and link")
		}
		p.capture("desktop-project-details")
		g.projectDetails.tab.SetSelected(4)
		g.renderProjectDetails()
		if !strings.Contains(g.projectDetails.body.Text(), "Fixture Notion content") {
			return fail("Notion detail view")
		}
		g.closeWorkbench()
		g.openCommand("layout focus")
		g.runCommandLine()
	case 117:
		if g.commands.layoutBusy {
			return false, nil
		}
		if g.inspectorVisible || g.sidebarVisible || g.commands.layout.Name != "focus" {
			return fail("focus layout did not apply")
		}
		g.openCommand("layout studio")
		g.runCommandLine()
	case 118:
		if g.commands.layoutBusy {
			return false, nil
		}
		if !g.inspectorVisible || !g.sidebarVisible {
			return fail("studio layout did not restore panes")
		}
		if err := g.executeCommand("resize viewer 55"); err != nil {
			return fail(err.Error())
		}
	case 119:
		if g.commands.layoutBusy {
			return false, nil
		}
		if g.commands.layout.Viewer != 55 {
			return fail("resize did not persist")
		}
		g.openCommand("project --help")
		g.runCommandLine()
	case 120:
		if g.preview.Text() == "Loading…" {
			return false, nil
		}
		if !strings.Contains(g.preview.Text(), "project [scan") {
			return fail("shared command help unavailable")
		}
		p.capture("desktop-command-help")
		g.setPrompt(strings.Repeat("line\n", 20))
		g.updateComposer()
		if g.composerMetrics.height < 150 || g.composerMetrics.height > 200 {
			return fail("composer did not grow within its configured row cap")
		}
		g.setPrompt("")
		g.updateComposer()
		if g.composerMetrics.height > 90 {
			return fail("composer did not shrink")
		}
		g.recordCommandOutput(ui.ChatEvent{Kind: "tool", ID: "command-fixture", Text: "exec", Detail: `args: {"argv":["printf","parity"]}`})
		g.selectTerminal(len(g.terminalTabs) - 1)
		g.recordCommandOutput(ui.ChatEvent{Kind: "tool", ID: "command-fixture", Text: "exec completed", Detail: `result: {"argv":["printf","parity"],"stdout":"parity output","exit_code":0}`})
		if !g.terminal.readonly || !strings.Contains(g.terminal.output.Text(), "parity output") {
			return fail("agent command did not update its read-only output tab")
		}
		for i := 0; i < 11; i++ {
			g.recordCommandOutput(ui.ChatEvent{Kind: "tool", ID: fmt.Sprintf("bounded-%d", i), Text: "exec", Detail: `args: {"argv":["echo","bounded"]}`})
		}
		if g.terminal != g.terminalTabs[g.terminalIndex] {
			return fail("command eviction lost selected tab")
		}
		g.closeWorkbench()
		g.showTerminal()
	case 121:
		p.capture("desktop-command-output")
		g.openCommand("layout ")
		if err := injectKeys("Tab", "Tab"); err != nil {
			return fail(err.Error())
		}
	case 122:
		if g.commands.input.Text() != "layout conversation" {
			return fail("native Tab did not cycle layout choices: " + g.commands.input.Text())
		}
		if err := injectKeys("Shift+Tab"); err != nil {
			return fail(err.Error())
		}
	case 123:
		if g.commands.input.Text() != "layout studio" {
			return fail("native Shift+Tab did not reverse completion")
		}
		p.step = 130
		return false, nil
	}
	p.step++
	return false, nil
}

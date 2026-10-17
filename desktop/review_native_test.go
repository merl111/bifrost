package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bifrost/internal/config"
	"bifrost/internal/ui"
)

// Error paths use disposable state and real native controls. No provider calls.
func (p *nativeParity) reviewParity(g *desktop) (bool, error) {
	fail := func(s string) (bool, error) { return false, fmt.Errorf("review phase %d: %s", p.step, s) }
	s := &g.workbench.settings
	switch p.step {
	case 160:
		g.app.TextScale(1)
		g.textScale = 1
		g.openWorkbench("Settings")
		g.loadSettings()
	case 161:
		if s.busy {
			return false, nil
		}
		for _, f := range s.document.Fields {
			if f.Key == "ui.theme" {
				g.selectSetting(f)
			}
		}
		s.value.SetText("")
		if !s.value.InsertText("light") {
			return fail("settings not editable")
		}
		// Switch controls while retaining the pending scalar edit.
		for _, f := range s.document.Fields {
			if f.Key == "defaults.allow_exec" {
				g.selectSetting(f)
			}
		}
		original := s.toggle.Checked()
		s.toggle.Activate()
		if s.toggle.Checked() == original || s.value.Text() != fmt.Sprint(!original) {
			return fail("boolean setting did not update")
		}
		s.toggle.Activate() // Restore the permission; only theme should change.
		g.saveSettings()    // Deliberately do not stage first.
	case 162:
		if s.busy {
			return false, nil
		}
		cfg, err := config.LoadSettings(filepath.Join(p.state, "config.json"))
		if err != nil || cfg.UI.Theme != "light" || s.dirty || len(s.pending) != 0 {
			return fail(fmt.Sprintf("direct save lost pending edit: %v", err))
		}
		for _, f := range s.document.Fields {
			if f.Key == "ui.theme" {
				g.selectSetting(f)
			}
		}
		p.capture("desktop-settings-reviewed")
		g.closeWorkbench()
		g.setPrompt("Inspect @sec")
		g.composer.SetTextRange(12, 12)
		g.queueMentions()
		request := g.mentions.request
		g.addChat("Review")
		if g.mentions.request == request || g.mentions.open || !g.mentions.due.IsZero() {
			return fail("tab switch retained old suggestions")
		}
		g.setPrompt("Inspect @sec")
		g.composer.SetTextRange(12, 12)
		g.queueMentions()
	case 163:
		if !g.mentions.open {
			return false, nil
		}
		g.composer.SetTextRange(0, 0)
		if g.acceptMention(0) || g.composer.Text() != "Inspect @sec" {
			return fail("stale caret accepted suggestion")
		}
		g.commandRun++
		g.recordCommandOutput(ui.ChatEvent{Kind: "tool", ID: "reused", Text: "exec first", Detail: `result: {"argv":["echo","first"],"stdout":"first","exit_code":0}`})
		first := g.terminalTabs[len(g.terminalTabs)-1]
		g.commandRun++
		g.recordCommandOutput(ui.ChatEvent{Kind: "tool", ID: "reused", Text: "exec second", Detail: `result: {"argv":["echo","second"],"stdout":"second","exit_code":0}`})
		if first == g.terminalTabs[len(g.terminalTabs)-1] || !strings.Contains(first.frame.Text, "first") {
			return fail("later turn overwrote command output")
		}
		d := &editorDraft{document: ui.EditorDocument{Path: "review-draft.go", Revision: "review", Content: strings.Repeat("x", (2<<20)+1)}}
		p.reviewDraft = d
		g.drafts[d.document.Path] = d
		g.flushEditorDrafts()
	case 164:
		d := p.reviewDraft
		if d.writing {
			return false, nil
		}
		if d.durable != "" || d.retryAfter.IsZero() {
			return fail("failed recovery write marked durable")
		}
		d.document.Content = "retained draft"
		d.retryAfter = time.Time{}
		g.flushEditorDrafts()
	case 165:
		d := p.reviewDraft
		if d.writing {
			return false, nil
		}
		restored, err := ui.ReadEditorDraft(filepath.Join(g.ws.StateDir, "drafts"), d.document)
		if err != nil || restored.Content != d.document.Content || d.durable == "" {
			return fail(fmt.Sprintf("draft did not recover after failure: %v", err))
		}
		dir := filepath.Join(g.ws.StateDir, "drafts")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fail(err.Error())
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			data, _ := os.ReadFile(path)
			var record struct{ Path string }
			if json.Unmarshal(data, &record) == nil && record.Path == d.document.Path {
				p.blockedDraft = path
				break
			}
		}
		if p.blockedDraft == "" {
			return fail("recovery file not found")
		}
		if err := os.Remove(p.blockedDraft); err != nil {
			return fail(err.Error())
		}
		if err := os.Mkdir(p.blockedDraft, 0700); err != nil {
			return fail(err.Error())
		}
		if err := os.WriteFile(filepath.Join(p.blockedDraft, "blocker"), []byte("retain"), 0600); err != nil {
			return fail(err.Error())
		}
		d.saved = d.document.Content // No confirmation needed for a clean document.
		g.openEditor(d.document.Path)
		g.reloadEditor()
		g.openEditor("second.go")
	case 166:
		d := p.reviewDraft
		if d.discarding || g.previewLoading {
			return false, nil
		}
		if g.drafts[d.document.Path] != d || g.previewPath != "second.go" {
			return fail("failed reload discarded draft or stole selected file")
		}
		if err := os.Remove(filepath.Join(p.blockedDraft, "blocker")); err != nil {
			return fail(err.Error())
		}
		if err := os.Remove(p.blockedDraft); err != nil {
			return fail(err.Error())
		}
		g.openEditor(d.document.Path)
		g.reloadEditor()
		g.openEditor("second.go")
	case 167:
		d := p.reviewDraft
		if d.discarding || g.previewLoading {
			return false, nil
		}
		if g.drafts[d.document.Path] != nil || g.previewPath != "second.go" {
			return fail("successful reload stole selected file")
		}
		g.switchAccount("alpha")
	case 168:
		if g.busy {
			return false, nil
		}
		g.selectChat(0)
		snapshot, err := g.ws.Snapshot(g.ctx)
		if err != nil || snapshot.Connection.Account != "alpha" || g.activeChat().snapshot.Connection.Account != "alpha" {
			return fail("chat switch advertised a profile different from the runtime")
		}
		for _, account := range g.accounts {
			if account.Active && account.ID != "alpha" {
				return fail("profile selector restored an obsolete account")
			}
		}
		return true, nil
	}
	p.step++
	return false, nil
}

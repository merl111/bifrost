package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"bifrost/internal/config"
	"bifrost/internal/domain"
	"bifrost/internal/tasks"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

// Runs in the same native event loop as TestNativeWorkspace. Every wait yields
// to GTK; assertions inspect actual controls and temporary on-disk results.
type nativeParity struct {
	requests             *atomic.Int32
	parallelRequests     int32
	tabCount             int
	expanded             string
	reviewDraft          *editorDraft
	blockedDraft         string
	step                 int
	root, state          string
	capture              func(string)
	installed, cancelled atomic.Bool
	interruptDeadline    time.Time
}

func injectKeys(keys ...string) error {
	args := append([]string{"testdata/keys.py"}, keys...)
	out, err := exec.Command("python3", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("native keys: %w: %s", err, out)
	}
	return nil
}

func (p *nativeParity) tick(g *desktop) (bool, error) {
	if p.step >= 100 {
		return p.fullParity(g)
	}
	fail := func(format string, args ...any) (bool, error) {
		return false, fmt.Errorf("parity phase %d: %s", p.step, fmt.Sprintf(format, args...))
	}
	s, m := &g.workbench.settings, &g.workbench.market
	switch p.step {
	case 0:
		g.window.SetSize(1440, 920)
		g.openEditor("workspace.go")
	case 1:
		if g.previewLoading {
			return false, nil
		}
		g.preview.SetTextRange(0, 0)
		if !g.preview.InsertText("// Native edit\n") {
			return fail("source is not editable")
		}
		g.rememberEditor()
		if !g.hasDrafts() {
			return fail("edit not tracked")
		}
		g.saveSource.Activate()
	case 2:
		if g.drafts["workspace.go"].saving {
			return false, nil
		}
		body, err := os.ReadFile(filepath.Join(p.root, "workspace.go"))
		if err != nil || !strings.HasPrefix(string(body), "// Native edit") {
			return fail("save: %s %v", body, err)
		}
		g.preview.InsertText("// Keep this draft\n")
		g.openEditor("second.go")
	case 3:
		if g.previewLoading {
			return false, nil
		}
		g.openEditor("workspace.go")
		if !strings.Contains(g.preview.Text(), "Keep this draft") {
			return fail("switch lost draft")
		}
		if err := os.WriteFile(filepath.Join(p.root, "workspace.go"), []byte("// External edit\n"), 0600); err != nil {
			return fail("%v", err)
		}
		g.saveSource.Activate()
	case 4:
		if g.drafts["workspace.go"].saving {
			return false, nil
		}
		body, _ := os.ReadFile(filepath.Join(p.root, "workspace.go"))
		if string(body) != "// External edit\n" || !strings.Contains(g.preview.Text(), "Keep this draft") || !g.hasDrafts() {
			return fail("conflict overwrote file or lost draft")
		}
		p.capture("desktop-editor")
		g.showTerminal()
	case 5:
		if g.terminal.session == nil {
			return false, nil
		}
		g.terminal.input.Focus()
		// Correct "native" with a real Left/Delete sequence before sending Return.
		if err := injectKeys("e", "c", "h", "o", "space", "n", "a", "t", "x", "v", "e", "Left", "Left", "Left", "Delete", "i", "End", "Return"); err != nil {
			return fail("%v", err)
		}
	case 6:
		if !strings.Contains(g.terminal.output.Text(), "\nnative") {
			return false, nil
		}
		p.capture("desktop-terminal")
		if err := injectKeys("s", "l", "e", "e", "p", "space", "1", "0", "Return"); err != nil {
			return fail("%v", err)
		}
		p.step = 40
		return false, nil
	case 40:
		p.interruptDeadline = time.Now().Add(2 * time.Second)
		if err := injectKeys("Control+c"); err != nil {
			return fail("%v", err)
		}
		p.step = 41
		return false, nil
	case 41:
		g.showSourcePage()
		// Hiding does not kill the shell; input is still delivered to its PTY.
		g.enqueueTerminal(func(t *ui.NativeTerminal) error { return t.Paste("echo survives") })
		g.terminalKey(cui.KeyEnter, 0)
		p.step = 7
		return false, nil
	case 7:
		if !strings.Contains(g.terminal.output.Text(), "\nsurvives") {
			if time.Now().After(p.interruptDeadline) {
				return fail("Ctrl+C did not interrupt the running shell command")
			}
			return false, nil
		}
		g.openWorkbench("Settings")
	case 8:
		if !s.loaded || s.busy {
			return false, nil
		}
		for _, f := range s.document.Fields {
			if f.Key == "ui.theme" {
				g.selectSetting(f)
			}
		}
		s.value.SetText("")
		if !s.value.InsertText("light") {
			return fail("settings value is not editable")
		}
		g.stageSetting()
		if !s.dirty {
			return fail("valid setting not staged")
		}
		s.save.Activate()
	case 9:
		if s.busy {
			return false, nil
		}
		cfg, err := config.LoadSettings(filepath.Join(p.state, "config.json"))
		if err != nil || cfg.UI.Theme != "light" || s.dirty || currentTheme != "light" {
			return fail("settings not saved/applied: %v", err)
		}
		p.capture("desktop-settings-light")
		// An external configuration revision must reject a later stale save.
		cfg.UI.Theme = "plain"
		if err := config.SaveSettings(filepath.Join(p.state, "config.json"), cfg, true); err != nil {
			return fail("%v", err)
		}
		s.value.SetText("midnight")
		g.stageSetting()
		s.save.Activate()
	case 10:
		if s.busy {
			return false, nil
		}
		cfg, _ := config.LoadSettings(filepath.Join(p.state, "config.json"))
		if !s.dirty || cfg.UI.Theme != "plain" {
			return fail("settings conflict lost draft or overwrote config")
		}
		g.loadSettings()
	case 11:
		if s.busy {
			return false, nil
		}
		s.key = "ui.theme"
		s.value.SetText("midnight")
		g.stageSetting()
		g.saveSettings()
	case 12:
		if s.busy {
			return false, nil
		}
		g.openWorkbench("Accounts")
		g.accountOperation(func(ctx context.Context) error { return g.ws.SwitchAccount(ctx, "beta") })
	case 13:
		if g.busy {
			return false, nil
		}
		snapshot, err := g.ws.Snapshot(g.ctx)
		if err != nil || snapshot.Connection.Account != "beta" {
			return fail("profile switch: %+v %v", snapshot.Connection, err)
		}
		p.capture("desktop-accounts")
		// Catalog UI uses an explicit fixture; no public installs or network calls.
		g.ws.Marketplace = ui.MarketplaceCallbacks{
			List: func(context.Context, string) ([]ui.MarketplaceEntry, error) {
				return []ui.MarketplaceEntry{{ID: "fixture", Name: "Workspace reviewer", Description: "Inspect changes with a reusable review workflow.", Source: "Fixture catalog · pinned source"}}, nil
			},
			Preview: func(_ context.Context, id string) (string, error) {
				return "Workspace reviewer\n\nSource: fixture/review@pinned\nPreview verified. Installation does not activate instructions.", nil
			},
			Install: func(_ context.Context, id, scope string) (string, error) {
				if id != "fixture" || scope != "global" {
					return "", fmt.Errorf("wrong installation %s/%s", id, scope)
				}
				p.installed.Store(true)
				return "Installed fixture in global scope", nil
			},
		}
		g.openWorkbench("Marketplace")
	case 14:
		if !m.loaded {
			return false, nil
		}
		g.installMarket()
		if p.installed.Load() {
			return fail("install before preview")
		}
		g.previewMarket("fixture")
	case 15:
		if m.selected == "" {
			return false, nil
		}
		p.capture("desktop-marketplace")
		m.scope.SetSelected(1)
		m.install.Activate()
	case 16:
		if m.installing {
			return false, nil
		}
		if !p.installed.Load() {
			return fail("selected ID/scope did not reach install callback")
		}
		g.ws.Tasks = ui.TaskMonitorCallbacks{
			Refresh: func(context.Context) ([]tasks.Summary, error) {
				status := tasks.StatusActive
				if p.cancelled.Load() {
					status = tasks.StatusCanceled
				}
				return []tasks.Summary{{Task: tasks.Task{ID: domain.ID("task-fixture"), Status: status, Prompt: "Review the desktop changes", Model: "Fixture model"}}}, nil
			},
			Events: func(context.Context, uint64, int) ([]tasks.Event, uint64, error) {
				return []tasks.Event{{TaskID: "task-fixture", Type: "progress", Message: "Inspecting shared services"}}, 1, nil
			},
			Cancel: func(_ context.Context, id domain.ID) error {
				if id != "task-fixture" {
					return fmt.Errorf("wrong task %s", id)
				}
				p.cancelled.Store(true)
				return nil
			},
		}
		g.openWorkbench("Tasks")
	case 17:
		if g.workbench.taskLoading {
			return false, nil
		}

		g.workbench.taskList.Focus()
		if err := injectKeys("Home"); err != nil {
			return fail("%v", err)
		}
		p.step = 70
		return false, nil
	case 70:
		if !strings.Contains(g.workbench.taskDetail.Text(), "Inspecting shared services") {
			return fail("native task selection did not show details")
		}
		p.capture("desktop-tasks")
		g.workbench.taskCancel.Activate()
		p.step = 18
		return false, nil

	case 18:
		if !p.cancelled.Load() || g.workbench.taskLoading {
			return false, nil
		}
		g.openWorkbench("Project")
	case 19:
		if g.workbench.projects.busy {
			return false, nil
		}
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			st, _, err := g.ws.Projects.AddAll(ctx)
			return st, err
		})
	case 20:
		pr := &g.workbench.projects
		if pr.busy {
			return false, nil
		}
		linked := 0
		for _, r := range pr.status.Repos {
			if r.Linked {
				linked++
			}
		}
		if linked != 2 {
			return fail("expected two linked repositories, got %d", linked)
		}
		next, err := parseProjectDefaults(pr.settings.Text(), pr.status)
		if err != nil {
			return fail("project JSON: %v", err)
		}
		next.AllowWrite = true
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) { return g.ws.Projects.SaveSettings(ctx, next) })
	case 21:
		pr := &g.workbench.projects
		if pr.busy {
			return false, nil
		}
		if !pr.status.AllowWrite {
			return fail("project defaults not saved")
		}
		p.capture("desktop-project")
		pr.settings.SetTextRange(0, 0)
		if !pr.settings.InsertText(" ") {
			return fail("project defaults not editable")
		}
		before := pr.settings.Text()
		g.openWorkbench("Tasks")
		g.openWorkbench("Project")
		if pr.settings.Text() != before {
			return fail("navigation replaced project draft")
		}
		g.openPalette()
		g.palette.PickerSetQuery("Open terminal")
		if !g.palette.PickerAccept() || g.workbench.open != "" || !g.terminal.visible {
			return fail("palette failed over workbench")
		}
		g.openWorkbench("Settings")
		g.window.SetSize(800, 720)
	case 22:
		p.capture("desktop-settings-narrow")
		g.window.SetSize(640, 600)
		g.openWorkbench("Accounts")
	case 23:
		p.capture("desktop-accounts-narrow")
		if w, h, _ := g.window.Size(); w > 640 || h > 600 {
			return fail("workbench minimum size %dx%d", w, h)
		}
		g.openWorkbench("Settings")
		g.openPalette()
		g.closePalette()
		if !g.workbench.settings.query.HasFocus() {
			return fail("palette did not restore settings focus")
		}
		g.closeWorkbench()
		g.feedback.FeedbackDismiss()
		g.setPrompt("line")
		g.composer.SetTextRange(4, 4)
		if err := injectKeys("Shift+Return", "t", "w", "o"); err != nil {
			return fail("%v", err)
		}
	case 24:
		if g.composer.Text() != "line\ntwo" || g.busy {
			return fail("Shift+Enter sent instead of newline: %q", g.composer.Text())
		}
		if err := injectKeys("Return"); err != nil {
			return fail("%v", err)
		}
	case 25:
		if !g.busy || g.composer.Text() != "" {
			return fail("Enter did not send the multiline composer")
		}
		g.cancelTurn()
	case 26:
		if g.busy {
			return false, nil
		}
		p.step = 100
		return false, nil
	}
	p.step++
	return false, nil
}

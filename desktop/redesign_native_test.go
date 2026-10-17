package main

import (
	"bifrost/internal/tasks"
	"bifrost/internal/ui"
	"context"
	"fmt"
	"strings"
)

func (p *nativeParity) redesignParity(g *desktop) (bool, error) {
	if p.step >= 150 {
		return p.chatPresentationParity(g)
	}
	fail := func(message string) (bool, error) {
		return false, fmt.Errorf("workspace redesign %d: %s", p.step, message)
	}
	w := g.workbench
	pr, m := &w.projects, &w.market
	switch p.step {
	case 130:
		g.commands.root.SetVisible(false)
		g.window.SetSize(2560, 1440)
		g.closeWorkbench()
	case 131:
		if width, _, ok := g.sidebar.AllocatedSize(); !ok || width > 360 {
			return fail(fmt.Sprintf("sidebar grew to %d", width))
		}
		p.capture("desktop-wide-conversation")
		g.openWorkbench("Project")
	case 132:
		if pr.busy {
			return false, nil
		}
		if w.open != "Project" || !strings.Contains(pr.reposMetric.Text(), "2 linked") {
			return fail("project navigation or repository overview missing")
		}
		if width, _, ok := w.body.AllocatedSize(); !ok || width > 1360 {
			return fail(fmt.Sprintf("reading width %d", width))
		}
		p.capture("desktop-project-wide")
		pr.alias.SetText("unfinished-repository")
		g.refreshProject()
	case 133:
		if pr.busy {
			return false, nil
		}
		if pr.alias.Text() != "unfinished-repository" {
			return fail("refresh replaced repository draft")
		}
		if err := g.commandProject([]string{"settings"}); err != nil {
			return fail(err.Error())
		}
		pr.form.skills.SetText("review, docs")
		g.stageProjectDefaultsForm()
		next, err := parseProjectDefaults(pr.settings.Text(), pr.status)
		if err != nil || len(next.Skills) != 2 || !next.AllowWrite {
			return fail("common form lost existing defaults")
		}
		g.selectProjectDefaultsMode(true)
		pr.settings.SetText("{broken")
		g.selectProjectDefaultsMode(false)
		if pr.form.mode.Selected() != 1 || pr.settings.Text() != "{broken" {
			return fail("invalid advanced draft discarded")
		}
		pr.settings.SetText(projectDefaultsJSON(next))
		g.selectProjectDefaultsMode(false)
		g.window.SetSize(1440, 920)
	case 134:
		if pr.busy {
			return false, nil
		}
		w.notice.FeedbackDismiss()
		p.capture("desktop-project-configuration")
		g.window.SetSize(800, 720)
		g.showProjectConfiguration(false)
	case 135:
		if width, height, _ := g.window.Size(); width > 800 || height > 720 {
			return fail("project cannot fit laptop window")
		}
		p.capture("desktop-project-narrow")
		g.ws.Marketplace.ListMCP = func(context.Context, string) ([]ui.MarketplaceEntry, error) {
			return []ui.MarketplaceEntry{{ID: "mcp:fixture", Name: "Repository tools", Description: "Read repository files and inspect changes through an MCP server.", Source: "Fixture registry · pinned package", Kind: "mcp"}}, nil
		}
		g.ws.Marketplace.Preview = func(_ context.Context, id string) (string, error) {
			return "Repository tools\n\nTransport: stdio\nPackage: fixture/repository-tools\nVersion: pinned-test\n\nThis fixture installs configuration only. No process is launched.", nil
		}
		w.navigation["MCP servers"].Activate()
	case 136:
		if !m.loaded {
			return false, nil
		}
		if len(m.entries) != 1 || m.entries[0].ID != "mcp:fixture" || w.open != "MCP servers" {
			return fail("MCP navigation showed skills")
		}
		g.previewMarket("mcp:fixture")
	case 137:
		if m.selected == "" {
			return false, nil
		}
		p.capture("desktop-mcp-narrow")
		g.window.SetSize(1440, 920)
	case 138:
		p.capture("desktop-mcp")
		// Keep an install pending long enough to exercise navigation interleaving.
		gate := make(chan struct{})
		g.ws.Marketplace.Install = func(ctx context.Context, id, scope string) (string, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			if id != "mcp:fixture" || scope != "project" {
				return "", fmt.Errorf("wrong MCP install %s/%s", id, scope)
			}
			p.installed.Store(true)
			return "Installed test server configuration", nil
		}
		p.installed.Store(false)
		m.scope.SetSelected(0)
		g.installMarket()
		w.navigation["Skills"].Activate()
		blocked := w.open == "MCP servers" && m.kind.Selected() == 1
		close(gate)
		if !blocked {
			return fail("category changed during installation")
		}
	case 139:
		if m.installing {
			return false, nil
		}
		if !p.installed.Load() {
			return fail("MCP installation did not use selected ID and scope")
		}
		w.navigation["Skills"].Activate()
	case 140:
		if !m.loaded {
			return false, nil
		}
		if len(m.entries) != 1 || m.entries[0].ID != "fixture" {
			return fail("skills catalog retained MCP results")
		}
		g.ws.Tasks.Refresh = func(context.Context) ([]tasks.Summary, error) {
			return []tasks.Summary{
				{Task: tasks.Task{ID: "active", Status: tasks.StatusActive, Prompt: "Review repository changes", Model: "Test model"}},
				{Task: tasks.Task{ID: "failed", Status: tasks.StatusFailed, Prompt: "Check the provider configuration", Error: "Test provider rejected the selected model."}},
				{Task: tasks.Task{ID: "done", Status: tasks.StatusCompleted, Prompt: "Document project setup"}},
			}, nil
		}
		w.navigation["Tasks"].Activate()
	case 141:
		if w.taskLoading {
			return false, nil
		}
		w.taskUI.filter.SetSelected(3)
		g.filterTasks()
		g.showTaskDetails()
		if len(w.taskUI.visible) != 1 || w.selectedTask != "failed" {
			return fail("task status filter selected wrong task")
		}
		if strings.Contains(w.taskDetail.Text(), "Task ID") {
			return fail("raw execution metadata is still default")
		}
		w.taskUI.tabs.SetSelected(1)
		g.showTaskDetails()
		if !strings.Contains(w.taskDetail.Text(), "Task ID") {
			return fail("execution metadata inaccessible")
		}
		w.taskUI.tabs.SetSelected(0)
		g.showTaskDetails()
	case 142:
		p.capture("desktop-task-failure")
		w.taskUI.filter.SetSelected(0)
		w.taskUI.query.SetText("document")
		g.filterTasks()
		g.showTaskDetails()
		if len(w.taskUI.visible) != 1 || w.selectedTask != "done" {
			return fail("task search selected wrong task")
		}
		w.taskUI.query.SetText("")
		g.filterTasks()
		g.showTaskDetails()
		g.window.SetSize(2560, 1440)
	case 143:
		p.capture("desktop-tasks-wide")
		g.window.SetSize(1440, 920)
		g.closeWorkbench()
		g.openEditor("second.go")
	case 144:
		if g.previewLoading {
			return false, nil
		}
		g.preview.Focus()
		if err := injectKeys("Control+f"); err != nil {
			return fail(err.Error())
		}
	case 145:
		if !g.editorSearch.HasFocus() {
			return fail("Ctrl+F did not open editor search")
		}
		if err := injectKeys("Escape"); err != nil {
			return fail(err.Error())
		}
	case 146:
		if !g.preview.HasFocus() {
			return fail("closing search did not restore editor focus")
		}
		p.step = 150
		return false, nil
	}
	p.step++
	return false, nil
}

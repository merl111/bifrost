package main

import (
	"bifrost/internal/ui"
	cui "cui.local/cui"
	"strings"
)

type projectDefaultsForm struct {
	body, mode, provider, environment, skills, mcp, write, exec, browser cui.Widget
}

func (g *desktop) buildProjectDefaultsForm(parent cui.Widget) {
	p := &g.workbench.projects
	f := &p.form
	f.mode = parent.Select([]string{"Common settings", "Advanced JSON · all settings"})
	f.mode.SetSelected(0)
	f.body = parent.Box(cui.Vertical, 8)
	row := f.body.Wrap(12)
	runtime := row.Box(cui.Vertical, 6)
	runtime.MinSize(230, 0)
	f.provider = labeledEntry(runtime, "Provider profile")
	f.provider.Placeholder("Use the default profile")
	f.environment = labeledEntry(runtime, "Environment")
	f.environment.Placeholder("local")
	tools := row.Box(cui.Vertical, 6)
	tools.MinSize(230, 0)
	f.skills = labeledEntry(tools, "Active skills · comma separated")
	f.mcp = labeledEntry(tools, "MCP configuration path")
	permissions := f.body.Wrap(12)
	f.write = permissions.Checkbox("Allow file edits", false)
	f.exec = permissions.Checkbox("Allow commands", false)
	f.browser = permissions.Checkbox("Enable browser tools", false)
	for _, field := range []cui.Widget{f.provider, f.environment, f.skills, f.mcp, f.write, f.exec, f.browser} {
		field.OnAction(func(cui.Widget) { g.stageProjectDefaultsForm() })
	}
	f.mode.OnAction(func(w cui.Widget) { g.selectProjectDefaultsMode(w.Selected() == 1) })
}

func (g *desktop) selectProjectDefaultsMode(advanced bool) {
	p := &g.workbench.projects
	f := &p.form
	if !advanced {
		next, err := parseProjectDefaults(p.settings.Text(), p.status)
		if err != nil {
			f.mode.SetSelected(1)
			g.workbenchNotice("Fix the JSON before switching to common settings", err)
			return
		}
		g.loadProjectDefaultsForm(next)
		f.mode.SetSelected(0)
	} else {
		f.mode.SetSelected(1)
	}
	g.workbench.notice.FeedbackDismiss()
	f.body.SetVisible(!advanced)
	p.settings.SetVisible(advanced)
}

func (g *desktop) loadProjectDefaultsForm(s ui.ProjectStatus) {
	f := &g.workbench.projects.form
	f.provider.SetText(s.Provider)
	f.environment.SetText(s.Environment)
	f.skills.SetText(strings.Join(s.Skills, ", "))
	f.mcp.SetText(s.MCPConfig)
	f.write.SetChecked(s.AllowWrite)
	f.exec.SetChecked(s.AllowExec)
	f.browser.SetChecked(s.Browser)
}

func (g *desktop) stageProjectDefaultsForm() {
	p := &g.workbench.projects
	next, err := parseProjectDefaults(p.settings.Text(), p.status)
	if err != nil {
		g.workbenchNotice("Cannot stage common settings", err)
		return
	}
	f := &p.form
	next.Provider = strings.TrimSpace(f.provider.Text())
	next.Environment = strings.TrimSpace(f.environment.Text())
	next.MCPConfig = strings.TrimSpace(f.mcp.Text())
	next.Skills = nil
	for _, skill := range strings.Split(f.skills.Text(), ",") {
		if skill = strings.TrimSpace(skill); skill != "" {
			next.Skills = append(next.Skills, skill)
		}
	}
	next.AllowWrite, next.AllowExec, next.Browser = f.write.Checked(), f.exec.Checked(), f.browser.Checked()
	p.settings.SetText(projectDefaultsJSON(next))
}

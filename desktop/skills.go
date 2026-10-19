package main

import (
	"bifrost/internal/cli"
	"context"
	cui "cui.local/cui"
	"fmt"
	"slices"
	"strings"
)

type skillsView struct {
	filter, list, detail, scope, toggle, inherit, state cui.Widget
	inventory                                           cli.SkillInventory
	visible                                             []cli.InstalledSkill
	busy                                                bool
}

func (g *desktop) buildSkillsPanel(parent cui.Widget) {
	s := &g.workbench.skills
	bar := parent.Wrap(8)
	s.filter = bar.Select([]string{"All installed", "Project", "Global", "Built-in"})
	s.filter.SetSelected(0)
	s.filter.OnAction(func(cui.Widget) { g.renderInstalledSkills() })
	button(bar, "Refresh", g.loadInstalledSkills)
	button(bar, "Install from folder…", func() { g.chooseExtensionPath(true) })
	button(bar, "Discover skills…", func() { g.openWorkbench("Skills") })
	s.state = label(parent, "", 11, muted)
	label(parent, "The AI searches available descriptions and loads relevant skills for each task. Auto-load always includes selected instructions. Scripts still follow command permissions.", 11, muted)
	split := parent.Split(cui.Horizontal, .36)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	s.list = split.Pane(0).List(nil)
	s.list.Expand(true)
	s.list.OnAction(func(cui.Widget) { g.showInstalledSkill() })
	right := split.Pane(1)
	right.Padding(12)
	s.detail = right.Textarea("")
	style(s.detail, surface, foreground, 8, true, 12)
	s.detail.Font("", 11, 400)
	s.detail.ReadOnly(true)
	s.detail.Expand(true)
	s.scope = right.Select([]string{"Project auto-load", "Global auto-load"})
	s.scope.SetSelected(0)
	s.scope.OnAction(func(cui.Widget) { g.showInstalledSkill() })
	s.toggle = button(right, "", g.toggleSkillAutoload)
	s.inherit = button(right, "Use global defaults for this project", func() { g.saveSkillAutoload("project", nil) })
}
func (g *desktop) loadInstalledSkills() {
	s := &g.workbench.skills
	if s.busy {
		return
	}
	s.busy = true
	s.state.SetText("Loading installed skills…")
	g.work(func(ctx context.Context) func() {
		inventory, err := g.ws.InstalledSkills(ctx)
		return func() {
			s.busy = false
			if err != nil {
				g.workbenchNotice("Could not load skills", err)
				return
			}
			s.inventory = inventory
			if inventory.Project == "" {
				s.scope.SetSelected(1)
			}
			s.state.SetText(fmt.Sprintf("%d installed entries · project copies take precedence · auto-load changes apply next turn", len(inventory.Items)))
			s.inherit.SetEnabled(inventory.Project != "" && inventory.ProjectOverride)
			g.renderInstalledSkills()
		}
	})
}
func (g *desktop) renderInstalledSkills() {
	s := &g.workbench.skills
	s.visible = nil
	var rows []string
	scopes := []string{"", "project", "global", "built-in"}
	scope := scopes[max(0, s.filter.Selected())]
	for _, item := range s.inventory.Items {
		if scope != "" && item.Scope != scope {
			continue
		}
		state := item.Scope
		if item.Shadowed {
			state += " · overridden"
		} else if item.AutoLoad {
			state += " · auto-load"
		} else {
			state += " · on demand"
		}
		rows = append(rows, item.Skill.Name+"\n"+state)
		s.visible = append(s.visible, item)
	}
	s.list.Items(rows)
	s.list.SetSelected(0)
	g.showInstalledSkill()
}
func (g *desktop) showInstalledSkill() {
	s := &g.workbench.skills
	i := s.list.Selected()
	if i < 0 || i >= len(s.visible) {
		s.detail.SetText("No skills in this scope. Browse Discover skills to install one.")
		s.toggle.SetEnabled(false)
		return
	}
	item := s.visible[i]
	scope := "project"
	names := s.inventory.ProjectDefaults
	if s.scope.Selected() == 1 {
		scope = "global"
		names = s.inventory.GlobalDefaults
	} else if !s.inventory.ProjectOverride {
		names = s.inventory.GlobalDefaults
	}
	state := "Available for AI selection on demand"
	if item.AutoLoad {
		state = "Automatically loaded into new turns"
	}
	if item.Shadowed {
		state = "Overridden by a higher-priority copy"
	}
	s.detail.SetText(item.Skill.Name + "\n" + state + "\n\n" + item.Skill.Description + "\n\nSource: " + item.Skill.Source + "\nRevision: " + item.Skill.Digest + "\n\n" + item.Skill.Body)
	text := "Auto-load in " + scope
	if slices.Contains(names, item.Skill.Name) {
		text = "Remove from " + scope + " auto-load"
	}
	s.toggle.SetText(text)
	editable := scope == "global" && item.Scope != "project" || scope == "project" && s.inventory.Project != "" && !item.Shadowed
	s.toggle.SetEnabled(!s.busy && editable)
}
func (g *desktop) toggleSkillAutoload() {
	s := &g.workbench.skills
	i := s.list.Selected()
	if s.busy || i < 0 || i >= len(s.visible) {
		return
	}
	scope := "project"
	names := s.inventory.ProjectDefaults
	if s.scope.Selected() == 1 {
		scope = "global"
		names = s.inventory.GlobalDefaults
	} else if !s.inventory.ProjectOverride {
		names = s.inventory.GlobalDefaults
	}
	next := append([]string{}, names...)
	name := s.visible[i].Skill.Name
	if slices.Contains(next, name) {
		next = slices.DeleteFunc(next, func(n string) bool { return n == name })
	} else {
		next = append(next, name)
	}
	g.saveSkillAutoload(scope, next)
}
func (g *desktop) saveSkillAutoload(scope string, names []string) {
	s := &g.workbench.skills
	if s.busy {
		return
	}
	s.busy = true
	s.toggle.SetEnabled(false)
	g.work(func(ctx context.Context) func() {
		err := g.ws.SetSkillAutoload(ctx, scope, names)
		return func() {
			s.busy = false
			g.workbenchNotice(strings.Title(scope)+" skill defaults saved", err)
			g.loadInstalledSkills()
		}
	})
}

package main

import (
	cui "cui.local/cui"
	"fmt"
	"path/filepath"
)

func (g *desktop) buildProjectPanel(parent cui.Widget) {
	p := &g.workbench.projects
	tabs := parent.Box(cui.Horizontal, 6)
	p.overviewTab = navigationButton(tabs, "Overview", cui.SymbolHome, func() { g.showProjectConfiguration(false) })
	p.configureTab = navigationButton(tabs, "Configuration", cui.SymbolSettings, func() { g.showProjectConfiguration(true) })
	tabs.Box(cui.Horizontal, 0).Expand(true)
	iconButton(tabs, cui.SymbolRepeat, "Refresh repositories", g.refreshProject)
	p.overview = parent.Box(cui.Vertical, 16)
	p.overview.Expand(true)
	hero := sectionCard(p.overview, "")
	p.title = label(hero, filepath.Base(g.ws.Root), 22, foreground)
	p.title.Font("", 22, 600)
	p.summary = label(hero, "Link repositories and give agents shared project context.", 12, muted)
	actions := hero.Box(cui.Horizontal, 8)
	button(actions, "Project sources", func() { g.openProjectDetails(0) })
	button(actions, "README", func() { g.openProjectDetails(1) })
	button(actions, "Pull requests", func() { g.openProjectDetails(2) })
	actions.Box(cui.Horizontal, 0).Expand(true)
	stats := p.overview.Box(cui.Horizontal, 12)
	for _, spec := range []struct {
		title string
		value *cui.Widget
	}{
		{"REPOSITORIES", &p.reposMetric}, {"RUNTIME", &p.runtimeMetric}, {"SKILLS & TOOLS", &p.skillsMetric},
	} {
		card := sectionCard(stats, spec.title)
		card.Expand(true)
		*spec.value = label(card, "—", 13, foreground)
	}
	split := p.overview.Split(cui.Horizontal, .46)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(12)
	right.Padding(18)
	header := left.Box(cui.Horizontal, 8)
	label(header, "Repositories", 13, foreground).Expand(true)
	iconButton(header, cui.SymbolPlus, "Link a repository", func() { g.showProjectConfiguration(true); p.alias.Focus() })
	p.repoList = left.List(nil)
	p.repoList.Expand(true)
	style(p.repoList, background, foreground, 6, false)
	style(right, surface, foreground, 10, false, 20)
	p.repoTitle = label(right, "Choose a repository", 17, foreground)
	p.repoMeta = label(right, "Branch and working tree details appear here.", 11, muted)
	p.repoPath = label(right, "", 11, muted)
	label(right, "Repository actions", 10, muted)
	repoActions := right.Box(cui.Horizontal, 8)
	p.repoOpen = button(repoActions, "Open workspace", g.openSelectedRepository)
	p.repoConfigure = button(repoActions, "Manage repository", func() {
		g.selectProjectRepository(p.repoList.Selected(), true)
		g.showProjectConfiguration(true)
		p.alias.Focus()
	})
	repoActions.Box(cui.Horizontal, 0).Expand(true)
	right.Box(cui.Vertical, 0).Expand(true)
	p.repoList.OnAction(func(w cui.Widget) { g.selectProjectRepository(w.Selected(), true) })
	p.configuration = parent.Box(cui.Vertical, 12)
	p.configuration.Expand(true)
	g.buildProjectConfiguration(p.configuration)
	g.showProjectConfiguration(false)
}
func (g *desktop) showProjectConfiguration(show bool) {
	p := &g.workbench.projects
	p.overview.SetVisible(!show)
	p.configuration.SetVisible(show)
	style(p.overviewTab, 0, muted, 6, false, 8)
	style(p.configureTab, 0, muted, 6, false, 8)
	active := p.overviewTab
	if show {
		active = p.configureTab
	}
	style(active, accentSoft, violet, 6, false, 8)
}
func (g *desktop) renderProjectOverview() {
	p := &g.workbench.projects
	s := p.status
	name := s.ProjectName
	if name == "" {
		name = filepath.Base(g.ws.Root)
	}
	p.title.SetText(name)
	description := s.ProjectDescription
	if description == "" {
		description = "Add a description in Configuration to give agents shared project context."
	}
	p.summary.SetText(description)
	linked := 0
	var names []string
	selected := -1
	for i, r := range s.Repos {
		state := "Discovered"
		if r.Linked {
			linked++
			state = "Linked"
		}
		branch := r.CurrentBranch
		if branch == "" {
			branch = r.DefaultBranch
		}
		if branch == "" {
			branch = "No branch"
		}
		changes := r.GitAdded + r.GitModified + r.GitDeleted
		names = append(names, r.Alias+"\n"+state+" · "+branch+fmt.Sprintf(" · %d changes", changes))
		if r.Alias == p.selected {
			selected = i
		}
	}
	p.reposMetric.SetText(fmt.Sprintf("%d linked · %d discovered", linked, len(s.Repos)-linked))
	profile := s.Provider
	if profile == "" {
		profile = "Default profile"
	}
	env := s.Environment
	if env == "" {
		env = "Local"
	}
	p.runtimeMetric.SetText(profile + "\n" + env)
	mcp := "No MCP configuration"
	if s.MCPConfig != "" {
		mcp = "MCP configuration set"
	}
	p.skillsMetric.SetText(fmt.Sprintf("%d active skills\n%s", len(s.Skills), mcp))
	p.repoList.Items(names)
	if selected < 0 && len(s.Repos) > 0 {
		selected = 0
	}
	p.repoList.SetSelected(selected)
	g.selectProjectRepository(selected, false)
}
func (g *desktop) selectProjectRepository(index int, fillForm bool) {
	p := &g.workbench.projects
	valid := index >= 0 && index < len(p.status.Repos)
	p.repoOpen.SetEnabled(valid)
	p.repoConfigure.SetEnabled(valid)
	if !valid {
		p.repoTitle.SetText("No repository selected")
		p.repoMeta.SetText("Link a repository to give this project a workspace.")
		p.repoPath.SetText("")
		p.selected = ""
		return
	}
	r := p.status.Repos[index]
	p.selected = r.Alias
	p.repoTitle.SetText(r.Alias)
	branch := r.CurrentBranch
	if branch == "" {
		branch = r.DefaultBranch
	}
	p.repoMeta.SetText(fmt.Sprintf("Branch  %s\n\n+%d added   ·   %d modified   ·   −%d removed", branch, r.GitAdded, r.GitModified, r.GitDeleted))
	location := r.Path
	if r.Remote != "" {
		location += "\n" + r.Remote
	}
	p.repoPath.SetText(location)
	if fillForm {
		p.alias.SetText(r.Alias)
		p.path.SetText(r.Path)
		p.remote.SetText(r.Remote)
		p.branch.SetText(r.DefaultBranch)
		p.repositoryBaseline = [4]string{r.Alias, r.Path, r.Remote, r.DefaultBranch}
	}
}

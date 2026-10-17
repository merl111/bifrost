package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type projectsView struct {
	form                                                             projectDefaultsForm
	overview, configuration, overviewTab, configureTab               cui.Widget
	title, summary, reposMetric, runtimeMetric, skillsMetric         cui.Widget
	repoList, repoTitle, repoMeta, repoPath, repoOpen, repoConfigure cui.Widget
	list, name, description, alias, path, remote, branch, settings   cui.Widget
	status                                                           ui.ProjectStatus
	selected                                                         string
	busy, loaded                                                     bool
	defaults                                                         string
	repositoryBaseline                                               [4]string
}

func (g *desktop) buildProjectConfiguration(parent cui.Widget) {
	p := &g.workbench.projects
	bar := parent.Box(cui.Horizontal, 8)
	iconButton(bar, cui.SymbolRepeat, "Scan repositories", g.refreshProject)
	button(bar, "Link all discovered", func() {
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			st, _, err := g.ws.Projects.AddAll(ctx)
			return st, err
		})
	})
	button(bar, "Unlink selected", func() {
		alias := p.selected
		if alias != "" {
			g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) { return g.ws.Projects.RemoveRepo(ctx, alias) })
		}
	})
	bar.Box(cui.Horizontal, 0).Expand(true)
	split := parent.Split(cui.Horizontal, .35)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(8)
	right.Padding(12)
	p.list = left.List(nil)
	p.list.Expand(true)
	p.list.OnAction(func(w cui.Widget) {
		i := w.Selected()
		if i < 0 || i >= len(p.status.Repos) {
			return
		}
		r := p.status.Repos[i]
		p.selected = r.Alias
		p.alias.SetText(r.Alias)
		p.path.SetText(r.Path)
		p.remote.SetText(r.Remote)
		p.branch.SetText(r.DefaultBranch)
		p.repositoryBaseline = [4]string{r.Alias, r.Path, r.Remote, r.DefaultBranch}
	})
	row := right.Wrap(12)
	col := row.Box(cui.Vertical, 5)
	col.MinSize(230, 0)
	p.name = labeledEntry(col, "Project name")
	p.description = labeledEntry(col, "Description")
	button(col, "Save project details", func() {
		name, description := p.name.Text(), p.description.Text()
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			return g.ws.Projects.UpdateMeta(ctx, name, description)
		})
	})
	col = row.Box(cui.Vertical, 5)
	col.MinSize(230, 0)
	p.alias = labeledEntry(col, "Repository alias")
	p.path = labeledEntry(col, "Repository path")
	p.remote = labeledEntry(col, "Remote URL (optional)")
	p.branch = labeledEntry(col, "Default branch (optional)")
	button(col, "Link repository", func() {
		alias, path, remote, branch := p.alias.Text(), p.path.Text(), p.remote.Text(), p.branch.Text()
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			return g.ws.Projects.AddRepo(ctx, alias, path, remote, branch)
		})
	})
	label(right, "Runtime & tools", 13, foreground)
	g.buildProjectDefaultsForm(right)
	p.settings = right.Code("")
	p.settings.ReadOnly(false)
	p.settings.Expand(true)
	p.settings.SetVisible(false)
	p.settings.MinSize(0, 150)
	style(p.settings, surface, foreground, 8, true, 10)
	button(right, "Save project defaults", func() {
		next, err := parseProjectDefaults(p.settings.Text(), p.status)
		if err != nil {
			g.workbenchNotice("Invalid project settings", err)
			return
		}
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) { return g.ws.Projects.SaveSettings(ctx, next) })
	})
	label(right, "Provider, skills and environment apply to this project. Permissions and decision routing are shared defaults. Restart to apply runtime changes.", 10, muted)
}

func (g *desktop) refreshProject() { g.projectOperation(g.ws.Projects.Scan) }

func (g *desktop) projectOperation(action func(context.Context) (ui.ProjectStatus, error)) {
	p := &g.workbench.projects
	if p.busy {
		return
	}
	if g.ws.Backend.Identity().Kind != "local" {
		g.workbenchNotice("Local project registry", errors.New("open the local workspace to manage repository links; remote files remain available in the editor"))
		return
	}
	p.busy = true
	g.work(func(ctx context.Context) func() {
		st, err := action(ctx)
		return func() {
			p.busy = false
			if err != nil {
				g.workbenchNotice("Project operation failed", err)
				return
			}
			if !p.loaded || p.name.Text() == p.status.ProjectName {
				p.name.SetText(st.ProjectName)
			}
			if !p.loaded || p.description.Text() == p.status.ProjectDescription {
				p.description.SetText(st.ProjectDescription)
			}
			p.status = st
			// Successful linking establishes a new baseline. Unrelated refreshes
			// retain an unfinished repository form and its unsaved indication.
			for _, r := range st.Repos {
				values := [4]string{r.Alias, r.Path, r.Remote, r.DefaultBranch}
				if r.Linked && p.repositoryValues() == values {
					p.repositoryBaseline = values
				}
			}
			var names []string
			for _, r := range st.Repos {
				state := "Discovered"
				if r.Linked {
					state = "Linked"
				}
				names = append(names, fmt.Sprintf("%s · %s\n%s", r.Alias, state, r.Path))
			}
			p.list.Items(names)
			data := projectDefaultsJSON(st)
			if !p.loaded || p.settings.Text() == p.defaults {
				p.settings.SetText(data)
				g.loadProjectDefaultsForm(st)
			}
			p.defaults = data
			p.loaded = true
			g.renderProjectOverview()
		}
	})
}

func (p *projectsView) repositoryValues() [4]string {
	return [4]string{p.alias.Text(), p.path.Text(), p.remote.Text(), p.branch.Text()}
}

func (g *desktop) openSelectedRepository() {
	p := &g.workbench.projects
	for _, r := range p.status.Repos {
		if r.Alias != p.selected {
			continue
		}
		if filepath.Clean(r.Path) == filepath.Clean(g.ws.Root) {
			g.closeWorkbench()
			return
		}
		args, err := g.ws.RepositoryArguments(p.status.ProjectName, r.Alias, r.Path, r.Linked)
		if err != nil {
			g.workbenchNotice("Cannot open repository", err)
			return
		}
		executable, err := os.Executable()
		if err != nil {
			g.workbenchNotice("Cannot open repository", err)
			return
		}
		command := exec.Command(executable, args...)
		if err := command.Start(); err != nil {
			g.workbenchNotice("Cannot open repository", err)
			return
		}
		go func() { _ = command.Wait() }()
		g.workbenchNotice("Opening repository in a separate window", nil)
		return
	}
}

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"bifrost/internal/cli"
	"bifrost/internal/domain"
	cui "cui.local/cui"
)

type projectPicker struct {
	window                                                                           cui.Window
	search, projects, repositories, description, path, notice, open, folder, current cui.Widget
	catalog                                                                          cli.DesktopLaunchCatalog
	visible                                                                          []domain.Project
	selected                                                                         *domain.Project
	args                                                                             []string
	busy                                                                             bool
	choose                                                                           func([]string)
}

func newProjectPicker(app *cui.App, args []string, choose func([]string)) *projectPicker {
	p := &projectPicker{args: args, choose: choose}
	p.window = app.Window("Bifrost — Open a project", 900, 640)
	root := p.window.Root().Box(cui.Vertical, 14)
	root.Expand(true)
	root.Padding(28)
	style(root, background, foreground, 0, false)
	brand := root.Box(cui.Horizontal, 12)
	label(brand, "◈", 32, violet)
	titles := brand.Box(cui.Vertical, 4)
	label(titles, "Open a project", 22, foreground)
	label(titles, "Choose a project or open a folder to begin.", 12, muted)
	p.search = root.Search("")
	p.search.Placeholder("Find a project…")
	p.search.Accessibility("Find a project", "Filter saved projects by name, description or repository")
	p.search.OnAction(func(cui.Widget) { p.filter() })
	p.search.OnKey(func(key int, mods uint) bool {
		if mods != 0 {
			return false
		}
		switch key {
		case cui.KeyEnter:
			p.filter()
			p.openSelected()
			return true
		case cui.KeyDown:
			p.projects.Focus()
			return true
		}
		return false
	})
	split := root.Split(cui.Horizontal, .4)
	split.Expand(true)
	split.SetSplitScrollable(0, true)
	split.SetSplitScrollable(1, true)
	left, right := split.Pane(0), split.Pane(1)
	left.Padding(8)
	right.Padding(18)
	right = right.Box(cui.Vertical, 12)
	label(left, "SAVED PROJECTS", 10, muted)
	p.projects = left.List(nil)
	p.projects.Expand(true)
	p.projects.Accessibility("Saved projects", "Select a project to see its repositories")
	p.projects.OnAction(func(w cui.Widget) { p.selectProject(w.Selected()) })
	p.projects.OnKey(func(key int, mods uint) bool {
		if key == cui.KeyEnter && mods == 0 {
			p.openSelected()
			return true
		}
		return false
	})
	p.description = label(right, "Loading your projects…", 14, foreground)
	label(right, "Repository to open", 11, muted)
	p.repositories = right.Select(nil)
	p.repositories.Accessibility("Project repository", "Choose which linked repository to open")
	p.repositories.OnAction(func(cui.Widget) { p.selectRepository() })
	p.path = label(right, "", 11, muted)
	p.open = button(root, "Open project  →", p.openSelected)
	style(p.open, violet, background, 8, false, 10)
	p.open.SetEnabled(false)
	p.notice = label(root, "Loading saved projects…", 11, muted)
	actions := root.Wrap(10)
	p.folder = button(actions, "Open a folder…", func() {
		p.window.FileDialog(cui.DialogFolder, "Open a workspace folder", p.catalog.Directory, func(result int, path string) {
			if result == cui.DialogAccepted && path != "" {
				p.openFolder(path)
			}
		})
	})
	p.current = button(actions, "Use current folder", func() { p.openFolder(p.catalog.Directory) })
	p.current.SetEnabled(false)
	label(root, "Each project keeps its linked repositories, skills and runtime defaults.", 10, muted)
	return p
}

func (p *projectPicker) apply(catalog cli.DesktopLaunchCatalog, err error) {
	p.catalog = catalog
	p.current.SetEnabled(catalog.Directory != "")
	p.filter()
	if err != nil {
		p.notice.SetText(safeText("Could not load saved projects: " + err.Error()))
	}
}

func (p *projectPicker) filter() {
	query := strings.ToLower(strings.TrimSpace(p.search.Text()))
	p.visible = nil
	var names []string
	for _, project := range p.catalog.Projects {
		text := project.Name + " " + project.Description
		for _, repo := range project.Repositories {
			text += " " + repo.Alias + " " + repo.Roots[p.catalog.Environment]
		}
		if !strings.Contains(strings.ToLower(text), query) {
			continue
		}
		p.visible = append(p.visible, project)
		names = append(names, fmt.Sprintf("%s  ·  %d", project.Name, len(project.Repositories)))
	}
	p.projects.Items(names)
	index := 0
	for i, project := range p.visible {
		if project.Name == p.catalog.DefaultProject || string(project.ID) == p.catalog.DefaultProject {
			index = i
		}
	}
	p.projects.SetSelected(index)
	p.selectProject(index)
	p.notice.SetText(fmt.Sprintf("%d saved projects · %s environment", len(p.visible), p.catalog.Environment))
}

func (p *projectPicker) selectProject(index int) {
	p.selected = nil
	p.repositories.Items(nil)
	p.open.SetEnabled(false)
	p.path.SetText("")
	if index < 0 || index >= len(p.visible) {
		p.description.SetText("No projects found. Open a folder to get started.")
		return
	}
	p.selected = &p.visible[index]
	text := p.selected.Name
	if p.selected.Description != "" {
		text += "\n\n" + p.selected.Description
	}
	p.description.SetText(safeText(text))
	var names []string
	for _, repo := range p.selected.Repositories {
		names = append(names, repo.Alias)
	}
	p.repositories.Items(names)
	p.repositories.SetSelected(0)
	p.selectRepository()
}

func (p *projectPicker) selectRepository() {
	p.open.SetEnabled(false)
	if p.selected == nil {
		return
	}
	index := p.repositories.Selected()
	if index < 0 || index >= len(p.selected.Repositories) {
		p.path.SetText("This project has no linked repositories. Open a folder and link it from Project settings.")
		return
	}
	path := p.selected.Repositories[index].Roots[p.catalog.Environment]
	if path == "" {
		p.path.SetText("No folder mapped for the selected environment.")
		return
	}
	p.path.SetText(safeText(path))
	p.open.SetEnabled(!p.busy)
}

func (p *projectPicker) openSelected() {
	index := p.repositories.Selected()
	if p.busy || p.selected == nil || index < 0 || index >= len(p.selected.Repositories) {
		return
	}
	args, err := cli.DesktopSelectionArguments(p.args, string(p.selected.ID), p.selected.Repositories[index].Alias, "")
	p.launch(args, err)
}

func (p *projectPicker) openFolder(path string) {
	if p.busy || path == "" {
		return
	}
	args, err := cli.DesktopSelectionArguments(p.args, "", "", path)
	p.launch(args, err)
}

func (p *projectPicker) launch(args []string, err error) {
	if err != nil {
		p.notice.SetText(err.Error())
		return
	}
	p.setBusy(true)
	p.notice.SetText("Opening workspace…")
	p.choose(args)
}

func (p *projectPicker) setBusy(busy bool) {
	p.busy = busy
	p.search.SetEnabled(!busy)
	p.projects.SetEnabled(!busy)
	p.repositories.SetEnabled(!busy)
	p.folder.SetEnabled(!busy)
	p.current.SetEnabled(!busy && p.catalog.Directory != "")
	p.selectRepository()
}

type workspaceLaunch struct {
	ws       *cli.DesktopWorkspace
	snapshot cli.DesktopSnapshot
	err      error
}

// startWithProjectChoice owns one event loop. Disk work happens off the UI
// thread, and every acquired workspace is closed even if the chooser is closed.
func startWithProjectChoice(ctx context.Context, app *cui.App, args []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	results := make(chan workspaceLaunch, 1)
	catalogs := make(chan struct {
		catalog cli.DesktopLaunchCatalog
		err     error
	}, 1)
	var g *desktop
	p := newProjectPicker(app, args, func(selected []string) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ws, err := cli.OpenDesktopWorkspace(ctx, selected, os.Stderr)
			result := workspaceLaunch{ws: ws, err: err}
			if err == nil {
				result.snapshot, result.err = ws.Snapshot(ctx)
			}
			results <- result
		}()
	})
	workers.Add(1)
	go func() {
		defer workers.Done()
		catalog, err := cli.DesktopProjects(ctx, args)
		catalogs <- struct {
			catalog cli.DesktopLaunchCatalog
			err     error
		}{catalog, err}
	}()
	timer := app.Every(33, func() {
		if ctx.Err() != nil {
			app.Quit()
			return
		}
		select {
		case loaded := <-catalogs:
			p.apply(loaded.catalog, loaded.err)
		case result := <-results:
			if result.err != nil {
				if result.ws != nil {
					result.ws.Close()
				}
				p.setBusy(false)
				p.notice.SetText(safeText("Could not open workspace: " + result.err.Error()))
				p.search.Focus()
				return
			}
			g = showDesktop(ctx, app, result.ws, result.snapshot)
			p.window.Close()
		default:
		}
	})
	defer timer.Stop()
	p.window.Show()
	p.search.Focus()
	err := app.Run()
	cancel()
	workers.Wait()
	if g != nil {
		g.cancel()
		g.workers.Wait()
		g.ws.Close()
	}
	select {
	case pending := <-results:
		if pending.ws != nil {
			pending.ws.Close()
		}
	default:
	}
	return err
}

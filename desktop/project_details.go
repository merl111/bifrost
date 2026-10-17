package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"bifrost/internal/store"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type projectDetailsView struct {
	cancel           context.CancelFunc
	tab, body, links cui.Widget
	data             ui.ProjectDetails
	urls             []string
	request          uint64
	loading          bool
}

func (g *desktop) buildProjectDetails(parent cui.Widget) {
	d := &g.projectDetails
	bar := parent.Wrap(8)
	d.tab = bar.Select([]string{"Overview & GitHub", "README", "Pull requests", "Issues", "Connectors & Notion"})
	d.tab.SetSelected(0)
	d.tab.OnAction(func(cui.Widget) { g.renderProjectDetails() })
	button(bar, "Refresh", g.fetchProjectDetails)
	button(bar, "Configure connectors", func() {
		g.openWorkbench("Project")
		g.showProjectConfiguration(true)
		g.selectProjectDefaultsMode(true)
		g.workbench.projects.settings.Focus()
	})
	d.body = parent.Code("")
	d.body.ReadOnly(true)
	d.body.Expand(true)
	style(d.body, surface, foreground, 10, false, 14)
	row := parent.Box(cui.Horizontal, 8)
	d.links = row.Select(nil)
	d.links.Expand(true)
	button(row, "Open selected link", func() {
		i := d.links.Selected()
		if i < 0 || i >= len(d.urls) {
			return
		}
		url := d.urls[i]
		g.work(func(context.Context) func() {
			err := ui.OpenExternalURL(url)
			return func() {
				if err != nil {
					g.showError(err)
				}
			}
		})
	})
}
func (g *desktop) openProjectDetails(tab int) {
	g.openWorkbench("Project details")
	g.projectDetails.tab.SetSelected(tab)
	g.fetchProjectDetails()
}
func (g *desktop) fetchProjectDetails() {
	d := &g.projectDetails
	if d.cancel != nil {
		d.cancel()
	}
	ctx, cancel := context.WithCancel(g.ctx)
	d.cancel = cancel
	d.request++
	request := d.request
	d.loading = true
	d.body.SetText("Loading project, GitHub and Notion details…")
	g.work(func(context.Context) func() {
		defer cancel()
		data, err := g.ws.Projects.FetchDetails(ctx)
		return func() {
			if request != d.request {
				return
			}
			d.loading = false
			if err != nil {
				g.workbenchNotice("Could not load project details", err)
				d.body.SetText(err.Error())
				return
			}
			d.data = data
			g.renderProjectDetails()
		}
	})
}
func (g *desktop) renderProjectDetails() {
	view := &g.projectDetails
	d := view.data
	var b strings.Builder
	var labels, urls []string
	link := func(label, url string) {
		if url != "" {
			labels = append(labels, label)
			urls = append(urls, url)
		}
	}
	switch view.tab.Selected() {
	case 0:
		fmt.Fprintf(&b, "%s\n%s\n\nGitHub account: %s\n%s\n\n", g.workbench.projects.status.ProjectName, g.workbench.projects.status.ProjectDescription, d.GitHubUser, d.GitHubError)
		for _, r := range d.GitHubRepos {
			fmt.Fprintf(&b, "%s · %s\n%s\nLanguage: %s · Default branch: %s\nStars: %d · Forks: %d · Open issues: %d\nPrivate: %t · Archived: %t\nTopics: %s\n%s\n\n", r.Alias, r.Slug, r.Description, r.Language, r.DefaultBranch, r.Stars, r.Forks, r.OpenIssues, r.Private, r.Archived, strings.Join(r.Topics, ", "), r.Error)
			link(r.Slug, r.HTMLURL)
		}
	case 1:
		fmt.Fprintf(&b, "%s\n\n%s", d.ReadmeTitle, d.ReadmeContent)
		for _, r := range d.GitHubRepos {
			if r.Readme != "" {
				fmt.Fprintf(&b, "\n\n%s\n\n%s", r.Slug, r.Readme)
			}
		}
	case 2:
		fmt.Fprintf(&b, "%d open pull requests\n\n", len(d.OpenPRs))
		for _, p := range d.OpenPRs {
			fmt.Fprintf(&b, "%s #%d · %s\n%s · %s · Draft: %t\n%s → %s · Updated %s\n%s\n\n", p.RepoSlug, p.Number, p.Title, p.State, p.Author, p.Draft, p.Head, p.Base, p.UpdatedAt, p.URL)
			link(fmt.Sprintf("%s #%d · %s", p.RepoSlug, p.Number, p.Title), p.URL)
		}
	case 3:
		fmt.Fprintf(&b, "%d open issues\n\n", len(d.OpenIssues))
		for _, i := range d.OpenIssues {
			fmt.Fprintf(&b, "%s #%d · %s\n%s · %s · %d comments\nLabels: %s · Updated %s\n%s\n\n", i.RepoSlug, i.Number, i.Title, i.State, i.Author, i.Comments, strings.Join(i.Labels, ", "), i.UpdatedAt, i.URL)
			link(fmt.Sprintf("%s #%d · %s", i.RepoSlug, i.Number, i.Title), i.URL)
		}
	case 4:
		fmt.Fprintf(&b, "GitHub\nConnected: %t · Account: %s\nToken environment: %s\n%s\n\nNotion\nConnected: %t · Workspace: %s · Bot: %s\nToken environment: %s\nLinked page: %s\n%s\n\n%s\n\n", d.GitHubConnected, d.GitHubUser, d.GitHubTokenEnv, d.GitHubError, d.NotionConnected, d.NotionWorkspace, d.NotionBotName, d.NotionTokenEnv, d.NotionPage, d.NotionError, d.NotionPageBody)
		for _, p := range d.NotionPages {
			fmt.Fprintf(&b, "%s · %s\nUpdated %s\n%s\n\n", p.Title, p.Object, p.LastEditedTime, p.URL)
			link(p.Title, p.URL)
		}
	}
	if b.Len() == 0 {
		b.WriteString("No details available. Refresh or configure project connectors.")
	}
	view.body.SetText(cleanText(b.String()))
	view.urls = urls
	view.links.Items(labels)
	if len(labels) > 0 {
		view.links.SetSelected(0)
	}
}
func (g *desktop) commandProject(a []string) error {
	if len(a) == 0 {
		g.openWorkbench("Project")
		return nil
	}
	sub := strings.ToLower(a[0])
	tabs := map[string]int{"details": 0, "info": 0, "github": 0, "gh": 0, "readme": 1, "prs": 2, "pr": 2, "pulls": 2, "issues": 3, "issue": 3, "notion": 4, "connectors": 4}
	if tab, ok := tabs[sub]; ok {
		if len(a) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		g.openProjectDetails(tab)
		return nil
	}
	switch sub {
	case "scan", "list", "browse", "settings", "config", "options":
		if len(a) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		g.openWorkbench("Project")
		g.refreshProject()
		if sub == "settings" || sub == "config" || sub == "options" {
			g.showProjectConfiguration(true)
		}
	case "name", "rename", "description", "desc", "describe":
		if len(a) == 1 {
			g.openWorkbench("Project")
			g.showProjectConfiguration(true)
			return nil
		}
		value := strings.Join(a[1:], " ")
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			st, err := g.ws.Projects.Scan(ctx)
			if err != nil {
				return st, err
			}
			if sub == "name" || sub == "rename" {
				st.ProjectName = value
			} else {
				st.ProjectDescription = value
			}
			return g.ws.Projects.UpdateMeta(ctx, st.ProjectName, st.ProjectDescription)
		})
	case "add-all":
		if len(a) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			st, _, err := g.ws.Projects.AddAll(ctx)
			return st, err
		})
	case "remove", "rm", "remove-repo", "unlink":
		if len(a) != 2 {
			return fmt.Errorf("expected repository alias")
		}
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) { return g.ws.Projects.RemoveRepo(ctx, a[1]) })
	case "add", "link":
		if len(a) == 1 {
			g.openWorkbench("Project")
			g.showProjectConfiguration(true)
			return nil
		}
		if len(a) > 3 {
			return fmt.Errorf("expected alias and optional remote")
		}
		target := a[1]
		remote := ""
		if len(a) == 3 {
			remote = a[2]
		}
		g.projectOperation(func(ctx context.Context) (ui.ProjectStatus, error) {
			st, err := g.ws.Projects.Scan(ctx)
			if err != nil {
				return st, err
			}
			alias, path, branch := store.SanitizeAlias(filepath.Base(target)), target, ""
			if !filepath.IsAbs(path) {
				path = filepath.Join(g.ws.Root, path)
			}
			for _, r := range st.Repos {
				if r.Alias == target || r.RelPath == strings.TrimPrefix(target, "./") || r.Path == target {
					alias, path, branch = r.Alias, r.Path, r.DefaultBranch
					if remote == "" {
						remote = r.Remote
					}
					break
				}
			}
			return g.ws.Projects.AddRepo(ctx, alias, path, remote, branch)
		})
	default:
		return fmt.Errorf("unknown project action")
	}
	return nil
}

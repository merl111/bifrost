package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ProjectGitHubPR summarizes one GitHub pull request for the project details modal.
type ProjectGitHubPR struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Draft     bool     `json:"draft"`
	Author    string   `json:"author,omitempty"`
	Head      string   `json:"head,omitempty"`
	Base      string   `json:"base,omitempty"`
	URL       string   `json:"url,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	RepoSlug  string   `json:"repo_slug,omitempty"`
}

// ProjectGitHubIssue summarizes one GitHub issue for the project details modal.
type ProjectGitHubIssue struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"`
	Author    string   `json:"author,omitempty"`
	Comments  int      `json:"comments"`
	Labels    []string `json:"labels,omitempty"`
	URL       string   `json:"url,omitempty"`
	UpdatedAt string   `json:"updated_at,omitempty"`
	RepoSlug  string   `json:"repo_slug,omitempty"`
}

// ProjectGitHubRepoInfo holds live GitHub metadata for one linked repository.
type ProjectGitHubRepoInfo struct {
	Alias         string   `json:"alias"`
	Slug          string   `json:"slug"`
	Description   string   `json:"description,omitempty"`
	HTMLURL       string   `json:"html_url,omitempty"`
	Language      string   `json:"language,omitempty"`
	DefaultBranch string   `json:"default_branch,omitempty"`
	Stars         int      `json:"stars"`
	Forks         int      `json:"forks"`
	OpenIssues    int      `json:"open_issues"`
	Private       bool     `json:"private"`
	Archived      bool     `json:"archived"`
	Topics        []string `json:"topics,omitempty"`
	UpdatedAt     string   `json:"updated_at,omitempty"`
	Readme        string   `json:"readme,omitempty"`
	Error         string   `json:"error,omitempty"`
}

// ProjectNotionItem summarizes one Notion page or database from the Notion connector.
type ProjectNotionItem struct {
	ID             string `json:"id"`
	Object         string `json:"object"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	LastEditedTime string `json:"last_edited_time,omitempty"`
}

// ProjectDetails holds the aggregated project details, GitHub info, README,
// open PRs, issues, and Notion connector state for the tabbed modal.
type ProjectDetails struct {
	GitHubConnected bool                    `json:"github_connected"`
	GitHubUser      string                  `json:"github_user,omitempty"`
	GitHubTokenEnv  string                  `json:"github_token_env,omitempty"`
	GitHubError     string                  `json:"github_error,omitempty"`
	GitHubRepos     []ProjectGitHubRepoInfo `json:"github_repos,omitempty"`
	ReadmeTitle     string                  `json:"readme_title,omitempty"`
	ReadmeContent   string                  `json:"readme_content,omitempty"`
	OpenPRs         []ProjectGitHubPR       `json:"open_prs,omitempty"`
	OpenIssues      []ProjectGitHubIssue    `json:"open_issues,omitempty"`

	NotionConnected bool                `json:"notion_connected"`
	NotionBotName   string              `json:"notion_bot_name,omitempty"`
	NotionWorkspace string              `json:"notion_workspace,omitempty"`
	NotionTokenEnv  string              `json:"notion_token_env,omitempty"`
	NotionPage      string              `json:"notion_page,omitempty"`
	NotionPageBody  string              `json:"notion_page_body,omitempty"`
	NotionError     string              `json:"notion_error,omitempty"`
	NotionPages     []ProjectNotionItem `json:"notion_pages,omitempty"`
}

type projectDetailsMsg struct {
	id      uint64
	details ProjectDetails
	err     error
}

var projectDetailsTabNames = []string{
	"Overview & GitHub",
	"README",
	"Pull Requests",
	"Issues",
	"Connectors (GitHub & Notion)",
}

func (m *Model) openProjectDetailsModal(tab int) tea.Cmd {
	if tab < 0 || tab >= len(projectDetailsTabNames) {
		tab = 0
	}
	m.projectOpen = true
	m.projectDetailsOpen = true
	m.projectSettingsOpen = false
	m.projectDetailsTab = tab
	m.projectDetailsScroll = 0
	m.projectDetailsSelected = 0
	m.focus = focusViewer
	return m.startProjectDetailsFetch()
}

func (m *Model) startProjectDetailsFetch() tea.Cmd {
	if m.projectCallbacks.FetchDetails == nil {
		// Fallback local README inspection when no callback is wired
		m.projectDetails = m.loadFallbackProjectDetails()
		m.projectDetailsLoading = false
		m.status = "project details loaded"
		return nil
	}
	m.projectDetailsLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	fetchFn := m.projectCallbacks.FetchDetails
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	m.status = "fetching project details, GitHub PRs/issues & Notion status…"
	return func() tea.Msg {
		details, err := fetchFn(ctx)
		return projectDetailsMsg{id: id, details: details, err: err}
	}
}

func (m *Model) loadFallbackProjectDetails() ProjectDetails {
	var d ProjectDetails
	for _, candidate := range []string{"README.md", "README.txt", "README", "docs/README.md"} {
		if text, err := m.readFile(candidate); err == nil && strings.TrimSpace(text) != "" {
			d.ReadmeTitle = candidate
			d.ReadmeContent = text
			break
		}
	}
	return d
}

func (m *Model) handleProjectDetailsMsg(msg projectDetailsMsg) tea.Cmd {
	if msg.id != m.projectID {
		return nil
	}
	m.projectDetailsLoading = false
	m.projectCancel = nil
	if msg.err != nil {
		m.projectError = sanitizeDisplay(msg.err.Error())
		m.status = "project details error: " + m.projectError
		return nil
	}
	m.projectDetails = msg.details
	m.status = "project details updated (Tab/1-5 switch tabs · j/k scroll · r refresh · Esc back)"
	return nil
}

func (m *Model) handleProjectDetailsKey(key string) tea.Cmd {
	switch key {
	case ":", "ctrl+p":
		m.paletteOpen = true
		m.paletteInput = ""
		m.paletteSelected = -1
		m.paletteCompletion = paletteCompletionState{}
		m.paletteError, m.paletteHelp = "", ""
		m.focus = focusViewer
		m.status = "command palette"
		return nil
	case "esc", "escape", "q", "backspace":
		m.projectDetailsOpen = false
		m.status = "returned to project repositories"
		return nil
	case "tab", "L", "]":
		m.projectDetailsTab = (m.projectDetailsTab + 1) % len(projectDetailsTabNames)
		m.projectDetailsScroll = 0
		m.projectDetailsSelected = 0
		return nil
	case "shift+tab", "H", "[":
		m.projectDetailsTab = (m.projectDetailsTab - 1 + len(projectDetailsTabNames)) % len(projectDetailsTabNames)
		m.projectDetailsScroll = 0
		m.projectDetailsSelected = 0
		return nil
	case "1":
		m.projectDetailsTab, m.projectDetailsScroll, m.projectDetailsSelected = 0, 0, 0
		return nil
	case "2":
		m.projectDetailsTab, m.projectDetailsScroll, m.projectDetailsSelected = 1, 0, 0
		return nil
	case "3":
		m.projectDetailsTab, m.projectDetailsScroll, m.projectDetailsSelected = 2, 0, 0
		return nil
	case "4":
		m.projectDetailsTab, m.projectDetailsScroll, m.projectDetailsSelected = 3, 0, 0
		return nil
	case "5":
		m.projectDetailsTab, m.projectDetailsScroll, m.projectDetailsSelected = 4, 0, 0
		return nil
	case "r", "R":
		return m.startProjectDetailsFetch()
	case "j", "down":
		switch m.projectDetailsTab {
		case 2:
			if m.projectDetailsSelected < len(m.projectDetails.OpenPRs)-1 {
				m.projectDetailsSelected++
			}
		case 3:
			if m.projectDetailsSelected < len(m.projectDetails.OpenIssues)-1 {
				m.projectDetailsSelected++
			}
		case 4:
			if m.projectDetailsSelected < 2 {
				m.projectDetailsSelected++
			}
		default:
			m.projectDetailsScroll++
		}
		return nil
	case "k", "up":
		switch m.projectDetailsTab {
		case 2, 3, 4:
			if m.projectDetailsSelected > 0 {
				m.projectDetailsSelected--
			}
		default:
			if m.projectDetailsScroll > 0 {
				m.projectDetailsScroll--
			}
		}
		return nil
	case "pgdown", "pagedown", "ctrl+d":
		m.projectDetailsScroll += 10
		return nil
	case "pgup", "pageup", "ctrl+u":
		m.projectDetailsScroll = maxInt(m.projectDetailsScroll-10, 0)
		return nil
	case "enter", "return", "o":
		return m.activateProjectDetailsSelection()
	}
	return nil
}

func (m *Model) activateProjectDetailsSelection() tea.Cmd {
	switch m.projectDetailsTab {
	case 2:
		if m.projectDetailsSelected >= 0 && m.projectDetailsSelected < len(m.projectDetails.OpenPRs) {
			pr := m.projectDetails.OpenPRs[m.projectDetailsSelected]
			if pr.URL != "" {
				m.status = fmt.Sprintf("opening PR #%d in browser: %s", pr.Number, pr.URL)
				urlStr := pr.URL
				return func() tea.Msg {
					_ = openExternalURL(urlStr)
					return nil
				}
			}
		}
	case 3:
		if m.projectDetailsSelected >= 0 && m.projectDetailsSelected < len(m.projectDetails.OpenIssues) {
			iss := m.projectDetails.OpenIssues[m.projectDetailsSelected]
			if iss.URL != "" {
				m.status = fmt.Sprintf("opening Issue #%d in browser: %s", iss.Number, iss.URL)
				urlStr := iss.URL
				return func() tea.Msg {
					_ = openExternalURL(urlStr)
					return nil
				}
			}
		}
	case 4:
		switch m.projectDetailsSelected {
		case 0:
			m.projectEditField = "github_token_env"
			m.projectEditInput = valueOr(m.projectStatus.GitHubTokenEnv, valueOr(m.projectDetails.GitHubTokenEnv, "GITHUB_TOKEN"))
			m.status = "editing GitHub token environment variable (e.g. GITHUB_TOKEN or GH_TOKEN)"
			return nil
		case 1:
			m.projectEditField = "notion_token_env"
			m.projectEditInput = valueOr(m.projectStatus.NotionTokenEnv, valueOr(m.projectDetails.NotionTokenEnv, "NOTION_API_KEY"))
			m.status = "editing Notion token environment variable (e.g. NOTION_API_KEY)"
			return nil
		case 2:
			m.projectEditField = "notion_page"
			m.projectEditInput = m.projectStatus.NotionPage
			m.status = "editing linked Notion page ID or search query for this project"
			return nil
		}
	}
	return nil
}

func (m *Model) renderProjectDetailsModal(width, height int, topHeader string) string {
	p := m.colors()
	var b strings.Builder
	b.WriteString(topHeader)

	// Render Tab Bar
	var tabs []string
	for i, name := range projectDetailsTabNames {
		badge := fmt.Sprintf(" [%d] %s ", i+1, name)
		if i == 2 && len(m.projectDetails.OpenPRs) > 0 {
			badge = fmt.Sprintf(" [%d] %s (%d) ", i+1, name, len(m.projectDetails.OpenPRs))
		} else if i == 3 && len(m.projectDetails.OpenIssues) > 0 {
			badge = fmt.Sprintf(" [%d] %s (%d) ", i+1, name, len(m.projectDetails.OpenIssues))
		}
		if i == m.projectDetailsTab {
			tabs = append(tabs, p.badge(badge))
		} else {
			tabs = append(tabs, p.mutedText(badge))
		}
	}
	b.WriteString(strings.Join(tabs, " ") + "\n")
	b.WriteString(p.quietText(strings.Repeat("━", maxInt(minInt(width-6, 112), 24))) + "\n")

	if m.projectDetailsLoading {
		b.WriteString("\n" + p.cyanText("⠋ Pulling live GitHub repository info, README, open PRs, issues, and Notion pages…") + "\n")
	}

	innerW := maxInt(width-6, 36)
	bodyBudget := maxInt(height-strings.Count(b.String(), "\n")-5, 6)

	var contentLines []string
	switch m.projectDetailsTab {
	case 0:
		contentLines = m.renderProjectDetailsOverviewTab(innerW)
	case 1:
		contentLines = m.renderProjectDetailsReadmeTab(innerW)
	case 2:
		contentLines = m.renderProjectDetailsPRsTab(innerW)
	case 3:
		contentLines = m.renderProjectDetailsIssuesTab(innerW)
	case 4:
		contentLines = m.renderProjectDetailsConnectorsTab(innerW)
	}

	if m.projectDetailsTab == 0 || m.projectDetailsTab == 1 {
		maxScroll := maxInt(len(contentLines)-bodyBudget, 0)
		if m.projectDetailsScroll > maxScroll {
			m.projectDetailsScroll = maxScroll
		}
		start := minInt(m.projectDetailsScroll, len(contentLines))
		end := minInt(start+bodyBudget, len(contentLines))
		visible := contentLines[start:end]
		for _, line := range visible {
			b.WriteString(line + "\n")
		}
		if maxScroll > 0 {
			b.WriteString(p.quietText(fmt.Sprintf("\n  Lines %d–%d of %d (j/k or PgUp/PgDn to scroll)", start+1, end, len(contentLines))))
		}
	} else {
		for i, line := range contentLines {
			if i >= bodyBudget {
				break
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n" + p.quietText("Tab / 1-5 switch tabs  ·  j/k navigate/scroll  ·  Enter open/configure  ·  r refresh live data  ·  Esc back to project"))
	return renderPanelTheme(m.theme, "PROJECT DETAILS  ·  GITHUB & NOTION", b.String(), width, true, height)
}

func (m *Model) renderProjectDetailsOverviewTab(width int) []string {
	p := m.colors()
	d := m.projectDetails
	var lines []string

	projName := valueOr(m.projectStatus.ProjectName, "workspace")
	lines = append(lines, p.title("◆ Project Overview: "+projName))
	if m.projectStatus.ProjectDescription != "" {
		lines = append(lines, p.mutedText("  "+m.projectStatus.ProjectDescription))
	}
	lines = append(lines,
		fmt.Sprintf("  Root: %s   ·   Environment: %s   ·   Default Provider: %s",
			p.inkText(m.root),
			p.cyanText(valueOr(m.projectStatus.Environment, "local")),
			p.violetText(valueOr(m.projectStatus.Provider, "(global default)")),
		),
	)
	lines = append(lines, "")

	// Connector status badges
	ghBadge := p.warnText("○ GitHub: unauthenticated (public API / set GITHUB_TOKEN or gh auth login)")
	if d.GitHubConnected {
		user := valueOr(d.GitHubUser, "authenticated")
		ghBadge = p.okText("● GitHub Connected (" + user + ")")
	}
	notionBadge := p.quietText("○ Notion: not connected (press 5 to configure NOTION_API_KEY)")
	if d.NotionConnected {
		ws := valueOr(d.NotionWorkspace, valueOr(d.NotionBotName, "connected"))
		notionBadge = p.okText("● Notion Connected (" + ws + ")")
	}
	lines = append(lines, "  "+ghBadge+"     "+notionBadge)
	if d.GitHubError != "" {
		lines = append(lines, "  "+p.dangerText("GitHub Notice: "+d.GitHubError))
	}
	lines = append(lines, "")

	lines = append(lines, p.accentText("📦 Linked GitHub Repositories"))
	lines = append(lines, p.quietText("  "+strings.Repeat("─", minInt(width-4, 96))))
	if len(d.GitHubRepos) == 0 {
		lines = append(lines, p.mutedText("  No GitHub remote repositories found yet. Link a repo in :project or add a GitHub remote."))
	}
	for _, repo := range d.GitHubRepos {
		vis := "public"
		if repo.Private {
			vis = "private"
		}
		header := fmt.Sprintf("  ★ %s  (%s)  ·  ★ %d stars  ·  ⑂ %d forks  ·  ⊙ %d open issues  ·  branch: %s",
			p.cyanText(repo.Slug),
			vis,
			repo.Stars,
			repo.Forks,
			repo.OpenIssues,
			valueOr(repo.DefaultBranch, "main"),
		)
		lines = append(lines, header)
		if repo.Description != "" {
			lines = append(lines, "    "+p.inkText(repo.Description))
		}
		if repo.Language != "" || len(repo.Topics) > 0 {
			meta := ""
			if repo.Language != "" {
				meta = "Language: " + repo.Language
			}
			if len(repo.Topics) > 0 {
				if meta != "" {
					meta += "  ·  "
				}
				meta += "Topics: " + strings.Join(repo.Topics, ", ")
			}
			lines = append(lines, "    "+p.mutedText(meta))
		}
		if repo.HTMLURL != "" {
			lines = append(lines, "    "+p.quietText(repo.HTMLURL))
		}
		if repo.Error != "" {
			lines = append(lines, "    "+p.warnText("API note: "+repo.Error))
		}
		lines = append(lines, "")
	}

	if len(d.NotionPages) > 0 || d.NotionPageBody != "" {
		lines = append(lines, p.accentText("📓 Linked Notion Knowledge"))
		lines = append(lines, p.quietText("  "+strings.Repeat("─", minInt(width-4, 96))))
		if d.NotionPageBody != "" {
			for _, l := range wrapChatText(d.NotionPageBody, maxInt(width-6, 30)) {
				lines = append(lines, "  "+l)
			}
			lines = append(lines, "")
		}
		for _, page := range d.NotionPages {
			lines = append(lines, fmt.Sprintf("  • %s  %s", p.inkText(page.Title), p.quietText(page.URL)))
		}
	}
	return lines
}

func (m *Model) renderProjectDetailsReadmeTab(width int) []string {
	p := m.colors()
	d := m.projectDetails
	title := valueOr(d.ReadmeTitle, "README.md")
	content := strings.TrimSpace(d.ReadmeContent)
	if content == "" {
		for _, repo := range d.GitHubRepos {
			if strings.TrimSpace(repo.Readme) != "" {
				title = repo.Slug + " / README.md"
				content = repo.Readme
				break
			}
		}
	}
	if content == "" {
		return []string{
			p.mutedText("  No README found locally or via GitHub API for this project."),
		}
	}
	lines := []string{
		p.accentText("📖 " + title),
		p.quietText("  " + strings.Repeat("─", minInt(width-4, 96))),
	}
	rendered := renderChatMarkdown(content, maxInt(width-4, 24), p)
	for _, line := range rendered {
		lines = append(lines, "  "+line)
	}
	return lines
}

func (m *Model) renderProjectDetailsPRsTab(width int) []string {
	p := m.colors()
	prs := m.projectDetails.OpenPRs
	var lines []string
	lines = append(lines, p.accentText(fmt.Sprintf("⑂ Open Pull Requests (%d)", len(prs)))+"  "+p.quietText("(j/k select · Enter/o open in browser · r refresh)"))
	lines = append(lines, p.quietText("  "+strings.Repeat("─", minInt(width-4, 96))))
	if len(prs) == 0 {
		if m.projectDetails.GitHubError != "" {
			lines = append(lines, "  "+p.warnText(m.projectDetails.GitHubError))
		} else {
			lines = append(lines, p.mutedText("  No open pull requests across linked GitHub repositories."))
		}
		return lines
	}
	for i, pr := range prs {
		prefix := "  "
		if i == m.projectDetailsSelected {
			prefix = "› "
		}
		stateBadge := "[OPEN]"
		if pr.Draft {
			stateBadge = "[DRAFT]"
		}
		repoPrefix := ""
		if pr.RepoSlug != "" {
			repoPrefix = pr.RepoSlug + " "
		}
		row := fmt.Sprintf("%s%-7s %s#%d  %s  (%s ← %s)",
			prefix,
			stateBadge,
			repoPrefix,
			pr.Number,
			sanitizeDisplay(pr.Title),
			valueOr(pr.Base, "main"),
			valueOr(pr.Head, "branch"),
		)
		if pr.Author != "" {
			row += "  by @" + pr.Author
		}
		row = ansi.Truncate(row, maxInt(width-2, 24), "…")
		if i == m.projectDetailsSelected {
			lines = append(lines, p.accentText(row))
			if pr.URL != "" {
				lines = append(lines, "          "+p.cyanText(pr.URL))
			}
		} else {
			lines = append(lines, p.inkText(row))
		}
	}
	return lines
}

func (m *Model) renderProjectDetailsIssuesTab(width int) []string {
	p := m.colors()
	issues := m.projectDetails.OpenIssues
	var lines []string
	lines = append(lines, p.accentText(fmt.Sprintf("⊙ Open Issues (%d)", len(issues)))+"  "+p.quietText("(j/k select · Enter/o open in browser · r refresh)"))
	lines = append(lines, p.quietText("  "+strings.Repeat("─", minInt(width-4, 96))))
	if len(issues) == 0 {
		if m.projectDetails.GitHubError != "" {
			lines = append(lines, "  "+p.warnText(m.projectDetails.GitHubError))
		} else {
			lines = append(lines, p.mutedText("  No open issues across linked GitHub repositories."))
		}
		return lines
	}
	for i, iss := range issues {
		prefix := "  "
		if i == m.projectDetailsSelected {
			prefix = "› "
		}
		repoPrefix := ""
		if iss.RepoSlug != "" {
			repoPrefix = iss.RepoSlug + " "
		}
		labels := ""
		if len(iss.Labels) > 0 {
			labels = " [" + strings.Join(iss.Labels, ", ") + "]"
		}
		row := fmt.Sprintf("%s%s#%d  %s%s",
			prefix,
			repoPrefix,
			iss.Number,
			sanitizeDisplay(iss.Title),
			labels,
		)
		if iss.Author != "" {
			row += "  by @" + iss.Author
		}
		if iss.Comments > 0 {
			row += fmt.Sprintf("  (%d 💬)", iss.Comments)
		}
		row = ansi.Truncate(row, maxInt(width-2, 24), "…")
		if i == m.projectDetailsSelected {
			lines = append(lines, p.accentText(row))
			if iss.URL != "" {
				lines = append(lines, "          "+p.cyanText(iss.URL))
			}
		} else {
			lines = append(lines, p.inkText(row))
		}
	}
	return lines
}

func (m *Model) renderProjectDetailsConnectorsTab(width int) []string {
	p := m.colors()
	d := m.projectDetails
	var lines []string

	lines = append(lines, p.accentText("🔌 Project Connectors (GitHub & Notion)")+"  "+p.quietText("(j/k select · Enter configure · r test & refresh)"))
	lines = append(lines, p.quietText("  "+strings.Repeat("─", minInt(width-4, 96))))

	ghStatus := "Unauthenticated (using public API or `gh auth token` if installed)"
	if d.GitHubConnected {
		ghStatus = "Connected as @" + valueOr(d.GitHubUser, "authenticated")
	}
	notionStatus := "Not connected (set NOTION_API_KEY in environment)"
	if d.NotionConnected {
		notionStatus = "Connected (" + valueOr(d.NotionWorkspace, valueOr(d.NotionBotName, "Notion Bot")) + ")"
	}

	rows := []struct {
		label string
		val   string
		hint  string
	}{
		{
			"GitHub Connector Token Env",
			valueOr(m.projectStatus.GitHubTokenEnv, valueOr(d.GitHubTokenEnv, "GITHUB_TOKEN / GH_TOKEN")),
			ghStatus,
		},
		{
			"Notion Connector Token Env",
			valueOr(m.projectStatus.NotionTokenEnv, valueOr(d.NotionTokenEnv, "NOTION_API_KEY")),
			notionStatus,
		},
		{
			"Project Notion Page / Query",
			valueOr(m.projectStatus.NotionPage, valueOr(d.NotionPage, "(search project name)")),
			"Enter to link a Notion page ID, URL, or workspace search query",
		},
	}

	for i, row := range rows {
		cursor := "  "
		if i == m.projectDetailsSelected {
			cursor = "› "
		}
		line := fmt.Sprintf("%s%-28s  %-28s  %s",
			cursor,
			row.label,
			truncateCell(sanitizeDisplay(row.val), 28),
			row.hint,
		)
		line = ansi.Truncate(line, maxInt(width-2, 24), "…")
		if i == m.projectDetailsSelected {
			lines = append(lines, p.accentText(line))
		} else {
			lines = append(lines, p.inkText(fmt.Sprintf("%s%-28s  ", cursor, row.label))+p.cyanText(fmt.Sprintf("%-28s  ", truncateCell(sanitizeDisplay(row.val), 28)))+p.mutedText(row.hint))
		}
	}

	lines = append(lines, "")
	if d.NotionError != "" {
		lines = append(lines, "  "+p.warnText("Notion Status: "+d.NotionError))
	}
	if len(d.NotionPages) > 0 {
		lines = append(lines, p.accentText("  Recent / Matching Notion Pages:"))
		for _, pg := range d.NotionPages {
			lines = append(lines, fmt.Sprintf("    • %s  %s", p.inkText(pg.Title), p.quietText(pg.URL)))
		}
	} else if d.NotionConnected {
		lines = append(lines, p.mutedText("  No matching Notion pages found for query."))
	}
	lines = append(lines, "")
	lines = append(lines, p.mutedText("  Tip: You can also install the GitHub and Notion MCP servers via `:mcp` for full agent tool access."))
	return lines
}

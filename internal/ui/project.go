package ui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"bifrost/internal/domain"
	"bifrost/internal/store"
)

// ProjectRepoEntry represents a linked or discovered Git/GitHub repository in
// the workspace project view.
type ProjectRepoEntry struct {
	Alias         string `json:"alias"`
	Path          string `json:"path,omitempty"`
	RelPath       string `json:"rel_path,omitempty"`
	Remote        string `json:"remote,omitempty"`
	GitHubSlug    string `json:"github_slug,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CurrentBranch string `json:"current_branch,omitempty"`
	GitAdded      int    `json:"git_added"`
	GitModified   int    `json:"git_modified"`
	GitDeleted    int    `json:"git_deleted"`
	Linked        bool   `json:"linked"`
	Discovered    bool   `json:"discovered"`
}

// ProjectStatus summarizes the active project and its linked/discovered
// repositories for the current workspace root.
type ProjectStatus struct {
	ProjectName           string             `json:"project_name"`
	ProjectDescription    string             `json:"project_description,omitempty"`
	ProjectID             string             `json:"project_id,omitempty"`
	Root                  string             `json:"root"`
	Environment           string             `json:"environment,omitempty"`
	Provider              string             `json:"provider,omitempty"`
	AvailableProviders    []string           `json:"available_providers,omitempty"`
	AllowWrite            bool               `json:"allow_write"`
	AllowExec             bool               `json:"allow_exec"`
	Browser               bool               `json:"browser"`
	Skills                []string           `json:"skills,omitempty"`
	AvailableSkills       []string           `json:"available_skills,omitempty"`
	AutoDetectedSkills    []string           `json:"auto_detected_skills,omitempty"`
	AgentsMDDetected      bool               `json:"agents_md_detected,omitempty"`
	MCPConfig             string             `json:"mcp_config,omitempty"`
	DecisionEnabled       bool               `json:"decision_enabled"`
	DecisionKind          string             `json:"decision_kind,omitempty"`
	DecisionModel         string             `json:"decision_model,omitempty"`
	DecisionAPIKeyEnv     string             `json:"decision_api_key_env,omitempty"`
	DecisionMinConfidence float64            `json:"decision_min_confidence,omitempty"`
	DecisionCandidates    map[string]string  `json:"decision_candidates,omitempty"`
	GitHubTokenEnv        string             `json:"github_token_env,omitempty"`
	NotionTokenEnv        string             `json:"notion_token_env,omitempty"`
	NotionPage            string             `json:"notion_page,omitempty"`
	Repos                 []ProjectRepoEntry `json:"repos"`
}

// ProjectCallbacks connects the UI project manager to the persistent project
// registry without coupling the TUI to CLI or store internals.
type ProjectCallbacks struct {
	Scan         func(ctx context.Context) (ProjectStatus, error)
	AddRepo      func(ctx context.Context, alias, path, remote, branch string) (ProjectStatus, error)
	AddAll       func(ctx context.Context) (ProjectStatus, int, error)
	RemoveRepo   func(ctx context.Context, alias string) (ProjectStatus, error)
	UpdateMeta   func(ctx context.Context, name, description string) (ProjectStatus, error)
	SaveSettings func(ctx context.Context, status ProjectStatus) (ProjectStatus, error)
	FetchDetails func(ctx context.Context) (ProjectDetails, error)
}

func WithProject(cb ProjectCallbacks) Option {
	return func(m *Model) {
		m.projectCallbacks = cb
	}
}

func WithInitialProjectStatus(st ProjectStatus) Option {
	return func(m *Model) {
		m.projectStatus = st
	}
}

type projectStatusMsg struct {
	id     uint64
	status ProjectStatus
	notice string
	err    error
}

func (m *Model) hasLinkedProjectRepos() bool {
	for _, r := range m.projectStatus.Repos {
		if r.Linked {
			return true
		}
	}
	return false
}

func (m *Model) leftPaneTitle() string {
	if m.hasLinkedProjectRepos() && m.dir == "" {
		name := strings.TrimSpace(m.projectStatus.ProjectName)
		if name != "" {
			return "PROJECT · " + strings.ToUpper(sanitizeDisplay(name))
		}
		return "PROJECT"
	}
	return "FILES"
}

func (m *Model) linkedRepoForEntry(entryPath string) (ProjectRepoEntry, bool) {
	clean := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(entryPath), "./"), "/")
	if clean == "" {
		return ProjectRepoEntry{}, false
	}
	for _, r := range m.projectStatus.Repos {
		if !r.Linked {
			continue
		}
		rel := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(r.RelPath), "./"), "/")
		if rel == clean || r.Alias == clean {
			return r, true
		}
		if r.Path != "" && m.root != "" {
			if rp, err := filepath.Rel(m.root, r.Path); err == nil && filepath.ToSlash(rp) == clean {
				return r, true
			}
		}
	}
	return ProjectRepoEntry{}, false
}

// formatGitStatsColored renders compact git status badges (+added ~modified -deleted)
// in distinct semantic colors (emerald green for added, amber warn for modified,
// coral danger for deleted, or a quiet ✓ when clean).
func (p uiPalette) formatGitStatsColored(added, modified, deleted int, selected bool) string {
	if added == 0 && modified == 0 && deleted == 0 {
		if !p.enabled() || selected {
			return "✓"
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#4ec994")).Render("✓")
	}
	var parts []string
	if added > 0 {
		s := fmt.Sprintf("+%d", added)
		if p.enabled() && !selected {
			s = lipgloss.NewStyle().Foreground(lipgloss.Color("#56d39a")).Bold(true).Render(s)
		}
		parts = append(parts, s)
	}
	if modified > 0 {
		s := fmt.Sprintf("~%d", modified)
		if p.enabled() && !selected {
			s = p.warnText(s)
		}
		parts = append(parts, s)
	}
	if deleted > 0 {
		s := fmt.Sprintf("-%d", deleted)
		if p.enabled() && !selected {
			s = p.dangerText(s)
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func formatGitStatsPlain(added, modified, deleted int) string {
	if added == 0 && modified == 0 && deleted == 0 {
		return "✓ clean"
	}
	var parts []string
	if added > 0 {
		parts = append(parts, fmt.Sprintf("+%d", added))
	}
	if modified > 0 {
		parts = append(parts, fmt.Sprintf("~%d", modified))
	}
	if deleted > 0 {
		parts = append(parts, fmt.Sprintf("-%d", deleted))
	}
	return strings.Join(parts, " ")
}

func (m *Model) cancelProjectRequest() {
	if m.projectCancel != nil {
		m.projectCancel()
		m.projectCancel = nil
	}
}

func (m *Model) openProjectManager() tea.Cmd {
	m.projectOpen = true
	m.projectEditField = ""
	m.projectEditInput = ""
	m.projectError = ""
	m.focus = focusViewer
	return m.startProjectScan("project repositories scanned")
}

func (m *Model) startProjectScan(notice string) tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	scanFn := m.projectCallbacks.Scan
	root := m.root
	fallback := m.projectStatus
	return func() tea.Msg {
		if scanFn != nil {
			st, err := scanFn(ctx)
			return projectStatusMsg{id: id, status: st, notice: notice, err: err}
		}
		st, err := scanLocalWorkspaceRepos(root, fallback)
		return projectStatusMsg{id: id, status: st, notice: notice, err: err}
	}
}

func (m *Model) startProjectAdd(alias, path, remote, branch string) tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	addFn := m.projectCallbacks.AddRepo
	root := m.root
	current := m.projectStatus
	return func() tea.Msg {
		if addFn != nil {
			st, err := addFn(ctx, alias, path, remote, branch)
			return projectStatusMsg{id: id, status: st, notice: "added repository " + alias, err: err}
		}
		st, err := addLocalWorkspaceRepo(root, current, alias, path, remote, branch)
		return projectStatusMsg{id: id, status: st, notice: "added repository " + alias, err: err}
	}
}

func (m *Model) startProjectAddAll() tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	addAllFn := m.projectCallbacks.AddAll
	root := m.root
	current := m.projectStatus
	return func() tea.Msg {
		if addAllFn != nil {
			st, count, err := addAllFn(ctx)
			notice := fmt.Sprintf("added %d discovered repositories", count)
			if count == 1 {
				notice = "added 1 discovered repository"
			}
			return projectStatusMsg{id: id, status: st, notice: notice, err: err}
		}
		st, count, err := addAllLocalWorkspaceRepos(root, current)
		notice := fmt.Sprintf("added %d discovered repositories", count)
		if count == 1 {
			notice = "added 1 discovered repository"
		}
		return projectStatusMsg{id: id, status: st, notice: notice, err: err}
	}
}

func (m *Model) startProjectRemove(alias string) tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	removeFn := m.projectCallbacks.RemoveRepo
	root := m.root
	current := m.projectStatus
	return func() tea.Msg {
		if removeFn != nil {
			st, err := removeFn(ctx, alias)
			return projectStatusMsg{id: id, status: st, notice: "removed repository " + alias, err: err}
		}
		st, err := removeLocalWorkspaceRepo(root, current, alias)
		return projectStatusMsg{id: id, status: st, notice: "removed repository " + alias, err: err}
	}
}

func (m *Model) startProjectMetaUpdate(name, description, notice string) tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	metaFn := m.projectCallbacks.UpdateMeta
	root := m.root
	current := m.projectStatus
	return func() tea.Msg {
		if metaFn != nil {
			st, err := metaFn(ctx, name, description)
			return projectStatusMsg{id: id, status: st, notice: notice, err: err}
		}
		st, err := updateLocalWorkspaceMeta(root, current, name, description)
		return projectStatusMsg{id: id, status: st, notice: notice, err: err}
	}
}

func (m *Model) startProjectSettingsSave(next ProjectStatus, notice string) tea.Cmd {
	m.cancelProjectRequest()
	m.projectLoading = true
	m.projectError = ""
	m.projectID++
	id := m.projectID
	ctx, cancel := context.WithCancel(context.Background())
	m.projectCancel = cancel
	saveFn := m.projectCallbacks.SaveSettings
	metaFn := m.projectCallbacks.UpdateMeta
	return func() tea.Msg {
		if saveFn != nil {
			st, err := saveFn(ctx, next)
			return projectStatusMsg{id: id, status: st, notice: notice, err: err}
		}
		if metaFn != nil {
			st, err := metaFn(ctx, next.ProjectName, next.ProjectDescription)
			if err == nil {
				st.AllowWrite = next.AllowWrite
				st.AllowExec = next.AllowExec
				st.Browser = next.Browser
				st.Provider = next.Provider
				st.Environment = next.Environment
				st.Skills = next.Skills
				st.MCPConfig = next.MCPConfig
			}
			return projectStatusMsg{id: id, status: st, notice: notice, err: err}
		}
		return projectStatusMsg{id: id, status: next, notice: notice}
	}
}

func (m *Model) updateProjectStatus(msg projectStatusMsg) tea.Cmd {
	if msg.id != m.projectID {
		return nil
	}
	m.projectLoading = false
	m.projectCancel = nil
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return nil
		}
		m.projectError = sanitizeDisplay(msg.err.Error())
		m.status = "project error: " + m.projectError
		return nil
	}
	m.projectError = ""
	m.projectStatus = msg.status
	m.invalidateChatMentionIndex()
	if m.projectSelected >= len(m.projectStatus.Repos) {
		m.projectSelected = maxInt(len(m.projectStatus.Repos)-1, 0)
	}
	if m.projectSelected < 0 {
		m.projectSelected = 0
	}
	if msg.notice != "" {
		m.status = msg.notice
	}
	return nil
}

func (m *Model) handleProjectKey(key, text string) tea.Cmd {
	if m.projectEditField != "" {
		switch key {
		case "esc", "escape":
			m.projectEditField = ""
			m.projectEditInput = ""
			m.status = "edit canceled"
			return nil
		case "enter", "return":
			field := m.projectEditField
			value := strings.TrimSpace(m.projectEditInput)
			m.projectEditField = ""
			m.projectEditInput = ""
			switch field {
			case "name":
				if value == "" {
					m.projectError = "project name cannot be empty"
					return nil
				}
				next := m.projectStatus
				next.ProjectName = value
				if m.projectSettingsOpen {
					return m.startProjectSettingsSave(next, "project renamed to "+value)
				}
				return m.startProjectMetaUpdate(value, m.projectStatus.ProjectDescription, "project renamed to "+value)
			case "description":
				next := m.projectStatus
				next.ProjectDescription = value
				if m.projectSettingsOpen {
					return m.startProjectSettingsSave(next, "project description updated")
				}
				return m.startProjectMetaUpdate(m.projectStatus.ProjectName, value, "project description updated")
			case "provider":
				next := m.projectStatus
				next.Provider = value
				return m.startProjectSettingsSave(next, "project provider set to "+valueOr(value, "default"))
			case "environment":
				next := m.projectStatus
				if value == "" {
					value = "local"
				}
				next.Environment = value
				return m.startProjectSettingsSave(next, "project environment set to "+value)
			case "skills":
				next := m.projectStatus
				var skills []string
				for _, part := range strings.Split(value, ",") {
					if s := strings.TrimSpace(part); s != "" {
						skills = append(skills, s)
					}
				}
				next.Skills = skills
				return m.startProjectSettingsSave(next, "project skills updated")
			case "mcp_config":
				next := m.projectStatus
				next.MCPConfig = value
				return m.startProjectSettingsSave(next, "project MCP config updated")
			case "decision_model":
				next := m.projectStatus
				if value == "" {
					value = "~typesafe/jev-latest"
				}
				next.DecisionModel = value
				return m.startProjectSettingsSave(next, "decision model set to "+value)
			case "decision_api_key_env":
				next := m.projectStatus
				if value == "" {
					value = "OPENROUTER_API_KEY"
				}
				next.DecisionAPIKeyEnv = value
				return m.startProjectSettingsSave(next, "decision API key env set to "+value)
			case "decision_min_confidence":
				next := m.projectStatus
				f, err := strconv.ParseFloat(value, 64)
				if err != nil || f < 0 || f > 1 {
					m.projectError = "decision min_confidence must be between 0.0 and 1.0"
					return nil
				}
				next.DecisionMinConfidence = f
				return m.startProjectSettingsSave(next, fmt.Sprintf("decision min confidence set to %.2f", f))
			case "decision_candidates":
				next := m.projectStatus
				candidates := make(map[string]string)
				for _, entry := range strings.Split(value, ",") {
					entry = strings.TrimSpace(entry)
					if entry == "" {
						continue
					}
					if name, desc, ok := strings.Cut(entry, "="); ok {
						name = strings.TrimSpace(name)
						desc = strings.TrimSpace(desc)
						if name != "" {
							if desc == "" {
								desc = "general coding and reasoning profile"
							}
							candidates[name] = desc
						}
					} else {
						candidates[entry] = "general coding and reasoning profile"
					}
				}
				next.DecisionCandidates = candidates
				return m.startProjectSettingsSave(next, "decision candidate profiles updated")
			case "github_token_env":
				next := m.projectStatus
				next.GitHubTokenEnv = value
				return m.startProjectSettingsSave(next, "GitHub connector token env set to "+valueOr(value, "default"))
			case "notion_token_env":
				next := m.projectStatus
				next.NotionTokenEnv = value
				return m.startProjectSettingsSave(next, "Notion connector token env set to "+valueOr(value, "default"))
			case "notion_page":
				next := m.projectStatus
				next.NotionPage = value
				return m.startProjectSettingsSave(next, "Notion page/query updated")
			}
			return nil
		case "backspace", "delete":
			last := 0
			for offset := 0; offset < len(m.projectEditInput); {
				last = offset
				cluster, _ := ansi.FirstGraphemeCluster(m.projectEditInput[offset:], ansi.GraphemeWidth)
				offset += len(cluster)
			}
			m.projectEditInput = m.projectEditInput[:last]
			return nil
		default:
			if text != "" && len([]rune(m.projectEditInput))+len([]rune(text)) <= 160 {
				m.projectEditInput += sanitizeDisplay(text)
			}
			return nil
		}
	}

	if m.projectDetailsOpen {
		return m.handleProjectDetailsKey(key)
	}
	if m.projectSettingsOpen {
		return m.handleProjectSettingsKey(key)
	}

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
	case "esc", "escape", "q":
		m.cancelProjectRequest()
		m.projectOpen = false
		m.projectSettingsOpen = false
		m.projectDetailsOpen = false
		m.projectLoading = false
		m.projectError = ""
		m.normalizeFocus()
		m.status = "project manager closed"
		return nil
	case "i", "g", "p", "v":
		return m.openProjectDetailsModal(0)
	case "1":
		return m.openProjectDetailsModal(0)
	case "2":
		return m.openProjectDetailsModal(1)
	case "3":
		return m.openProjectDetailsModal(2)
	case "4":
		return m.openProjectDetailsModal(3)
	case "5":
		return m.openProjectDetailsModal(4)
	case "s", "c", "tab":
		m.projectSettingsOpen = true
		m.projectSettingsSelected = 0
		m.projectError = ""
		m.status = "project settings: j/k select · Space/Enter toggle or edit · Esc back"
		return nil
	case "n", "N":
		m.projectEditField = "name"
		m.projectEditInput = m.projectStatus.ProjectName
		if m.projectEditInput == "" {
			m.projectEditInput = store.SanitizeAlias(filepath.Base(m.root))
		}
		m.projectError = ""
		m.status = "editing project name (Enter to save, Esc to cancel)"
		return nil
	case "D", "m":
		m.projectEditField = "description"
		m.projectEditInput = m.projectStatus.ProjectDescription
		m.projectError = ""
		m.status = "editing project description (Enter to save, Esc to cancel)"
		return nil
	case "j", "down":
		if m.projectSelected < len(m.projectStatus.Repos)-1 {
			m.projectSelected++
		}
		return nil
	case "k", "up":
		if m.projectSelected > 0 {
			m.projectSelected--
		}
		return nil
	case "r":
		return m.startProjectScan("rescanned workspace repositories")
	case "A":
		return m.startProjectAddAll()
	case "enter", "a":
		if m.projectSelected >= 0 && m.projectSelected < len(m.projectStatus.Repos) {
			entry := m.projectStatus.Repos[m.projectSelected]
			return m.startProjectAdd(entry.Alias, entry.Path, entry.Remote, entry.DefaultBranch)
		}
		return nil
	case "d", "x", "delete", "backspace":
		if m.projectSelected >= 0 && m.projectSelected < len(m.projectStatus.Repos) {
			entry := m.projectStatus.Repos[m.projectSelected]
			if !entry.Linked {
				m.projectError = fmt.Sprintf("repository %q is not linked to the project yet", entry.Alias)
				m.status = m.projectError
				return nil
			}
			return m.startProjectRemove(entry.Alias)
		}
		return nil
	}
	return nil
}

func (m *Model) renderProjectPanel(width, height int) string {
	p := m.colors()
	var b strings.Builder

	projName := strings.TrimSpace(m.projectStatus.ProjectName)
	if projName == "" {
		projName = store.SanitizeAlias(filepath.Base(m.root))
	}
	projDesc := strings.TrimSpace(m.projectStatus.ProjectDescription)
	linkedCount, discoveredCount := 0, 0
	totalAdded, totalModified, totalDeleted := 0, 0, 0
	for _, repo := range m.projectStatus.Repos {
		if repo.Linked {
			linkedCount++
		}
		if repo.Discovered {
			discoveredCount++
		}
		totalAdded += repo.GitAdded
		totalModified += repo.GitModified
		totalDeleted += repo.GitDeleted
	}

	titleLine := fmt.Sprintf("PROJECT  ·  %s", projName)
	b.WriteString(p.violetText(titleLine))
	if projDesc != "" {
		b.WriteString(p.mutedText("   —   " + sanitizeDisplay(projDesc)))
	} else {
		b.WriteString(p.quietText("   (press D to set a project description, n to rename)"))
	}
	b.WriteByte('\n')

	summary := fmt.Sprintf("Root: %s   ·   %d linked   ·   %d discovered   ·   Git: ", sanitizeDisplay(m.root), linkedCount, discoveredCount)
	b.WriteString(p.mutedText(summary))
	b.WriteString(p.formatGitStatsColored(totalAdded, totalModified, totalDeleted, false))
	b.WriteByte('\n')

	actions := "Keys: j/k select · i/g GitHub & Notion details (PRs/issues/README) · Enter/a link · A link all · d/x unlink · s settings · r rescan · Esc close"
	b.WriteString(p.cyanText(actions))
	b.WriteString("\n")

	if m.projectEditField != "" {
		label := "Project Name"
		switch m.projectEditField {
		case "description":
			label = "Project Description"
		case "provider":
			label = "Default Provider Profile"
		case "environment":
			label = "Default Environment"
		case "skills":
			label = "Active Project Skills (comma-separated)"
		case "mcp_config":
			label = "MCP Config File Path"
		case "github_token_env":
			label = "GitHub Token Env Variable"
		case "notion_token_env":
			label = "Notion Token Env Variable"
		case "notion_page":
			label = "Linked Notion Page ID / Query"
		}
		b.WriteString("\n")
		b.WriteString(p.accentText(fmt.Sprintf("✎ %s: ", label)) + sanitizeDisplay(m.projectEditInput) + p.cyanText("▏") + p.quietText("  (Enter save · Esc cancel)"))
		b.WriteString("\n")
	}
	b.WriteByte('\n')

	if m.projectDetailsOpen {
		return m.renderProjectDetailsModal(width, height, b.String())
	}
	if m.projectSettingsOpen {
		return m.renderProjectSettingsModal(width, height, b.String())
	}

	if m.projectLoading {
		b.WriteString(p.mutedText("Scanning workspace for Git and GitHub repositories..."))
		b.WriteByte('\n')
	}
	if m.projectError != "" {
		b.WriteString(p.dangerText("Error: " + m.projectError))
		b.WriteString("\n\n")
	}

	if len(m.projectStatus.Repos) == 0 && !m.projectLoading {
		b.WriteString(p.mutedText("No Git repositories discovered in this workspace directory."))
		b.WriteByte('\n')
		b.WriteString(p.mutedText("Use :project add ALIAS OWNER/REPO to link a remote repository manually."))
	} else {
		innerW := maxInt(width-6, 36)
		header := fmt.Sprintf("   %-10s %-18s %-16s %-26s %-10s %s", "STATUS", "ALIAS", "PATH", "GITHUB / REMOTE", "BRANCH", "GIT (+A ~M -D)")
		b.WriteString(p.mutedText(ansi.Truncate(header, innerW, "…")))
		b.WriteByte('\n')
		b.WriteString(p.quietText(strings.Repeat("─", minInt(innerW, 108))))
		b.WriteByte('\n')

		for i, repo := range m.projectStatus.Repos {
			prefix := "  "
			if i == m.projectSelected {
				prefix = "› "
			}
			statusBadge := "[DISCOVER]"
			if repo.Linked {
				statusBadge = "[LINKED]  "
			}
			relPath := repo.RelPath
			if relPath == "" {
				if repo.Path != "" {
					if rel, err := filepath.Rel(m.root, repo.Path); err == nil {
						relPath = filepath.ToSlash(rel)
					} else {
						relPath = repo.Path
					}
				} else {
					relPath = "-"
				}
			}
			if relPath != "." && !strings.HasPrefix(relPath, "/") && !strings.HasPrefix(relPath, "./") && relPath != "-" {
				relPath = "./" + relPath
			}

			remoteLabel := repo.GitHubSlug
			if remoteLabel == "" && repo.Remote != "" {
				remoteLabel = repo.Remote
			}
			if remoteLabel == "" {
				remoteLabel = "(local git only)"
			}
			branch := repo.CurrentBranch
			if branch == "" {
				branch = repo.DefaultBranch
			}
			if branch == "" {
				branch = "main"
			}

			selected := i == m.projectSelected
			baseCols := fmt.Sprintf("%s%-10s %-18s %-16s %-26s %-10s ",
				prefix,
				statusBadge,
				truncateCell(sanitizeDisplay(repo.Alias), 18),
				truncateCell(sanitizeDisplay(relPath), 16),
				truncateCell(sanitizeDisplay(remoteLabel), 26),
				truncateCell(sanitizeDisplay(branch), 10),
			)
			if selected {
				plainGit := formatGitStatsPlain(repo.GitAdded, repo.GitModified, repo.GitDeleted)
				rowText := ansi.Truncate(baseCols+plainGit, innerW, "…")
				b.WriteString(p.accentText(truncatePad(rowText, innerW)))
			} else {
				styledBase := baseCols
				if repo.Linked {
					styledBase = p.cyanText(baseCols)
				}
				gitBadge := p.formatGitStatsColored(repo.GitAdded, repo.GitModified, repo.GitDeleted, false)
				rowText := ansi.Truncate(styledBase+gitBadge, innerW, "…")
				b.WriteString(rowText)
			}
			b.WriteByte('\n')
		}
	}

	b.WriteString("\n")
	b.WriteString(p.mutedText("Commands: :project name <name>  ·  :project description <text>  ·  :project add-all  ·  :project remove <alias>"))

	return renderPanelTheme(m.theme, "PROJECT  ·  GITHUB REPOSITORIES", b.String(), width, true, height)
}

func truncateCell(s string, maxW int) string {
	if ansi.StringWidth(s) <= maxW {
		return s
	}
	return ansi.Truncate(s, maxW, "…")
}

func scanLocalWorkspaceRepos(root string, existing ProjectStatus) (ProjectStatus, error) {
	discovered, err := store.ScanGitRepositories(root)
	if err != nil {
		return ProjectStatus{}, err
	}
	projName := existing.ProjectName
	if projName == "" {
		projName = store.SanitizeAlias(filepath.Base(root))
	}
	linkedByAlias := make(map[string]ProjectRepoEntry, len(existing.Repos))
	linkedByPath := make(map[string]ProjectRepoEntry, len(existing.Repos))
	for _, r := range existing.Repos {
		if r.Linked {
			linkedByAlias[r.Alias] = r
			if r.Path != "" {
				linkedByPath[filepath.Clean(r.Path)] = r
			}
		}
	}

	seenAliases := make(map[string]bool)
	var merged []ProjectRepoEntry
	for _, d := range discovered {
		entry := ProjectRepoEntry{
			Alias:         d.Alias,
			Path:          d.Path,
			RelPath:       d.RelPath,
			Remote:        d.Remote,
			GitHubSlug:    d.GitHubSlug,
			DefaultBranch: d.DefaultBranch,
			CurrentBranch: d.CurrentBranch,
			GitAdded:      d.GitAdded,
			GitModified:   d.GitModified,
			GitDeleted:    d.GitDeleted,
			Discovered:    true,
		}
		if linked, ok := linkedByPath[filepath.Clean(d.Path)]; ok {
			entry.Linked = true
			entry.Alias = linked.Alias
			if linked.Remote != "" {
				entry.Remote = linked.Remote
				if o, r, _, isGH := store.ParseGitHubRemote(linked.Remote); isGH {
					entry.GitHubSlug = o + "/" + r
				}
			}
		} else if linked, ok := linkedByAlias[d.Alias]; ok {
			entry.Linked = true
			if linked.Remote != "" {
				entry.Remote = linked.Remote
				if o, r, _, isGH := store.ParseGitHubRemote(linked.Remote); isGH {
					entry.GitHubSlug = o + "/" + r
				}
			}
		}
		seenAliases[entry.Alias] = true
		merged = append(merged, entry)
	}
	for _, r := range existing.Repos {
		if r.Linked && !seenAliases[r.Alias] {
			if r.Path != "" {
				r.GitAdded, r.GitModified, r.GitDeleted = store.InspectGitWorktreeCounts(r.Path)
			}
			merged = append(merged, r)
		}
	}
	return ProjectStatus{
		ProjectName:        projName,
		ProjectDescription: existing.ProjectDescription,
		ProjectID:          existing.ProjectID,
		Root:               root,
		Environment:        existing.Environment,
		Repos:              merged,
	}, nil
}

func updateLocalWorkspaceMeta(root string, current ProjectStatus, name, description string) (ProjectStatus, error) {
	st, err := scanLocalWorkspaceRepos(root, current)
	if err != nil {
		st = current
	}
	name = strings.TrimSpace(name)
	if name != "" {
		if err := domain.ValidateName(name); err != nil {
			return st, err
		}
		st.ProjectName = name
	}
	st.ProjectDescription = strings.TrimSpace(description)
	return st, nil
}

func addLocalWorkspaceRepo(root string, current ProjectStatus, alias, path, remote, branch string) (ProjectStatus, error) {
	st, err := scanLocalWorkspaceRepos(root, current)
	if err != nil {
		st = current
	}
	alias = strings.TrimSpace(alias)
	if alias == "" && path != "" {
		alias = store.SanitizeAlias(filepath.Base(path))
	}
	if alias == "" {
		return st, errors.New("repository alias is required")
	}
	if o, r, canonical, ok := store.ParseGitHubRemote(remote); ok {
		remote = canonical
		_ = o
		_ = r
	}
	found := false
	for i := range st.Repos {
		if st.Repos[i].Alias == alias || (path != "" && st.Repos[i].Path == path) {
			st.Repos[i].Linked = true
			if remote != "" {
				st.Repos[i].Remote = remote
				if o, r, _, ok := store.ParseGitHubRemote(remote); ok {
					st.Repos[i].GitHubSlug = o + "/" + r
				}
			}
			if branch != "" {
				st.Repos[i].DefaultBranch = branch
			}
			found = true
			break
		}
	}
	if !found {
		if path == "" {
			candidate := filepath.Join(root, alias)
			if info, err := filepath.Abs(candidate); err == nil {
				path = info
			} else {
				path = root
			}
		}
		var slug string
		if o, r, _, ok := store.ParseGitHubRemote(remote); ok {
			slug = o + "/" + r
		}
		if branch == "" {
			branch = "main"
		}
		added, modified, deleted := store.InspectGitWorktreeCounts(path)
		st.Repos = append(st.Repos, ProjectRepoEntry{
			Alias:         alias,
			Path:          path,
			Remote:        remote,
			GitHubSlug:    slug,
			DefaultBranch: branch,
			CurrentBranch: branch,
			GitAdded:      added,
			GitModified:   modified,
			GitDeleted:    deleted,
			Linked:        true,
		})
	}
	return st, nil
}

func addAllLocalWorkspaceRepos(root string, current ProjectStatus) (ProjectStatus, int, error) {
	st, err := scanLocalWorkspaceRepos(root, current)
	if err != nil {
		return current, 0, err
	}
	added := 0
	for i := range st.Repos {
		if !st.Repos[i].Linked && st.Repos[i].Discovered {
			st.Repos[i].Linked = true
			added++
		}
	}
	return st, added, nil
}

func removeLocalWorkspaceRepo(root string, current ProjectStatus, alias string) (ProjectStatus, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return current, errors.New("repository alias is required")
	}
	found := false
	next := make([]ProjectRepoEntry, 0, len(current.Repos))
	for _, r := range current.Repos {
		if r.Alias == alias && r.Linked {
			found = true
			if r.Discovered {
				r.Linked = false
				next = append(next, r)
			}
			continue
		}
		next = append(next, r)
	}
	if !found {
		return current, fmt.Errorf("repository %q is not linked", alias)
	}
	current.Repos = next
	return current, nil
}

func (m *Model) projectPaletteSuggestions(input, verb string) []paletteSuggestion {
	fields := strings.Fields(strings.TrimSpace(input))
	prefix := ""
	if len(fields) > 2 {
		prefix = strings.ToLower(fields[2])
	}
	repos := m.projectStatus.Repos
	if len(repos) == 0 && m.root != "" {
		if st, err := scanLocalWorkspaceRepos(m.root, m.projectStatus); err == nil {
			repos = st.Repos
		}
	}
	var items []paletteSuggestion
	for _, repo := range repos {
		if repo.Alias == "" {
			continue
		}
		if verb == "remove" || verb == "rm" {
			if !repo.Linked {
				continue
			}
		}
		if prefix != "" && !strings.HasPrefix(strings.ToLower(repo.Alias), prefix) {
			continue
		}
		canonicalVerb := verb
		if canonicalVerb == "rm" {
			canonicalVerb = "remove"
		}
		helpText := "add repository to project"
		if canonicalVerb == "remove" {
			helpText = "remove repository from project"
		} else if repo.GitHubSlug != "" {
			helpText = "add " + repo.GitHubSlug
		}
		items = append(items, paletteSuggestion{
			text: "project " + canonicalVerb + " " + repo.Alias,
			help: helpText,
		})
	}
	if len(items) == 0 {
		canonicalVerb := verb
		if canonicalVerb == "rm" {
			canonicalVerb = "remove"
		}
		return []paletteSuggestion{{text: "project " + canonicalVerb + " ", help: canonicalVerb + " repository by alias"}}
	}
	return items
}

func (m *Model) executeProjectCommand(args []string, usage func() tea.Cmd) tea.Cmd {
	if len(args) == 0 {
		return m.openProjectManager()
	}
	sub := strings.ToLower(strings.TrimSpace(args[0]))
	switch sub {
	case "scan", "list", "browse":
		if len(args) != 1 {
			return usage()
		}
		m.projectSettingsOpen = false
		m.projectDetailsOpen = false
		return m.openProjectManager()
	case "details", "info", "github", "gh":
		return m.openProjectDetailsModal(0)
	case "readme":
		return m.openProjectDetailsModal(1)
	case "prs", "pr", "pulls":
		return m.openProjectDetailsModal(2)
	case "issues", "issue":
		return m.openProjectDetailsModal(3)
	case "notion", "connectors":
		return m.openProjectDetailsModal(4)
	case "settings", "config", "options":
		if len(args) != 1 {
			return usage()
		}
		m.projectSettingsOpen = true
		m.projectDetailsOpen = false
		m.projectSettingsSelected = 0
		return m.openProjectManager()
	case "name", "rename":
		if len(args) == 1 {
			m.projectOpen = true
			m.focus = focusViewer
			m.projectEditField = "name"
			m.projectEditInput = m.projectStatus.ProjectName
			return nil
		}
		if len(args) != 2 {
			return usage()
		}
		newName := strings.TrimSpace(args[1])
		return m.startProjectMetaUpdate(newName, m.projectStatus.ProjectDescription, "project renamed to "+newName)
	case "description", "desc", "describe":
		if len(args) == 1 {
			m.projectOpen = true
			m.focus = focusViewer
			m.projectEditField = "description"
			m.projectEditInput = m.projectStatus.ProjectDescription
			return nil
		}
		desc := strings.TrimSpace(strings.Join(args[1:], " "))
		return m.startProjectMetaUpdate(m.projectStatus.ProjectName, desc, "project description updated")
	case "add-all":
		if len(args) != 1 {
			return usage()
		}
		m.projectOpen = true
		m.focus = focusViewer
		return m.startProjectAddAll()
	case "add", "link":
		if len(args) == 1 {
			return m.openProjectManager()
		}
		if len(args) > 3 {
			return usage()
		}
		target := strings.TrimSpace(args[1])
		var remote string
		if len(args) == 3 {
			remote = strings.TrimSpace(args[2])
		}
		var path, branch string
		alias := target
		repos := m.projectStatus.Repos
		if len(repos) == 0 && m.root != "" {
			if st, err := scanLocalWorkspaceRepos(m.root, m.projectStatus); err == nil {
				m.projectStatus = st
				repos = st.Repos
			}
		}
		for _, r := range repos {
			if r.Alias == target || r.RelPath == strings.TrimPrefix(target, "./") || r.Path == target {
				alias = r.Alias
				path = r.Path
				if remote == "" {
					remote = r.Remote
				}
				branch = r.DefaultBranch
				break
			}
		}
		if path == "" {
			candidate := target
			if !filepath.IsAbs(candidate) {
				candidate = filepath.Join(m.root, candidate)
			}
			alias = store.SanitizeAlias(filepath.Base(target))
			path = candidate
		}
		m.projectOpen = true
		m.focus = focusViewer
		return m.startProjectAdd(alias, path, remote, branch)
	case "remove", "rm", "remove-repo", "unlink":
		if len(args) != 2 {
			return usage()
		}
		m.projectOpen = true
		m.focus = focusViewer
		return m.startProjectRemove(strings.TrimSpace(args[1]))
	default:
		return usage()
	}
}

const projectSettingsRowCount = 15

func formatDecisionCandidatesShort(c map[string]string) string {
	if len(c) == 0 {
		return "(auto from accounts)"
	}
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func formatDecisionCandidatesEdit(c map[string]string, available []string) string {
	if len(c) == 0 {
		var parts []string
		for i, p := range available {
			if i == 0 {
				parts = append(parts, p+"=fast routine coding")
			} else {
				parts = append(parts, p+"=complex architecture & review")
			}
		}
		return strings.Join(parts, ", ")
	}
	names := make([]string, 0, len(c))
	for k := range c {
		names = append(names, k)
	}
	sort.Strings(names)
	var parts []string
	for _, k := range names {
		parts = append(parts, k+"="+c[k])
	}
	return strings.Join(parts, ", ")
}

func (m *Model) handleProjectSettingsKey(key string) tea.Cmd {
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
	case "esc", "escape", "s", "tab", "q":
		m.projectSettingsOpen = false
		m.status = "returned to project repositories"
		return nil
	case "j", "down":
		m.projectSettingsSelected = (m.projectSettingsSelected + 1) % projectSettingsRowCount
		return nil
	case "k", "up":
		m.projectSettingsSelected = (m.projectSettingsSelected - 1 + projectSettingsRowCount) % projectSettingsRowCount
		return nil
	case "left":
		if m.projectSettingsSelected == 5 {
			return m.cycleProjectProvider(-1)
		}
		if m.projectSettingsSelected == 10 {
			return m.cycleDecisionKind()
		}
	case "right":
		if m.projectSettingsSelected == 5 {
			return m.cycleProjectProvider(1)
		}
		if m.projectSettingsSelected == 10 {
			return m.cycleDecisionKind()
		}
	case "space", "enter", "return":
		switch m.projectSettingsSelected {
		case 0:
			m.projectEditField = "name"
			m.projectEditInput = m.projectStatus.ProjectName
			if m.projectEditInput == "" {
				m.projectEditInput = store.SanitizeAlias(filepath.Base(m.root))
			}
			m.status = "editing project name (Enter to save, Esc to cancel)"
			return nil
		case 1:
			m.projectEditField = "description"
			m.projectEditInput = m.projectStatus.ProjectDescription
			m.status = "editing project description (Enter to save, Esc to cancel)"
			return nil
		case 2:
			next := m.projectStatus
			next.AllowWrite = !next.AllowWrite
			state := "disabled"
			if next.AllowWrite {
				state = "enabled"
			}
			return m.startProjectSettingsSave(next, "allow_write "+state)
		case 3:
			next := m.projectStatus
			next.AllowExec = !next.AllowExec
			state := "disabled"
			if next.AllowExec {
				state = "enabled"
			}
			return m.startProjectSettingsSave(next, "allow_exec "+state)
		case 4:
			next := m.projectStatus
			next.Browser = !next.Browser
			state := "disabled"
			if next.Browser {
				state = "enabled"
			}
			return m.startProjectSettingsSave(next, "browser automation "+state)
		case 5:
			if key == "space" {
				return m.cycleProjectProvider(1)
			}
			m.projectEditField = "provider"
			m.projectEditInput = m.projectStatus.Provider
			m.status = "editing project default provider (Enter to save, Esc to cancel)"
			return nil
		case 6:
			m.projectEditField = "environment"
			m.projectEditInput = valueOr(m.projectStatus.Environment, "local")
			m.status = "editing project environment (Enter to save, Esc to cancel)"
			return nil
		case 7:
			m.projectEditField = "skills"
			m.projectEditInput = strings.Join(m.projectStatus.Skills, ", ")
			m.status = "editing active project skills (comma-separated, Enter to save)"
			return nil
		case 8:
			m.projectEditField = "mcp_config"
			m.projectEditInput = m.projectStatus.MCPConfig
			m.status = "editing project MCP config path (Enter to save, Esc to cancel)"
			return nil
		case 9:
			next := m.projectStatus
			next.DecisionEnabled = !next.DecisionEnabled
			if next.DecisionEnabled {
				if next.DecisionKind == "" {
					next.DecisionKind = "openrouter"
				}
				if next.DecisionModel == "" {
					next.DecisionModel = "~typesafe/jev-latest"
				}
				if next.DecisionAPIKeyEnv == "" {
					if next.DecisionKind == "typesafe" {
						next.DecisionAPIKeyEnv = "TYPESAFE_API_KEY"
					} else {
						next.DecisionAPIKeyEnv = "OPENROUTER_API_KEY"
					}
				}
				if next.DecisionMinConfidence <= 0 {
					next.DecisionMinConfidence = 0.80
				}
				if len(next.DecisionCandidates) < 2 {
					cands := make(map[string]string)
					for k, v := range next.DecisionCandidates {
						cands[k] = v
					}
					for _, p := range next.AvailableProviders {
						if _, ok := cands[p]; !ok {
							if len(cands) == 0 {
								cands[p] = "fast routine coding and inspection"
							} else {
								cands[p] = "complex reasoning, architecture, and high-risk work"
							}
						}
					}
					next.DecisionCandidates = cands
				}
			}
			state := "disabled"
			if next.DecisionEnabled {
				state = "enabled"
			}
			return m.startProjectSettingsSave(next, "decision engine "+state)
		case 10:
			return m.cycleDecisionKind()
		case 11:
			m.projectEditField = "decision_model"
			m.projectEditInput = valueOr(m.projectStatus.DecisionModel, "~typesafe/jev-latest")
			m.status = "editing decision router model (Enter to save, Esc to cancel)"
			return nil
		case 12:
			m.projectEditField = "decision_api_key_env"
			m.projectEditInput = valueOr(m.projectStatus.DecisionAPIKeyEnv, "OPENROUTER_API_KEY")
			m.status = "editing decision API key environment variable (Enter to save)"
			return nil
		case 13:
			m.projectEditField = "decision_min_confidence"
			conf := m.projectStatus.DecisionMinConfidence
			if conf <= 0 {
				conf = 0.80
			}
			m.projectEditInput = fmt.Sprintf("%.2f", conf)
			m.status = "editing decision min confidence 0.0..1.0 (Enter to save)"
			return nil
		case 14:
			m.projectEditField = "decision_candidates"
			m.projectEditInput = formatDecisionCandidatesEdit(m.projectStatus.DecisionCandidates, m.projectStatus.AvailableProviders)
			m.status = "editing decision candidates (profile=description, comma-separated)"
			return nil
		}
	}
	return nil
}

func (m *Model) cycleDecisionKind() tea.Cmd {
	next := m.projectStatus
	if next.DecisionKind == "typesafe" {
		next.DecisionKind = "openrouter"
		if next.DecisionAPIKeyEnv == "" || next.DecisionAPIKeyEnv == "TYPESAFE_API_KEY" {
			next.DecisionAPIKeyEnv = "OPENROUTER_API_KEY"
		}
	} else {
		next.DecisionKind = "typesafe"
		if next.DecisionAPIKeyEnv == "" || next.DecisionAPIKeyEnv == "OPENROUTER_API_KEY" {
			next.DecisionAPIKeyEnv = "TYPESAFE_API_KEY"
		}
	}
	return m.startProjectSettingsSave(next, "decision engine backend set to "+next.DecisionKind)
}

func (m *Model) cycleProjectProvider(delta int) tea.Cmd {
	options := append([]string{""}, m.projectStatus.AvailableProviders...)
	if len(options) <= 1 && len(m.accounts) > 0 {
		for _, a := range m.accounts {
			options = append(options, a.ID)
		}
	}
	if len(options) <= 1 {
		m.status = "no provider profiles configured yet — use :accounts to add one"
		return nil
	}
	curIdx := 0
	for i, opt := range options {
		if opt == m.projectStatus.Provider {
			curIdx = i
			break
		}
	}
	nextIdx := (curIdx + delta + len(options)) % len(options)
	next := m.projectStatus
	next.Provider = options[nextIdx]
	return m.startProjectSettingsSave(next, "project provider set to "+valueOr(next.Provider, "(global default)"))
}

func (m *Model) renderProjectSettingsModal(width, height int, topHeader string) string {
	p := m.colors()
	var b strings.Builder
	b.WriteString(topHeader)
	b.WriteString(p.accentText("⚙  PROJECT SETTINGS & CAPABILITIES") + "   " + p.quietText("(j/k select · Space/Enter toggle or edit · s/Esc back to repos)") + "\n\n")

	if m.projectError != "" {
		b.WriteString(p.dangerText("Error: "+m.projectError) + "\n\n")
	}

	boolBadge := func(on bool) string {
		if on {
			if p.enabled() {
				return lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(lipgloss.Color("#56d39a")).Bold(true).Render(" ON ")
			}
			return "[ON]"
		}
		if p.enabled() {
			return lipgloss.NewStyle().Foreground(p.muted).Background(lipgloss.Color("#252b3d")).Render(" OFF ")
		}
		return "[OFF]"
	}

	autoInfo := "AGENTS.md: none"
	if m.projectStatus.AgentsMDDetected {
		autoInfo = "AGENTS.md: auto-loaded ✓"
	}
	if len(m.projectStatus.AutoDetectedSkills) > 0 {
		autoInfo += "  ·  Auto-detected skills: " + strings.Join(m.projectStatus.AutoDetectedSkills, ", ")
	}
	b.WriteString(p.okText("✦  "+autoInfo) + "\n\n")

	conf := m.projectStatus.DecisionMinConfidence
	if conf <= 0 {
		conf = 0.80
	}
	rows := []struct {
		label string
		value string
		hint  string
	}{
		{"Project Name", valueOr(m.projectStatus.ProjectName, store.SanitizeAlias(filepath.Base(m.root))), "Enter to rename project"},
		{"Description", valueOr(m.projectStatus.ProjectDescription, "(none)"), "Enter to set project description"},
		{"Allow File Writes", boolBadge(m.projectStatus.AllowWrite), "Space/Enter to toggle agent file creation & editing"},
		{"Allow Command Exec", boolBadge(m.projectStatus.AllowExec), "Space/Enter to toggle agent shell/command execution"},
		{"Browser Automation", boolBadge(m.projectStatus.Browser), "Space/Enter to toggle Chromium browser tools"},
		{"Default Provider", valueOr(m.projectStatus.Provider, "(global default)"), "Space/←/→ cycle profile · Enter edit"},
		{"Default Environment", valueOr(m.projectStatus.Environment, "local"), "Enter to set local or SSH environment name"},
		{"Active Skills", valueOr(strings.Join(m.projectStatus.Skills, ", "), "(auto)"), "Enter to set explicit skills (auto-detected always load)"},
		{"MCP Config File", valueOr(m.projectStatus.MCPConfig, "(none)"), "Enter to set project MCP JSON path"},
		{"Decision Engine", boolBadge(m.projectStatus.DecisionEnabled), "Space/Enter to toggle bounded Jev/OpenRouter routing"},
		{"Decision Backend", fmt.Sprintf("◀ %s ▶", valueOr(m.projectStatus.DecisionKind, "openrouter")), "Space/←/→ switch openrouter or typesafe"},
		{"Decision Model", valueOr(m.projectStatus.DecisionModel, "~typesafe/jev-latest"), "Enter to set decision router model"},
		{"Decision API Key Env", valueOr(m.projectStatus.DecisionAPIKeyEnv, "OPENROUTER_API_KEY"), "Enter to set API key env var for decision engine"},
		{"Decision Confidence", fmt.Sprintf("%.2f", conf), "Enter to set min routing confidence (0.0..1.0)"},
		{"Decision Candidates", formatDecisionCandidatesShort(m.projectStatus.DecisionCandidates), "Enter to configure candidate accounts/profiles"},
	}

	innerW := maxInt(width-6, 44)
	for i, row := range rows {
		selected := i == m.projectSettingsSelected
		cursor := "  "
		if selected {
			cursor = "› "
		}
		labelCol := fmt.Sprintf("%-22s", row.label+":")
		if selected && p.enabled() {
			plainVal := ansi.Strip(row.value)
			line := fmt.Sprintf("%s%s %-26s  %s", cursor, labelCol, truncateCell(plainVal, 26), row.hint)
			line = ansi.Truncate(line, innerW, "…")
			b.WriteString(lipgloss.NewStyle().Foreground(p.ink).Background(lipgloss.Color("#2d2550")).Bold(true).Render(padRight(line, minInt(innerW, 96))))
		} else {
			b.WriteString(cursor + p.mutedText(labelCol) + " " + p.cyanText(fmt.Sprintf("%-26s", row.value)) + "  " + p.quietText(row.hint))
		}
		b.WriteByte('\n')
	}

	b.WriteString("\n" + p.quietText("Commands: :project settings  ·  :project  ·  :accounts  ·  :marketplace"))
	return renderPanelTheme(m.theme, "PROJECT  ·  SETTINGS & PERMISSIONS", b.String(), width, true, height)
}

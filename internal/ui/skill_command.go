package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const skillCommandUsage = "usage: :skill browse | install ID [--scope project|global]"

// skillPaletteSuggestions adds catalog-backed completions to the static
// command suggestions. Marketplace IDs are deliberately sourced from the
// loaded catalog only; completion never starts a network request.
func (m *Model) skillPaletteSuggestions(input string) []paletteSuggestion {
	fields := strings.Fields(input)
	trailingSpace := len(input) > 0 && (input[len(input)-1] == ' ' || input[len(input)-1] == '\t')
	if len(fields) < 2 || !strings.EqualFold(fields[0], "skill") || !strings.EqualFold(fields[1], "install") {
		return paletteSuggestions(input)
	}

	if len(fields) == 2 && !trailingSpace {
		return paletteSuggestions(input)
	}

	if len(fields) == 2 || (len(fields) == 3 && !trailingSpace) {
		prefix := ""
		if len(fields) == 3 {
			prefix = strings.ToLower(fields[2])
		}
		items := make([]paletteSuggestion, 0, len(m.marketplaceEntries))
		for _, entry := range m.marketplaceEntries {
			id := sanitizeDisplay(strings.TrimSpace(entry.ID))
			if id == "" || (prefix != "" && !strings.HasPrefix(strings.ToLower(id), prefix)) {
				continue
			}
			items = append(items, paletteSuggestion{text: "skill install " + id, help: "install this marketplace skill"})
		}
		return items
	}

	if len(fields) == 3 && trailingSpace {
		return []paletteSuggestion{{text: "skill install " + fields[2] + " --scope ", help: "choose project or global scope"}}
	}
	if len(fields) == 4 {
		flag := strings.ToLower(fields[3])
		if flag != "--scope" {
			if strings.HasPrefix("--scope", flag) {
				return []paletteSuggestion{{text: "skill install " + fields[2] + " --scope ", help: "choose project or global scope"}}
			}
			return nil
		}
		items := make([]paletteSuggestion, 0, 2)
		for _, scope := range []string{"project", "global"} {
			items = append(items, paletteSuggestion{text: "skill install " + fields[2] + " --scope " + scope, help: "install this marketplace skill"})
		}
		return items
	}
	if len(fields) == 5 && strings.EqualFold(fields[3], "--scope") {
		prefix := strings.ToLower(fields[4])
		items := make([]paletteSuggestion, 0, 2)
		for _, scope := range []string{"project", "global"} {
			if strings.HasPrefix(scope, prefix) {
				items = append(items, paletteSuggestion{text: "skill install " + fields[2] + " --scope " + scope, help: "install this marketplace skill"})
			}
		}
		return items
	}
	return nil
}

// executeSkillCommand validates the whole command before scheduling any
// marketplace callback. That keeps malformed input side-effect free.
func (m *Model) executeSkillCommand(args []string, usage func() tea.Cmd) tea.Cmd {
	if len(args) == 1 && strings.EqualFold(args[0], "browse") {
		return m.startMarketplace()
	}
	if len(args) == 0 || !strings.EqualFold(args[0], "install") {
		return usage()
	}
	if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
		return usage()
	}
	id := strings.TrimSpace(args[1])
	if strings.HasPrefix(id, "-") {
		return usage()
	}
	scope := "project"
	switch len(args) {
	case 2:
	case 4:
		if !strings.EqualFold(args[2], "--scope") || (!strings.EqualFold(args[3], "project") && !strings.EqualFold(args[3], "global")) {
			return usage()
		}
		scope = strings.ToLower(args[3])
	default:
		return usage()
	}
	return m.startSkillInstall(id, scope)
}

// startSkillInstall opens the marketplace preview and runs preview followed
// by install in one cancellable command. The application callback's preview
// is the catalog snapshot used by Install, so installation cannot bypass the
// reviewed entry.
func (m *Model) startSkillInstall(id, scope string) tea.Cmd {
	id = sanitizeDisplay(strings.TrimSpace(id))
	scope = strings.ToLower(strings.TrimSpace(scope))
	if id == "" || (scope != "project" && scope != "global") {
		return nil
	}
	if m.marketplace.Preview == nil || m.marketplace.Install == nil {
		m.marketplaceError = "marketplace unavailable"
		m.status = m.marketplaceError
		return nil
	}
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
	}
	m.marketplaceOpen = true
	m.marketplaceSearching = false
	m.marketplacePreviewID = id
	m.marketplacePreview = "Resolving skill..."
	m.marketplaceError = ""
	m.marketplaceLoading = true
	m.marketplaceInstalling = true
	m.marketplaceInstallScope = scope
	m.marketplaceID++
	op := m.marketplaceID
	ctx, cancel := context.WithCancel(context.Background())
	m.marketplaceCancel = cancel
	m.status = fmt.Sprintf("resolving %s (%s)", id, scope)
	preview, install := m.marketplace.Preview, m.marketplace.Install
	return func() tea.Msg {
		_, err := preview(ctx, id)
		if err != nil {
			return marketplaceInstallMsg{id: op, scope: scope, err: fmt.Errorf("preview: %w", err)}
		}
		if err := ctx.Err(); err != nil {
			return marketplaceInstallMsg{id: op, scope: scope, err: fmt.Errorf("preview canceled: %w", err)}
		}
		text, err := install(ctx, id, scope)
		return marketplaceInstallMsg{id: op, scope: scope, text: text, err: err}
	}
}

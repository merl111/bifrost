package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// MarketplaceEntry is the display-safe provider-neutral representation of an
// installable skill or tool. Source keeps the exact repo/ref/path supplied by
// the marketplace so preview and install decisions remain reviewable.
type MarketplaceEntry struct {
	ID          string
	Name        string
	Description string
	Source      string
	Kind        string // "skill" (default) or "mcp"
}

// MarketplaceCallbacks are owned by the application layer. The UI never
// performs network, filesystem, or installation work itself.
type MarketplaceCallbacks struct {
	List    func(context.Context, string) ([]MarketplaceEntry, error)
	Preview func(context.Context, string) (string, error)
	Install func(context.Context, string, string) (string, error)
	ListMCP func(context.Context, string) ([]MarketplaceEntry, error)
}

// WithMarketplace connects the optional skill marketplace pane.
func WithMarketplace(callbacks MarketplaceCallbacks) Option {
	return func(m *Model) { m.marketplace = callbacks }
}

type marketplaceListMsg struct {
	id      uint64
	query   string
	entries []MarketplaceEntry
	err     error
}

type marketplacePreviewMsg struct {
	id    uint64
	entry MarketplaceEntry
	text  string
	err   error
}

type marketplaceInstallMsg struct {
	id    uint64
	scope string
	text  string
	err   error
}

// marketplaceViewState is presentation-only state. Keeping scroll offsets
// separate from catalog and request state lets selection/search survive while
// the panel is redrawn at a different terminal size.
type marketplaceViewState struct {
	resultOffset  int
	previewOffset int
	searchHit     marketplaceHit
	skillsTabHit  marketplaceHit
	mcpTabHit     marketplaceHit
	resultHits    []marketplaceHit
	projectHit    marketplaceHit
	globalHit     marketplaceHit
	modalHit      marketplaceHit
	closeHit      marketplaceHit
	columns       int
}

type marketplaceHit struct {
	x, y, width, height int
	index               int
}

func (h marketplaceHit) contains(x, y int) bool {
	return h.width > 0 && x >= h.x && x < h.x+h.width && y >= h.y && y < h.y+h.height
}

func (m *Model) startMarketplace() tea.Cmd {
	return m.startMarketplaceTab(m.marketplaceTab)
}

func (m *Model) startMCPMarketplace() tea.Cmd {
	return m.startMarketplaceTab(1)
}

func (m *Model) startMarketplaceTab(tab int) tea.Cmd {
	if tab != 0 && tab != 1 {
		tab = 0
	}
	m.marketplaceTab = tab
	list := m.marketplace.List
	if tab == 1 && m.marketplace.ListMCP != nil {
		list = m.marketplace.ListMCP
	}
	if list == nil {
		m.status = "marketplace unavailable"
		return nil
	}
	m.marketplaceOpen = true
	m.marketplaceSearching = false
	m.marketplacePreview = ""
	m.marketplacePreviewID = ""
	m.marketplaceView = marketplaceViewState{}
	m.marketplaceError = ""
	m.marketplaceSelected = 0
	m.marketplaceLoading = true
	m.marketplaceInstalling = false
	m.marketplaceID++
	id := m.marketplaceID
	query := ""
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.marketplaceCancel = cancel
	if tab == 1 {
		m.status = "loading MCP servers marketplace"
	} else {
		m.status = "loading marketplace"
	}
	return func() tea.Msg {
		entries, err := list(ctx, query)
		return marketplaceListMsg{id: id, query: query, entries: entries, err: err}
	}
}

func (m *Model) startMarketplacePreview() tea.Cmd {
	if m.marketplace.Preview == nil || m.marketplaceLoading {
		return nil
	}
	entries := m.marketplaceVisibleEntries()
	if m.marketplaceSelected < 0 || m.marketplaceSelected >= len(entries) {
		return nil
	}
	entry := entries[m.marketplaceSelected]
	m.marketplacePreview = ""
	m.marketplacePreviewID = entry.ID
	m.marketplaceInstallScope = ""
	m.marketplaceError = ""
	m.marketplaceLoading = true
	m.marketplaceID++
	id := m.marketplaceID
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.marketplaceCancel = cancel
	m.status = "loading preview: " + sanitizeDisplay(entry.Name)
	preview := m.marketplace.Preview
	return func() tea.Msg {
		text, err := preview(ctx, entry.ID)
		return marketplacePreviewMsg{id: id, entry: entry, text: text, err: err}
	}
}

func (m *Model) startMarketplaceInstall(scope string) tea.Cmd {
	if m.marketplace.Install == nil || m.marketplaceLoading || m.marketplacePreviewID == "" || m.marketplaceInstallScope != "preview-ready" {
		return nil
	}
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != "project" && scope != "global" {
		return nil
	}
	id := m.marketplacePreviewID
	entryID := id
	m.marketplaceLoading = true
	m.marketplaceInstalling = true
	m.marketplaceInstallScope = scope
	m.marketplaceError = ""
	m.marketplaceID++
	op := m.marketplaceID
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.marketplaceCancel = cancel
	m.status = fmt.Sprintf("installing %s (%s)", sanitizeDisplay(id), scope)
	install := m.marketplace.Install
	return func() tea.Msg {
		text, err := install(ctx, entryID, scope)
		return marketplaceInstallMsg{id: op, scope: scope, text: text, err: err}
	}
}

func (m *Model) marketplaceKey(stroke string, text string) tea.Cmd {
	if m.marketplaceInstalling || m.marketplaceLoading && !m.marketplaceSearching {
		if stroke == "escape" || stroke == "esc" {
			if m.marketplacePreviewID != "" {
				m.closeMarketplacePreview()
			} else {
				m.closeMarketplace()
			}
		}
		return nil
	}
	if m.marketplaceSearching {
		switch stroke {
		case "escape", "esc":
			m.marketplaceSearching = false
			m.status = "marketplace search canceled"
		case "backspace":
			runes := []rune(m.marketplaceQuery)
			if len(runes) > 0 {
				m.marketplaceQuery = string(runes[:len(runes)-1])
				m.marketplaceSelected = 0
				m.marketplaceView.resultOffset = 0
			}
		case "ctrl+u":
			m.marketplaceQuery = ""
			m.marketplaceSelected = 0
			m.marketplaceView.resultOffset = 0
		case "enter", "return":
			m.marketplaceSearching = false
		case "up", "down":
			m.marketplaceSearching = false
			m.marketplaceGridMove(stroke)
		default:
			if text != "" && len([]rune(m.marketplaceQuery)) < 128 {
				m.marketplaceQuery += sanitizeDisplay(text)
				m.marketplaceSelected = 0
				m.marketplaceView.resultOffset = 0
			}
		}
		return nil
	}
	if m.marketplacePreviewID != "" {
		switch stroke {
		case "escape", "esc":
			m.closeMarketplacePreview()
		case "up", "k":
			m.marketplaceView.previewOffset--
			m.clampMarketplacePreviewOffset()
		case "pgup", "pageup":
			m.marketplaceView.previewOffset -= m.marketplacePreviewPageSize()
			m.clampMarketplacePreviewOffset()
		case "down", "j":
			m.marketplaceView.previewOffset++
		case "pgdown", "pagedown":
			m.marketplaceView.previewOffset += m.marketplacePreviewPageSize()
			m.clampMarketplacePreviewOffset()
		case "i":
			return m.startMarketplaceInstall("project")
		case "g":
			return m.startMarketplaceInstall("global")
		}
		return nil
	}
	switch stroke {
	case "escape", "esc":
		m.closeMarketplace()
	case "/":
		m.marketplaceError = ""
		m.marketplacePreview = ""
		m.marketplacePreviewID = ""
		m.marketplaceInstallScope = ""
		m.marketplaceSearching = true
		m.marketplaceSelected = 0
		m.marketplaceView.resultOffset = 0
		m.status = "search marketplace"
	case "tab", "shift+tab":
		if m.marketplace.ListMCP != nil {
			return m.startMarketplaceTab(1 - m.marketplaceTab)
		}
	case "1":
		if m.marketplaceTab != 0 {
			return m.startMarketplaceTab(0)
		}
	case "2":
		if m.marketplace.ListMCP != nil && m.marketplaceTab != 1 {
			return m.startMarketplaceTab(1)
		}
	case "up", "k", "down", "j", "left", "h", "right", "l":
		m.marketplaceGridMove(stroke)
	case "pgup", "pageup":
		m.marketplaceMoveSelection(false, m.marketplacePageSize())
	case "pgdown", "pagedown":
		m.marketplaceMoveSelection(true, m.marketplacePageSize())
	case "r":
		return m.startMarketplace()
	case "enter", "return":
		return m.startMarketplacePreview()
	case "i":
		return m.startMarketplaceInstall("project")
	case "g":
		return m.startMarketplaceInstall("global")
	}
	return nil
}

func (m *Model) marketplaceMoveSelection(forward bool, steps ...int) {
	entries := m.marketplaceVisibleEntries()
	if len(entries) == 0 {
		m.marketplaceSelected = 0
		return
	}
	delta := 1
	if len(steps) > 0 && steps[0] > 0 {
		delta = steps[0]
	}
	if !forward {
		delta = -delta
	}
	m.marketplaceSelected = maxInt(0, minInt(m.marketplaceSelected+delta, len(entries)-1))
}

// marketplaceMouse handles the coordinates of the full-width panel wired by
// Model.Update. Search and result clicks are intentionally forgiving because
// the panel may be rendered with or without the outer status row.
func (m *Model) marketplaceMouse(mouse tea.Mouse, wheel bool) tea.Cmd {
	if !wheel && mouse.Button == tea.MouseLeft && m.marketplacePreviewID != "" && m.marketplaceView.closeHit.contains(mouse.X, mouse.Y) {
		m.closeMarketplacePreview()
		return nil
	}
	if m.marketplaceLoading || m.marketplaceInstalling {
		return nil
	}
	if wheel {
		if m.marketplacePreviewID != "" {
			if !m.marketplaceView.modalHit.contains(mouse.X, mouse.Y) {
				return nil
			}
			if mouse.Button == tea.MouseWheelUp {
				m.marketplaceView.previewOffset -= m.marketplacePreviewPageSize()
			} else if mouse.Button == tea.MouseWheelDown {
				m.marketplaceView.previewOffset += m.marketplacePreviewPageSize()
			}
			m.clampMarketplacePreviewOffset()
			return nil
		}
		switch mouse.Button {
		case tea.MouseWheelUp:
			m.marketplaceGridMove("up")
		case tea.MouseWheelDown:
			m.marketplaceGridMove("down")
		}
		return nil
	}
	if mouse.Button != tea.MouseLeft {
		return nil
	}
	if m.marketplacePreviewID != "" {
		if m.marketplaceView.projectHit.contains(mouse.X, mouse.Y) {
			return m.startMarketplaceInstall("project")
		}
		if m.marketplaceView.globalHit.contains(mouse.X, mouse.Y) {
			return m.startMarketplaceInstall("global")
		}
		return nil
	}
	if m.marketplaceView.skillsTabHit.contains(mouse.X, mouse.Y) {
		if m.marketplaceTab != 0 {
			return m.startMarketplaceTab(0)
		}
		return nil
	}
	if m.marketplaceView.mcpTabHit.contains(mouse.X, mouse.Y) && m.marketplace.ListMCP != nil {
		if m.marketplaceTab != 1 {
			return m.startMarketplaceTab(1)
		}
		return nil
	}
	if m.marketplaceView.searchHit.contains(mouse.X, mouse.Y) {
		m.marketplaceSearching = true
		m.marketplaceError = ""
		m.status = "search marketplace"
		return nil
	}
	for _, hit := range m.marketplaceView.resultHits {
		if hit.contains(mouse.X, mouse.Y) {
			m.marketplaceSelected = hit.index
			m.marketplaceSearching = false
			return m.startMarketplacePreview()
		}
	}
	return nil
}

func (m *Model) marketplacePageSize() int {
	return maxInt(len(m.marketplaceView.resultHits), 1)
}

func (m *Model) marketplacePreviewPageSize() int {
	return maxInt(minInt(m.height-8, 25)-6, 1)
}

func (m *Model) clampMarketplacePreviewOffset() {
	if m.marketplaceView.previewOffset < 0 {
		m.marketplaceView.previewOffset = 0
	}
}

func (m *Model) closeMarketplacePreview() {
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
		m.marketplaceCancel = nil
	}
	m.marketplaceID++
	m.marketplaceLoading, m.marketplaceInstalling = false, false
	m.marketplacePreview, m.marketplacePreviewID = "", ""
	m.marketplaceInstallScope, m.marketplaceError = "", ""
	m.marketplaceView.previewOffset = 0
	m.status = "marketplace results"
}

func (m *Model) closeMarketplace() {
	if m.marketplaceCancel != nil {
		m.marketplaceCancel()
		m.marketplaceCancel = nil
	}
	m.marketplaceOpen = false
	m.marketplaceSearching = false
	m.marketplaceLoading = false
	m.marketplaceInstalling = false
	m.marketplacePreview = ""
	m.marketplacePreviewID = ""
	m.marketplaceInstallScope = ""
	m.marketplaceError = ""
	m.marketplaceView = marketplaceViewState{}
	m.normalizeFocus()
	m.status = "marketplace closed"
}

func (m *Model) updateMarketplaceList(msg marketplaceListMsg) tea.Cmd {
	if !m.marketplaceOpen || msg.id != m.marketplaceID {
		return nil
	}
	m.marketplaceLoading = false
	m.marketplaceCancel = nil
	if msg.err != nil {
		m.marketplaceError = "marketplace: " + sanitizeDisplay(msg.err.Error())
		m.status = "marketplace failed"
		return nil
	}
	// The provider is always asked for the complete catalog. An empty query in
	// the response means the current local search should remain active.
	if strings.TrimSpace(msg.query) != "" {
		m.marketplaceQuery = sanitizeDisplay(msg.query)
	}
	m.marketplaceEntries = append([]MarketplaceEntry(nil), msg.entries...)
	for i := range m.marketplaceEntries {
		m.marketplaceEntries[i].ID = sanitizeDisplay(m.marketplaceEntries[i].ID)
		m.marketplaceEntries[i].Name = sanitizeDisplay(m.marketplaceEntries[i].Name)
		m.marketplaceEntries[i].Description = sanitizeDisplay(m.marketplaceEntries[i].Description)
		m.marketplaceEntries[i].Source = sanitizeDisplay(m.marketplaceEntries[i].Source)
	}
	if len(m.marketplaceEntries) == 0 {
		m.marketplaceSelected = 0
	} else if entries := m.marketplaceVisibleEntries(); m.marketplaceSelected >= len(entries) {
		m.marketplaceSelected = len(entries) - 1
	}
	m.status = fmt.Sprintf("marketplace: %d results", len(m.marketplaceEntries))
	return nil
}

func (m *Model) updateMarketplacePreview(msg marketplacePreviewMsg) tea.Cmd {
	if !m.marketplaceOpen || msg.id != m.marketplaceID || msg.entry.ID != m.marketplacePreviewID {
		return nil
	}
	m.marketplaceLoading = false
	m.marketplaceCancel = nil
	if msg.err != nil {
		m.marketplacePreviewID = ""
		m.marketplacePreview = ""
		m.marketplaceInstallScope = ""
		m.marketplaceError = "preview: " + sanitizeDisplay(msg.err.Error())
		m.status = "marketplace preview failed"
		return nil
	}
	m.marketplacePreviewID = msg.entry.ID
	m.marketplacePreview = sanitizeDisplay(msg.text)
	m.marketplaceInstallScope = "preview-ready"
	m.marketplaceView.previewOffset = 0
	m.status = "preview ready · i project · g global"
	return nil
}

func (m *Model) updateMarketplaceInstall(msg marketplaceInstallMsg) tea.Cmd {
	if !m.marketplaceOpen || msg.id != m.marketplaceID {
		return nil
	}
	m.marketplaceLoading = false
	m.marketplaceInstalling = false
	m.marketplaceCancel = nil
	if msg.err != nil {
		m.marketplaceError = "install: " + sanitizeDisplay(msg.err.Error())
		m.status = "marketplace install failed"
		return nil
	}
	m.marketplaceError = ""
	m.marketplacePreview = "✓ Installed " + msg.scope + "\n\n" + sanitizeDisplay(msg.text)
	m.marketplaceView.previewOffset = 0
	m.status = fmt.Sprintf("installed %s (%s)", sanitizeDisplay(msg.text), msg.scope)
	return nil
}

// marketplaceVisibleEntries keeps the complete response available while the
// user types. Search is immediate and local; Enter returns focus to the grid.
func (m *Model) marketplaceVisibleEntries() []MarketplaceEntry {
	query := strings.ToLower(strings.TrimSpace(sanitizeDisplay(m.marketplaceQuery)))
	if query == "" {
		return m.marketplaceEntries
	}
	visible := make([]MarketplaceEntry, 0, len(m.marketplaceEntries))
	words := strings.Fields(query)
	for _, entry := range m.marketplaceEntries {
		haystack := strings.ToLower(strings.Join([]string{entry.Name, entry.Description, entry.Source, entry.ID}, " "))
		matches := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				matches = false
				break
			}
		}
		if matches {
			visible = append(visible, entry)
		}
	}
	return visible
}

func (m *Model) marketplacePaste(text string) {
	if !m.marketplaceSearching || m.marketplaceLoading || m.marketplaceInstalling {
		return
	}
	text = strings.ReplaceAll(sanitizeDisplay(text), "\n", " ")
	if len([]rune(m.marketplaceQuery))+len([]rune(text)) > 128 {
		m.status = "search exceeds 128 character limit"
		return
	}
	m.marketplaceQuery += text
	m.marketplaceSelected, m.marketplaceView.resultOffset = 0, 0
}

func (m *Model) marketplacePreviewEntry() MarketplaceEntry {
	for _, entry := range m.marketplaceEntries {
		if entry.ID == m.marketplacePreviewID {
			return entry
		}
	}
	return MarketplaceEntry{ID: m.marketplacePreviewID}
}

func (m *Model) marketplaceSearchField(width int) string {
	p := m.colors()
	query := sanitizeDisplay(m.marketplaceQuery)
	if query == "" && !m.marketplaceSearching {
		query = "Search skills, tools, or sources"
	}
	available := maxInt(width-8, 8)
	query = ansi.Truncate(query, available, "…")
	if m.marketplaceSearching {
		query += "▏"
	}
	field := "/ " + query
	if p.enabled() {
		style := lipgloss.NewStyle().Foreground(p.ink).Background(p.surface).Padding(0, 1).Width(maxInt(width-2, 1)).Border(lipgloss.RoundedBorder()).BorderForeground(p.border)
		if m.marketplaceSearching {
			style = style.BorderForeground(p.cyan)
		}
		return style.Render(field)
	}
	return "[ " + field + " ]"
}

func (m *Model) renderMarketplacePreview(width, height int) string {
	p := m.colors()
	m.marketplaceView.projectHit, m.marketplaceView.globalHit = marketplaceHit{}, marketplaceHit{}
	entry := m.marketplacePreviewEntry()
	name := sanitizeDisplay(entry.Name)
	if name == "" {
		name = sanitizeDisplay(entry.ID)
	}
	pinned := []string{p.quietText("Esc  back to results"), "", p.title(ansi.Truncate(name, width, "…")), p.quietText(ansi.Truncate(entry.ID, width, "…")), ""}
	if height < 10 {
		pinned = []string{p.quietText("Esc  back to results"), p.title(ansi.Truncate(name, width, "…"))}
	}
	preview := m.marketplacePreview
	switch {
	case m.marketplaceInstalling:
		preview = "◌ Installing skill…"
	case m.marketplaceLoading:
		preview = "◌ Loading skill details…"
	case m.marketplaceError != "":
		preview = m.marketplaceError
	case strings.TrimSpace(preview) == "":
		preview = entry.Description
	}
	body := renderChatMarkdown(sanitizeDisplay(preview), maxInt(width, 1), p)
	footer := m.marketplacePreviewFooter(width, height)
	available := maxInt(height-len(pinned)-len(footer), 0)
	offset := minInt(maxInt(m.marketplaceView.previewOffset, 0), maxInt(len(body)-available, 0))
	m.marketplaceView.previewOffset = offset
	end := minInt(offset+available, len(body))
	visible := append([]string(nil), body[offset:end]...)
	for len(visible) < available {
		visible = append(visible, "")
	}
	return strings.Join(append(append(pinned, visible...), footer...), "\n")
}

func (m *Model) marketplacePreviewFooter(width, height int) []string {
	p := m.colors()
	if height < 6 || width < 24 {
		return []string{p.quietText("Resize for actions")}
	}
	if m.marketplaceLoading || m.marketplaceInstallScope != "preview-ready" {
		return []string{p.quietText("↑↓ / PgUp PgDn scroll · Esc back")}
	}
	projectLabel, globalLabel := "[ i  Install to project ]", "[ g  Install globally ]"
	projectWidth, globalWidth := ansi.StringWidth(projectLabel), ansi.StringWidth(globalLabel)
	if projectWidth+3+globalWidth <= width {
		m.marketplaceView.projectHit = marketplaceHit{x: 1, y: height + 1, width: projectWidth, height: 1}
		m.marketplaceView.globalHit = marketplaceHit{x: 1 + projectWidth + 3, y: height + 1, width: globalWidth, height: 1}
		return []string{p.cyanText(projectLabel + "   " + globalLabel)}
	}
	m.marketplaceView.projectHit = marketplaceHit{x: 1, y: height, width: minInt(projectWidth, width), height: 1}
	m.marketplaceView.globalHit = marketplaceHit{x: 1, y: height + 1, width: minInt(globalWidth, width), height: 1}
	return []string{p.cyanText(projectLabel), p.cyanText(globalLabel)}
}

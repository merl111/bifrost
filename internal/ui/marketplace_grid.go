package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func marketplaceColumns(width int) int { return maxInt(1, minInt(4, (width-2)/34)) }

func (m *Model) marketplaceGridMove(key string) {
	columns := marketplaceColumns(maxInt(m.width-2, 1))
	if m.marketplaceView.columns > 0 {
		columns = m.marketplaceView.columns
	}
	delta := 1
	switch key {
	case "left", "h":
		delta = -1
	case "up", "k":
		delta = -columns
	case "down", "j":
		delta = columns
	}
	m.marketplaceSelected = maxInt(0, minInt(m.marketplaceSelected+delta, len(m.marketplaceVisibleEntries())-1))
}

func (m *Model) renderMarketplace() string {
	return m.renderMarketplacePanel(m.width, maxInt(m.height-2, 3))
}

func (m *Model) renderMarketplacePanel(width, height int) string {
	width, height = maxInt(width, 1), maxInt(height, 3)
	panelTitle := "SKILLS"
	modalTitle := "SKILL DETAILS"
	if m.marketplaceTab == 1 {
		panelTitle = "MCP SERVERS"
		modalTitle = "MCP SERVER DETAILS"
	}
	grid := renderPanelTheme(m.theme, panelTitle, m.renderMarketplaceGrid(maxInt(width-2, 1), maxInt(height-2, 1)), width, true, height)
	if m.marketplacePreviewID == "" {
		return grid
	}
	modalWidth := minInt(88, maxInt(width-8, 1))
	if width < 40 {
		modalWidth = maxInt(width-4, 1)
	}
	modalHeight := minInt(27, maxInt(height-4, 3))
	x, y := (width-modalWidth)/2, (height-modalHeight)/2
	padding := 2
	if modalWidth < 32 {
		padding = 0
	}
	content := m.renderMarketplacePreview(maxInt(modalWidth-2-padding*2, 1), maxInt(modalHeight-2, 1))
	padded := strings.Split(content, "\n")
	for i := range padded {
		padded[i] = strings.Repeat(" ", padding) + padded[i]
	}
	content = strings.Join(padded, "\n")
	modal := renderPanelTheme(m.theme, modalTitle, content, modalWidth, true, modalHeight)
	for _, hit := range []*marketplaceHit{&m.marketplaceView.projectHit, &m.marketplaceView.globalHit} {
		if hit.width > 0 {
			hit.x += x + padding
			hit.y += y
		}
	}
	m.marketplaceView.modalHit = marketplaceHit{x: x, y: y + 1, width: modalWidth, height: modalHeight}
	m.marketplaceView.closeHit = marketplaceHit{x: x + 1 + padding, y: y + 2, width: minInt(19, modalWidth-2), height: 1}
	return m.overlayMarketplaceModal(grid, modal, x, y, width)
}

func (m *Model) overlayMarketplaceModal(background, modal string, x, y, width int) string {
	base := strings.Split(background, "\n")
	p := m.colors()
	for i, row := range base {
		base[i] = p.quietText(ansi.Strip(row))
	}
	for i, row := range strings.Split(modal, "\n") {
		if y+i >= len(base) {
			break
		}
		base[y+i] = ansi.Cut(base[y+i], 0, x) + row + ansi.Cut(base[y+i], x+ansi.StringWidth(row), width)
	}
	return strings.Join(base, "\n")
}

func (m *Model) renderMarketplaceGrid(width, height int) string {
	p := m.colors()
	v := &m.marketplaceView
	v.resultHits = nil
	v.projectHit, v.globalHit, v.closeHit = marketplaceHit{}, marketplaceHit{}, marketplaceHit{}
	v.skillsTabHit, v.mcpTabHit = marketplaceHit{}, marketplaceHit{}
	entries := m.marketplaceVisibleEntries()
	search := m.marketplaceSearchField(maxInt(width-4, 1))
	skillsTabLabel := " [1] Skills "
	mcpTabLabel := " [2] MCP Servers "
	if m.marketplaceTab == 1 {
		skillsTabLabel = p.mutedText(skillsTabLabel)
		mcpTabLabel = p.badge(mcpTabLabel)
	} else {
		skillsTabLabel = p.badge(skillsTabLabel)
		mcpTabLabel = p.mutedText(mcpTabLabel)
	}
	titleLine := p.violetText("  ✦  Discover skills")
	subLine := p.mutedText("  Small additions. More capable agents.")
	countLine := p.quietText(fmt.Sprintf("  %d skills  ·  project & global installation  ·  Tab switch to MCP Servers", len(entries)))
	if m.marketplaceTab == 1 {
		titleLine = p.violetText("  ✦  Discover MCP servers")
		subLine = p.mutedText("  Connect tools, databases, browsers, and services via Model Context Protocol.")
		countLine = p.quietText(fmt.Sprintf("  %d MCP servers  ·  project & global installation  ·  Tab switch to Skills", len(entries)))
	}
	if m.marketplace.ListMCP != nil && width >= 54 {
		titleLine = titleLine + "    " + skillsTabLabel + " " + mcpTabLabel
		v.skillsTabHit = marketplaceHit{x: 30, y: 2, width: 12, height: 1}
		v.mcpTabHit = marketplaceHit{x: 43, y: 2, width: 17, height: 1}
	}
	header := []string{titleLine, subLine, ""}
	for _, line := range strings.Split(search, "\n") {
		header = append(header, "  "+line)
	}
	v.searchHit = marketplaceHit{x: 3, y: 5, width: maxInt(width-4, 1), height: strings.Count(search, "\n") + 1}
	header = append(header, countLine, "")
	// Short terminals keep search and at least a compact card visible.
	if height < 16 {
		shortTitle := p.violetText("  ✦ Skills")
		if m.marketplaceTab == 1 {
			shortTitle = p.violetText("  ✦ MCP Servers")
		}
		header = []string{shortTitle, "  / " + sanitizeDisplay(m.marketplaceQuery), ""}
		v.searchHit = marketplaceHit{x: 1, y: 3, width: width, height: 1}
	}
	budget := maxInt(height-len(header)-1, 1)
	columns := marketplaceColumns(width)
	v.columns = columns
	cardWidth := maxInt((width-4-2*(columns-1))/columns, 1)
	cardHeight := minInt(9, budget)
	rowStride := cardHeight + 1
	visibleRows := maxInt((budget+1)/rowStride, 1)
	selectedRow := maxInt(m.marketplaceSelected, 0) / columns
	totalRows := (len(entries) + columns - 1) / columns
	if selectedRow < v.resultOffset {
		v.resultOffset = selectedRow
	}
	if selectedRow >= v.resultOffset+visibleRows {
		v.resultOffset = selectedRow - visibleRows + 1
	}
	v.resultOffset = maxInt(0, minInt(v.resultOffset, maxInt(totalRows-visibleRows, 0)))
	var body []string
	for row := v.resultOffset; row < minInt(totalRows, v.resultOffset+visibleRows); row++ {
		body = append(body, m.marketplaceCardRow(entries, row*columns, columns, cardWidth, cardHeight, 2+len(header)+len(body))...)
		if len(body) < budget {
			body = append(body, "")
		}
	}
	if len(entries) == 0 {
		message := "No matching skills. Press / to change your search."
		if m.marketplaceTab == 1 {
			message = "No matching MCP servers. Press / to change your search."
		}
		if m.marketplaceLoading {
			message = "Loading the skill catalog…"
			if m.marketplaceTab == 1 {
				message = "Loading the MCP server catalog…"
			}
		}
		body = wrapChatText(message, maxInt(width-4, 1))
	}
	if m.marketplaceError != "" && m.marketplacePreviewID == "" {
		body = wrapChatText(m.marketplaceError+" · r retry", maxInt(width-4, 1))
	}
	body = body[:minInt(len(body), budget)]
	for len(body) < budget {
		body = append(body, "")
	}
	footer := "  Arrows / hjkl select · Enter preview · / search · Tab Skills/MCP · Esc close"
	if width < 70 {
		footer = "  Arrows select · Enter preview · / search · Tab switch"
	}
	return strings.Join(append(append(header, body...), p.quietText(footer)), "\n")
}

// marketplaceCardRow pairs card cells with their measured screen hitboxes.
func (m *Model) marketplaceCardRow(entries []MarketplaceEntry, start, columns, width, height, y int) []string {
	rows := make([]string, height)
	for i := range rows {
		rows[i] = "  "
	}
	for col := 0; col < columns; col++ {
		index := start + col
		var card []string
		if index < len(entries) {
			card = m.marketplaceCard(entries[index], index == m.marketplaceSelected, width, height)
			m.marketplaceView.resultHits = append(m.marketplaceView.resultHits, marketplaceHit{x: 3 + col*(width+2), y: y, width: width, height: height, index: index})
		}
		for i := range rows {
			if col > 0 {
				rows[i] += "  "
			}
			if i < len(card) {
				rows[i] += card[i]
			} else {
				rows[i] += strings.Repeat(" ", width)
			}
		}
	}
	return rows
}

func (m *Model) marketplaceCard(entry MarketplaceEntry, selected bool, width, height int) []string {
	p := m.colors()
	inner := maxInt(width-4, 1)
	name := sanitizeDisplay(entry.Name)
	if name == "" {
		name = sanitizeDisplay(entry.ID)
	}
	name = ansi.Truncate(name, inner, "…")
	content := []string{p.title(name), ""}
	description := wrapChatText(entry.Description, inner)
	count := maxInt(minInt(3, height-6), 1)
	if len(description) > count {
		description = description[:count]
		description[count-1] = ansi.Truncate(description[count-1]+"…", inner, "…")
	}
	for _, line := range description {
		content = append(content, p.mutedText(line))
	}
	for len(content) < height-3 {
		content = append(content, "")
	}
	if height >= 7 {
		cta := "View skill  →"
		if entry.Kind == "mcp" || m.marketplaceTab == 1 {
			cta = "View MCP server  →"
		}
		content = append(content, p.cyanText(cta))
	}
	border, background := p.border, p.surface
	if selected {
		border, background = p.cyan, p.border
	}
	style := lipgloss.NewStyle().Foreground(border)
	paintBorder := func(text string) string {
		if !p.enabled() {
			return text
		}
		return style.Render(text)
	}
	rows := []string{paintBorder("╭" + strings.Repeat("─", maxInt(width-2, 0)) + "╮")}
	for i := 0; i < maxInt(height-2, 0); i++ {
		line := ""
		if i < len(content) {
			line = content[i]
		}
		line = truncatePad(" "+line+" ", maxInt(width-2, 0))
		if p.enabled() {
			line = surfaceText(line, p.ink, background)
		}
		rows = append(rows, paintBorder("│")+line+paintBorder("│"))
	}
	if height > 1 {
		rows = append(rows, paintBorder("╰"+strings.Repeat("─", maxInt(width-2, 0))+"╯"))
	}
	return rows
}

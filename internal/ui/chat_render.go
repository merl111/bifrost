package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

const chatComposerDefaultMaxBodyRows = 8

// Cache complete messages so streaming only reparses the changing message.
// The key includes presentation settings; resizing never reuses stale wrapping.
type chatRenderCache struct {
	text, detail, kind, theme, icon, provider, model string
	collapsed                                        bool
	width                                            int
	lines                                            []string
}

func (m *Model) chatReadingWidth() int {
	return minInt(maxInt(m.chatPaneInnerWidth()-4, 1), 88)
}

func (m *Model) chatMargin() string {
	return strings.Repeat(" ", maxInt((m.chatPaneInnerWidth()-m.chatReadingWidth())/2, 0))
}

func (m *Model) chatTranscriptLines() []string {
	m.chatFolds = m.chatFolds[:0]
	p := m.colors()
	width := m.chatReadingWidth()
	margin := m.chatMargin()
	if len(m.chatLines) == 0 {
		message := "What would you like to build?"
		if m.chatRunner == nil {
			message = "Connect a provider to start a conversation."
		}
		lines := []string{"", p.accentText("✦  Welcome to Bifrost"), ""}
		lines = append(lines, wrapChatText(message, width)...)
		if width >= 34 {
			projLabel := strings.TrimSpace(m.projectStatus.ProjectName)
			if projLabel == "" && m.root != "" {
				projLabel = sanitizeDisplay(displayDir(m.root))
			}
			linkedCount := 0
			for _, r := range m.projectStatus.Repos {
				if r.Linked {
					linkedCount++
				}
			}
			wsLine := "Workspace  ·  " + valueOr(projLabel, "local")
			if linkedCount > 0 {
				wsLine += fmt.Sprintf(" (%d linked repos)", linkedCount)
			}
			connLine := "Model      ·  " + providerDisplayName(valueOr(m.connection.Provider, "offline"))
			if m.connection.Model != "" {
				connLine += " / " + m.connection.Model
			}
			lines = append(lines,
				"",
				p.mutedText(ansi.Truncate(wsLine, width, "…")),
				p.mutedText(ansi.Truncate(connLine, width, "…")),
				"",
				p.cyanText(":project")+p.quietText("      scan repos & project settings"),
				p.cyanText(":accounts")+p.quietText("     add or switch provider accounts"),
				p.cyanText(":history")+p.quietText("      resume saved conversations (:new for fresh)"),
				p.cyanText(":marketplace")+p.quietText("  browse & install skills"),
				p.cyanText(":layout")+p.quietText("       studio · conversation · review · focus"),
			)
		} else {
			lines = append(lines, "", p.quietText(":project  ·  :accounts  ·  :marketplace  ·  :layout"))
		}
		for i := range lines {
			lines[i] = margin + ansi.Truncate(lines[i], width, "…")
		}
		return lines
	}
	var lines []string
	for i := range m.chatLines {
		line := &m.chatLines[i]
		if strings.TrimSpace(line.text) == "" && strings.TrimSpace(line.detail) == "" {
			continue
		}
		if len(lines) > 0 {
			switch line.kind {
			case "user":
				// Extra breathing room + subtle turn divider before each new user prompt
				lines = append(lines, "")
				if width >= 24 {
					lines = append(lines, margin+p.quietText(strings.Repeat("─", width)))
				}
				lines = append(lines, "")
			case "assistant":
				lines = append(lines, "")
			default:
				if i > 0 && (m.chatLines[i-1].kind == "user" || m.chatLines[i-1].kind == "assistant") {
					lines = append(lines, "")
				}
			}
		}
		if chatFoldable(line.kind) {
			m.chatFolds = append(m.chatFolds, chatFoldRow{line: i, row: len(lines)})
		}
		for row, rendered := range m.renderChatMessage(line, width) {
			if row == 0 && i == m.chatFoldSelected && chatFoldable(line.kind) {
				rendered = p.cyanText(ansi.Strip(rendered))
			}
			lines = append(lines, margin+rendered)
		}
	}
	return lines
}

func (m *Model) renderChatMessage(line *chatLine, width int) []string {
	icon := m.providerIcon(line.provider)
	c := line.rendered
	if c != nil && c.text == line.text && c.detail == line.detail && c.collapsed == line.collapsed && c.kind == line.kind && c.theme == m.theme && c.width == width && c.icon == icon && c.provider == line.provider && c.model == line.model {
		return c.lines
	}
	p := m.colors()
	var lines []string
	switch line.kind {
	case "user":
		header := p.cyanText("›  You")
		if p.enabled() {
			header = lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(" › You ")
		}
		lines = append(lines, header, "")
		rail := p.cyanText("┃ ")
		msgWidth := maxInt(width-2, 1)
		for _, r := range renderChatMarkdown(line.text, msgWidth, p) {
			lines = append(lines, boundedLine(rail+r, width))
		}
	case "assistant":
		heading := "✦  Bifrost"
		if line.provider != "" {
			heading = icon + "  " + providerDisplayName(line.provider)
		}
		header := p.accentText(foldMark(line.collapsed) + heading)
		if line.model != "" && width >= 48 {
			header += p.quietText("  ·  " + sanitizeDisplay(line.model))
		}
		lines = append(lines, ansi.Truncate(header, width, "…"))
		if !line.collapsed {
			lines = append(lines, "")
			lines = append(lines, renderChatMarkdown(line.text, width, p)...)
		}
	case "tool", "thinking", "reasoning", "summary", "decision", "status":
		lines = m.renderChatActivity(line, width)
	default:
		prefix := "· "
		style := p.quietText
		if line.kind == "tool" {
			prefix = "↳ "
		} else if line.kind == "error" {
			prefix, style = "! ", p.dangerText
		}
		for i, text := range wrapChatText(line.text, maxInt(width-2, 1)) {
			if i > 0 {
				prefix = "  "
			}
			lines = append(lines, style(ansi.Truncate(prefix+text, width, "…")))
		}
	}
	line.rendered = &chatRenderCache{text: line.text, detail: line.detail, collapsed: line.collapsed, kind: line.kind, theme: m.theme, width: width, icon: icon, provider: line.provider, model: line.model, lines: lines}
	return lines
}

func (m *Model) chatFooterLines() []string {
	width := m.chatReadingWidth()
	margin := m.chatMargin()
	p := m.colors()
	maxRows := maxInt(m.mainPaneHeight()-3, 1)
	text := sanitizeDisplay(m.chatInput)
	if text == "" {
		text = "Ask anything, or describe a change…"
		if width < 40 {
			text = "Message Bifrost…"
		}
	}
	if width < 8 || maxRows < 4 {
		if m.chatEditing {
			text += "▏"
			available := width - 2
			if available < 1 {
				return []string{margin + ansi.Truncate("▏", width, "")}
			}
			rows := wrapChatText(text, available)
			return []string{margin + "› " + ansi.Truncate(rows[len(rows)-1], available, "")}
		}
		return []string{margin + ansi.Truncate("› "+text, width, "…")}
	}
	if m.chatEditing {
		text += "▏"
	}
	body := wrapChatText(text, width-4)
	composerMaxRows := m.composerMaxRows
	if composerMaxRows <= 0 {
		composerMaxRows = chatComposerDefaultMaxBodyRows
	}
	visibleRows := minInt(composerMaxRows, maxInt(maxRows-3, 1))
	if len(body) > visibleRows {
		body = body[len(body)-visibleRows:]
	}
	border := p.quietText
	if m.chatEditing {
		border = p.cyanText
	}
	label := " Message "
	if len(label) > width-2 {
		label = ""
	}
	var lines []string
	// Keep the transcript from sitting on the composer border.
	if maxRows >= 8 {
		lines = append(lines, "", "")
	}
	if m.chatEditing && m.chatMention.open {
		popoverBudget := maxInt(maxRows-(len(body)+3), 0)
		if popover := m.renderChatMentionPopover(width, popoverBudget); len(popover) > 0 {
			lines = append(lines, popover...)
		}
	}
	topBox := border("╭" + label + strings.Repeat("─", width-2-len(label)) + "╮")
	if p.enabled() {
		topBox = surfaceText(topBox, p.ink, p.elevated)
	}
	lines = append(lines, topBox)
	for _, line := range body {
		if m.chatInput == "" {
			line = p.quietText(line)
		} else if p.enabled() {
			line = highlightComposerMentions(line, p)
		}
		row := border("│ ") + truncatePad(line, width-4) + border(" │")
		if p.enabled() {
			row = surfaceText(row, p.ink, p.elevated)
		}
		lines = append(lines, row)
	}
	bottomBox := border("╰" + strings.Repeat("─", width-2) + "╯")
	if p.enabled() {
		bottomBox = surfaceText(bottomBox, p.ink, p.elevated)
	}
	lines = append(lines, bottomBox)
	hint := "c compose  ·  Tab folds  ·  Enter toggle"
	if m.chatEditing {
		hint = "@ context  ·  Enter send  ·  " + m.chatNewlineLabel() + " newline  ·  Esc close"
	} else if m.chatRunning {
		hint = "● Working…  ·  Ctrl-C stop"
	}
	if len(lines) < maxRows {
		lines = append(lines, p.quietText(ansi.Truncate(hint, width, "…")))
	}
	for i := range lines {
		lines[i] = margin + lines[i]
	}
	return lines
}

func (m *Model) chatNewlineLabel() string {
	switch strings.ToLower(strings.TrimSpace(m.composerNewline)) {
	case "alt+enter":
		return "Alt+Enter"
	case "ctrl+j":
		return "Ctrl+J"
	default:
		return "Shift+Enter"
	}
}

func (m *Model) renderChat() string {
	innerHeight := maxInt(m.mainPaneHeight()-2, 1)
	var header []string
	if m.chatTopBarVisible(innerHeight) {
		header = m.renderChatTopBar(m.chatPaneInnerWidth())
	}
	transcript := m.chatTranscriptLines()
	viewport := m.chatViewport()
	m.chatScroll = maxInt(0, minInt(m.chatScroll, maxInt(len(transcript)-viewport, 0)))
	start := minInt(m.chatScroll, len(transcript))
	end := minInt(start+viewport, len(transcript))
	lines := append([]string(nil), header...)
	for idx := start; idx < end; idx++ {
		lines = append(lines, m.applyChatSelectionHighlight(idx, transcript[idx]))
	}
	for len(lines) < len(header)+viewport {
		lines = append(lines, "")
	}
	return strings.Join(append(lines, m.chatFooterLines()...), "\n")
}

// renderChatPanel frames the conversation pane with the unified workspace panel
// chrome while preserving the two-row height and two-column width border budget.
func (m *Model) renderChatPanel(width, height int, focused bool) string {
	title := "CONVERSATION"
	if m.chatRunning {
		title += "  ·  WORKING"
	}
	return renderPanelTheme(m.theme, title, m.renderChat(), width, focused, height)
}

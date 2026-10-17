package ui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

type helpItem struct {
	keys string
	text string
}

type helpSection struct {
	title string
	note  string
	items []helpItem
}

func (m *Model) openHelp() {
	m.helpOpen = true
	m.helpScroll = 0
	m.status = "help"
}

func (m *Model) clampHelpScroll() {
	room := m.helpInnerRows
	if room < 1 {
		room = 1
	}
	maxScroll := maxInt(m.helpLineCount-room, 0)
	if m.helpScroll > maxScroll {
		m.helpScroll = maxScroll
	}
	if m.helpScroll < 0 {
		m.helpScroll = 0
	}
}

func helpCommand(name string) (helpItem, bool) {
	command, ok := paletteCommandFor(name)
	if !ok {
		return helpItem{}, false
	}
	return helpItem{keys: ":" + command.name, text: command.help}, true
}

func helpCommandItems(names ...string) []helpItem {
	items := make([]helpItem, 0, len(names))
	for _, name := range names {
		if item, ok := helpCommand(name); ok {
			items = append(items, item)
		}
	}
	return items
}

func helpSections() []helpSection {
	grouped := map[string]bool{}
	groups := []struct {
		title string
		note  string
		names []string
	}{
		{title: "Work", names: []string{"tasks", "terminal", "review", "layout", "settings"}},
		{title: "Conversation", names: []string{"history", "new", "compact", "chat", "model", "accounts"}},
		{title: "Workspace", names: []string{"project", "marketplace", "mcp", "skill", "editor", "external", "resize", "config", "save", "discard", "help", "quit"}},
	}
	for _, group := range groups {
		for _, name := range group.names {
			grouped[name] = true
		}
	}
	var extra []string
	for _, command := range paletteCommands {
		if !grouped[command.name] {
			extra = append(extra, command.name)
		}
	}
	sections := []helpSection{
		{title: "Navigate", items: []helpItem{
			{keys: "Ctrl-W h/j/k/l", text: "Cycle panes"},
			{keys: "h / l", text: "Jump to files or chat"},
			{keys: "j / k", text: "Move in the focused pane"},
			{keys: "PgUp / PgDn", text: "Scroll a page"},
			{keys: "Click", text: "Focus the pane under the pointer"},
			{keys: "Drag", text: "Resize a pane divider"},
			{keys: "?", text: "Open this help"},
			{keys: "Esc", text: "Close help, the palette, or the composer"},
			{keys: ": / Ctrl-P", text: "Open the command palette"},
			{keys: "q", text: "Quit"},
			{keys: "Ctrl-C", text: "Cancel the turn. Press again to quit"},
		}},
		{title: "Files", items: []helpItem{
			{keys: "j / k", text: "Choose a file"},
			{keys: "Enter", text: "Open the file or directory"},
		}},
		{title: "Chat", items: []helpItem{
			{keys: "c / i", text: "Compose a message"},
			{keys: "Enter", text: "Send, and keep the cursor in the composer"},
			{keys: "Shift-Enter", text: "Insert a newline"},
			{keys: "Alt-Enter / Ctrl-J", text: "Insert a newline when Shift-Enter is unavailable"},
			{keys: "Tab / Shift-Tab", text: "Select a fold"},
			{keys: "Space", text: "Open or close the selected fold"},
			{keys: "@", text: "Attach a file, diff, or the terminal"},
			{keys: "y", text: "Copy the last reply"},
			{keys: "m", text: "Switch model"},
			{keys: "Ctrl-T", text: "Open another chat tab"},
			{keys: "{ / }", text: "Previous or next chat tab"},
			{keys: "R", text: "Rename the chat tab"},
		}},
		{title: "Viewer", items: []helpItem{
			{keys: "t", text: "Open a shell tab. The message box keeps the keyboard"},
			{keys: "T", text: "Add another shell"},
			{keys: "[ / ]", text: "Previous or next viewer tab"},
			{keys: "i / Enter", text: "Type into the focused shell"},
			{keys: "Esc", text: "Leave the shell and return to the message box"},
			{keys: "E / e", text: "Inline editor or external editor"},
			{keys: "r", text: "Refresh the review"},
		}},
		{title: "Editor", items: []helpItem{
			{keys: "i", text: "Insert text"},
			{keys: "Esc", text: "Return to normal mode"},
			{keys: "Ctrl-S", text: "Save"},
			{keys: "d", text: "Discard changes"},
			{keys: "/", text: "Search"},
		}},
		{title: "History", note: "The latest conversation opens again when Bifrost starts.", items: []helpItem{
			{keys: ":history", text: "Browse saved conversations"},
			{keys: ":new", text: "Start a fresh conversation"},
			{keys: "Enter", text: "Resume the selected conversation"},
		}},
	}
	for _, group := range groups {
		sections = append(sections, helpSection{title: group.title, note: group.note, items: helpCommandItems(group.names...)})
	}
	if len(extra) > 0 {
		sections = append(sections, helpSection{title: "Commands", items: helpCommandItems(extra...)})
	}
	return sections
}

func helpKeyWidth(sections []helpSection) int {
	width := 14
	for _, section := range sections {
		for _, item := range section.items {
			if n := ansi.StringWidth(item.keys); n > width {
				width = n
			}
		}
	}
	if width > 24 {
		width = 24
	}
	return width
}

func (m *Model) helpLines(width int) []string {
	p := m.colors()
	width = maxInt(width, 24)
	sections := helpSections()
	keyWidth := helpKeyWidth(sections)
	var lines []string
	for _, section := range sections {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, p.violetText(section.title))
		if section.note != "" {
			lines = append(lines, p.quietText(ansi.Truncate("  "+section.note, width, "…")))
		}
		for _, item := range section.items {
			lines = append(lines, helpRow(p, item, keyWidth, width))
		}
	}
	return lines
}

func helpRow(p uiPalette, item helpItem, keyWidth, width int) string {
	label := item.keys
	if ansi.StringWidth(label) > keyWidth {
		label = ansi.Truncate(label, keyWidth, "…")
	}
	label += strings.Repeat(" ", keyWidth-ansi.StringWidth(label))
	descWidth := maxInt(width-keyWidth-4, 8)
	desc := ansi.Truncate(item.text, descWidth, "…")
	row := "  " + label + "  " + desc
	if p.enabled() {
		row = "  " + p.cyanText(label) + "  " + desc
	}
	return row
}

func (m *Model) overlayHelpModal(background string) string {
	p := m.colors()
	modalWidth := maxInt(m.width-4, 1)
	if m.width < 40 {
		modalWidth = maxInt(m.width-2, 1)
	}
	modalHeight := maxInt(m.height-2, 8)
	if modalHeight > m.height-1 {
		modalHeight = maxInt(m.height-1, 3)
	}
	x, y := (m.width-modalWidth)/2, (m.height-modalHeight)/2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	innerWidth := maxInt(modalWidth-4, 1)
	innerRows := maxInt(modalHeight-3, 1)
	lines := m.helpLines(innerWidth)
	m.helpLineCount = len(lines)
	m.helpInnerRows = innerRows
	m.clampHelpScroll()
	start := m.helpScroll
	end := minInt(start+innerRows, len(lines))
	visible := append([]string(nil), lines[start:end]...)
	for len(visible) < innerRows {
		visible = append(visible, "")
	}
	footer := "Esc or ? closes    ↑↓ scroll"
	if m.helpLineCount > innerRows {
		footer = "Esc or ? closes    ↑↓ scroll    more below"
		if start > 0 && end >= len(lines) {
			footer = "Esc or ? closes    ↑↓ scroll"
		}
	}
	content := strings.Join(visible, "\n") + "\n" + p.quietText(ansi.Truncate(footer, innerWidth, "…"))
	padded := strings.Split(content, "\n")
	for i := range padded {
		padded[i] = " " + padded[i]
	}
	modal := renderPanelTheme(m.theme, "HELP", strings.Join(padded, "\n"), modalWidth, true, modalHeight)
	m.helpModalX, m.helpModalY, m.helpModalW, m.helpModalH = x, y, modalWidth, modalHeight
	return m.overlayMarketplaceModal(background, modal, x, y, m.width)
}

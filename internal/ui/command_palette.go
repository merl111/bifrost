package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"bifrost/internal/domain"
	tea "charm.land/bubbletea/v2"
)

// paletteCommand is the single description used by the command palette,
// help, and command validation. Names and aliases are compared without case;
// arguments are passed through unchanged unless a command documents an enum.
type paletteCommand struct {
	name    string
	aliases []string
	usage   string
	help    string
}

var paletteCommands = []paletteCommand{
	{name: "explain", usage: "explain [TOPIC]", help: "prepare a step-by-step code or PR review request"},
	{name: "walkthrough", usage: "walkthrough next|prev|explanation|code|close", help: "navigate the current explanation or review"},
	{name: "accounts", aliases: []string{"account"}, usage: "accounts [add]", help: "switch or add provider accounts"},
	{name: "chat", aliases: []string{"c"}, usage: "chat [new [NAME] | rename NAME | next | prev | close]", help: "focus chat or manage parallel chat tabs"},
	{name: "compact", aliases: []string{"summarize"}, usage: "compact", help: "compact and summarize earlier conversation turns"},
	{name: "config", usage: "config", help: "open settings"},
	{name: "discard", usage: "discard", help: "discard inline editor changes"},
	{name: "editor", aliases: []string{"edit"}, usage: "editor", help: "open inline editor"},
	{name: "external", aliases: []string{"external-editor"}, usage: "external", help: "open external editor"},
	{name: "help", aliases: []string{"?"}, usage: "help", help: "show keyboard shortcuts"},
	{name: "history", aliases: []string{"sessions", "conversations", "resume"}, usage: "history [SESSION_ID | new]", help: "browse or resume saved conversations"},
	{name: "layout", usage: "layout NAME", help: "choose studio, conversation, review, or focus"},
	{name: "marketplace", aliases: []string{"market", "skills"}, usage: "marketplace [skills|mcp]", help: "browse and preview skills and MCP servers"},
	{name: "mcp", aliases: []string{"mcp-servers"}, usage: "mcp [browse] | config [PATH] [--scope project|global]", help: "browse servers or validate and save a custom MCP config; restart to connect"},
	{name: "model", aliases: []string{"models"}, usage: "model [MODEL_SLUG]", help: "quick-switch active model or account"},
	{name: "new", aliases: []string{"clear"}, usage: "new", help: "start a new conversation"},
	{name: "project", aliases: []string{"projects", "repo", "repos"}, usage: "project [scan | details | readme | prs | issues | notion | settings | name NAME | description TEXT | add ALIAS [OWNER/REPO] | add-all | remove ALIAS]", help: "scan and manage project metadata, GitHub PRs/issues, Notion & repositories"},
	{name: "quit", aliases: []string{"q"}, usage: "quit", help: "exit Bifrost"},
	{name: "resize", usage: "resize PANE N", help: "resize files, viewer, chat, or terminal by percentage"},
	{name: "review", aliases: []string{"r"}, usage: "review", help: "refresh review"},
	{name: "save", aliases: []string{"write"}, usage: "save", help: "save inline editor changes"},
	{name: "skill", usage: "skill browse | install ID_OR_PATH [--scope project|global] | install-git REPOSITORY REF SUBDIRECTORY [--scope project|global]", help: "install catalog, local SKILL.md directories or Git skills; installation does not activate instructions"},
	{name: "settings", usage: "settings", help: "open settings"},
	{name: "tasks", aliases: []string{"task"}, usage: "tasks [refresh|cancel ID]", help: "show, refresh, or cancel tasks"},
	{name: "terminal", aliases: []string{"t"}, usage: "terminal [on|off|new|next|prev]", help: "open a terminal tab in the viewer, add one, or switch tabs"},
}

type paletteSuggestion struct {
	text string
	help string
}

// paletteCompletionState keeps the source query and its candidate list while
// Tab cycles through completions. The input changes after each completion, so
// deriving the next list from the completed text would otherwise drop or
// reorder candidates and make Enter select an unintended command.
type paletteCompletionState struct {
	source      string
	current     string
	suggestions []paletteSuggestion
	index       int
	active      bool
}

func init() {
	sort.Slice(paletteCommands, func(i, j int) bool { return paletteCommands[i].name < paletteCommands[j].name })
}

func paletteCommandFor(name string) (paletteCommand, bool) {
	name = strings.ToLower(name)
	for _, command := range paletteCommands {
		if command.name == name {
			return command, true
		}
		for _, alias := range command.aliases {
			if alias == name {
				return command, true
			}
		}
	}
	return paletteCommand{}, false
}

func paletteCommandNames() []paletteSuggestion {
	items := make([]paletteSuggestion, 0, len(paletteCommands))
	for _, command := range paletteCommands {
		items = append(items, paletteSuggestion{text: command.name, help: command.help})
	}
	return items
}

// paletteSuggestions returns complete, executable command text. The trailing
// space on a command with arguments deliberately makes the next completion
// step discoverable while keeping Tab useful for keyboard users.
func paletteSuggestions(input string) []paletteSuggestion {
	trimmed := strings.TrimSpace(input)
	fields := strings.Fields(trimmed)
	trailingSpace := len(input) > 0 && (input[len(input)-1] == ' ' || input[len(input)-1] == '\t')
	if len(fields) == 0 {
		return paletteCommandNames()
	}

	if len(fields) == 1 && !trailingSpace {
		prefix := strings.ToLower(fields[0])
		var items []paletteSuggestion
		for _, command := range paletteCommands {
			canonicalMatch := strings.HasPrefix(command.name, prefix)
			if canonicalMatch {
				text := command.name
				if command.name == "layout" || command.name == "resize" || command.name == "tasks" || command.name == "terminal" || command.name == "skill" {
					text += " "
				}
				items = append(items, paletteSuggestion{text: text, help: command.help})
				continue
			}
			for _, alias := range command.aliases {
				if strings.HasPrefix(alias, prefix) {
					items = append(items, paletteSuggestion{text: alias, help: command.help})
				}
			}
		}
		return items
	}

	command, ok := paletteCommandFor(fields[0])
	if !ok {
		return nil
	}
	argPrefix := ""
	if len(fields) > 1 {
		argPrefix = fields[len(fields)-1]
		if trailingSpace {
			argPrefix = ""
		}
	}
	argPrefixLower := strings.ToLower(argPrefix)
	var values []string
	switch command.name {
	case "help":
		for _, target := range paletteCommands {
			values = append(values, target.name)
		}
	case "walkthrough":
		values = []string{"next", "prev", "explanation", "code", "close"}
	case "layout":
		values = []string{"studio", "conversation", "review", "focus"}
	case "terminal":
		values = []string{"on", "off", "new", "next", "prev"}
	case "accounts":
		values = []string{"add"}
	case "chat":
		values = []string{"new ", "rename ", "next", "prev", "close"}
	case "model":
		values = []string{"auto", "claude-sonnet-4-5", "claude-opus-4-6", "gpt-5", "gemini-2.5-pro"}
	case "history":
		values = []string{"new"}
	case "resize":
		if len(fields) <= 1 || (len(fields) == 2 && !trailingSpace) {
			values = []string{"files", "viewer", "chat", "terminal"}
		} else {
			pane := strings.ToLower(fields[1])
			if pane != "files" && pane != "file" && pane != "viewer" && pane != "review" && pane != "chat" && pane != "conversation" && pane != "terminal" && pane != "term" {
				return nil
			}
			// A percentage is intentionally only suggested after the pane is
			// complete; accepting arbitrary values still gives a useful error.
			if strings.EqualFold(fields[1], "terminal") || strings.EqualFold(fields[1], "term") {
				values = []string{"10", "20", "30", "40", "50", "60"}
			} else {
				values = []string{"10", "20", "30", "40", "50", "60", "70", "80"}
			}
		}
	case "tasks":
		if len(fields) <= 2 || (len(fields) == 2 && trailingSpace) {
			values = []string{"refresh", "cancel "}
		}
	case "skill":
		if len(fields) <= 2 || (len(fields) == 2 && trailingSpace) {
			values = []string{"browse", "install ", "install-git "}
		} else if strings.EqualFold(fields[1], "install") {
			// Skill IDs come from the catalog already loaded by the marketplace.
			// The model-aware completion below adds these values; this static
			// branch only handles the scope flag once an ID is present.
			if len(fields) == 3 && trailingSpace {
				values = []string{"--scope "}
			} else if len(fields) >= 4 && strings.EqualFold(fields[3], "--scope") {
				values = []string{"project", "global"}
			}
		}
	case "project":
		if len(fields) <= 2 || (len(fields) == 2 && trailingSpace) {
			values = []string{"scan", "details", "readme", "prs", "issues", "notion", "settings", "name ", "description ", "add ", "add-all", "remove "}
		}
	case "mcp":
		if len(fields) <= 2 {
			values = []string{"browse", "config "}
		}
	case "marketplace":
		if len(fields) <= 2 || (len(fields) == 2 && trailingSpace) {
			values = []string{"skills", "mcp"}
		}
	}
	if len(fields) == 2 && strings.HasPrefix(argPrefix, "-") {
		values = []string{"--help"}
	}
	items := make([]paletteSuggestion, 0, len(values))
	for _, value := range values {
		if argPrefix != "" && !strings.HasPrefix(strings.ToLower(value), argPrefixLower) {
			continue
		}
		text := command.name + " " + value
		if command.name == "resize" && len(fields) >= 2 && len(values) > 0 && (len(fields) > 2 || trailingSpace) {
			text = command.name + " " + fields[1] + " " + value
		}
		items = append(items, paletteSuggestion{text: text, help: command.help})
	}
	return items
}

func (m *Model) paletteSuggestions(input string) []paletteSuggestion {
	items := paletteSuggestions(input)
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) >= 2 {
		if cmd, ok := paletteCommandFor(fields[0]); ok && cmd.name == "project" {
			if strings.EqualFold(fields[1], "add") || strings.EqualFold(fields[1], "remove") || strings.EqualFold(fields[1], "rm") {
				return m.projectPaletteSuggestions(input, strings.ToLower(fields[1]))
			}
		}
	}
	if len(fields) < 2 || !strings.EqualFold(fields[0], "tasks") || !strings.EqualFold(fields[1], "cancel") {
		if len(fields) < 2 || !strings.EqualFold(fields[0], "skill") || !strings.EqualFold(fields[1], "install") {
			return items
		}
		return m.skillPaletteSuggestions(input)
	}
	items = nil
	prefix := ""
	if len(fields) > 2 {
		prefix = strings.ToLower(fields[2])
	}
	for _, summary := range m.taskSummaries {
		id := string(summary.ID)
		if id == "" || (prefix != "" && !strings.HasPrefix(strings.ToLower(id), prefix)) {
			continue
		}
		items = append(items, paletteSuggestion{text: "tasks cancel " + id, help: "cancel this task"})
	}
	if len(fields) == 2 {
		// The cancel verb is a prefix; never fabricate an ID. Real IDs are
		// offered when the task monitor has them, otherwise leave the prefix
		// available for manual entry.
		if len(items) == 0 {
			return []paletteSuggestion{{text: "tasks cancel ", help: "cancel a task by ID"}}
		}
	}
	return items
}

func paletteUsage(command paletteCommand) string {
	return "usage: :" + command.usage
}

func paletteUnknownUsage(input string) string {
	if input == "" {
		return "usage: :help (type :help for commands)"
	}
	return fmt.Sprintf("unknown command %q; type :help for commands", sanitizeDisplay(input))
}

func paletteResizeUsage() string {
	return "usage: :resize PANE N (files/viewer/chat 10-80%; terminal 10-60%)"
}

func paletteCommandHelp(command paletteCommand) string {
	example := ":" + command.name
	switch command.name {
	case "layout":
		example = ":layout conversation"
	case "resize":
		example = ":resize chat 60"
	case "tasks":
		example = ":tasks refresh"
	case "terminal":
		example = ":terminal off"
	case "skill":
		example = ":skill install openai-skill-creator --scope project"
	case "project":
		example = ":project scan"
	}
	return paletteUsage(command) + "\n" + command.help + "\nexample: " + example
}

func (m *Model) executePaletteCommand(input string) tea.Cmd {
	input = strings.TrimSpace(input)
	m.paletteError = ""
	if input == "" {
		m.openHelp()
		return nil
	}
	fields, err := CommandFields(input)
	if err != nil {
		m.paletteError = err.Error()
		m.status = m.paletteError
		return nil
	}
	if len(fields) == 0 {
		return nil
	}
	command, ok := paletteCommandFor(fields[0])
	if !ok {
		m.paletteError = paletteUnknownUsage(fields[0])
		m.status = m.paletteError
		return nil
	}
	args := fields[1:]
	usage := func() tea.Cmd {
		m.status = paletteUsage(command)
		m.paletteError = m.status
		return nil
	}
	if len(args) == 1 && strings.EqualFold(args[0], "--help") {
		m.paletteHelp = paletteCommandHelp(command)
		m.status = paletteUsage(command) + " · " + command.help
		return nil
	}
	if request, custom, err := ParseExtensionCommand(command.name, args); custom {
		if err != nil {
			m.paletteError = err.Error()
			m.status = m.paletteError
			return nil
		}
		return m.startExtension(request)
	}
	switch command.name {
	case "explain", "walkthrough":
		return m.walkthroughCommand(command.name, args)
	case "help":
		if len(args) == 1 {
			if target, ok := paletteCommandFor(args[0]); ok {
				m.paletteHelp = paletteCommandHelp(target)
				m.status = paletteUsage(target) + " · " + target.help
				return nil
			}
			m.paletteError = paletteUnknownUsage(args[0])
			m.status = m.paletteError
			return nil
		}
		if len(args) != 0 {
			return usage()
		}
		m.openHelp()
	case "save":
		if len(args) != 0 {
			return usage()
		}
		if m.mode == ModeInline {
			return m.saveInlineEditor()
		}
		m.status = "save is available in the inline editor"
	case "discard":
		if len(args) != 0 {
			return usage()
		}
		if m.mode == ModeInline {
			m.discardInlineEditor()
		} else {
			m.status = "discard is available in the inline editor"
		}
	case "review":
		if len(args) != 0 {
			return usage()
		}
		return m.startReview()
	case "terminal":
		if len(args) == 0 {
			m.openShellTab()
			return nil
		}
		sub := strings.ToLower(args[0])
		if len(args) == 1 && sub == "new" {
			return m.addShellTab()
		}
		if len(args) == 1 && (sub == "next" || sub == "prev") {
			m.focus = focusViewer
			if sub == "next" {
				m.switchViewerTab(1)
			} else {
				m.switchViewerTab(-1)
			}
			return nil
		}
		if len(args) != 1 || (sub != "on" && sub != "off") {
			return usage()
		}
		visible := sub == "on"
		if err := m.setTerminalVisible(visible); err != nil {
			m.paletteError = "terminal failed: " + sanitizeDisplay(err.Error())
			m.status = m.paletteError
		} else if visible {
			m.status = "shell"
		} else {
			m.status = "terminal hidden"
		}
	case "chat":
		if len(args) == 0 {
			m.focus, m.chatEditing = focusChat, true
			return nil
		}
		sub := strings.ToLower(args[0])
		switch sub {
		case "new", "open", "add":
			name := ""
			if len(args) > 1 {
				name = strings.Join(args[1:], " ")
			}
			return m.openNewChatTab(name)
		case "rename", "name", "title":
			if len(args) < 2 {
				m.startChatTabRename()
				return nil
			}
			m.renameActiveChatTab(strings.Join(args[1:], " "))
			return nil
		case "next":
			return m.switchChatTab(1)
		case "prev", "previous":
			return m.switchChatTab(-1)
		case "close":
			return m.closeActiveChatTab()
		default:
			return usage()
		}
	case "compact":
		if len(args) != 0 {
			return usage()
		}
		return m.startCompactSession()
	case "model":
		if len(args) == 0 {
			m.focus = focusChat
			return m.openChatModelPicker()
		}
		slug := strings.TrimSpace(args[0])
		if m.accountSelectModel != nil && m.accountLabel != "" {
			m.accountLoading = true
			m.accountID++
			id := m.accountID
			fn := m.accountSelectModel
			acct := m.accountLabel
			m.status = fmt.Sprintf("switching model to %s…", slug)
			return func() tea.Msg {
				choices, choice, events, err := fn(context.Background(), acct, slug)
				return accountSwitchMsg{id: id, choices: choices, choice: choice, events: events, err: err}
			}
		}
		m.connection.Model = slug
		m.status = "selected model " + sanitizeDisplay(slug)
		return nil
	case "accounts":
		if len(args) == 0 {
			m.openAccounts()
			return nil
		}
		if len(args) == 1 && strings.EqualFold(args[0], "add") {
			m.openAccounts()
			cmd := m.initAccountForm(0)
			m.status = "add account: Enter opens dropdowns · Tab/↑/↓ move fields · Ctrl+S save"
			return cmd
		}
		return usage()
	case "history":
		if len(args) == 0 {
			return m.startSessionsView()
		}
		if len(args) == 1 && strings.EqualFold(args[0], "new") {
			return m.startNewSession()
		}
		if len(args) == 1 {
			return m.startResumeSession(strings.TrimSpace(args[0]))
		}
		return usage()
	case "new":
		if len(args) != 0 {
			return usage()
		}
		return m.startNewSession()
	case "marketplace":
		if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "skills")) {
			return m.startMarketplaceTab(0)
		}
		if len(args) == 1 && (strings.EqualFold(args[0], "mcp") || strings.EqualFold(args[0], "servers")) {
			return m.startMCPMarketplace()
		}
		return usage()
	case "mcp":
		if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "browse")) {
			return m.startMCPMarketplace()
		}
		return usage()
	case "skill":
		return m.executeSkillCommand(args, usage)
	case "project":
		return m.executeProjectCommand(args, usage)
	case "editor":
		if len(args) != 0 {
			return usage()
		}
		return m.startInlineEditor()
	case "external":
		if len(args) != 0 {
			return usage()
		}
		return m.startEditor()
	case "tasks":
		if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "refresh")) {
			return m.startTaskMonitor()
		}
		if len(args) != 2 || !strings.EqualFold(args[0], "cancel") {
			return usage()
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			m.paletteError = "invalid task ID: " + sanitizeDisplay(args[1])
			m.status = m.paletteError
			return nil
		}
		if err := m.cancelTask(id); err != nil {
			m.paletteError = "task cancel failed: " + sanitizeDisplay(err.Error())
			m.status = m.paletteError
		} else {
			m.status = "task canceled"
			return m.startTaskMonitor()
		}
	case "layout":
		if len(args) != 1 {
			return usage()
		}
		if err := m.setLayout(strings.ToLower(args[0])); err != nil {
			m.paletteError = "layout failed: " + sanitizeDisplay(err.Error())
			m.status = m.paletteError
		}
	case "resize":
		if len(args) != 2 {
			return usage()
		}
		pane, value, err := parseResizeCommand(input)
		if err != nil {
			m.paletteError = paletteResizeUsage() + ": " + sanitizeDisplay(err.Error())
			m.status = m.paletteError
		} else if err := m.resizePane(pane, value); err != nil {
			m.paletteError = paletteResizeUsage() + ": " + sanitizeDisplay(err.Error())
			m.status = m.paletteError
		}
	case "settings", "config":
		if len(args) != 0 {
			return usage()
		}
		return m.openSettings()
	case "quit":
		if len(args) != 0 {
			return usage()
		}
		return tea.Quit
	default:
		return usage()
	}
	return nil
}

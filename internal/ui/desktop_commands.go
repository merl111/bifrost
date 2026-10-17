package ui

import (
	"bifrost/internal/tasks"
	"os/exec"
)

// CommandSpec exposes the one command catalog used by the TUI and native UI.
type CommandSpec struct {
	Name, Usage, Help string
	Aliases           []string
}

func Commands() []CommandSpec {
	out := make([]CommandSpec, 0, len(paletteCommands))
	for _, c := range paletteCommands {
		out = append(out, CommandSpec{c.name, c.usage, c.help, append([]string(nil), c.aliases...)})
	}
	return out
}
func Command(name string) (CommandSpec, bool) {
	c, ok := paletteCommandFor(name)
	return CommandSpec{c.name, c.usage, c.help, append([]string(nil), c.aliases...)}, ok
}
func LayoutPreset(name string) (LayoutSettings, bool) {
	l, ok := layoutFor(name)
	return LayoutSettings{Name: string(l.name), Files: l.proportions[0], Viewer: l.proportions[1], Chat: l.proportions[2], TerminalVisible: l.terminalVisible, TerminalPercent: l.terminalPercent}, ok
}

// ExternalEditorCommand uses the TUI's rooted path validation and argv parser.
func ExternalEditorCommand(root, path string, argv []string, remote bool) (*exec.Cmd, error) {
	if remote {
		return (&Model{remoteFS: true}).editorCommand(path)
	}
	tree, err := newFileTree(root)
	if err != nil {
		return nil, err
	}
	defer tree.close()
	return (&Model{tree: tree, editor: argv}).editorCommand(path)
}

func ResizeLayout(current LayoutSettings, pane string, value int) (LayoutSettings, error) {
	m := Model{layout: layoutState{name: LayoutName(current.Name), proportions: [3]int{current.Files, current.Viewer, current.Chat}, terminalPercent: current.TerminalPercent, terminalVisible: current.TerminalVisible}}
	if err := m.resizePane(pane, value); err != nil {
		return current, err
	}
	return m.layoutSettings(), nil
}

// CommandContext contains already-loaded data; completion performs no I/O.
type CommandContext struct {
	Tasks   []tasks.Summary
	Project ProjectStatus
	Catalog []MarketplaceEntry
}

func (c CommandContext) apply(m *Model) {
	m.taskSummaries, m.projectStatus, m.marketplaceEntries = c.Tasks, c.Project, c.Catalog
}
func CommandSuggestions(input string, context ...CommandContext) []string {
	m := &Model{}
	if len(context) > 0 {
		context[0].apply(m)
	}
	var result []string
	for _, s := range m.paletteSuggestions(input) {
		result = append(result, s.text)
	}
	return result
}

// CommandCompletion retains the TUI's forward/reverse Tab cycle.
type CommandCompletion struct{ model *Model }

func (c *CommandCompletion) Complete(input string, forward bool, context CommandContext) string {
	if c.model == nil {
		c.model = &Model{}
	}
	context.apply(c.model)
	c.model.paletteInput = input
	c.model.paletteComplete(forward)
	return c.model.paletteInput
}
func OpenExternalURL(url string) error { return openExternalURL(url) }

// ExecToolDetail is the shared command-output decoder used for terminal tabs.
func ExecToolDetail(detail string) (command, output string, exitCode int, hasExit bool) {
	return parseExecToolDetail(detail)
}

package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// ExtensionRequest describes an inert skill installation or a saved MCP config.
// Catalog skill installs keep the marketplace preview/install contract.
type ExtensionRequest struct{ Kind, Source, Ref, Directory, Scope string }
type ExtensionCommand func(context.Context, ExtensionRequest) (string, error)

func WithExtensions(run ExtensionCommand) Option { return func(m *Model) { m.extensions = run } }

// IsChatCommand leaves ordinary absolute paths as prompts. Colon commands and
// known slash commands use the same registry in both frontends.
func IsChatCommand(input string) bool {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, ":") {
		return true
	}
	fields := strings.Fields(input)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return false
	}
	_, ok := Command(strings.TrimPrefix(fields[0], "/"))
	return ok
}

// CommandFields supports quoted extension paths without shell expansion. Other
// commands retain their existing free-text semantics (including apostrophes).
func CommandFields(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, ":") || strings.HasPrefix(input, "/") {
		input = input[1:]
	}
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return nil, nil
	}
	spec, _ := Command(fields[0])
	if spec.Name != "skill" && spec.Name != "mcp" {
		return fields, nil
	}
	return quotedCommandFields(input)
}

func quotedCommandFields(input string) ([]string, error) {
	var result []string
	var word strings.Builder
	var quote rune
	started := false
	for _, r := range input {
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				result = append(result, word.String())
				word.Reset()
				started = false
			}
			continue
		}
		word.WriteRune(r)
		started = true
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote; wrap paths containing spaces in matching quotes")
	}
	if started {
		result = append(result, word.String())
	}
	return result, nil
}

// ParseExtensionCommand validates before any service callback can run. The bool
// distinguishes catalog/browse commands, which retain their existing handlers.
func ParseExtensionCommand(name string, args []string) (ExtensionRequest, bool, error) {
	request := ExtensionRequest{Scope: "project"}
	request.Kind = customExtensionKind(name, args)
	if request.Kind == "" {
		return request, false, nil
	}
	if len(args) >= 2 && args[len(args)-2] == "--scope" {
		request.Scope = args[len(args)-1]
		args = args[:len(args)-2]
	}
	if request.Scope != "project" && request.Scope != "global" {
		return request, true, fmt.Errorf("scope must be project or global")
	}
	switch {
	case request.Kind == "mcp-config":
		if len(args) > 2 {
			return request, true, fmt.Errorf("expected config [PATH] [--scope project|global]")
		}
		if len(args) == 2 {
			request.Source = args[1]
		}
	case request.Kind == "skill-git":
		if len(args) != 4 {
			return request, true, fmt.Errorf("expected install-git REPOSITORY REF SUBDIRECTORY [--scope project|global]")
		}
		request.Source, request.Ref, request.Directory = args[1], args[2], args[3]
	default:
		if len(args) != 2 {
			return request, true, fmt.Errorf("expected install PATH [--scope project|global]")
		}
		request.Source = args[1]
	}
	if request.Source == "" && len(args) > 1 || strings.HasPrefix(request.Source, "-") {
		return request, true, fmt.Errorf("a nonempty source is required")
	}
	return request, true, nil
}
func customExtensionKind(name string, args []string) string {
	if len(args) == 0 {
		return ""
	}
	switch {
	case name == "mcp" && strings.EqualFold(args[0], "config"):
		return "mcp-config"
	case name == "skill" && strings.EqualFold(args[0], "install-git"):
		return "skill-git"
	case name == "skill" && strings.EqualFold(args[0], "install"):
		if len(args) > 1 && localSkillPath(args[1]) {
			return "skill-local"
		}
	}
	return ""
}

func localSkillPath(source string) bool {
	return filepath.IsAbs(source) || source == "." || source == ".." || strings.HasPrefix(source, "./") || strings.HasPrefix(source, "../") || strings.HasPrefix(source, "~/")
}

type extensionResultMsg struct {
	text string
	err  error
}

func (m *Model) startExtension(request ExtensionRequest) tea.Cmd {
	if m.extensions == nil {
		m.paletteError = "custom extensions unavailable"
		m.status = m.paletteError
		return nil
	}
	if m.extensionCancel != nil {
		m.paletteError = "an extension operation is already running"
		m.status = m.paletteError
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.extensionCancel = cancel
	m.status = "Configuring extension…"
	run := m.extensions
	return func() tea.Msg { text, err := run(ctx, request); return extensionResultMsg{text, err} }
}
func (m *Model) updateExtension(msg extensionResultMsg) tea.Cmd {
	if m.extensionCancel != nil {
		m.extensionCancel()
		m.extensionCancel = nil
	}
	m.status = sanitizeDisplay(msg.text)
	if msg.err != nil {
		m.status = "Extension: " + sanitizeDisplay(msg.err.Error())
	}
	// Keep the result readable until dismissed instead of losing it to a stream update.
	m.paletteHelp = m.status
	m.paletteOpen = true
	return nil
}

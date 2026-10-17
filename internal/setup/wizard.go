// Package setup implements the first-run terminal onboarding flow.
package setup

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"bifrost/internal/config"
	"bifrost/internal/domain"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var ErrCancelled = errors.New("setup canceled; configuration was not changed")

type Result struct {
	Settings config.Settings
	Project  string
	Root     string
}
type Wizard struct {
	result                                                                             Result
	step, field, choice, width                                                         int
	kind, profile, model, url, keyEnv, account, executable, project, root, mcp, skills string
	permission                                                                         int
	browser, done, cancelled                                                           bool
	problem                                                                            string
}

var kinds = []string{"offline", "responses", "anthropic", "gemini", "chat-completions", "openrouter", "chatgpt", "cursor", "claude"}
var labels = []string{"Explore offline", "OpenAI · Responses", "Anthropic · Messages", "Google · Gemini", "Compatible API / local model", "OpenRouter · Models", "ChatGPT subscription · account login", "Cursor subscription · account login", "Claude subscription · account login"}
var titleSteps = []string{"Start", "Provider", "Workspace", "Permissions", "Tools", "Review"}
var defaults = map[string][2]string{"responses": {"https://api.openai.com/v1", "OPENAI_API_KEY"}, "anthropic": {"https://api.anthropic.com/v1", "ANTHROPIC_API_KEY"}, "gemini": {"https://generativelanguage.googleapis.com/v1beta", "GEMINI_API_KEY"}, "chat-completions": {"http://localhost:11434/v1", "BIFROST_API_KEY"}, "openrouter": {"https://openrouter.ai/api/v1", "OPENROUTER_API_KEY"}}

func subscriptionKind(kind string) bool {
	return kind == "chatgpt" || kind == "cursor" || kind == "claude"
}

func New(initial config.Settings, root string) *Wizard {
	w := &Wizard{result: Result{Settings: initial}, root: root, kind: "offline", width: 80, browser: initial.Defaults.Browser, mcp: initial.Defaults.MCPConfig, skills: strings.Join(initial.Defaults.Skills, ",")}
	w.project = filepath.Base(root)
	active := initial.ActiveProvider
	if active == "" {
		active = initial.Defaults.Provider
	}
	if p, ok := initial.Providers[active]; ok {
		w.profile = active
		w.kind = p.Kind
		w.model = p.Model
		w.url = p.BaseURL
		w.keyEnv = p.APIKeyEnv
		w.account = p.Account
		w.executable = p.Executable
		for i, k := range kinds {
			if k == p.Kind {
				w.choice = i
			}
		}
	}
	if initial.Defaults.AllowWrite {
		w.permission = 1
	}
	if initial.Defaults.AllowExec {
		w.permission = 2
	}
	return w
}
func Run(ctx context.Context, initial config.Settings, root string) (Result, error) {
	m, err := tea.NewProgram(New(initial, root), tea.WithContext(ctx)).Run()
	if err != nil {
		return Result{}, err
	}
	w := m.(*Wizard)
	if !w.done {
		return Result{}, ErrCancelled
	}
	return w.result, nil
}
func (w *Wizard) Init() tea.Cmd { return nil }
func (w *Wizard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		w.width = m.Width
	case tea.PasteMsg:
		w.insert(m.Content)
	case tea.KeyPressMsg:
		key := m.String()
		if key == "ctrl+c" || key == "esc" {
			w.cancelled = true
			return w, tea.Quit
		}
		if key == "shift+tab" {
			if w.field > 0 {
				w.field--
			} else if w.step > 0 {
				w.step--
				if w.kind == "offline" && w.step == 1 {
					w.step--
				}
			}
			w.problem = ""
			return w, nil
		}
		if key == "enter" {
			if w.step == 5 {
				if err := w.finish(); err != nil {
					w.problem = err.Error()
					return w, nil
				}
				return w, tea.Quit
			}
			w.next()
			return w, nil
		}
		if key == "tab" {
			w.field = (w.field + 1) % w.fieldCount()
			return w, nil
		}
		if w.step == 0 {
			if key == "up" || key == "k" {
				w.choice = (w.choice + len(kinds) - 1) % len(kinds)
			}
			if key == "down" || key == "j" {
				w.choice = (w.choice + 1) % len(kinds)
			}
			return w, nil
		}
		if w.step == 3 {
			if key == "up" || key == "k" {
				w.permission = (w.permission + 2) % 3
			}
			if key == "down" || key == "j" {
				w.permission = (w.permission + 1) % 3
			}
			return w, nil
		}
		if w.step == 4 && w.field == 0 {
			if key == "space" || key == " " {
				w.browser = !w.browser
			}
			return w, nil
		}
		if key == "backspace" {
			if target := w.target(); target != nil {
				r := []rune(*target)
				if len(r) > 0 {
					*target = string(r[:len(r)-1])
				}
			}
			return w, nil
		}
		if key == "ctrl+u" {
			if target := w.target(); target != nil {
				*target = ""
			}
			return w, nil
		}
		if m.Text != "" {
			w.insert(m.Text)
		}
	}
	return w, nil
}
func (w *Wizard) fieldCount() int {
	switch w.step {
	case 1:
		return 3
	case 2:
		return 2
	case 4:
		return 3
	}
	return 1
}
func (w *Wizard) target() *string {
	switch w.step {
	case 1:
		s := []*string{&w.model, &w.url, &w.keyEnv}
		return s[w.field%3]
	case 2:
		if w.field == 0 {
			return &w.project
		}
		return &w.root
	case 4:
		if w.field == 1 {
			return &w.mcp
		}
		if w.field == 2 {
			return &w.skills
		}
	}
	return nil
}
func (w *Wizard) insert(value string) {
	if target := w.target(); target != nil {
		value = strings.Map(func(r rune) rune {
			if r < 32 || r == 127 {
				return -1
			}
			return r
		}, value)
		if len(*target)+len(value) <= 4096 {
			*target += value
		}
	}
}
func (w *Wizard) next() {
	w.problem = ""
	if w.step == 0 {
		previousKind := w.kind
		w.kind = kinds[w.choice]
		if previousKind != w.kind {
			// A profile name is tied to its provider kind. Keep an existing
			// custom profile only while the user is reviewing that provider.
			w.profile = ""
		}
		if w.kind == "offline" {
			w.step = 2
			return
		}
		d := defaults[w.kind]
		if previousKind != w.kind {
			w.model = ""
			w.url = d[0]
			w.keyEnv = d[1]
			w.account = ""
			w.executable = ""
		}
		if subscriptionKind(w.kind) {
			if w.profile == "" || w.account == "" {
				w.problem = "Set up an account first with `bifrost account login`, then return here."
				return
			}
			w.step = 2
			return
		}
	}
	if w.step == 1 {
		p := config.ProviderSettings{Kind: w.kind, Model: w.model, BaseURL: w.url, APIKeyEnv: w.keyEnv}
		if err := p.Validate(); err != nil {
			w.problem = err.Error()
			return
		}
	}
	if w.step == 2 {
		if err := domain.ValidateName(w.project); err != nil {
			w.problem = "Choose a project name using letters, numbers, dots, underscores or hyphens."
			return
		}
		if !filepath.IsAbs(w.root) {
			w.problem = "Use an absolute workspace path."
			return
		}
	}
	w.step++
	w.field = 0
}
func (w *Wizard) finish() error {
	cfg := w.result.Settings
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderSettings{}
	}
	if w.kind != "offline" {
		name := w.profile
		if name == "" {
			name = w.kind
		}
		cfg.Providers[name] = config.ProviderSettings{Kind: w.kind, Model: strings.TrimSpace(w.model), BaseURL: strings.TrimSpace(w.url), APIKeyEnv: strings.TrimSpace(w.keyEnv), Account: strings.TrimSpace(w.account), Executable: strings.TrimSpace(w.executable)}
		cfg.ActiveProvider = name
		cfg.Defaults.Provider = name
	} else {
		cfg.ActiveProvider = ""
		cfg.Defaults.Provider = ""
	}
	cfg.Defaults.Project = w.project
	cfg.Defaults.AllowWrite = w.permission >= 1
	cfg.Defaults.AllowExec = w.permission == 2
	cfg.Defaults.Browser = w.browser
	cfg.Defaults.MCPConfig = strings.TrimSpace(w.mcp)
	cfg.Defaults.Skills = nil
	for _, s := range strings.Split(w.skills, ",") {
		if s = strings.TrimSpace(s); s != "" {
			cfg.Defaults.Skills = append(cfg.Defaults.Skills, s)
		}
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	w.result = Result{Settings: cfg, Project: w.project, Root: filepath.Clean(w.root)}
	w.done = true
	return nil
}
func (w *Wizard) View() tea.View {
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#66e5c5")).Bold(true)
	violet := lipgloss.NewStyle().Foreground(lipgloss.Color("#a78bfa"))
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("#8c9bb3"))
	quiet := lipgloss.NewStyle().Foreground(lipgloss.Color("#62718d"))
	title := []string{"Make yourself at home", "Connect your model", "Choose your workspace", "Choose agent permissions", "Add your tools", "Ready to build"}[w.step]
	var body strings.Builder
	body.WriteString(accent.Render("◈  BIFROST") + "  " + muted.Render("FIRST-RUN WORKSPACE SETUP") + "\n")
	body.WriteString(quiet.Render(strings.Repeat("─", 50)) + "\n\n")
	progress := make([]string, 0, 6)
	for i := range titleSteps {
		marker := "○"
		if i < w.step {
			marker = "●"
		}
		if i == w.step {
			marker = "◆"
		}
		progress = append(progress, marker)
	}
	body.WriteString(accent.Render(strings.Join(progress, "  ")) + "  " + muted.Render(fmt.Sprintf("step %d of 6", w.step+1)) + "\n\n")
	body.WriteString(violet.Render(title) + "\n")
	switch w.step {
	case 0:
		body.WriteString("Start with a provider, or explore files and terminals offline.\nSubscription profiles are configured with `bifrost account login`.\n\n")
		for i, label := range labels {
			prefix := "  "
			if i == w.choice {
				prefix = "› "
				label = accent.Render(label)
			}
			body.WriteString(prefix + label + "\n")
		}
	case 1:
		body.WriteString("Enter a model ID available on your account.\nCredentials stay in your environment; enter the variable name only.\n\n")
		w.form(&body, []string{"Model ID", "API base URL", "API key variable"}, []string{w.model, w.url, w.keyEnv}, accent)
	case 2:
		body.WriteString("A project can contain several independent repositories.\nYou can add more repositories later.\n\n")
		w.form(&body, []string{"Project name", "Workspace directory"}, []string{w.project, w.root}, accent)
	case 3:
		choices := []string{"Read and explain · no built-in writes or commands", "Edit files · revision-checked saves", "Develop · edits and host command execution"}
		for i, c := range choices {
			prefix := "  "
			if i == w.permission {
				prefix = "› "
				c = accent.Render(c)
			}
			body.WriteString(prefix + c + "\n")
		}
		body.WriteString("\nCommands use your account's filesystem and network permissions.")
	case 4:
		b := "off"
		if w.browser {
			b = "on"
		}
		w.form(&body, []string{"Browser tools (Space toggles)", "MCP config path (optional)", "Active installed skills (comma-separated)"}, []string{b, w.mcp, w.skills}, accent)
		body.WriteString("\nMCP and browser tools can have effects outside your files.\nThese options are saved only when you choose to enable them.")
	case 5:
		body.WriteString(fmt.Sprintf("Project     %s\nWorkspace   %s\nProvider    %s\nModel       %s\nAccount     %s\nBrowser     %t\n\n", w.project, w.root, w.kind, w.model, w.account, w.browser))
		body.WriteString("Enter saves your configuration and registers this project.\nNothing is downloaded and no model request is sent during setup.")
	}
	if w.problem != "" {
		body.WriteString("\n\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("#ff997f")).Render("!  "+w.problem))
	}
	body.WriteString("\n\n" + quiet.Render("Enter continue  ·  Tab next field  ·  Shift-Tab back\nCtrl-U clear field  ·  Esc cancel"))
	inner := max(24, min(max(w.width-10, 24), 84))
	cardWidth := max(30, min(max(w.width-6, 30), 88))
	content := lipgloss.NewStyle().Padding(1, 2).Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#405674")).Width(cardWidth).Render(ansi.Hardwrap(body.String(), inner, true))
	view := tea.NewView(content)
	view.AltScreen = true
	return view
}
func (w *Wizard) form(b *strings.Builder, labels, values []string, style lipgloss.Style) {
	for i, label := range labels {
		marker := "  "
		if i == w.field {
			marker = "› "
		}
		b.WriteString(marker + label + "\n")
		v := ansi.Strip(values[i])
		if i == w.field {
			v = style.Render(v + "▏")
		}
		b.WriteString("  " + v + "\n\n")
	}
}

package ui

import (
	"fmt"
	"strconv"
	"strings"
)

// LayoutName identifies a workspace arrangement. Layouts change only the
// presentation; they do not stop the PTY or discard any view state.
type LayoutName string

const (
	LayoutStudio       LayoutName = "studio"
	LayoutConversation LayoutName = "conversation"
	LayoutReview       LayoutName = "review"
	LayoutFocus        LayoutName = "focus"
)

// LayoutSettings is the complete persisted workspace presentation state.
// The UI owns its shape while the embedding application owns persistence.
type LayoutSettings struct {
	Name            string
	Files           int
	Viewer          int
	Chat            int
	TerminalPercent int
	TerminalVisible bool
}

type layoutState struct {
	name            LayoutName
	proportions     [3]int // files, viewer, chat; total is 100
	terminalVisible bool
	terminalPercent int
}

func studioLayout() layoutState {
	return layoutState{name: LayoutStudio, proportions: [3]int{22, 42, 36}, terminalVisible: true, terminalPercent: 25}
}

func layoutFor(name string) (layoutState, bool) {
	switch LayoutName(strings.ToLower(strings.TrimSpace(name))) {
	case LayoutStudio:
		return studioLayout(), true
	case LayoutConversation:
		return layoutState{name: LayoutConversation, proportions: [3]int{16, 30, 54}, terminalVisible: true, terminalPercent: 18}, true
	case LayoutReview:
		return layoutState{name: LayoutReview, proportions: [3]int{16, 60, 24}, terminalVisible: false, terminalPercent: 0}, true
	case LayoutFocus:
		return layoutState{name: LayoutFocus, proportions: [3]int{0, 0, 100}, terminalVisible: false, terminalPercent: 0}, true
	default:
		return layoutState{}, false
	}
}

func normalizeLayoutName(name string) (LayoutName, bool) {
	l, ok := layoutFor(name)
	return l.name, ok
}

// WithLayout selects the initial workspace preset. Unknown names use Studio.
func WithLayout(name string) Option {
	return func(m *Model) {
		if layout, ok := layoutFor(name); ok {
			m.layout = layout
		}
	}
}

// WithLayoutChange receives a normalized layout name whenever the user
// changes presets at runtime. The callback can persist the choice in the
// owning configuration layer without making the UI depend on config.
func WithLayoutChange(change func(string) error) Option {
	return func(m *Model) { m.layoutChange = change }
}

// WithLayoutSettingsChange receives the complete settings whenever a layout,
// pane size, or terminal visibility changes. Returning an error rolls the UI
// state back to its previous value.
func WithLayoutSettingsChange(change func(LayoutSettings) error) Option {
	return func(m *Model) { m.layoutSettingsChange = change }
}

// WithPaneProportions customizes the initial three-column split. Values are
// percentages and must be positive and sum to 100; invalid values are ignored.
func WithPaneProportions(files, viewer, chat int) Option {
	return func(m *Model) {
		if validProportions([3]int{files, viewer, chat}) {
			m.layout.proportions = [3]int{files, viewer, chat}
		}
	}
}

// WithTerminalVisible records the saved terminal preference. A shell opens as a
// viewer tab from `t`, `:terminal`, or an agent command, and hiding that tab
// leaves the process running.
func WithTerminalVisible(visible bool) Option {
	return func(m *Model) { m.layout.terminalVisible = visible }
}

// WithTerminalPercent customizes the initial terminal row percentage.
func WithTerminalPercent(percent int) Option {
	return func(m *Model) {
		if percent >= 10 && percent <= 60 {
			m.layout.terminalPercent = percent
		}
	}
}

// Layout reports the active preset name for embedding applications.
func (m *Model) Layout() string {
	if m == nil || m.layout.name == "" {
		return string(LayoutStudio)
	}
	return string(m.layout.name)
}

func validProportions(p [3]int) bool {
	return p[0] >= 10 && p[0] <= 80 && p[1] >= 10 && p[1] <= 80 && p[2] >= 10 && p[2] <= 80 && p[0]+p[1]+p[2] == 100
}

func (m *Model) activeLayout() layoutState {
	if m == nil || m.layout.name == "" {
		return studioLayout()
	}
	return m.layout
}

func (m *Model) layoutSettings() LayoutSettings {
	layout := m.activeLayout()
	return LayoutSettings{
		Name:            string(layout.name),
		Files:           layout.proportions[0],
		Viewer:          layout.proportions[1],
		Chat:            layout.proportions[2],
		TerminalPercent: layout.terminalPercent,
		TerminalVisible: layout.terminalVisible,
	}
}

func (m *Model) persistLayoutSettings() error {
	if m.layoutSettingsChange == nil {
		return nil
	}
	return m.layoutSettingsChange(m.layoutSettings())
}

func (m *Model) visibleFocuses() []focus {
	layout := m.activeLayout()
	if layout.name == LayoutFocus {
		return []focus{focusChat}
	}
	if layout.name == LayoutConversation {
		// Conversation keeps the transcript in the visual center and puts the
		// inspector on the right, matching the reading order of the workspace.
		return []focus{focusFiles, focusChat, focusViewer}
	}
	return []focus{focusFiles, focusViewer, focusChat}
}

func (m *Model) focusVisible(target focus) bool {
	for _, item := range m.visibleFocuses() {
		if item == target {
			return true
		}
	}
	return false
}

func (m *Model) normalizeFocus() {
	if m.focusVisible(m.focus) {
		return
	}
	visible := m.visibleFocuses()
	if len(visible) > 0 {
		m.focus = visible[0]
	}
	if m.mode == ModeTerminal && !m.viewerTabIsShell() {
		m.mode = ModeNormal
	}
}

func (m *Model) moveFocus(delta int) {
	visible := m.visibleFocuses()
	if len(visible) == 0 {
		return
	}
	index := 0
	for i, item := range visible {
		if item == m.focus {
			index = i
			break
		}
	}
	index = (index + delta + len(visible)) % len(visible)
	m.focus = visible[index]
}

func (m *Model) setLayout(name string) error {
	layout, ok := layoutFor(name)
	if !ok {
		return fmt.Errorf("unknown layout %q (choose studio, conversation, review, or focus)", name)
	}
	previous := m.layout
	previousFocus, previousMode := m.focus, m.mode
	m.layout = layout
	m.normalizeFocus()
	if m.layoutSettingsChange != nil {
		if err := m.persistLayoutSettings(); err != nil {
			m.layout = previous
			m.focus, m.mode = previousFocus, previousMode
			return err
		}
	} else if m.layoutChange != nil {
		if err := m.layoutChange(string(layout.name)); err != nil {
			m.layout = previous
			m.focus, m.mode = previousFocus, previousMode
			return err
		}
	}
	m.resizeShells()
	m.status = "layout: " + string(layout.name)
	return nil
}

func (m *Model) resizePane(name string, value int) error {
	name = strings.ToLower(strings.TrimSpace(name))
	previous := m.layout
	if name == "terminal" || name == "term" {
		if value < 10 || value > 60 {
			return fmt.Errorf("terminal size must be between 10 and 60 percent")
		}
		m.layout.terminalPercent = value
		if err := m.persistLayoutSettings(); err != nil {
			m.layout = previous
			return err
		}
		m.resizeShells()
		m.status = fmt.Sprintf("terminal: %d%%", value)
		return nil
	}
	index := map[string]int{"files": 0, "file": 0, "viewer": 1, "review": 1, "chat": 2, "conversation": 2}[name]
	if name != "files" && name != "file" && name != "viewer" && name != "review" && name != "chat" && name != "conversation" {
		return fmt.Errorf("unknown pane %q (choose files, viewer, chat, or terminal)", name)
	}
	if value < 10 || value > 80 {
		return fmt.Errorf("pane size must be between 10 and 80 percent")
	}
	other := 100 - value
	left := other / 2
	right := other - left
	if index == 0 {
		m.layout.proportions = [3]int{value, left, right}
	} else if index == 1 {
		m.layout.proportions = [3]int{left, value, right}
	} else {
		m.layout.proportions = [3]int{left, right, value}
	}
	if !validProportions(m.layout.proportions) {
		m.layout = previous
		return fmt.Errorf("pane sizes must leave at least 10 percent for each pane")
	}
	if err := m.persistLayoutSettings(); err != nil {
		m.layout = previous
		return err
	}
	m.resizeShells()
	m.status = fmt.Sprintf("%s: %d%%", name, value)
	return nil
}

func (m *Model) setTerminalVisible(visible bool) error {
	previous := m.layout
	previousShell, previousTab, previousMode, previousFocus := m.shellOpen, m.viewerTab, m.mode, m.focus
	wasShell := m.viewerTabIsShell()
	m.layout.terminalVisible = visible
	m.shellOpen = visible
	if visible && m.layout.terminalPercent == 0 {
		m.layout.terminalPercent = 25
	}
	if visible {
		m.viewerTab = 1
		m.focus = focusViewer
	} else if wasShell {
		m.viewerTab = 0
		if m.mode == ModeTerminal {
			m.mode = ModeNormal
		}
	}
	m.normalizeFocus()
	if err := m.persistLayoutSettings(); err != nil {
		m.layout = previous
		m.shellOpen, m.viewerTab, m.mode, m.focus = previousShell, previousTab, previousMode, previousFocus
		m.normalizeFocus()
		return err
	}
	m.resizeShells()
	return nil
}

func parseResizeCommand(command string) (string, int, error) {
	parts := strings.Fields(command)
	if len(parts) != 3 {
		return "", 0, fmt.Errorf("usage: resize [files|viewer|chat|terminal] N")
	}
	value, err := strconv.Atoi(parts[2])
	if err != nil {
		return "", 0, fmt.Errorf("size must be a percentage")
	}
	return parts[1], value, nil
}

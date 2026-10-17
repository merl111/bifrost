package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// SettingsDocument is an editable snapshot; Data is owned by the configuration
// adapter, while the UI only edits the described fields. Revision prevents an
// old draft from overwriting changes made by another process.
type SettingsDocument struct {
	Path, Revision, Data string
	Fields               []SettingsField
}

type SettingsField struct{ Key, Value, Kind, Description string }

type SettingsCallbacks struct {
	Load func() (SettingsDocument, error)
	Edit func(SettingsDocument, string, string) (SettingsDocument, error)
	Save func(SettingsDocument) (SettingsDocument, []Option, error)
}

func WithSettings(callbacks SettingsCallbacks) Option {
	return func(m *Model) { m.settingsCallbacks = callbacks }
}

type settingsEditorState struct {
	document                                            SettingsDocument
	selected, offset, cursor                            int
	query, input, err                                   string
	loading, saving, editing, searching, dirty, discard bool
	id                                                  uint64
	help                                                bool
	helpOffset                                          int
}

type settingsLoadedMsg struct {
	id       uint64
	document SettingsDocument
	err      error
}
type settingsSavedMsg struct {
	id       uint64
	document SettingsDocument
	apply    []Option
	err      error
}

func (m *Model) openSettings() tea.Cmd {
	if m.settingsCallbacks.Load == nil {
		m.status = "settings editor unavailable"
		return nil
	}
	m.settingsOpen = true
	m.settingsEditor = settingsEditorState{id: m.settingsEditor.id + 1, loading: true}
	id, load := m.settingsEditor.id, m.settingsCallbacks.Load
	m.status = "loading settings"
	return func() tea.Msg { doc, err := load(); return settingsLoadedMsg{id, doc, err} }
}

func (m *Model) updateSettingsLoaded(msg settingsLoadedMsg) {
	if !m.settingsOpen || msg.id != m.settingsEditor.id {
		return
	}
	s := &m.settingsEditor
	s.loading = false
	if msg.err != nil {
		s.err = msg.err.Error()
		return
	}
	s.document = msg.document
	m.status = "settings · / search · Enter edit · Ctrl-S save"
}

func (m *Model) settingsFields() []SettingsField {
	s := &m.settingsEditor
	query := strings.ToLower(s.query)
	var fields []SettingsField
	for _, field := range s.document.Fields {
		if strings.Contains(strings.ToLower(field.Key+" "+field.Description), query) {
			fields = append(fields, field)
		}
	}
	return fields
}

func (m *Model) settingsSelected() (SettingsField, bool) {
	fields := m.settingsFields()
	s := &m.settingsEditor
	if len(fields) == 0 {
		return SettingsField{}, false
	}
	s.selected = maxInt(0, minInt(s.selected, len(fields)-1))
	return fields[s.selected], true
}

func (m *Model) stageSetting() bool {
	s := &m.settingsEditor
	field, ok := m.settingsSelected()
	if !ok || m.settingsCallbacks.Edit == nil {
		return false
	}
	doc, err := m.settingsCallbacks.Edit(s.document, field.Key, s.input)
	if err != nil {
		s.err = err.Error()
		return false
	}
	s.document = doc
	s.editing, s.dirty, s.discard = false, true, false
	s.err = ""
	m.status = "change staged · Ctrl-S saves to config"
	return true
}

func (m *Model) saveSettings() tea.Cmd {
	s := &m.settingsEditor
	if s.loading || s.saving || m.settingsCallbacks.Save == nil {
		return nil
	}
	if s.editing && !m.stageSetting() {
		return nil
	}
	if !s.dirty {
		m.status = "settings unchanged"
		return nil
	}
	s.saving, s.err = true, ""
	id, doc, save := s.id, s.document, m.settingsCallbacks.Save
	return func() tea.Msg { doc, apply, err := save(doc); return settingsSavedMsg{id, doc, apply, err} }
}

func (m *Model) updateSettingsSaved(msg settingsSavedMsg) {
	s := &m.settingsEditor
	if !m.settingsOpen || msg.id != s.id {
		return
	}
	s.saving = false
	if msg.err != nil {
		s.err = msg.err.Error()
		return
	}
	s.document, s.dirty, s.discard = msg.document, false, false
	atBottom := m.chatAtBottom()
	for _, option := range msg.apply {
		if option != nil {
			option(m)
		}
	}
	m.normalizeFocus()
	if atBottom {
		m.chatScroll = m.chatScrollMax()
	} else {
		m.clampChatScroll()
	}
	m.resizeShells()
	m.status = "settings saved · appearance applied · restart for runtime changes"
}

func (m *Model) settingsKey(stroke, text string) tea.Cmd {
	s := &m.settingsEditor
	if s.loading || s.saving {
		return nil
	}
	if s.help {
		switch stroke {
		case "esc", "escape", "?":
			s.help = false
		case "up", "k":
			s.helpOffset = maxInt(s.helpOffset-1, 0)
		case "down", "j":
			s.helpOffset++
		case "pgup":
			s.helpOffset = maxInt(s.helpOffset-maxInt(m.height-6, 1), 0)
		case "pgdown":
			s.helpOffset += maxInt(m.height-6, 1)
		}
		return nil
	}
	if stroke == "ctrl+s" {
		return m.saveSettings()
	}
	if s.editing {
		m.settingsEditKey(stroke, text)
		return nil
	}
	if s.searching {
		switch stroke {
		case "esc", "escape", "enter", "return":
			s.searching = false
		case "backspace":
			s.query = removeLastSettingRune(s.query)
		case "ctrl+u":
			s.query = ""
		default:
			if len(s.query)+len(text) <= 256 {
				s.query += sanitizeDisplay(text)
			}
		}
		s.selected, s.offset = 0, 0
		return nil
	}
	if stroke != "esc" && stroke != "escape" {
		s.discard = false
	}
	switch stroke {
	case "?":
		s.help, s.helpOffset = true, 0
	case "esc", "escape":
		if s.dirty && !s.discard {
			s.discard = true
			return nil
		}
		m.settingsOpen = false
		m.status = "settings closed"
	case "/":
		s.searching = true
	case "up", "k":
		s.selected--
	case "down", "j":
		s.selected++
	case "pgup":
		s.selected -= m.settingsListHeight()
	case "pgdown":
		s.selected += m.settingsListHeight()
	case "home", "g":
		s.selected = 0
	case "end", "G":
		s.selected = len(m.settingsFields()) - 1
	case "r":
		if s.dirty {
			s.err = "Save changes first, or Esc twice to discard and reopen settings."
		} else {
			return m.openSettings()
		}
	case "enter", "return", "space":
		if field, ok := m.settingsSelected(); ok {
			s.input, s.cursor, s.err = field.Value, len([]rune(field.Value)), ""
			if stroke == "space" && field.Kind == "bool" {
				if field.Value == "true" {
					s.input = "false"
				} else {
					s.input = "true"
				}
				m.stageSetting()
			} else {
				s.editing = true
			}
		}
	}
	m.settingsSelected()
	return nil
}

func removeLastSettingRune(text string) string {
	r := []rune(text)
	if len(r) == 0 {
		return ""
	}
	return string(r[:len(r)-1])
}

func (m *Model) settingsEditKey(stroke, text string) {
	s := &m.settingsEditor
	r := []rune(s.input)
	s.cursor = maxInt(0, minInt(s.cursor, len(r)))
	switch stroke {
	case "esc", "escape":
		s.editing, s.err = false, ""
		return
	case "enter", "return":
		m.stageSetting()
		return
	case "shift+enter", "alt+enter", "ctrl+j":
		text = "\n"
	case "left":
		s.cursor = maxInt(0, s.cursor-1)
		return
	case "right":
		s.cursor = minInt(len(r), s.cursor+1)
		return
	case "up", "down":
		s.cursor = settingVerticalCursor(r, s.cursor, stroke == "down")
		return
	case "home", "ctrl+a":
		s.cursor = 0
		return
	case "end", "ctrl+e":
		s.cursor = len(r)
		return
	case "ctrl+u":
		s.input, s.cursor = "", 0
		return
	case "backspace":
		if s.cursor > 0 {
			r = append(r[:s.cursor-1], r[s.cursor:]...)
			s.cursor--
		}
		s.input = string(r)
		return
	case "delete":
		if s.cursor < len(r) {
			r = append(r[:s.cursor], r[s.cursor+1:]...)
		}
		s.input = string(r)
		return
	}
	if text != "" {
		m.insertSettingText(text)
	}
}

func settingVerticalCursor(text []rune, cursor int, down bool) int {
	start := cursor
	for start > 0 && text[start-1] != '\n' {
		start--
	}
	column := cursor - start
	end := cursor
	for end < len(text) && text[end] != '\n' {
		end++
	}
	if down {
		if end == len(text) {
			return cursor
		}
		start = end + 1
		end = start
		for end < len(text) && text[end] != '\n' {
			end++
		}
	} else {
		if start == 0 {
			return cursor
		}
		end = start - 1
		start = end
		for start > 0 && text[start-1] != '\n' {
			start--
		}
	}
	return minInt(start+column, end)
}

func (m *Model) insertSettingText(text string) {
	s := &m.settingsEditor
	text = sanitizeDisplay(strings.ReplaceAll(text, "\r\n", "\n"))
	if len(s.input)+len(text) > 1<<20 {
		s.err = "Setting exceeds 1 MiB limit"
		return
	}
	r := []rune(s.input)
	s.cursor = maxInt(0, minInt(s.cursor, len(r)))
	s.input = string(r[:s.cursor]) + text + string(r[s.cursor:])
	s.cursor += len([]rune(text))
}

func (m *Model) settingsPaste(text string) {
	if m.settingsEditor.editing && !m.settingsEditor.saving {
		m.insertSettingText(text)
	}
}

func (m *Model) settingsListHeight() int { return maxInt((m.height-12)/2, 1) }

func (m *Model) settingsMouse(mouse tea.Mouse, wheel bool) {
	s := &m.settingsEditor
	if s.editing || s.loading || s.saving || s.help {
		return
	}
	if wheel {
		if mouse.Button == tea.MouseWheelUp {
			s.selected -= 3
		}
		if mouse.Button == tea.MouseWheelDown {
			s.selected += 3
		}
	} else if mouse.Button == tea.MouseLeft && mouse.Y >= 5 && mouse.Y < 5+m.settingsListHeight() {
		s.selected = s.offset + mouse.Y - 5
	}
	m.settingsSelected()
}

func (m *Model) renderSettingsPanel(width, height int) string {
	s, p := &m.settingsEditor, m.colors()
	if s.help {
		return m.renderSettingHelp(width, height)
	}
	inner := maxInt(width-4, 1)
	title := "SETTINGS"
	if s.dirty {
		title += " · unsaved"
	}
	lines := []string{p.quietText(ansi.Truncate(sanitizeDisplay(s.document.Path), inner, "…"))}
	fields := m.settingsFields()
	search := fmt.Sprintf("Search /  %s  ·  %d matches", sanitizeDisplay(s.query), len(fields))
	if s.searching {
		search += "▏"
	}
	lines = append(lines, p.cyanText(search), "")
	field, selected := m.settingsSelected()
	listHeight := m.settingsListHeight()
	if s.selected < s.offset {
		s.offset = s.selected
	}
	if s.selected >= s.offset+listHeight {
		s.offset = s.selected - listHeight + 1
	}
	s.offset = maxInt(0, minInt(s.offset, maxInt(len(fields)-listHeight, 0)))
	for i := 0; i < listHeight; i++ {
		index := s.offset + i
		line := ""
		if index < len(fields) {
			f := fields[index]
			value := strings.ReplaceAll(sanitizeDisplay(f.Value), "\n", " ")
			keyWidth := minInt(36, maxInt(inner/2, 1))
			line = "  " + truncatePad(sanitizeDisplay(f.Key), keyWidth) + "  " + ansi.Truncate(value, maxInt(inner-keyWidth-4, 1), "…")
			if index == s.selected {
				line = p.cyanText("› " + strings.TrimPrefix(line, "  "))
			}
		}
		lines = append(lines, line)
	}
	lines = append(lines, "")
	if selected {
		lines = append(lines, p.accentText(sanitizeDisplay(field.Key+" · "+field.Kind)))
		description := wrapChatText(field.Description, inner)
		for _, row := range description[:minInt(2, len(description))] {
			lines = append(lines, p.mutedText(row))
		}
	}
	message := m.settingsMessage()
	messageRows := wrapChatText(message, inner)
	messageRows = messageRows[:minInt(len(messageRows), 4)]
	hints := []string{"↑↓ select · Enter edit · Space toggle · / search", "Ctrl-S save · r reload · ? help · Esc close"}
	if s.editing {
		hints = []string{"Enter stage · Ctrl-S save · Ctrl-U clear · Esc cancel"}
	}
	bodyBudget := maxInt(height-2-len(hints)-len(messageRows), 1)
	if s.editing {
		lines = lines[:minInt(len(lines), maxInt(bodyBudget-1, 0))]
		r := []rune(s.input)
		cursor := maxInt(0, minInt(s.cursor, len(r)))
		before := string(r[:cursor])
		rows := wrapChatText(before+"▏"+string(r[cursor:]), inner)
		cursorRow := len(wrapChatText(before+"▏", inner)) - 1
		budget := maxInt(bodyBudget-len(lines), 1)
		start := maxInt(cursorRow-budget+1, 0)
		end := minInt(start+budget, len(rows))
		lines = append(lines, rows[start:end]...)
	} else {
		lines = lines[:minInt(len(lines), bodyBudget)]
	}
	for len(lines) < bodyBudget {
		lines = append(lines, "")
	}
	for _, hint := range hints {
		lines = append(lines, p.quietText(hint))
	}
	for _, row := range messageRows {
		lines = append(lines, p.warnText(row))
	}
	return renderPanelTheme(m.theme, title, strings.Join(lines, "\n"), width, true, height)
}

func (m *Model) settingsMessage() string {
	s := &m.settingsEditor
	message := "Appearance applies on save. Runtime settings need a restart."
	if s.loading {
		message = "Loading settings…"
	}
	if s.saving {
		message = "Saving settings…"
	}
	if len(m.settingsFields()) == 0 && !s.loading {
		message = "No matching settings. Press / then Ctrl-U to clear search."
	}
	if s.discard {
		message = "Unsaved changes: Esc again discards; Ctrl-S saves."
	}
	if s.err != "" {
		message = s.err
	}
	return message
}

func (m *Model) renderSettingHelp(width, height int) string {
	s, p := &m.settingsEditor, m.colors()
	field, _ := m.settingsSelected()
	text := field.Key + "\n\n" + field.Description + "\n\nCurrent value:\n" + field.Value
	if s.err != "" {
		text += "\n\n" + s.err
	}
	rows := wrapChatText(text, maxInt(width-4, 1))
	budget := maxInt(height-4, 1)
	s.helpOffset = maxInt(0, minInt(s.helpOffset, maxInt(len(rows)-budget, 0)))
	lines := append([]string(nil), rows[s.helpOffset:minInt(s.helpOffset+budget, len(rows))]...)
	lines = append(lines, "", p.quietText("↑↓ / PgUp PgDn scroll · Esc back"))
	return renderPanelTheme(m.theme, "SETTING HELP", strings.Join(lines, "\n"), width, true, height)
}

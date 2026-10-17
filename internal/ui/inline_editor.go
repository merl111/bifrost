package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

const maxInlineDocumentBytes = 2 << 20

// EditorDocument is the revisioned text passed between the UI and workspace
// service. The saver must reject a stale Revision rather than overwriting a
// file changed by another process.
type EditorDocument struct {
	Path     string
	Content  string
	Revision string
}

// EditorLoader loads one workspace-relative text file.
type EditorLoader func(context.Context, string) (EditorDocument, error)

// EditorSaver stores a document if its Revision still matches the workspace.
// It returns the new document revision on success.
type EditorSaver func(context.Context, EditorDocument) (EditorDocument, error)

// WithInlineEditor connects revision-aware load/save operations. Nil callbacks
// use the UI's rooted local file implementation.
func WithInlineEditor(loader EditorLoader, saver EditorSaver) Option {
	return func(m *Model) {
		m.inlineLoader, m.inlineSaver = loader, saver
	}
}

// WithDraftStateDir enables bounded durable drafts. A matching draft is
// restored when a file is opened; stale drafts are kept out of the save path.
func WithDraftStateDir(dir string) Option {
	return func(m *Model) { m.draftStateDir = dir }
}

type inlineEditorState struct {
	doc       EditorDocument
	content   []rune
	cursor    int
	goalCol   int
	insert    bool
	dirty     bool
	saving    bool
	undo      []inlineSnapshot
	redo      []inlineSnapshot
	search    string
	searching bool
}

type inlineSnapshot struct {
	content []rune
	cursor  int
}

type inlineLoadedMsg struct {
	id    uint64
	doc   EditorDocument
	draft *draftRecord
	err   error
}

type inlineSavedMsg struct {
	id  uint64
	doc EditorDocument
	err error
}

type draftRecord struct {
	Path     string `json:"path"`
	Revision string `json:"revision"`
	Content  string `json:"content"`
}

func (m *Model) startInlineEditor() tea.Cmd {
	path, err := m.editorTarget()
	if err != nil {
		m.status = "inline editor unavailable: " + err.Error()
		return nil
	}
	loader := m.inlineLoader
	if loader == nil {
		loader = m.defaultEditorLoad
	}
	if m.inlineCancel != nil {
		m.inlineCancel()
	}
	m.inlineID++
	id := m.inlineID
	ctx, cancel := context.WithCancel(context.Background())
	m.inlineCancel = cancel
	m.status = "loading editor"
	return func() tea.Msg {
		doc, loadErr := loader(ctx, path)
		if loadErr != nil {
			return inlineLoadedMsg{id: id, err: loadErr}
		}
		var draft *draftRecord
		if candidate, draftErr := m.readDraft(path); draftErr == nil && candidate.Path == path && candidate.Revision == doc.Revision {
			draft = candidate
		}
		return inlineLoadedMsg{id: id, doc: doc, draft: draft}
	}
}

func (m *Model) handleInlineLoaded(msg inlineLoadedMsg) tea.Cmd {
	if msg.id != m.inlineID {
		return nil
	}
	m.inlineCancel = nil
	if msg.err != nil {
		m.status = "inline editor failed: " + msg.err.Error()
		return nil
	}
	content := msg.doc.Content
	status := "inline editor: Normal mode"
	if msg.draft != nil {
		content = msg.draft.Content
		status = "draft restored; inline editor: Normal mode"
	}
	m.inline = &inlineEditorState{doc: msg.doc, content: []rune(content), dirty: msg.draft != nil}
	m.mode, m.focus = ModeInline, focusViewer
	m.viewerOffset = 0
	m.status = status
	m.writeDraft()
	return nil
}

func (m *Model) saveInlineEditor() tea.Cmd {
	if m.inline == nil || m.inline.saving {
		return nil
	}
	if !m.inline.dirty {
		m.status = "inline editor: no changes"
		return nil
	}
	saver := m.inlineSaver
	if saver == nil {
		saver = m.defaultEditorSave
	}
	m.inline.saving = true
	m.inlineID++
	id := m.inlineID
	doc := m.inline.doc
	doc.Content = string(m.inline.content)
	ctx, cancel := context.WithCancel(context.Background())
	m.inlineCancel = cancel
	m.status = "saving inline editor"
	return func() tea.Msg {
		updated, err := saver(ctx, doc)
		return inlineSavedMsg{id: id, doc: updated, err: err}
	}
}

func (m *Model) handleInlineSaved(msg inlineSavedMsg) tea.Cmd {
	if msg.id != m.inlineID || m.inline == nil {
		return nil
	}
	m.inline.saving = false
	m.inlineCancel = nil
	if msg.err != nil {
		m.status = "inline save failed: " + msg.err.Error()
		return nil
	}
	oldContent := msg.doc.Content
	m.inline.doc = msg.doc
	if string(m.inline.content) == oldContent {
		m.inline.dirty = false
		m.removeDraft(m.inline.doc.Path)
		m.status = "saved " + m.inline.doc.Path
	} else {
		m.status = "saved previous changes; newer edits remain"
		m.writeDraft()
	}
	m.viewer = string(m.inline.content)
	m.viewPath = m.inline.doc.Path
	return nil
}

func (m *Model) discardInlineEditor() {
	if m.inline == nil {
		return
	}
	path := m.inline.doc.Path
	m.inline = nil
	m.mode = ModeNormal
	m.removeDraft(path)
	m.status = "inline changes discarded"
}

func (m *Model) leaveInlineEditor() bool {
	if m.inline == nil {
		m.mode = ModeNormal
		return true
	}
	if m.inline.dirty {
		m.status = "unsaved changes: Ctrl-S saves, d discards"
		return false
	}
	m.inline = nil
	m.mode = ModeNormal
	m.status = "inline editor closed"
	return true
}

func (m *Model) inlineEditorKey(msg tea.KeyPressMsg) tea.Cmd {
	e := m.inline
	if e == nil {
		m.mode = ModeNormal
		return nil
	}
	stroke, text := msg.String(), msg.Text
	if e.searching {
		return m.inlineSearchKey(stroke, text)
	}
	if e.insert {
		return m.inlineInsertKey(stroke, text)
	}
	return m.inlineNormalKey(stroke)
}

func (m *Model) inlineInsertKey(stroke, text string) tea.Cmd {
	e := m.inline
	switch stroke {
	case "escape", "esc":
		e.insert = false
		m.status = "inline editor: Normal mode"
	case "ctrl+s":
		return m.saveInlineEditor()
	case ":", "ctrl+p":
		e.searching = false
		m.paletteOpen = true
		m.paletteInput = ""
		m.paletteCompletion = paletteCompletionState{}
		m.paletteError = ""
		m.paletteHelp = ""
		m.status = "command palette"
	case "?":
		m.openHelp()
	case "ctrl+z":
		m.inlineUndo()
	case "ctrl+r":
		m.inlineRedo()
	case "backspace":
		if e.cursor > 0 {
			m.inlineEdit(e.cursor-1, e.cursor, nil)
		}
	case "delete":
		if e.cursor < len(e.content) {
			m.inlineEdit(e.cursor, e.cursor+1, nil)
		}
	case "enter", "return":
		m.inlineEdit(e.cursor, e.cursor, []rune{'\n'})
	default:
		if text != "" {
			runes := []rune(text)
			filtered := runes[:0]
			for _, r := range runes {
				if r == '\n' || r == '\t' || !unicode.IsControl(r) {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) > 0 {
				m.inlineEdit(e.cursor, e.cursor, filtered)
			}
		}
	}
	m.inlineEnsureCursorVisible()
	return nil
}

func (m *Model) inlineNormalKey(stroke string) tea.Cmd {
	e := m.inline
	switch stroke {
	case "i":
		e.insert = true
		m.status = "inline editor: Insert mode"
	case "a":
		if e.cursor < len(e.content) {
			e.cursor++
		}
		e.insert = true
	case "o":
		m.inlineEdit(e.lineEnd(e.cursor), e.lineEnd(e.cursor), []rune{'\n'})
		e.insert = true
	case "O":
		m.inlineEdit(e.lineStart(e.cursor), e.lineStart(e.cursor), []rune{'\n'})
		e.insert = true
	case "h", "left":
		if e.cursor > 0 {
			e.cursor--
		}
	case "l", "right":
		if e.cursor < len(e.content) {
			e.cursor++
		}
	case "j", "down":
		e.moveVertical(1)
	case "k", "up":
		e.moveVertical(-1)
	case "w":
		e.moveWord(1)
	case "b":
		e.moveWord(-1)
	case "0", "home":
		e.cursor = e.lineStart(e.cursor)
	case "$", "end":
		e.cursor = e.lineEnd(e.cursor)
	case "G":
		e.cursor = len(e.content)
	case "g":
		// A second g is intentionally unnecessary for a useful small editor:
		// g moves to the beginning, while G moves to the end.
		e.cursor = 0
	case "x", "delete":
		if e.cursor < len(e.content) {
			m.inlineEdit(e.cursor, e.cursor+1, nil)
		}
	case "u", "ctrl+z":
		m.inlineUndo()
	case "ctrl+r":
		m.inlineRedo()
	case "ctrl+s":
		return m.saveInlineEditor()
	case "/":
		e.searching = true
		e.search = ""
		m.status = "search: "
	case "d":
		m.discardInlineEditor()
	case "q", "escape", "esc":
		m.leaveInlineEditor()
	}
	m.inlineEnsureCursorVisible()
	return nil
}

func (m *Model) inlineSearchKey(stroke, text string) tea.Cmd {
	e := m.inline
	switch stroke {
	case "escape", "esc":
		e.searching = false
		m.status = "inline editor: Normal mode"
	case "enter", "return":
		e.searching = false
		m.inlineFindNext()
	case "backspace":
		r := []rune(e.search)
		if len(r) > 0 {
			e.search = string(r[:len(r)-1])
		}
		m.inlineFindNext()
	default:
		if text != "" {
			e.search += text
			m.inlineFindNext()
		}
	}
	if e.searching {
		m.status = "search: " + sanitizeDisplay(e.search)
	}
	return nil
}

func (m *Model) inlineEdit(start, end int, insert []rune) {
	e := m.inline
	if e == nil || start < 0 || end < start || end > len(e.content) || len(e.content)+len(insert)-(end-start) > maxInlineDocumentBytes {
		return
	}
	e.undo = appendBoundedSnapshots(e.undo, inlineSnapshot{content: append([]rune(nil), e.content...), cursor: e.cursor})
	e.redo = nil
	content := make([]rune, 0, len(e.content)-(end-start)+len(insert))
	content = append(content, e.content[:start]...)
	content = append(content, insert...)
	content = append(content, e.content[end:]...)
	e.content, e.cursor, e.dirty = content, start+len(insert), true
	m.writeDraft()
}

func appendBoundedSnapshots(items []inlineSnapshot, item inlineSnapshot) []inlineSnapshot {
	items = append(items, item)
	if len(items) > 128 {
		items = items[len(items)-128:]
	}
	return items
}

func (m *Model) inlineUndo() {
	e := m.inline
	if e == nil || len(e.undo) == 0 {
		return
	}
	e.redo = appendBoundedSnapshots(e.redo, inlineSnapshot{content: append([]rune(nil), e.content...), cursor: e.cursor})
	snapshot := e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	e.content, e.cursor, e.dirty = append([]rune(nil), snapshot.content...), snapshot.cursor, true
	m.writeDraft()
}

func (m *Model) inlineRedo() {
	e := m.inline
	if e == nil || len(e.redo) == 0 {
		return
	}
	e.undo = appendBoundedSnapshots(e.undo, inlineSnapshot{content: append([]rune(nil), e.content...), cursor: e.cursor})
	snapshot := e.redo[len(e.redo)-1]
	e.redo = e.redo[:len(e.redo)-1]
	e.content, e.cursor, e.dirty = append([]rune(nil), snapshot.content...), snapshot.cursor, true
	m.writeDraft()
}

func (e *inlineEditorState) lineStart(pos int) int {
	if pos > len(e.content) {
		pos = len(e.content)
	}
	for pos > 0 && e.content[pos-1] != '\n' {
		pos--
	}
	return pos
}

func (e *inlineEditorState) lineEnd(pos int) int {
	if pos > len(e.content) {
		pos = len(e.content)
	}
	for pos < len(e.content) && e.content[pos] != '\n' {
		pos++
	}
	return pos
}

func (e *inlineEditorState) rowCol() (int, int) {
	row, col := 0, 0
	for i := 0; i < e.cursor && i < len(e.content); i++ {
		if e.content[i] == '\n' {
			row, col = row+1, 0
		} else {
			col += ansi.StringWidth(string(e.content[i]))
		}
	}
	return row, col
}

func (e *inlineEditorState) moveVertical(delta int) {
	row, col := e.rowCol()
	if e.goalCol > 0 {
		col = e.goalCol
	}
	target := row + delta
	if target < 0 {
		target = 0
	}
	start := 0
	for current := 0; current < target; current++ {
		end := e.lineEnd(start)
		if end >= len(e.content) {
			return
		}
		start = end + 1
	}
	end := e.lineEnd(start)
	position := start
	for position < end {
		width := ansi.StringWidth(string(e.content[position]))
		if col < width {
			break
		}
		col -= width
		position++
	}
	e.cursor, e.goalCol = position, col
}

func (e *inlineEditorState) moveWord(direction int) {
	if direction > 0 {
		for e.cursor < len(e.content) && (unicode.IsLetter(e.content[e.cursor]) || unicode.IsDigit(e.content[e.cursor])) {
			e.cursor++
		}
		for e.cursor < len(e.content) && unicode.IsSpace(e.content[e.cursor]) {
			e.cursor++
		}
		return
	}
	for e.cursor > 0 && unicode.IsSpace(e.content[e.cursor-1]) {
		e.cursor--
	}
	for e.cursor > 0 && !unicode.IsSpace(e.content[e.cursor-1]) {
		e.cursor--
	}
}

func (m *Model) inlineFindNext() {
	e := m.inline
	if e == nil || e.search == "" {
		return
	}
	needle := []rune(e.search)
	for start := e.cursor + 1; start+len(needle) <= len(e.content); start++ {
		if string(e.content[start:start+len(needle)]) == e.search {
			e.cursor = start
			return
		}
	}
	for start := 0; start+len(needle) <= e.cursor; start++ {
		if string(e.content[start:start+len(needle)]) == e.search {
			e.cursor = start
			return
		}
	}
}

func (m *Model) inlineEnsureCursorVisible() {
	e := m.inline
	if e == nil {
		return
	}
	row, _ := e.rowCol()
	viewport := maxInt(m.viewerViewport()-1, 1)
	if row < m.viewerOffset {
		m.viewerOffset = row
	}
	if row >= m.viewerOffset+viewport {
		m.viewerOffset = row - viewport + 1
	}
}

func (m *Model) renderInlineEditor() string {
	e := m.inline
	if e == nil {
		return "inline editor unavailable"
	}
	lines := strings.Split(string(e.content), "\n")
	row, col := e.rowCol()
	start := minInt(m.viewerOffset, maxInt(len(lines)-1, 0))
	end := minInt(start+maxInt(m.viewerViewport()-1, 1), len(lines))
	visible := make([]string, 0, end-start+1)
	for index := start; index < end; index++ {
		line := sanitizeDisplay(lines[index])
		if index == row {
			line = insertCursor(line, col)
		}
		visible = append(visible, fmt.Sprintf("%4d %s", index+1, line))
	}
	if len(visible) == 0 {
		visible = []string{"   1 ▏"}
	}
	mode := "NORMAL"
	if e.insert {
		mode = "INSERT"
	}
	footer := fmt.Sprintf("%s  %s  %s", mode, e.doc.Path, "Ctrl-S save  Esc normal")
	if e.dirty {
		footer += " *"
	}
	return strings.Join(append(visible, footer), "\n")
}

func insertCursor(line string, cell int) string {
	if cell <= 0 {
		return "▏" + line
	}
	for index, r := range line {
		if ansi.StringWidth(line[:index]) >= cell {
			return line[:index] + "▏" + line[index:]
		}
		_ = r
	}
	return line + "▏"
}

func (m *Model) defaultEditorLoad(ctx context.Context, path string) (EditorDocument, error) {
	if err := ctx.Err(); err != nil {
		return EditorDocument{}, err
	}
	content, err := m.readFile(path)
	if err != nil {
		return EditorDocument{}, err
	}
	if strings.HasPrefix(content, "[binary file;") {
		return EditorDocument{}, errors.New("binary files cannot be opened in the inline editor")
	}
	return EditorDocument{Path: path, Content: content, Revision: revisionOf(content)}, nil
}

func (m *Model) defaultEditorSave(ctx context.Context, doc EditorDocument) (EditorDocument, error) {
	if err := ctx.Err(); err != nil {
		return EditorDocument{}, err
	}
	if m.remoteFS {
		return EditorDocument{}, errors.New("remote inline saving requires an injected editor saver")
	}
	if m.tree.root == nil {
		return EditorDocument{}, errors.New("workspace is unavailable")
	}
	path, err := cleanRelative(doc.Path)
	if err != nil {
		return EditorDocument{}, err
	}
	file, err := m.tree.root.Open(path)
	if err != nil {
		return EditorDocument{}, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return EditorDocument{}, err
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return EditorDocument{}, fmt.Errorf("%s is not a regular file", path)
	}
	current, err := io.ReadAll(io.LimitReader(file, maxInlineDocumentBytes+1))
	_ = file.Close()
	if err != nil {
		return EditorDocument{}, err
	}
	if len(current) > maxInlineDocumentBytes {
		return EditorDocument{}, fmt.Errorf("file is larger than %d bytes", maxInlineDocumentBytes)
	}
	if revisionOf(string(current)) != doc.Revision {
		return EditorDocument{}, errors.New("file changed on disk; reload before saving")
	}
	temp := fmt.Sprintf(".bifrost-edit-%d", time.Now().UnixNano())
	tmp, err := m.tree.root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return EditorDocument{}, err
	}
	_, writeErr := tmp.WriteString(doc.Content)
	closeErr := tmp.Close()
	if writeErr != nil || closeErr != nil {
		_ = m.tree.root.Remove(temp)
		return EditorDocument{}, errors.Join(writeErr, closeErr)
	}
	if err := m.tree.root.Rename(temp, path); err != nil {
		_ = m.tree.root.Remove(temp)
		return EditorDocument{}, err
	}
	doc.Revision = revisionOf(doc.Content)
	return doc, nil
}

func revisionOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func (m *Model) draftFile(path string) string {
	if m.draftStateDir == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(m.draftStateDir, hex.EncodeToString(sum[:])+".json")
}

func (m *Model) readDraft(path string) (*draftRecord, error) {
	name := m.draftFile(path)
	if name == "" {
		return nil, os.ErrNotExist
	}
	b, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(b) > maxInlineDocumentBytes+1024 {
		return nil, fmt.Errorf("draft exceeds size limit")
	}
	var draft draftRecord
	if err := json.Unmarshal(b, &draft); err != nil {
		return nil, err
	}
	if len([]byte(draft.Content)) > maxInlineDocumentBytes {
		return nil, fmt.Errorf("draft exceeds size limit")
	}
	return &draft, nil
}

func (m *Model) writeDraft() {
	if m.inline == nil || m.draftStateDir == "" || !m.inline.dirty {
		return
	}
	if len([]byte(string(m.inline.content))) > maxInlineDocumentBytes {
		return
	}
	if err := os.MkdirAll(m.draftStateDir, 0o700); err != nil {
		return
	}
	draft := draftRecord{Path: m.inline.doc.Path, Revision: m.inline.doc.Revision, Content: string(m.inline.content)}
	b, err := json.Marshal(draft)
	if err != nil {
		return
	}
	name := m.draftFile(draft.Path)
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
	}
}

func (m *Model) removeDraft(path string) {
	if name := m.draftFile(path); name != "" {
		_ = os.Remove(name)
	}
}

func (m *Model) paletteKey(stroke, text string) tea.Cmd {
	switch stroke {
	case "escape", "esc":
		m.paletteOpen = false
		m.paletteSelected = -1
		m.paletteCompletion = paletteCompletionState{}
		m.paletteError = ""
		m.paletteHelp = ""
		m.normalizeFocus()
		m.status = "command palette closed"
	case "backspace":
		runes := []rune(m.paletteInput)
		if len(runes) > 0 {
			m.paletteInput = string(runes[:len(runes)-1])
			m.paletteSelected = -1
			m.paletteCompletion = paletteCompletionState{}
			m.paletteError = ""
			m.paletteHelp = ""
		}
	case "up", "down":
		m.paletteCompletion = paletteCompletionState{}
		m.paletteMoveSelection(stroke == "down")
	case "tab", "shift+tab":
		m.paletteComplete(stroke == "tab")
	case "enter", "return":
		command := m.paletteInput
		if !m.paletteCompletion.active && m.paletteSelected >= 0 {
			suggestions := m.paletteSuggestions(m.paletteInput)
			if m.paletteSelected < len(suggestions) {
				command = suggestions[m.paletteSelected].text
			}
		}
		m.paletteError = ""
		m.paletteHelp = ""
		cmd := m.executePaletteCommand(command)
		if m.paletteError != "" {
			// Keep the invalid input visible so the user can fix it in place.
			return cmd
		}
		if m.paletteHelp != "" {
			return cmd
		}
		m.paletteOpen = false
		m.paletteSelected = -1
		m.paletteCompletion = paletteCompletionState{}
		return cmd
	default:
		if text != "" && len([]rune(m.paletteInput)) < 128 {
			m.paletteInput += sanitizeDisplay(text)
			m.paletteSelected = -1
			m.paletteCompletion = paletteCompletionState{}
			m.paletteError = ""
			m.paletteHelp = ""
		}
	}
	return nil
}

func (m *Model) paletteMoveSelection(forward bool) {
	suggestions := m.paletteSuggestions(m.paletteInput)
	if len(suggestions) == 0 {
		m.paletteSelected = -1
		return
	}
	if m.paletteSelected < 0 || m.paletteSelected >= len(suggestions) {
		if forward {
			m.paletteSelected = 0
		} else {
			m.paletteSelected = len(suggestions) - 1
		}
		return
	}
	if forward {
		m.paletteSelected = (m.paletteSelected + 1) % len(suggestions)
	} else {
		m.paletteSelected = (m.paletteSelected + len(suggestions) - 1) % len(suggestions)
	}
}

func (m *Model) paletteComplete(forward bool) {
	completion := &m.paletteCompletion
	if completion.active && completion.current == m.paletteInput && forward && len(completion.suggestions) == 1 && strings.HasSuffix(completion.current, " ") {
		completion.source = completion.current
		completion.suggestions = m.paletteSuggestions(completion.current)
		completion.index = -1
	} else if !completion.active || completion.current != m.paletteInput {
		completion.source = m.paletteInput
		completion.suggestions = m.paletteSuggestions(m.paletteInput)
		completion.index = -1
		completion.active = true
	}
	suggestions := completion.suggestions
	if len(suggestions) == 0 {
		m.paletteSelected = -1
		return
	}
	if completion.index < 0 || completion.index >= len(suggestions) {
		if forward {
			completion.index = 0
		} else {
			completion.index = len(suggestions) - 1
		}
	} else if forward {
		completion.index = (completion.index + 1) % len(suggestions)
	} else {
		completion.index = (completion.index + len(suggestions) - 1) % len(suggestions)
	}
	m.paletteInput = suggestions[completion.index].text
	if len(suggestions) == 1 && !strings.HasSuffix(m.paletteInput, " ") {
		fields := strings.Fields(m.paletteInput)
		if len(fields) == 2 && strings.EqualFold(fields[0], "resize") {
			m.paletteInput += " "
		}
	}
	completion.current = m.paletteInput
	m.paletteSelected = -1
}

func (m *Model) renderPalette() string {
	return m.renderPaletteContent(0, 0, true)
}

// renderPalettePanel gives the palette the full pane width so usage errors and
// command help remain readable instead of being truncated inside a narrow
// generic viewer frame.
func (m *Model) renderPalettePanel(width, height int) string {
	inner := maxInt(width-4, 1)
	content := m.renderPaletteContent(inner, maxInt(height-3, 1), false)
	return renderPanelTheme(m.theme, "COMMAND PALETTE", content, width, true, height)
}

func (m *Model) renderPaletteContent(width, budget int, heading bool) string {
	suggestions := m.paletteSuggestions(m.paletteInput)
	inner := width
	if inner < 1 {
		inner = 120
	}
	lines := make([]string, 0, len(suggestions)+8)
	if heading {
		lines = append(lines, m.colors().violetText("COMMAND PALETTE"), "")
	}
	border := m.colors().cyanText("╭" + strings.Repeat("─", maxInt(inner-2, 1)) + "╮")
	lines = append(lines, border)
	query := "│ " + sanitizeDisplay(m.paletteInput) + "▏"
	lines = append(lines, m.colors().cyanText(truncatePad(query, inner)), m.colors().cyanText("╰"+strings.Repeat("─", maxInt(inner-2, 1))+"╯"))
	if m.paletteError != "" {
		for _, row := range wrapChatText(m.paletteError, inner) {
			lines = append(lines, m.colors().dangerText(row))
		}
	}
	if m.paletteHelp != "" {
		for _, row := range wrapChatText(m.paletteHelp, inner) {
			lines = append(lines, m.colors().cyanText(row))
		}
	}
	footer := "Tab/Shift-Tab complete · ↑/↓ select · Enter run · Esc close"
	limit := len(suggestions)
	if budget > 0 {
		limit = maxInt(budget-len(lines)-1, 1)
		limit = minInt(limit, len(suggestions))
	}
	start := 0
	if m.paletteSelected >= limit && limit > 0 {
		start = m.paletteSelected - limit + 1
	}
	if start > len(suggestions)-limit {
		start = maxInt(len(suggestions)-limit, 0)
	}
	for i := start; i < minInt(start+limit, len(suggestions)); i++ {
		suggestion := suggestions[i]
		prefix := "  "
		if i == m.paletteSelected {
			prefix = "▸ "
		}
		lines = append(lines, prefix+sanitizeDisplay(suggestion.text)+"  "+sanitizeDisplay(suggestion.help))
	}
	lines = append(lines, m.colors().quietText(ansi.Truncate(footer, inner, "…")))
	return strings.Join(lines, "\n")
}

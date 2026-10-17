package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"bifrost/internal/atomicfile"
)

// EditorBuffer shares the TUI's editing, navigation, search, and undo engine.
// Native input methods can edit normally; Replace records those edits in the
// same per-document history. It performs no filesystem or native UI operations.
type EditorBuffer struct{ model Model }

func NewEditorBuffer(doc EditorDocument) *EditorBuffer {
	return &EditorBuffer{Model{inline: &inlineEditorState{doc: doc, content: []rune(doc.Content), insert: true}}}
}
func (b *EditorBuffer) Text() string { return string(b.model.inline.content) }
func (b *EditorBuffer) Cursor() int {
	e := b.model.inline
	return len(string(e.content[:min(e.cursor, len(e.content))]))
}
func (b *EditorBuffer) SetCursor(offset int) {
	text := b.Text()
	offset = max(0, min(offset, len(text)))
	for offset > 0 && offset < len(text) && !utf8.RuneStart(text[offset]) {
		offset--
	}
	b.model.inline.cursor = utf8.RuneCountInString(text[:offset])
}
func (b *EditorBuffer) Replace(text string, cursor int) {
	if text != b.Text() {
		b.model.inlineEdit(0, len(b.model.inline.content), []rune(text))
	}
	b.SetCursor(cursor)
}
func (b *EditorBuffer) Undo() { b.model.inlineUndo() }
func (b *EditorBuffer) Redo() { b.model.inlineRedo() }
func (b *EditorBuffer) NormalKey(key string) {
	if key == "d" || key == "q" || key == "escape" || key == "ctrl+s" || key == "/" {
		return
	}
	b.model.inlineNormalKey(key)
}
func (b *EditorBuffer) InsertMode() bool          { return b.model.inline.insert }
func (b *EditorBuffer) SetInsertMode(insert bool) { b.model.inline.insert = insert }
func (b *EditorBuffer) Find(query string) bool {
	b.model.inline.search = query
	before := b.model.inline.cursor
	b.model.inlineFindNext()
	return b.model.inline.cursor != before || (query != "" && strings.HasPrefix(b.Text()[b.Cursor():], query))
}

func ReadEditorDraft(dir string, doc EditorDocument) (EditorDocument, error) {
	name := (&Model{draftStateDir: dir}).draftFile(doc.Path)
	if name == "" {
		return doc, os.ErrNotExist
	}
	f, err := os.Open(name)
	if err != nil {
		return doc, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 12*maxInlineDocumentBytes+1025))
	if err != nil {
		return doc, err
	}
	if len(data) > 12*maxInlineDocumentBytes+1024 {
		return doc, fmt.Errorf("draft exceeds size limit")
	}
	var draft draftRecord
	if err = json.Unmarshal(data, &draft); err != nil {
		return doc, err
	}
	if len(draft.Content) > maxInlineDocumentBytes {
		return doc, fmt.Errorf("draft exceeds size limit")
	}
	if draft.Path != doc.Path || draft.Revision != doc.Revision {
		return doc, fmt.Errorf("saved draft has a different file revision")
	}
	doc.Content = draft.Content
	return doc, nil
}
func SaveEditorDraft(dir string, doc EditorDocument) error {
	if dir == "" {
		return nil
	}
	if len(doc.Content) > maxInlineDocumentBytes {
		return fmt.Errorf("draft exceeds size limit")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(draftRecord{doc.Path, doc.Revision, doc.Content})
	if err != nil {
		return err
	}
	return atomicfile.Write((&Model{draftStateDir: dir}).draftFile(doc.Path), data)
}
func RemoveEditorDraft(dir, path string) error {
	name := (&Model{draftStateDir: dir}).draftFile(path)
	if name == "" {
		return nil
	}
	err := os.Remove(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

package ui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openInlineTestEditor(t *testing.T, m *Model) {
	t.Helper()
	m.focus, m.viewPath = focusViewer, "note.txt"
	cmd := m.startInlineEditor()
	if cmd == nil {
		t.Fatal("inline editor did not start")
	}
	_, _ = m.Update(cmd())
	if m.mode != ModeInline || m.inline == nil {
		t.Fatalf("inline state mode=%v editor=%#v", m.mode, m.inline)
	}
}

func TestInlineEditorDefaultSaveAndConflict(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(root)
	openInlineTestEditor(t, m)
	m.inlineNormalKey("i")
	m.inlineInsertKey("x", "X")
	if !m.inline.dirty || string(m.inline.content) != "Xone\n" {
		t.Fatalf("edit content=%q dirty=%v", string(m.inline.content), m.inline.dirty)
	}
	cmd := m.saveInlineEditor()
	if cmd == nil {
		t.Fatal("save command is nil")
	}
	_, _ = m.Update(cmd())
	content, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(content) != "Xone\n" {
		t.Fatalf("saved content=%q err=%v", content, err)
	}
	if m.inline.dirty {
		t.Fatal("successful save remains dirty")
	}
	_ = m.Close()

	m = New(root)
	openInlineTestEditor(t, m)
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.inlineNormalKey("i")
	m.inlineInsertKey("x", "Y")
	cmd = m.saveInlineEditor()
	_, _ = m.Update(cmd())
	if !strings.Contains(m.status, "changed on disk") {
		t.Fatalf("conflict status=%q", m.status)
	}
	content, _ = os.ReadFile(filepath.Join(root, "note.txt"))
	if string(content) != "other\n" {
		t.Fatalf("conflict overwrote disk content=%q", content)
	}
	_ = m.Close()
}

func TestInlineEditorDraftRecoveryAndNavigation(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("hello 世界\nsecond"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(root, WithDraftStateDir(state))
	openInlineTestEditor(t, m)
	m.inlineNormalKey("i")
	m.inlineInsertKey("x", "draft ")
	if _, err := os.Stat(m.draftFile("note.txt")); err != nil {
		t.Fatalf("draft not persisted: %v", err)
	}
	_ = m.Close()

	restored := New(root, WithDraftStateDir(state))
	openInlineTestEditor(t, restored)
	if !restored.inline.dirty || !strings.HasPrefix(string(restored.inline.content), "draft ") {
		t.Fatalf("draft state dirty=%v content=%q", restored.inline.dirty, string(restored.inline.content))
	}
	restored.inlineNormalKey("G")
	if restored.inline.cursor != len(restored.inline.content) {
		t.Fatalf("G cursor=%d content=%d", restored.inline.cursor, len(restored.inline.content))
	}
	restored.inlineNormalKey("g")
	if restored.inline.cursor != 0 {
		t.Fatalf("g cursor=%d", restored.inline.cursor)
	}
	restored.inlineNormalKey("/")
	restored.inlineSearchKey("", "second")
	if restored.inline.cursor == 0 {
		t.Fatal("search did not move cursor")
	}
	restored.inlineNormalKey("d")
	if restored.mode != ModeNormal || restored.inline != nil {
		t.Fatalf("discard mode=%v editor=%v", restored.mode, restored.inline != nil)
	}
	_ = restored.Close()
}

func TestInlineEditorInjectedRevisionCallbacks(t *testing.T) {
	root := t.TempDir()
	loaded := EditorDocument{Path: "note.txt", Content: "callback", Revision: "r1"}
	var saved EditorDocument
	m := New(root, WithInlineEditor(
		func(context.Context, string) (EditorDocument, error) { return loaded, nil },
		func(_ context.Context, doc EditorDocument) (EditorDocument, error) {
			saved = doc
			doc.Revision = "r2"
			return doc, nil
		},
	))
	m.focus, m.viewPath = focusViewer, loaded.Path
	cmd := m.startInlineEditor()
	_, _ = m.Update(cmd())
	m.inlineNormalKey("i")
	m.inlineInsertKey("x", "!")
	cmd = m.saveInlineEditor()
	_, _ = m.Update(cmd())
	if saved.Revision != "r1" || saved.Content != "!callback" || m.inline.doc.Revision != "r2" {
		t.Fatalf("callback save=%#v model=%#v", saved, m.inline.doc)
	}
	_ = m.Close()
}

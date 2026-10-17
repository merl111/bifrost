package ui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEditorCommandUsesArgvAndRefreshesViewer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	root := t.TempDir()
	file := filepath.Join(root, "note.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := filepath.Join(root, "fixture-editor")
	if err := os.WriteFile(editor, []byte("#!/bin/sh\nprintf 'after\\n' > \"$2\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	m := New(root, WithEditor([]string{editor, "--fixture"}))
	m.focus = focusFiles
	m.selected = 0
	m.viewPath = "note.txt"
	m.viewer = "before\n"
	cmd, err := m.editorCommand("note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := cmd.Args[len(cmd.Args)-1]; got != file {
		t.Fatalf("editor target = %q, want %q", got, file)
	}
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != "after\n" {
		t.Fatalf("edited file = %q", got)
	}
	updated, _ := m.Update(editorFinishedMsg{path: "note.txt", focus: focusFiles})
	m = updated.(*Model)
	if m.focus != focusFiles || m.viewer != "after\n" || m.viewerOffset != 0 {
		t.Fatalf("return state focus=%v viewer=%q offset=%d", m.focus, m.viewer, m.viewerOffset)
	}
	if !strings.Contains(m.status, "refreshed") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestEditorRejectsEscapesAndShellSyntax(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(root, WithEditor([]string{"editor"}))
	if _, err := m.editorPath("../outside"); err == nil {
		t.Fatal("editor path escape was accepted")
	}
	if _, err := parseEditorEnv("vim --wait | tee"); err == nil {
		t.Fatal("shell syntax was accepted")
	}
	if _, err := validateEditorArgv([]string{"vim", "bad\x00arg"}); err == nil {
		t.Fatal("NUL editor argument was accepted")
	}
	m.focus = focusViewer
	m.viewPath = "review"
	if cmd := m.startEditor(); cmd != nil || !strings.Contains(m.status, "select a file") {
		t.Fatalf("review editor target cmd=%v status=%q", cmd, m.status)
	}
}

func TestEditorFinishedErrorRestoresFocus(t *testing.T) {
	m := &Model{focus: focusViewer, status: "editor running"}
	updated, _ := m.Update(editorFinishedMsg{path: "x", focus: focusViewer, err: os.ErrNotExist})
	m = updated.(*Model)
	if m.focus != focusViewer || !strings.Contains(m.status, "editor failed") {
		t.Fatalf("focus=%v status=%q", m.focus, m.status)
	}
}

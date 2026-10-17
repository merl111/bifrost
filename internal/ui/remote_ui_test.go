package ui

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestRemoteFilesystemNeverNeedsHostRoot(t *testing.T) {
	var listed, read string
	m := New("remote:/project", WithFilesystem(
		func(_ context.Context, path string) ([]FileEntry, error) {
			listed = path
			return []FileEntry{{Path: "note.txt"}}, nil
		},
		func(_ context.Context, path string) (string, error) {
			read = path
			return "remote content", nil
		},
	))
	defer m.Close()
	if len(m.files) != 1 || listed != "" {
		t.Fatalf("files=%v listed=%q", m.files, listed)
	}
	m.openSelected()
	if read != "note.txt" || m.viewer != "remote content" {
		t.Fatalf("read=%q viewer=%q", read, m.viewer)
	}
	if _, err := m.editorCommand("note.txt"); err == nil || !strings.Contains(err.Error(), "remote") {
		t.Fatalf("editor err=%v", err)
	}
}

func TestRemoteFilesystemCallbacksReceiveContext(t *testing.T) {
	ctxErr := errors.New("cancelled")
	fs := NewRemoteFilesystem(func(ctx context.Context, _ string) ([]FileEntry, error) { return nil, ctx.Err() }, func(ctx context.Context, _ string) (string, error) { return "", ctxErr })
	if _, err := fs.Read(context.Background(), "x"); !errors.Is(err, ctxErr) {
		t.Fatalf("read err=%v", err)
	}
	_ = fs.Close()
}

func TestExplicitTerminalArgvValidation(t *testing.T) {
	if _, err := startPTYArgv(t.TempDir(), 20, 8, []string{"sh", "-c", "printf '\x00'"}); err == nil {
		t.Fatal("NUL terminal argv accepted")
	}
}

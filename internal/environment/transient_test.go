package environment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTransientLocalOpensPerOperation(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before\n"), 0600); err != nil {
		t.Fatal(err)
	}
	backend, err := OpenTransientLocal(LocalConfig{Root: root, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if got := backend.Identity().Root; got != root {
		t.Fatalf("root=%q want %q", got, root)
	}
	text, rev, err := backend.Read(context.Background(), "note.txt")
	if err != nil || text != "before\n" || rev == "" {
		t.Fatalf("read text=%q rev=%q err=%v", text, rev, err)
	}
	if _, err := backend.Write(context.Background(), "note.txt", &rev, "after\n"); err != nil {
		t.Fatal(err)
	}
	// The transient backend has released its workspace lock after Write.
	l, err := OpenLocal(LocalConfig{Root: root, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
}

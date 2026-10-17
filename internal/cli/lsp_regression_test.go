package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLSPDocumentIsRootedAndBounded(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.go"), []byte("package demo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := rootedLSPPath(root, "note.go")
	if err != nil {
		t.Fatal(err)
	}
	b, err := readLSPDocument(root, path)
	if err != nil || string(b) != "package demo\n" {
		t.Fatalf("document=%q err=%v", b, err)
	}
	for _, value := range []string{"../outside.go", filepath.Join(root, "..", "outside.go")} {
		if _, err := rootedLSPPath(root, value); err == nil {
			t.Fatalf("accepted path outside workspace: %q", value)
		}
	}
	if got := lspLanguageID(path); got != "go" {
		t.Fatalf("language=%q", got)
	}
	if _, err := readLSPDocument(root, filepath.Join(root, "missing.go")); err == nil || !strings.Contains(err.Error(), "read LSP document") {
		t.Fatalf("missing document err=%v", err)
	}
}

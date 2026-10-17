package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEqualValueFlagsDoNotSwallowFollowingFlags(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	_, _, err := invoke(t, "--root", root, "--state-dir", state, "file", "write", "a", "--content=hello", "--must-not-exist", "--json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "a"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("content: %q %v", b, err)
	}
}

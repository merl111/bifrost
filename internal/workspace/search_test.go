package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSearchLiteralUnicodeBinaryAndSymlinkSafety(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "unicode.txt"), []byte("prefix needle café\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "binary.bin"), []byte("needle\x00hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	result, err := w.Search(context.Background(), "needle")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "unicode.txt" || result.Matches[0].Line != 1 || !strings.Contains(result.Matches[0].Text, "café") {
		t.Fatalf("search result = %+v", result)
	}
	if _, err := w.Search(context.Background(), ""); err == nil {
		t.Fatal("empty query unexpectedly accepted")
	}
	if _, err := w.Search(context.Background(), strings.Repeat("q", 4097)); err == nil {
		t.Fatal("oversized query unexpectedly accepted")
	}
}

func TestSearchMatchBoundAndUnicodeLineTruncation(t *testing.T) {
	w, root := testWorkspace(t)
	for i := 0; i < 205; i++ {
		name := filepath.Join(root, "match-"+strings.Repeat("0", 3)+string(rune('a'+i/26))+string(rune('a'+i%26))+".txt")
		if err := os.WriteFile(name, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	long := "needle-long" + strings.Repeat("é", 3000) + "\n"
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := w.Search(context.Background(), "needle")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 200 || !result.Truncated {
		t.Fatalf("bounded matches = %d truncated=%v", len(result.Matches), result.Truncated)
	}
	longResult, err := w.Search(context.Background(), "needle-long")
	if err != nil || len(longResult.Matches) != 1 {
		t.Fatalf("long unicode search = %+v, %v", longResult, err)
	}
	if len(longResult.Matches[0].Text) > 2048 || !strings.Contains(longResult.Matches[0].Text, "needle-long") || !strings.ContainsRune(longResult.Matches[0].Text, 'é') {
		t.Fatalf("unicode line truncation = %+v", longResult.Matches[0])
	}
}

func TestSearchCancellation(t *testing.T) {
	w, root := testWorkspace(t)
	for i := 0; i < 64; i++ {
		if err := os.WriteFile(filepath.Join(root, "file-"+string(rune('a'+i))+".txt"), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := w.Search(ctx, "needle")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v result=%+v", err, result)
	}
}

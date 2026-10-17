package gitview

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadUnbornStatusAndRootScope(t *testing.T) {
	root := initGitRepo(t)
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	review, err := Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(review.Status, "?? untracked.txt") {
		t.Fatalf("unborn status = %q", review.Status)
	}
	if review.Unstaged != "" || review.Staged != "" {
		t.Fatalf("unborn diffs = unstaged %q staged %q", review.Unstaged, review.Staged)
	}
	if err := runGit(root, "add", "untracked.txt"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "untracked.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	review, err = Load(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(review.Status, "AM untracked.txt") || review.Unstaged == "" || review.Staged == "" {
		t.Fatalf("staged and unstaged = status %q unstaged %q staged %q", review.Status, review.Unstaged, review.Staged)
	}
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(context.Background(), nested); err == nil {
		t.Fatal("nested repository scope unexpectedly accepted")
	}
}

func TestLoadDisablesAliasesExternalDiffAndTextconv(t *testing.T) {
	root := initGitRepo(t)
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "helper.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf executed > "+shellQuote(marker)+"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("tracked.txt diff=fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "commit", "-m", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "alias.status", "!printf alias-status > "+shellQuote(marker)); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "alias.diff", "!printf alias-diff > "+shellQuote(marker)); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "diff.fixture.textconv", script); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_EXTERNAL_DIFF", script)
	if _, err := Load(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("external helper was executed: %v", err)
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := runGit(root, "init", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(root, "config", "user.name", "Bifrost Test"); err != nil {
		t.Fatal(err)
	}
	return root
}

func runGit(root string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		return errors.New(strings.TrimSpace(string(output)))
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

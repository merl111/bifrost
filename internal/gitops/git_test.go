package gitops

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitStatusStageCommitAndValidation(t *testing.T) {
	root := initRepo(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	status, err := g.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Entries) != 1 || status.Entries[0].Path != "a.txt" {
		t.Fatalf("status = %+v", status.Entries)
	}
	if _, err := g.Stage(context.Background(), []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Commit(context.Background(), CommitOptions{Message: "add a"}); err != nil {
		t.Fatal(err)
	}
	status, err = g.Status(context.Background())
	if err != nil || len(status.Entries) != 0 {
		t.Fatalf("clean status = %+v, %v", status.Entries, err)
	}
	for _, paths := range [][]string{{"-a"}, {"../escape"}, {root}, {"."}} {
		if _, err := g.Stage(context.Background(), paths); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("Stage(%q) error = %v", paths, err)
		}
	}
	if _, err := g.Commit(context.Background(), CommitOptions{}); err == nil {
		t.Fatal("empty commit accepted")
	}
}

func TestGitDiffAndUnstage(t *testing.T) {
	root := initRepo(t)
	path := filepath.Join(root, "a.txt")
	if err := os.WriteFile(path, []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	g, _ := Open(root)
	if _, err := g.Stage(context.Background(), []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	diff, err := g.Diff(context.Background(), DiffOptions{Cached: true, Paths: []string{"a.txt"}})
	if err != nil || !strings.Contains(diff.Text, "two") {
		t.Fatalf("diff = %+v, %v", diff, err)
	}
	if _, err := g.Unstage(context.Background(), []string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	status, err := g.Status(context.Background())
	if err != nil || len(status.Entries) != 1 || status.Entries[0].XY != "??" {
		t.Fatalf("unstaged status = %+v, %v", status.Entries, err)
	}
}

func TestGitWorktreeLifecycleRefusesDirtyRemoval(t *testing.T) {
	root := initRepo(t)
	g, _ := Open(root)
	target := filepath.Join(root, ".bifrost-worktree")
	if _, err := g.WorktreeCreate(context.Background(), target, "HEAD"); err != nil {
		t.Fatal(err)
	}
	list, err := g.WorktreeList(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("worktrees = %+v, %v", list, err)
	}
	if err := os.WriteFile(filepath.Join(target, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.WorktreeRemove(context.Background(), target); !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("dirty removal error = %v", err)
	}
	if err := os.Remove(filepath.Join(target, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := g.WorktreeRemove(context.Background(), target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree remains: %v", err)
	}
}

func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "config", "user.email", "fixture@example.invalid")
	gitRun(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", "--", "base.txt")
	gitRun(t, root, "commit", "-qm", "base")
	return root
}
func gitRun(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/store"
)

func TestGitHubLinkUpdatesSelectedProjectRemote(t *testing.T) {
	state := t.TempDir()
	r, err := store.OpenRegistry(state + "/registry")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Create(store.CreateProjectRequest{Name: "demo", Repositories: []store.RepositoryInput{{Alias: "main", Roots: map[string]string{"local": t.TempDir()}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runGitHub(context.Background(), options{stateDir: state, project: p.Name}, []string{"link", "acme/widgets"}, &out); err != nil {
		t.Fatal(err)
	}
	r, err = store.OpenRegistry(state + "/registry")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.Get(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Repositories[0].Remote != "https://github.com/acme/widgets.git" {
		t.Fatalf("remote = %q", got.Repositories[0].Remote)
	}
	if !strings.Contains(out.String(), "linked main") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestGitHubCLIValidation(t *testing.T) {
	if err := runGitHub(context.Background(), options{}, []string{"link", "bad"}, &strings.Builder{}); err == nil {
		t.Fatal("accepted malformed link")
	}
	if err := runGitHub(context.Background(), options{}, []string{"link", "o/r"}, &strings.Builder{}); !strings.Contains(err.Error(), "--project") {
		t.Fatalf("error = %v", err)
	}
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	var out strings.Builder
	if err := runGitHub(context.Background(), options{}, []string{"auth-status"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "not authenticated\n" {
		t.Fatalf("auth output = %q", out.String())
	}
}

func TestGitCLIUsesExplicitRepositoryOperations(t *testing.T) {
	root := t.TempDir()
	gitCLIInit(t, root, "init", "-q")
	gitCLIInit(t, root, "config", "user.email", "fixture@example.invalid")
	gitCLIInit(t, root, "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := runGit(context.Background(), options{root: root}, []string{"status"}, &out); err != nil || !strings.Contains(out.String(), "a.txt") {
		t.Fatalf("status=%q err=%v", out.String(), err)
	}
	if err := runGit(context.Background(), options{root: root}, []string{"stage", "a.txt"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runGit(context.Background(), options{root: root}, []string{"commit", "--message", "add"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := runGit(context.Background(), options{root: root}, []string{"stage", "../escape"}, &out); err == nil {
		t.Fatal("accepted path outside root")
	}
}

func gitCLIInit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}

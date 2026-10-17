package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectScanAddAndRemoveRepoCLI(t *testing.T) {
	state := t.TempDir()
	root := t.TempDir()
	for _, repo := range []struct {
		name   string
		remote string
	}{
		{name: "laura-admin", remote: "git@github.com:aklabs/laura-admin.git"},
		{name: "laura-backend", remote: "https://github.com/aklabs/laura-backend.git"},
	} {
		gitDir := filepath.Join(root, repo.name, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "[remote \"origin\"]\n\turl = " + repo.remote + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var out strings.Builder
	opts := options{stateDir: state, root: root, environment: "local"}
	if err := runProject(opts, []string{"scan", "laura", "--root", root, "--add"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "linked\tlaura-admin") || !strings.Contains(got, "aklabs/laura-backend") {
		t.Fatalf("scan --add output = %q", got)
	}

	out.Reset()
	if err := runProject(opts, []string{"remove-repo", "laura", "laura-admin"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "removed repository laura-admin from laura") {
		t.Fatalf("remove-repo output = %q", out.String())
	}

	out.Reset()
	if err := runProject(opts, []string{"scan", "laura", "--root", root}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "discovered\tlaura-admin") || !strings.Contains(out.String(), "linked\tlaura-backend") {
		t.Fatalf("scan after remove output = %q", out.String())
	}
}

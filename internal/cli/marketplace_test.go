package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"bifrost/internal/marketplace"
	"bifrost/internal/skill"
)

func TestRunSkillMarketInstallUsesRequestedScope(t *testing.T) {
	repo := t.TempDir()
	gitFixture(t, repo)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"entries": []marketplace.Entry{{ID: "fixture", Name: "Fixture", Description: "test", Repo: repo, Ref: "main", Path: "skills/fixture"}}})
	}))
	defer srv.Close()
	opts := options{stateDir: t.TempDir(), root: t.TempDir()}
	var out bytes.Buffer
	if err := runSkillMarket(context.Background(), opts, []string{"install", "fixture", "--catalog", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var project struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(out.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.Scope != "project" {
		t.Fatalf("default install scope = %q", project.Scope)
	}
	m, err := skillManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load("fixture"); err != nil {
		t.Fatalf("project install missing: %v", err)
	}
	out.Reset()
	if err := runSkillMarket(context.Background(), opts, []string{"install", "fixture", "--scope", "global", "--catalog", srv.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var global struct {
		Scope string `json:"scope"`
	}
	if err := json.Unmarshal(out.Bytes(), &global); err != nil {
		t.Fatal(err)
	}
	if global.Scope != "global" {
		t.Fatalf("explicit install scope = %q", global.Scope)
	}
	gm, err := marketplaceManager(opts, "global")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gm.Load("fixture"); err != nil {
		t.Fatalf("global install missing: %v", err)
	}
}

func TestGlobalSkillIsVisibleFromEveryWorkspace(t *testing.T) {
	state := t.TempDir()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: global-only\ndescription: global\n---\nGlobal.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	global, err := skill.New(state, "global")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := global.Install(source); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{t.TempDir(), t.TempDir()} {
		m, err := skillManager(options{stateDir: state, root: root})
		if err != nil {
			t.Fatal(err)
		}
		got, err := m.Load("global-only")
		if err != nil || got.Name != "global-only" {
			t.Fatalf("global skill from %s = %+v, %v", root, got, err)
		}
	}
}

func gitFixture(t *testing.T, repo string) {
	t.Helper()
	gitRun(t, repo, "init", "-b", "main")
	gitRun(t, repo, "config", "user.email", "test@example.invalid")
	gitRun(t, repo, "config", "user.name", "fixture")
	dir := filepath.Join(repo, "skills", "fixture")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: fixture\ndescription: test\n---\nfixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repo, "add", ".")
	gitRun(t, repo, "commit", "-m", "fixture")
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, b)
	}
}

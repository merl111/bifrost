package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/skill"
)

func TestCatalogFetchListShowAndValidation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Catalog{Entries: []Entry{{ID: "local-tool", Name: "Local Tool", Description: "fixture", Repo: "https://github.com/example/repo", Ref: "main", Path: "skills/tool"}}})
	}))
	defer srv.Close()
	c, err := New(Config{CatalogURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.List(context.Background(), "local")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "local-tool" {
		t.Fatalf("unexpected list: %#v", got)
	}
	if _, err := c.Get(context.Background(), "../escape"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if err := ValidateEntry(Entry{ID: "bad", Name: "bad", Repo: "https://example.com/x", Ref: "main", Path: "../escape"}); err == nil {
		t.Fatal("accepted path escape")
	}
}

func TestBuiltinCatalogHasRichCuratedSkillsAndSkillsMPAPIAdapter(t *testing.T) {
	if len(Builtin()) < 15 {
		t.Fatalf("expected at least 15 built-in curated skills, got %d", len(Builtin()))
	}
	for _, b := range Builtin() {
		if err := ValidateEntry(b); err != nil {
			t.Fatalf("invalid builtin entry %q: %v", b.ID, err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/skills/search":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{
					"skills": []map[string]any{
						{
							"id":          "acme/repo/docker-helper",
							"name":        "Docker Helper",
							"description": "Build and inspect container images safely.",
							"github_url":  "https://github.com/acme/repo/tree/main/skills/docker-helper",
						},
					},
				},
			})
		case "/repos/example/skills/git/trees/main":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sha": "49f948faa9258a0c61caceaf225e179651397431",
				"tree": []map[string]any{
					{"path": "skills/k8s-debug/SKILL.md", "type": "blob"},
				},
			})
		}
	}))
	defer srv.Close()

	mpClient, err := New(Config{CatalogURL: srv.URL + "/api/v1/skills/search"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := mpClient.List(context.Background(), "docker")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "acme-repo-docker-helper" || got[0].Path != "skills/docker-helper" {
		t.Fatalf("unexpected SkillsMP list result: %#v", got)
	}
	resolved, err := mpClient.Get(context.Background(), "acme-repo-docker-helper")
	if err != nil || resolved.Name != "Docker Helper" {
		t.Fatalf("expected cached SkillsMP Get resolution, got %#v, err=%v", resolved, err)
	}

	ghClient, err := New(Config{CatalogURL: srv.URL + "/repos/example/skills/git/trees/main"})
	if err != nil {
		t.Fatal(err)
	}
	ghGot, err := ghClient.List(context.Background(), "k8s-debug")
	if err != nil {
		t.Fatal(err)
	}
	if len(ghGot) != 1 || ghGot[0].ID != "k8s-debug" || ghGot[0].Repo != "https://github.com/example/skills" {
		t.Fatalf("unexpected GitHub tree list result: %#v", ghGot)
	}
}

func TestCatalogRejectsRedirectAndInvalidSource(t *testing.T) {
	if _, err := New(Config{CatalogURL: "http://example.com/catalog"}); err == nil {
		t.Fatal("accepted non-loopback HTTP")
	}
	srv := httptest.NewServer(http.RedirectHandler("https://example.com", http.StatusFound))
	defer srv.Close()
	c, err := New(Config{CatalogURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.List(context.Background(), ""); err == nil {
		t.Fatal("followed redirect")
	}
}

func TestInstallPinnedLocalGitFixture(t *testing.T) {
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "test@example.invalid")
	runGit(t, repo, "config", "user.name", "fixture")
	skillDir := filepath.Join(repo, "skills", "tool")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: fixture-tool\ndescription: fixture\n---\nUse fixture.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "fixture")
	state := t.TempDir()
	m, err := skill.New(state, "project")
	if err != nil {
		t.Fatal(err)
	}
	// Use the explicit catalog entry path, then install through the same API
	// used by the CLI. The local absolute repository is test-only and never
	// accepted as an implicit source by the CLI.
	e := Entry{ID: "fixture-tool", Name: "Fixture", Repo: repo, Ref: "main", Path: "skills/tool"}
	if err := validateEntry(e, true); err != nil {
		t.Fatal(err)
	}
	if _, err := m.InstallGit(context.Background(), e.Repo, e.Ref, e.Path); err != nil {
		t.Fatal(err)
	}
	v, err := m.Load("fixture-tool")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v.Source, repo+"@") || v.Name != "fixture-tool" {
		t.Fatalf("unexpected installed metadata: %#v", v)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v (%s)", args, err, out)
	}
}

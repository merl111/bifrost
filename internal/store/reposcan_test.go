package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseGitHubRemote(t *testing.T) {
	tests := []struct {
		raw       string
		wantOwner string
		wantRepo  string
		wantURL   string
		wantOK    bool
	}{
		{
			raw:       "https://github.com/acme/widgets.git",
			wantOwner: "acme",
			wantRepo:  "widgets",
			wantURL:   "https://github.com/acme/widgets.git",
			wantOK:    true,
		},
		{
			raw:       "git@github.com:aklabs/laura-backend.git",
			wantOwner: "aklabs",
			wantRepo:  "laura-backend",
			wantURL:   "https://github.com/aklabs/laura-backend.git",
			wantOK:    true,
		},
		{
			raw:       "ssh://git@github.com/aklabs/laura-client",
			wantOwner: "aklabs",
			wantRepo:  "laura-client",
			wantURL:   "https://github.com/aklabs/laura-client.git",
			wantOK:    true,
		},
		{
			raw:       "aklabs/laura-sdk",
			wantOwner: "aklabs",
			wantRepo:  "laura-sdk",
			wantURL:   "https://github.com/aklabs/laura-sdk.git",
			wantOK:    true,
		},
		{
			raw:    "https://gitlab.com/acme/widgets.git",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		owner, repo, canonical, ok := ParseGitHubRemote(tt.raw)
		if ok != tt.wantOK {
			t.Fatalf("ParseGitHubRemote(%q) ok = %v, want %v", tt.raw, ok, tt.wantOK)
		}
		if !ok {
			continue
		}
		if owner != tt.wantOwner || repo != tt.wantRepo || canonical != tt.wantURL {
			t.Fatalf("ParseGitHubRemote(%q) = (%q, %q, %q), want (%q, %q, %q)", tt.raw, owner, repo, canonical, tt.wantOwner, tt.wantRepo, tt.wantURL)
		}
	}
}

func TestScanGitRepositoriesFindsMultiRepoWorkspace(t *testing.T) {
	root := t.TempDir()
	repos := []struct {
		dir    string
		remote string
		branch string
	}{
		{dir: "laura-admin", remote: "git@github.com:aklabs/laura-admin.git", branch: "main"},
		{dir: "laura-backend", remote: "https://github.com/aklabs/laura-backend.git", branch: "develop"},
		{dir: "nested/laura-sdk", remote: "https://github.com/aklabs/laura-sdk", branch: "main"},
	}
	for _, r := range repos {
		gitDir := filepath.Join(root, filepath.FromSlash(r.dir), ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = " + r.remote + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		head := "ref: refs/heads/" + r.branch + "\n"
		if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte(head), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	discovered, err := ScanGitRepositories(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(discovered) != 3 {
		t.Fatalf("len(discovered) = %d, want 3: %#v", len(discovered), discovered)
	}
	if discovered[0].Alias != "laura-admin" || discovered[0].GitHubSlug != "aklabs/laura-admin" {
		t.Fatalf("discovered[0] = %#v", discovered[0])
	}
	if discovered[1].Alias != "laura-backend" || discovered[1].DefaultBranch != "develop" {
		t.Fatalf("discovered[1] = %#v", discovered[1])
	}
	if discovered[2].Alias != "nested-laura-sdk" || discovered[2].GitHubSlug != "aklabs/laura-sdk" {
		t.Fatalf("discovered[2] = %#v", discovered[2])
	}
}

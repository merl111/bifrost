package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestProjectCommandScansAddsAndRemovesGitHubRepos(t *testing.T) {
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

	m := New(root)
	m.width, m.height = 120, 36

	cmd := m.executePaletteCommand("project scan")
	if cmd == nil {
		t.Fatal("expected scan command")
	}
	m.Update(cmd())

	if !m.projectOpen {
		t.Fatal("project manager did not open")
	}
	if len(m.projectStatus.Repos) != 2 {
		t.Fatalf("len(Repos) = %d, want 2: %#v", len(m.projectStatus.Repos), m.projectStatus.Repos)
	}
	plain := ansi.Strip(m.render())
	if !strings.Contains(plain, "laura-admin") || !strings.Contains(plain, "aklabs/laura-backend") {
		t.Fatalf("project view missing discovered repos:\n%s", plain)
	}

	// Add the selected repository via Enter key.
	_, addCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if addCmd == nil {
		t.Fatal("expected add command on Enter")
	}
	m.Update(addCmd())
	if !m.projectStatus.Repos[0].Linked {
		t.Fatal("first repository was not linked after Enter")
	}

	// Add all remaining repositories via :project add-all.
	addAllCmd := m.executePaletteCommand("project add-all")
	if addAllCmd == nil {
		t.Fatal("expected add-all command")
	}
	m.Update(addAllCmd())
	for _, r := range m.projectStatus.Repos {
		if !r.Linked {
			t.Fatalf("repo %q was not linked after add-all", r.Alias)
		}
	}

	// Remove a repository via :project remove.
	rmCmd := m.executePaletteCommand("project remove laura-admin")
	if rmCmd == nil {
		t.Fatal("expected remove command")
	}
	m.Update(rmCmd())
	if m.projectStatus.Repos[0].Linked {
		t.Fatal("laura-admin remained linked after remove")
	}
}

func TestProjectNameDescriptionAndLeftPaneProjectView(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"laura-admin", "laura-backend"} {
		gitDir := filepath.Join(root, name, ".git")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := "[remote \"origin\"]\n\turl = git@github.com:aklabs/" + name + ".git\n"
		if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	m := New(root, WithInitialProjectStatus(ProjectStatus{
		ProjectName:        "laura",
		ProjectDescription: "Laura multi-repo workspace",
		Root:               root,
		Repos: []ProjectRepoEntry{
			{
				Alias:         "laura-admin",
				Path:          filepath.Join(root, "laura-admin"),
				RelPath:       "laura-admin",
				GitHubSlug:    "aklabs/laura-admin",
				DefaultBranch: "main",
				CurrentBranch: "main",
				GitAdded:      3,
				GitModified:   2,
				GitDeleted:    1,
				Linked:        true,
			},
			{
				Alias:         "laura-backend",
				Path:          filepath.Join(root, "laura-backend"),
				RelPath:       "laura-backend",
				GitHubSlug:    "aklabs/laura-backend",
				DefaultBranch: "main",
				CurrentBranch: "feat/api",
				Linked:        true,
			},
		},
	}))
	m.width, m.height = 140, 36

	plain := ansi.Strip(m.render())
	if !strings.Contains(plain, "PROJECT · LAURA") {
		t.Fatalf("left pane title did not switch to PROJECT · LAURA:\n%s", plain)
	}
	if !strings.Contains(plain, "Laura multi-repo workspace") {
		t.Fatalf("left pane did not show project description:\n%s", plain)
	}
	if !strings.Contains(plain, "+3 ~2 -1") {
		t.Fatalf("left pane did not show git status counts (+3 ~2 -1):\n%s", plain)
	}

	// Open :project and rename via inline 'n' key.
	m.projectOpen = true
	m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if m.projectEditField != "name" {
		t.Fatalf("projectEditField = %q, want name", m.projectEditField)
	}
	m.projectEditInput = "laura-platform"
	_, saveCmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if saveCmd == nil {
		t.Fatal("expected save command when pressing Enter in project name editor")
	}
	m.Update(saveCmd())
	if m.projectStatus.ProjectName != "laura-platform" {
		t.Fatalf("ProjectName = %q, want laura-platform", m.projectStatus.ProjectName)
	}

	// Update description via :project description command.
	descCmd := m.executePaletteCommand("project description Unified x402 payment platform")
	if descCmd == nil {
		t.Fatal("expected description command")
	}
	m.Update(descCmd())
	if m.projectStatus.ProjectDescription != "Unified x402 payment platform" {
		t.Fatalf("ProjectDescription = %q, want Unified x402 payment platform", m.projectStatus.ProjectDescription)
	}
}

func TestChatMentionsPopupAndContextExpansion(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# Agent Instructions\nHello world\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "laura-backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "laura-backend", "server.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := New(root,
		WithInitialProjectStatus(ProjectStatus{
			ProjectName: "laura",
			Root:        root,
			Repos: []ProjectRepoEntry{
				{
					Alias:         "laura-backend",
					Path:          filepath.Join(root, "laura-backend"),
					RelPath:       "laura-backend",
					GitHubSlug:    "aklabs/laura-backend",
					DefaultBranch: "main",
					GitAdded:      2,
					GitModified:   1,
					Linked:        true,
				},
			},
		}),
	)
	m.width, m.height = 140, 36
	m.focus = focusChat
	m.chatEditing = true

	// Typing "@laura" should open the mention popup with @repo:laura-backend first.
	for _, r := range "explain @laura" {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if !m.chatMention.open || len(m.chatMention.candidates) == 0 {
		t.Fatalf("expected @ mention popup to open, got open=%v candidates=%d", m.chatMention.open, len(m.chatMention.candidates))
	}
	if m.chatMention.candidates[0].Token != "repo:laura-backend" {
		t.Fatalf("first mention candidate = %q, want repo:laura-backend", m.chatMention.candidates[0].Token)
	}
	plain := ansi.Strip(m.render())
	if !strings.Contains(plain, "@laura") || !strings.Contains(plain, "@repo:laura-backend") {
		t.Fatalf("rendered chat panel did not show @ mention popup:\n%s", plain)
	}

	// Pressing Tab completes the @ mention inline without sending the message.
	m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.chatMention.open {
		t.Fatal("expected @ mention popup to close after Tab completion")
	}
	if m.chatInput != "explain @repo:laura-backend " {
		t.Fatalf("chatInput after Tab = %q, want %q", m.chatInput, "explain @repo:laura-backend ")
	}

	// Add a file mention @AGENTS.md and verify context expansion.
	m.chatInput += "and @AGENTS.md"
	expanded, labels := m.expandChatPromptMentions(m.chatInput)
	if len(labels) != 2 {
		t.Fatalf("labels = %v, want 2 attached items", labels)
	}
	if !strings.Contains(expanded, `<context kind="repository" alias="laura-backend"`) || !strings.Contains(expanded, "# Agent Instructions") {
		t.Fatalf("expanded prompt missing repository or file context:\n%s", expanded)
	}
}

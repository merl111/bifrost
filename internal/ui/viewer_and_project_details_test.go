package ui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestChatUserTurnSpacingAndDistinction(t *testing.T) {
	m := New(t.TempDir())
	m.width = 140
	m.height = 42
	m.chatLines = []chatLine{
		{kind: "user", text: "Hello there"},
		{kind: "assistant", text: "First answer from assistant."},
		{kind: "user", text: "ok thx"},
	}
	m.chatScroll = m.chatScrollMax()
	view := ansi.Strip(m.renderChat())
	if !strings.Contains(view, "› You") {
		t.Fatalf("expected user header badge '› You' in chat view, got:\n%s", view)
	}
	if !strings.Contains(view, "┃ ok thx") {
		t.Fatalf("expected user rail '┃ ok thx' in chat view, got:\n%s", view)
	}
	if !strings.Contains(view, "───") {
		t.Fatalf("expected turn separator line before second user prompt, got:\n%s", view)
	}
}

func TestChatClickFileAndDirectoryLinksOpenInViewer(t *testing.T) {
	root := t.TempDir()
	subDir := filepath.Join(root, "pkg")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(subDir, "demo.go")
	if err := os.WriteFile(filePath, []byte("package pkg\n\nfunc Demo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := New(root)
	target, ok := m.resolveTokenAsLinkTarget("pkg/demo.go:3")
	if !ok || target.IsDir || target.RelPath != "pkg/demo.go" || target.LineNum != 3 {
		t.Fatalf("resolveTokenAsLinkTarget(pkg/demo.go:3) = %+v, %v", target, ok)
	}
	m.openChatLinkTarget(target)
	if m.viewPath != "pkg/demo.go" {
		t.Fatalf("viewPath = %q, want pkg/demo.go", m.viewPath)
	}

	dirTarget, ok := m.resolveTokenAsLinkTarget("pkg/")
	if !ok || !dirTarget.IsDir || dirTarget.RelPath != "pkg" {
		t.Fatalf("resolveTokenAsLinkTarget(pkg/) = %+v, %v", dirTarget, ok)
	}
}

func TestViewerAgentControlHighlightDiffAndFlow(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "sample.go")
	content := "package main\n\nfunc First() {\n\tprintln(1)\n}\n\nfunc Second() {\n\tprintln(2)\n}\n"
	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	m := New(root)
	payload := ViewerCommandPayload{
		Action: "open",
		Path:   "sample.go",
		Symbol: "Second",
		Note:   "Entry point for second stage",
	}
	raw, _ := json.Marshal(payload)
	m.appendChatEvent(ChatEvent{Kind: "viewer", Detail: string(raw)})

	if m.viewPath != "sample.go" {
		t.Fatalf("viewPath = %q, want sample.go", m.viewPath)
	}
	if len(m.viewerHighlights) == 0 || m.viewerHighlights[0].StartLine != 7 {
		t.Fatalf("viewerHighlights = %+v, want startLine=7", m.viewerHighlights)
	}
	rendered := ansi.Strip(m.renderViewer())
	if !strings.Contains(rendered, "Entry point for second stage") {
		t.Fatalf("expected banner note in viewer output:\n%s", rendered)
	}

	// Test multi-step code flow
	flowPayload := ViewerCommandPayload{
		Action: "flow",
		Title:  "Execution Flow",
		Steps: []ViewerStep{
			{Title: "Step 1", Path: "sample.go", StartLine: 3, EndLine: 5, Note: "Starts in First"},
			{Title: "Step 2", Path: "sample.go", StartLine: 7, EndLine: 9, Note: "Calls Second"},
		},
	}
	flowRaw, _ := json.Marshal(flowPayload)
	m.appendChatEvent(ChatEvent{Kind: "viewer", Detail: string(flowRaw)})
	if m.viewerFlowIndex != 0 {
		t.Fatalf("viewerFlowIndex = %d, want 0", m.viewerFlowIndex)
	}
	m.stepViewerFlow(1)
	if m.viewerFlowIndex != 1 || m.viewerHighlights[0].StartLine != 7 {
		t.Fatalf("after stepViewerFlow(1): index=%d highlights=%+v", m.viewerFlowIndex, m.viewerHighlights)
	}
}

func TestProjectDetailsTabbedModal(t *testing.T) {
	m := New(t.TempDir(), WithProject(ProjectCallbacks{
		FetchDetails: func(ctx context.Context) (ProjectDetails, error) {
			return ProjectDetails{
				GitHubConnected: true,
				GitHubUser:      "octocat",
				GitHubRepos: []ProjectGitHubRepoInfo{
					{Alias: "bifrost", Slug: "acme/bifrost", Stars: 42, Forks: 7, OpenIssues: 2, Description: "AI harness"},
				},
				ReadmeTitle:   "README.md",
				ReadmeContent: "# Bifrost Project\nHello README",
				OpenPRs: []ProjectGitHubPR{
					{Number: 12, Title: "Add Notion & GitHub connectors", Author: "mathias", Base: "main", Head: "feat/connectors"},
				},
				OpenIssues: []ProjectGitHubIssue{
					{Number: 5, Title: "Support viewer highlights", Author: "mathias", Comments: 3},
				},
				NotionConnected: true,
				NotionWorkspace: "Acme Docs",
				NotionPages: []ProjectNotionItem{
					{ID: "p1", Title: "Architecture Spec", URL: "https://notion.so/p1"},
				},
			}, nil
		},
	}))

	cmd := m.openProjectDetailsModal(0)
	if cmd != nil {
		msg := cmd()
		_, _ = m.Update(msg)
	}

	view0 := ansi.Strip(m.renderProjectPanel(120, 32))
	if !strings.Contains(view0, "acme/bifrost") || !strings.Contains(view0, "octocat") {
		t.Fatalf("Overview tab missing repo/user info:\n%s", view0)
	}

	m.handleProjectKey("2", "2")
	view1 := ansi.Strip(m.renderProjectPanel(120, 32))
	if !strings.Contains(view1, "Hello README") {
		t.Fatalf("README tab missing content:\n%s", view1)
	}

	m.handleProjectKey("3", "3")
	view2 := ansi.Strip(m.renderProjectPanel(120, 32))
	if !strings.Contains(view2, "Add Notion & GitHub connectors") {
		t.Fatalf("PRs tab missing PR:\n%s", view2)
	}

	m.handleProjectKey("4", "4")
	view3 := ansi.Strip(m.renderProjectPanel(120, 32))
	if !strings.Contains(view3, "Support viewer highlights") {
		t.Fatalf("Issues tab missing issue:\n%s", view3)
	}

	m.handleProjectKey("5", "5")
	view4 := ansi.Strip(m.renderProjectPanel(120, 32))
	if !strings.Contains(view4, "Acme Docs") || !strings.Contains(view4, "Architecture Spec") {
		t.Fatalf("Connectors tab missing Notion info:\n%s", view4)
	}
}

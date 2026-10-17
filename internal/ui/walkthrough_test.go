package ui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplanationWalkthroughNavigationAndGeometry(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0600)
	steps := []ViewerStep{
		{Kind: "concept", Title: "Request lifecycle", Explanation: strings.Repeat("Follow the request through validation, processing and response.\n", 25)},
		{Path: "main.go", Title: "Entry point", StartLine: 3, EndLine: 3, Explanation: "The process begins here."},
		{Kind: "diff", Title: "Review the change", Content: "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new", Explanation: "Check whether this change preserves the contract."},
	}
	for _, theme := range []string{"dark", "light", "plain"} {
		for _, size := range [][2]int{{40, 16}, {80, 24}, {120, 36}, {160, 48}} {
			m := New(root)
			m.theme = theme
			m.width, m.height = size[0], size[1]
			m.executeViewerPayload(ViewerCommandPayload{Action: "flow", Title: "Guided review", Steps: steps})
			m.focus = focusViewer
			if !m.viewerExplanationOpen || m.viewErr != "" {
				t.Fatal("concept required a file")
			}
			m.stepViewerFlow(-1)
			if m.viewerFlowIndex != 0 {
				t.Fatal("back wrapped at start")
			}
			m.handleNormalNavigation("pagedown")
			if m.viewerExplanationOffset == 0 {
				t.Fatal("explanation cannot scroll")
			}
			m.stepViewerFlow(1)
			if m.viewerFlowIndex != 1 || m.viewerExplanationOpen || !strings.Contains(m.viewer, "func main") || m.viewerHighlights[0].StartLine != 3 {
				t.Fatal("code step not selected")
			}
			m.stepViewerFlow(1)
			m.stepViewerFlow(1)
			if m.viewerFlowIndex != 2 || !strings.HasPrefix(m.viewPath, "diff") || !strings.Contains(m.viewer, "+new") {
				t.Fatal("diff or boundary failed")
			}
			m.stepViewerFlow(-1)
			if m.viewerFlowIndex != 1 {
				t.Fatal("back lost position")
			}
			for _, line := range strings.Split(m.render(), "\n") {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("walkthrough overflow at %v", size)
				}
			}
			m.executeViewerPayload(ViewerCommandPayload{Action: "open", Path: "main.go"})
			if len(m.viewerFlowSteps) != 0 || m.viewerExplanation != "" {
				t.Fatal("unrelated file retained walkthrough")
			}
		}
	}
}

func TestWalkthroughValidationAndDraftPreservation(t *testing.T) {
	for _, steps := range [][]ViewerStep{nil, make([]ViewerStep, 65), {{Kind: "bad", Note: "x"}}, {{Path: "../escape"}}, {{Kind: "diff", Note: "missing diff"}}, {{Path: "a", StartLine: 4, EndLine: 2}}, {{Explanation: strings.Repeat("x", 16385)}}} {
		if ValidateWalkthrough(steps) == nil {
			t.Fatalf("accepted invalid walkthrough: %d steps", len(steps))
		}
	}
	m := New(t.TempDir())
	m.chatInput = "keep my draft"
	m.executePaletteCommand("explain request routing")
	if m.chatInput != "keep my draft" || m.paletteError == "" {
		t.Fatal("explanation overwrote draft")
	}
	m.chatInput = ""
	m.executePaletteCommand("explain review my changes")
	if !strings.Contains(m.chatInput, "Back/Next") || m.chatRunning {
		t.Fatal("explanation did not prepare editable request")
	}
}

func TestTerminalBrandFitsAndHasFallback(t *testing.T) {
	for _, mode := range []string{IconsASCII, IconsUnicode, IconsNerd} {
		for _, theme := range []string{"dark", "light", "plain"} {
			for _, size := range [][2]int{{32, 16}, {80, 24}, {120, 36}, {160, 48}} {
				m := New(t.TempDir(), WithIcons(mode))
				m.width, m.height = size[0], size[1]
				m.theme = theme
				if mode == IconsASCII && m.brandMark() != "|>|" {
					t.Fatal("missing ASCII fallback")
				}
				rendered := m.render()
				lines := strings.Split(rendered, "\n")
				if len(lines) > size[1] {
					t.Fatal("logo grew viewport")
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatal("logo overflow")
					}
				}
				if theme == "plain" && strings.Contains(rendered, "\x1b[") {
					t.Fatal("plain brand emits color")
				}
			}
		}
	}
}

// Rendered terminal fixtures: these are real Model output, not GUI mockups.
func TestWriteExplanationSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_UI_SNAPSHOTS") != "1" {
		t.Skip("opt-in render fixtures")
	}
	m := New(t.TempDir())
	m.width, m.height = 160, 48
	m.layout, _ = layoutFor("conversation")
	m.root = "bifrost"
	m.projectStatus.ProjectName = "Bifrost"
	m.connection = ConnectionInfo{Provider: "openai", Model: "workspace model"}
	if err := os.WriteFile("testdata/snapshots/tui-brand-160x48.ansi", []byte(m.render()), 0600); err != nil {
		t.Fatal(err)
	}
	m.layout, _ = layoutFor("review")
	m.executeViewerPayload(ViewerCommandPayload{Action: "flow", Title: "Request lifecycle", Steps: []ViewerStep{
		{Kind: "code", Title: "Validate before processing", Content: "func Handle(input string) error {\n    if input == \"\" {\n        return ErrEmptyInput\n    }\n    return process(input)\n}", Path: "handler.go", StartLine: 2, EndLine: 4, Explanation: "Reject empty input at the boundary. This keeps the processor focused on valid requests.\n\nReview the callers and add a regression for the error case before changing this contract."},
		{Kind: "concept", Title: "Verify the contract", Explanation: "Check valid requests and the empty-input error independently."},
	}})
	m.focus = focusViewer
	if err := os.WriteFile("testdata/snapshots/tui-explanation-160x48.ansi", []byte(m.render()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWalkthroughMouseUsesViewerCoordinates(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 120, 36
	m.executeViewerPayload(ViewerCommandPayload{Action: "flow", Steps: []ViewerStep{{Kind: "concept", Explanation: strings.Repeat("context\n", 80)}, {Kind: "concept", Explanation: "Next"}}})
	m.focus = focusChat
	x, _ := m.viewerContentOrigin()
	if !m.walkthroughMouse(tea.Mouse{X: x + 11, Y: 3, Button: tea.MouseLeft}, false) || m.viewerFlowIndex != 1 {
		t.Fatal("next hit target failed")
	}
	if !m.walkthroughMouse(tea.Mouse{X: x + 1, Y: 3, Button: tea.MouseLeft}, false) || m.viewerFlowIndex != 0 {
		t.Fatal("back hit target failed")
	}
	m.walkthroughMouse(tea.Mouse{X: x + 1, Y: 8, Button: tea.MouseWheelDown}, true)
	if m.viewerExplanationOffset != 3 {
		t.Fatal("explanation wheel did not scroll")
	}
	m.walkthroughMouse(tea.Mouse{X: x + 21, Y: 3, Button: tea.MouseLeft}, false)
	if m.viewerExplanationOpen {
		t.Fatal("explanation toggle hit target failed")
	}
	m.paletteOpen = true
	if m.walkthroughMouse(tea.Mouse{X: x + 11, Y: 3, Button: tea.MouseLeft}, false) {
		t.Fatal("walkthrough intercepted palette")
	}
}

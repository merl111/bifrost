package ui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ValidateWalkthrough bounds model-provided presentations before either UI uses them.
func ValidateWalkthrough(steps []ViewerStep) error {
	if len(steps) == 0 || len(steps) > 64 {
		return errors.New("a walkthrough needs 1–64 steps")
	}
	size := 0
	for i, s := range steps {
		if s.Kind != "" && s.Kind != "code" && s.Kind != "concept" && s.Kind != "diff" {
			return fmt.Errorf("step %d: kind must be code, concept, or diff", i+1)
		}
		if s.Path != "" && !filepath.IsLocal(s.Path) {
			return fmt.Errorf("step %d: path must be workspace-relative", i+1)
		}
		if s.Kind == "diff" && strings.TrimSpace(s.Content) == "" {
			return fmt.Errorf("step %d: diff content is required", i+1)
		}
		if s.Path == "" && s.Content == "" && s.Explanation == "" && s.Note == "" {
			return fmt.Errorf("step %d is empty", i+1)
		}
		if s.StartLine < 0 || s.EndLine < 0 || (s.EndLine > 0 && s.EndLine < s.StartLine) {
			return fmt.Errorf("step %d: invalid line range", i+1)
		}
		size += len(s.Title) + len(s.Path) + len(s.Note) + len(s.Explanation) + len(s.Content)
		if len(s.Explanation)+len(s.Note) > 16<<10 || len(s.Content) > 64<<10 || size > 128<<10 {
			return errors.New("walkthrough exceeds text budget (16 KiB explanation, 64 KiB code, 128 KiB total)")
		}
	}
	return nil
}

// WalkthroughStep is shared by terminal and native presentations.
func WalkthroughStep(title string, steps []ViewerStep, index int) ViewerCommandPayload {
	if index < 0 || index >= len(steps) {
		return ViewerCommandPayload{}
	}
	s := steps[index]
	action := "open"
	if s.Kind == "diff" {
		action = "diff"
	}
	if s.Kind == "concept" {
		action = "snippet"
	}
	return ViewerCommandPayload{Action: action, Path: s.Path, Title: s.Title,
		Content: s.Content, StartLine: s.StartLine, EndLine: s.EndLine,
		Explanation: valueOr(s.Explanation, s.Note),
		Note:        fmt.Sprintf("%s · Step %d/%d · %s", valueOr(title, "Walkthrough"), index+1, len(steps), s.Title)}
}

// ExplanationPrompt prepares an editable request; starting it remains a normal chat send.
func ExplanationPrompt(topic string) string {
	return "Create a step-by-step explanation of " + valueOr(strings.TrimSpace(topic), "this codebase's main flow") + " with Back/Next navigation. Inspect the source first and pair each explanation with relevant code or an actual diff. Do not modify files."
}

func (m *Model) closeWalkthrough() {
	m.viewerFlowSteps = nil
	m.viewerExplanation = ""
	m.viewerExplanationOpen = false
	m.viewerExplanationOffset = 0
	m.viewerBanner = ""
}

func (m *Model) walkthroughExplanationLines() []string {
	width := max(1, m.terminalWidth()-2)
	return wrapChatText(sanitizeDisplay(m.viewerExplanation), width)
}

func (m *Model) scrollExplanation(stroke string) bool {
	if !m.viewerExplanationOpen || m.focus != focusViewer || !m.viewerTabIsFile() {
		return false
	}
	delta := 0
	switch stroke {
	case "up", "k":
		delta = -1
	case "down", "j":
		delta = 1
	case "pageup":
		delta = -m.viewerViewport()
	case "pagedown":
		delta = m.viewerViewport()
	default:
		return false
	}
	m.viewerExplanationOffset = max(0, min(m.viewerExplanationOffset+delta, max(0, len(m.walkthroughExplanationLines())-m.viewerViewport()+3)))
	return true
}

func (m *Model) walkthroughPreviewLines() []string {
	if len(m.viewerFlowSteps) == 0 || m.viewerExplanation == "" {
		return nil
	}
	lines := m.walkthroughExplanationLines()
	limit := min(5, max(1, (m.mainPaneHeight()-3)/3))
	if len(lines) > limit {
		return append(lines[:limit], "[x] Read full explanation")
	}
	return lines
}

func (m *Model) walkthroughControls() string {
	return m.colors().cyanText("[p] Back  [n] Next  [x] Explain/code")
}

func (m *Model) walkthroughMouse(mouse tea.Mouse, wheel bool) bool {
	if len(m.viewerFlowSteps) == 0 || !m.viewerTabIsFile() || !m.focusVisible(focusViewer) || m.mode != ModeNormal || m.paletteOpen || m.helpOpen || m.accountsOpen || m.marketplaceOpen || m.settingsOpen {
		return false
	}
	x, width := m.viewerContentOrigin()
	if mouse.X < x || mouse.X >= x+width || mouse.Y < 3 || mouse.Y >= m.mainPaneHeight() {
		return false
	}
	if wheel {
		previous := m.focus
		m.focus = focusViewer
		key := "down"
		if mouse.Button == tea.MouseWheelUp {
			key = "up"
		}
		for range 3 {
			m.handleNormalNavigation(key)
		}
		m.focus = previous
		return true
	}
	if mouse.Button != tea.MouseLeft || mouse.Y != 3 {
		return false
	}
	switch col := mouse.X - x; {
	case col < 8:
		m.stepViewerFlow(-1)
	case col >= 10 && col < 18:
		m.stepViewerFlow(1)
	case col >= 20 && col < 34:
		m.viewerExplanationOpen = !m.viewerExplanationOpen
	default:
		return false
	}
	m.focus = focusViewer
	return true
}

func (m *Model) walkthroughCommand(name string, args []string) tea.Cmd {
	switch name {
	case "explain":
		if strings.TrimSpace(m.chatInput) != "" {
			m.paletteError = "Send or clear the current draft before preparing an explanation"
			m.status = m.paletteError
			return nil
		}
		m.chatInput = ExplanationPrompt(strings.Join(args, " "))
		m.focus, m.chatEditing = focusChat, true
		m.status = "Explanation request ready · edit it, then send"
	case "walkthrough":
		if len(args) != 1 {
			m.paletteError = "usage: :walkthrough next|prev|explanation|code|close"
			m.status = m.paletteError
			return nil
		}
		if len(m.viewerFlowSteps) == 0 {
			m.paletteError = "No walkthrough loaded · use :explain TOPIC"
			m.status = m.paletteError
			return nil
		}
		m.viewerTab = 0
		switch args[0] {
		case "next":
			m.stepViewerFlow(1)
		case "prev":
			m.stepViewerFlow(-1)
		case "explanation":
			m.viewerExplanationOpen = true
		case "code":
			m.viewerExplanationOpen = false
		case "close":
			m.closeWalkthrough()
		default:
			m.paletteError = "usage: :walkthrough next|prev|explanation|code|close"
			m.status = m.paletteError
			return nil
		}
		m.focus = focusViewer
	}
	return nil
}

func (m *Model) renderWalkthroughExplanation() string {
	lines := m.walkthroughExplanationLines()
	start := min(m.viewerExplanationOffset, len(lines))
	end := min(start+max(1, m.viewerViewport()-3), len(lines))
	return m.walkthroughControls() + "\n" + m.colors().accentText(sanitizeDisplay(m.viewerBanner)) + "\n" + strings.Join(lines[start:end], "\n")
}

func (m *Model) viewerHeading() string {
	p := m.colors()
	explanationRows := m.walkthroughPreviewLines()
	header := "◆ " + sanitizeDisplay(m.viewPath)
	if len(m.viewerFlowSteps) > 0 {
		header = m.walkthroughControls() + "\n" + header
	}
	if m.viewerBanner != "" {
		header += "\n" + p.violetText("✦ "+sanitizeDisplay(m.viewerBanner))
	}
	if len(explanationRows) > 0 {
		header += "\n" + strings.Join(explanationRows, "\n") + "\n"
	}
	return header
}

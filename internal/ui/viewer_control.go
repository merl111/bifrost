package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
)

// ViewerHighlightRange marks a span of 1-indexed lines in the viewer with an
// optional annotation label (for example to highlight a function or a step in
// a code flow).
type ViewerHighlightRange struct {
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
	Label     string `json:"label,omitempty"`
}

// ViewerStep represents one stop in a multi-file or multi-function code flow
// walkthrough pushed by the AI agent into the viewer.
type ViewerStep struct {
	Kind        string `json:"kind,omitempty"` // code (default), concept, or diff
	Content     string `json:"content,omitempty"`
	Explanation string `json:"explanation,omitempty"`
	Title       string `json:"title,omitempty"`
	Path        string `json:"path"`
	StartLine   int    `json:"start_line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
	Note        string `json:"note,omitempty"`
}

// ViewerCommandPayload is the structured payload emitted by agent viewer tools
// (`viewer_open`, `viewer_diff`, `viewer_flow`) inside a ChatEvent with
// Kind == "viewer".
type ViewerCommandPayload struct {
	Explanation string                 `json:"explanation,omitempty"`
	Action      string                 `json:"action"` // "open", "diff", "flow", "snippet"
	Path        string                 `json:"path,omitempty"`
	Title       string                 `json:"title,omitempty"`
	Content     string                 `json:"content,omitempty"`
	Line        int                    `json:"line,omitempty"`
	StartLine   int                    `json:"start_line,omitempty"`
	EndLine     int                    `json:"end_line,omitempty"`
	Symbol      string                 `json:"symbol,omitempty"`
	Note        string                 `json:"note,omitempty"`
	Highlights  []ViewerHighlightRange `json:"highlights,omitempty"`
	Steps       []ViewerStep           `json:"steps,omitempty"`
}

func (m *Model) applyViewerChatEvent(event ChatEvent) {
	raw := strings.TrimSpace(event.Detail)
	if raw == "" {
		raw = strings.TrimSpace(event.Text)
	}
	if raw == "" {
		return
	}
	var payload ViewerCommandPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return
	}
	m.executeViewerPayload(payload)
}

func (m *Model) executeViewerPayload(payload ViewerCommandPayload) {
	// Ensure the viewer pane is visible if the user is in Focus mode
	if m.activeLayout().name == LayoutFocus {
		m.layout, _ = layoutFor(string(LayoutStudio))
	}

	if payload.Action != "flow" {
		m.closeWalkthrough()
	}
	switch strings.ToLower(strings.TrimSpace(payload.Action)) {
	case "diff", "review":
		title := strings.TrimSpace(payload.Title)
		if title == "" {
			if payload.Path != "" {
				title = "diff · " + payload.Path
			} else {
				title = "review"
			}
		}
		m.viewPath = title
		if strings.TrimSpace(payload.Content) != "" {
			m.viewer = payload.Content
			m.viewErr = ""
			m.viewerOffset = 0
			m.viewerHighlights = payload.Highlights
			m.viewerBanner = payload.Note
		} else if m.reviewLoader != nil {
			if text, err := m.reviewLoader(context.Background()); err == nil {
				m.viewer = text
				m.viewErr = ""
				m.viewerOffset = 0
			}
		}
		m.status = "agent opened diff in viewer: " + title

	case "flow":
		if err := ValidateWalkthrough(payload.Steps); err != nil {
			m.status = err.Error()
			return
		}
		m.viewerTab = 0
		m.viewerFlowSteps = payload.Steps
		m.viewerFlowIndex = 0
		m.viewerFlowTitle = valueOr(payload.Title, "Code Flow Walkthrough")
		m.openViewerFlowStep(0)

	case "snippet":
		m.viewPath = valueOr(payload.Title, valueOr(payload.Path, "agent-snippet"))
		m.viewer = payload.Content
		m.viewErr = ""
		m.viewerOffset = 0
		m.viewerHighlights = payload.Highlights
		m.viewerBanner = payload.Note
		m.status = "agent displayed snippet in viewer"

	default: // "open" or "highlight"
		relPath := strings.TrimPrefix(strings.TrimSpace(payload.Path), "./")
		if relPath == "" && payload.Content != "" {
			m.viewPath = valueOr(payload.Title, "viewer")
			m.viewer = payload.Content
			m.viewErr = ""
			m.viewerOffset = 0
			m.viewerHighlights = payload.Highlights
			m.viewerBanner = payload.Note
			return
		}
		content := payload.Content
		if content == "" && relPath != "" {
			loaded, err := m.readFile(relPath)
			if err != nil {
				m.status = "viewer error: " + err.Error()
				return
			}
			content = loaded
		}
		m.viewPath = relPath
		m.viewer = content
		m.viewErr = ""
		m.viewerBanner = payload.Note
		m.viewerHighlights = nil

		// Resolve symbol or line ranges
		startLine := payload.StartLine
		endLine := payload.EndLine
		if startLine <= 0 && payload.Line > 0 {
			startLine = payload.Line
		}
		if payload.Symbol != "" && startLine <= 0 {
			sLine, eLine := findSymbolLineRange(content, payload.Symbol)
			if sLine > 0 {
				startLine = sLine
				endLine = eLine
			}
		}
		if startLine > 0 {
			if endLine < startLine {
				endLine = startLine
			}
			m.viewerHighlights = append(m.viewerHighlights, ViewerHighlightRange{
				StartLine: startLine,
				EndLine:   endLine,
				Label:     valueOr(payload.Note, payload.Symbol),
			})
		}
		if len(payload.Highlights) > 0 {
			m.viewerHighlights = append(m.viewerHighlights, payload.Highlights...)
		}
		if len(m.viewerHighlights) > 0 && m.viewerHighlights[0].StartLine > 1 {
			m.viewerOffset = maxInt(m.viewerHighlights[0].StartLine-4, 0)
			m.clampViewerOffset()
		} else {
			m.viewerOffset = 0
		}
		m.syncFileTreeToPath(relPath)
		if startLine > 0 {
			m.status = fmt.Sprintf("agent highlighted %s:%d in viewer", relPath, startLine)
		} else {
			m.status = "agent opened " + relPath + " in viewer"
		}
	}
}

func (m *Model) openViewerFlowStep(index int) {
	if len(m.viewerFlowSteps) == 0 {
		return
	}
	index = maxInt(0, minInt(index, len(m.viewerFlowSteps)-1))
	m.viewerFlowIndex = index
	step := m.viewerFlowSteps[index]
	payload := WalkthroughStep(m.viewerFlowTitle, m.viewerFlowSteps, index)
	m.viewerExplanationOpen = step.Kind == "concept"
	m.viewerExplanationOffset = 0
	m.viewerExplanation = payload.Explanation
	m.viewerBanner = fmt.Sprintf("Step %d/%d · %s", index+1, len(m.viewerFlowSteps), step.Title)
	m.viewer, m.viewErr, m.viewerOffset = "", "", 0
	m.viewPath = valueOr(payload.Path, valueOr(step.Title, "Explanation"))
	if step.Kind == "diff" {
		m.viewPath = "diff · " + m.viewPath
	}
	m.viewer = payload.Content
	if m.viewer == "" && payload.Path != "" && step.Kind != "diff" && step.Kind != "concept" {
		var err error
		m.viewer, err = m.readFile(payload.Path)
		if err != nil {
			m.viewErr = err.Error()
		}
	}
	m.viewerHighlights = ViewerHighlights(m.viewer, payload)
	for i := range m.viewerHighlights {
		m.viewerHighlights[i].Label = step.Title
	}
	if len(m.viewerHighlights) > 0 {
		m.viewerOffset = maxInt(m.viewerHighlights[0].StartLine-4, 0)
	}
	m.syncFileTreeToPath(payload.Path)
	m.status = payload.Note

}

func (m *Model) stepViewerFlow(delta int) bool {
	if len(m.viewerFlowSteps) == 0 {
		return false
	}
	next := max(0, min(m.viewerFlowIndex+delta, len(m.viewerFlowSteps)-1))
	m.openViewerFlowStep(next)
	return true
}

func (m *Model) syncFileTreeToPath(relPath string) {
	if relPath == "" {
		return
	}
	parentDir := path.Dir(relPath)
	if parentDir == "." {
		parentDir = ""
	}
	m.dir = parentDir
	m.files = m.listFiles(m.dir)
	baseName := path.Base(relPath)
	for i, f := range m.files {
		if f.Path == baseName {
			m.selected = i
			m.ensureFileVisible()
			break
		}
	}
}

func (m *Model) viewerLineHighlight(lineNum1Based int) (bool, string) {
	for _, h := range m.viewerHighlights {
		end := h.EndLine
		if end < h.StartLine {
			end = h.StartLine
		}
		if lineNum1Based >= h.StartLine && lineNum1Based <= end {
			if lineNum1Based == h.StartLine {
				return true, h.Label
			}
			return true, ""
		}
	}
	return false, ""
}

func findSymbolLineRange(content, symbol string) (int, int) {
	symbol = strings.TrimSpace(symbol)
	if symbol == "" {
		return 0, 0
	}
	lines := strings.Split(content, "\n")
	startLine := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "func "+symbol+"(") ||
			strings.Contains(trimmed, ") "+symbol+"(") ||
			strings.Contains(trimmed, "type "+symbol+" ") ||
			strings.Contains(trimmed, "class "+symbol) ||
			strings.Contains(trimmed, "def "+symbol+"(") ||
			strings.Contains(trimmed, "function "+symbol+"(") ||
			strings.Contains(trimmed, symbol) {
			startLine = i + 1
			break
		}
	}
	if startLine == 0 {
		return 0, 0
	}
	endLine := minInt(startLine+20, len(lines))
	// Try to find closing top-level brace
	for i := startLine; i < minInt(startLine+80, len(lines)); i++ {
		if lines[i] == "}" || strings.HasPrefix(lines[i], "}") {
			endLine = i + 1
			break
		}
	}
	return startLine, endLine
}

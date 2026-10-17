package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type viewerState struct {
	path                      string
	edit                      cui.Widget
	steps                     []ui.ViewerStep
	index                     int
	title                     string
	bar, note, previous, next cui.Widget
	explanation, close        cui.Widget
}

func (g *desktop) buildViewerControls() {
	v := &g.viewer
	v.bar = g.sourcePage.Box(cui.Vertical, 6)
	v.note = label(v.bar, "", 11, violet)
	v.explanation = v.bar.Textarea("")
	v.explanation.ReadOnly(true)
	v.explanation.MinSize(0, 145)
	v.explanation.Font("", 12, 400)
	v.explanation.Accessibility("Step explanation", "Scrollable explanation for the selected walkthrough step")
	v.explanation.SetVisible(false)
	row := v.bar.Box(cui.Horizontal, 6)
	v.edit = button(row, "Edit file", g.editViewedSource)
	v.previous = button(row, "Previous step", func() { g.viewerStep(-1) })
	v.next = button(row, "Next step", func() { g.viewerStep(1) })
	v.close = button(row, "End walkthrough", g.closeWalkthrough)
	v.bar.SetVisible(false)
}

func (g *desktop) receiveViewer(e ui.ChatEvent) {
	raw := e.Detail
	if strings.TrimSpace(raw) == "" {
		raw = e.Text
	}
	var p ui.ViewerCommandPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		g.showError(fmt.Errorf("viewer command: %w", err))
		return
	}
	if p.Action == "flow" {
		if err := ui.ValidateWalkthrough(p.Steps); err != nil {
			g.showError(err)
			return
		}
		g.viewer.steps = append([]ui.ViewerStep(nil), p.Steps...)
		g.viewer.title = p.Title
		g.viewer.index = -1
		g.viewerStep(1)
		return
	}
	g.viewer.steps = nil
	g.displayViewer(p)
}

func (g *desktop) viewerStep(delta int) {
	v := &g.viewer
	if len(v.steps) == 0 {
		return
	}
	v.index = max(0, min(v.index+delta, len(v.steps)-1))
	g.displayViewer(ui.WalkthroughStep(v.title, v.steps, v.index))
}

func (g *desktop) displayViewer(p ui.ViewerCommandPayload) {
	g.rememberEditor()
	g.showSourcePage()
	g.viewRequest++
	request := g.viewRequest
	g.previewPath = ""
	g.viewer.path = ""
	g.viewer.edit.SetEnabled(false)
	g.previewLoading = false
	g.updateEditorActions()
	g.mention.SetEnabled(false)
	if !g.inspectorVisible {
		g.toggleInspector()
	}
	title := p.Title
	if title == "" {
		title = p.Path
	}
	if title == "" {
		title = "Agent viewer"
	}
	g.previewTitle.SetText(shortText(title, 40))
	g.previewTitle.Tooltip(title)
	g.setEditorText("Loading…")
	g.viewer.note.SetText(safeText(p.Note))
	g.viewer.explanation.SetText(cleanText(p.Explanation))
	g.viewer.explanation.SetTextRange(0, 0)
	g.viewer.explanation.SetVisible(p.Explanation != "")
	g.viewer.previous.SetEnabled(g.viewer.index > 0)
	g.viewer.next.SetEnabled(g.viewer.index+1 < len(g.viewer.steps))
	g.viewer.close.SetVisible(len(g.viewer.steps) > 0)
	g.viewer.bar.SetVisible(p.Note != "" || len(g.viewer.steps) > 0 || p.Path != "")
	g.viewer.edit.SetVisible(p.Path != "" && p.Action == "open")
	g.viewer.previous.SetVisible(len(g.viewer.steps) > 0)
	g.viewer.next.SetVisible(len(g.viewer.steps) > 0)
	g.work(func(ctx context.Context) func() {
		content := p.Content
		var err error
		if content == "" {
			if p.Action == "diff" || p.Action == "review" {
				r, e := g.ws.Backend.Review(ctx)
				err = e
				content = r.Text()
			} else if p.Path != "" && p.Action == "open" {
				content, _, err = g.ws.Backend.Read(ctx, p.Path)
			}
		}
		return func() {
			if request != g.viewRequest {
				return
			}
			if err != nil {
				g.showError(err)
				g.setEditorText(err.Error())
				return
			}
			if p.Action == "open" && p.Path != "" && p.Content == "" {
				g.viewer.path = p.Path
				g.viewer.edit.SetEnabled(true)
				g.mention.SetEnabled(true)
			}
			content = cleanText(content)
			g.setEditorText(content)
			if p.Action == "diff" || p.Action == "review" {
				g.decorateDiff(content)
			} else {
				g.decorateSource(p.Path, content)
			}
			ranges := ui.ViewerHighlights(content, p)
			lines := strings.SplitAfter(content, "\n")
			first := -1
			offset := 0
			for i, line := range lines {
				for _, r := range ranges {
					if i+1 >= r.StartLine && i+1 <= max(r.StartLine, r.EndLine) {
						g.preview.DecorateText(offset, offset+len(line), 0, themeColor(accentSoft), 0)
						if first < 0 {
							first = offset
						}
						break
					}
				}
				offset += len(line)
			}
			if first >= 0 {
				g.preview.SetTextRange(uint(first), uint(first))
			}
		}
	})
}

func (g *desktop) sourcePath() string {
	if g.previewPath != "" {
		return g.previewPath
	}
	return g.viewer.path
}
func (g *desktop) editViewedSource() {
	if path := g.sourcePath(); path != "" {
		g.openEditor(path)
		g.preview.Focus()
	} else {
		g.showFiles()
	}
}

func (g *desktop) closeWalkthrough() {
	v := &g.viewer
	v.steps = nil
	v.previous.SetVisible(false)
	v.next.SetVisible(false)
	v.close.SetVisible(false)
	v.explanation.SetVisible(false)
	v.note.SetText("Walkthrough closed")
}

func (g *desktop) commandExplain(a []string) error {
	draft := strings.TrimSpace(g.composer.Text())
	if draft != "" && !strings.HasPrefix(draft, ":explain") {
		return fmt.Errorf("send or clear the current draft first")
	}
	g.setPrompt(ui.ExplanationPrompt(strings.Join(a, " ")))
	return nil
}
func (g *desktop) commandWalkthrough(a []string) error {
	if len(a) != 1 {
		return fmt.Errorf("choose next, prev, explanation, code, or close")
	}
	if len(g.viewer.steps) == 0 {
		return fmt.Errorf("no walkthrough loaded; use :explain TOPIC")
	}
	g.showSourcePage()
	if !g.inspectorVisible {
		g.toggleInspector()
	}
	switch a[0] {
	case "next":
		g.viewerStep(1)
	case "prev":
		g.viewerStep(-1)
	case "explanation":
		g.viewer.explanation.SetVisible(true)
		g.viewer.explanation.Focus()
	case "code":
		g.preview.Focus()
	case "close":
		g.closeWalkthrough()
	default:
		return fmt.Errorf("unknown walkthrough action")
	}
	return nil
}

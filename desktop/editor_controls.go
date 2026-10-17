package main

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

func (g *desktop) buildEditorControls() {
	row := g.sourcePage.Box(cui.Horizontal, 4)
	g.editorToolbar = row
	g.previewTitle = label(row, "No file selected", 11, foreground)
	g.previewTitle.Expand(true)
	g.editorUndoButton = button(row, "↶", func() { g.editorUndo(false) })
	g.editorUndoButton.Tooltip("Undo · Ctrl+Z")
	g.editorUndoButton.Accessibility("Undo", "Undo the last edit")
	g.editorRedoButton = button(row, "↷", func() { g.editorUndo(true) })
	g.editorRedoButton.Tooltip("Redo · Ctrl+Y")
	g.editorRedoButton.Accessibility("Redo", "Restore the last undone edit")
	iconButton(row, cui.SymbolSearch, "Find in file · Ctrl+F", g.showEditorSearch)
	g.editorMenu = g.app.Menu()
	for _, a := range []struct {
		name   string
		action func()
	}{
		{"Use Vim editing", func() {
			g.editorVim = !g.editorVim
			if d := g.drafts[g.previewPath]; d != nil && d.buffer != nil {
				d.buffer.SetInsertMode(!g.editorVim)
			}
			g.updateEditorActions()
			g.preview.Focus()
		}},
		{"Open in external editor", g.externalEditor},
		{"Copy file contents", func() { g.window.Clipboard(g.preview.Text()) }},
		{"Reload from disk…", g.reloadEditor},
	} {
		g.editorMenu.Add(g.app.Command(a.name, 0, 0, a.action))
	}
	var more cui.Widget
	more = iconButton(row, cui.SymbolMore, "Editor actions", func() { g.editorMenu.Popup(more) })
	g.editorSearchRow = g.sourcePage.Box(cui.Horizontal, 6)
	g.editorSearch = g.editorSearchRow.Search("")
	g.editorSearch.Expand(true)
	g.editorSearch.Placeholder("Find in file…")
	g.editorSearch.OnKey(func(key int, _ uint) bool {
		if key == cui.KeyEnter {
			g.findInEditor()
			return true
		}
		if key == cui.KeyEscape {
			g.closeEditorSearch()
			return true
		}
		return false
	})
	iconButton(g.editorSearchRow, cui.SymbolDown, "Next match", g.findInEditor)
	iconButton(g.editorSearchRow, cui.SymbolClose, "Close find", g.closeEditorSearch)
	g.editorSearchRow.SetVisible(false)
}
func (g *desktop) closeEditorSearch() { g.editorSearchRow.SetVisible(false); g.preview.Focus() }
func (g *desktop) showEditorSearch()  { g.editorSearchRow.SetVisible(true); g.editorSearch.Focus() }
func (g *desktop) editorUndo(redo bool) {
	g.rememberEditor()
	d := g.drafts[g.previewPath]
	if d == nil || d.buffer == nil || d.discarding {
		return
	}
	if redo {
		d.buffer.Redo()
	} else {
		d.buffer.Undo()
	}
	g.applyEditorBuffer(d)
}
func (g *desktop) applyEditorBuffer(d *editorDraft) {
	d.document.Content = d.buffer.Text()
	g.setEditorText(d.document.Content)
	cursor := uint(d.buffer.Cursor())
	g.preview.SetTextRange(cursor, cursor)
	g.decorateSource(d.document.Path, d.document.Content)
	g.updateEditorActions()
}
func (g *desktop) findInEditor() {
	g.rememberEditor()
	d := g.drafts[g.previewPath]
	if d == nil || d.buffer == nil {
		return
	}
	query := g.editorSearch.Text()
	if query == "" {
		return
	}
	if !d.buffer.Find(query) {
		g.status.SetText("No further match")
		return
	}
	cursor := uint(d.buffer.Cursor())
	g.preview.SetTextRange(cursor, cursor+uint(len(query)))
}
func (g *desktop) editorKey(key int, mods uint) bool {
	d := g.drafts[g.previewPath]
	if d == nil || d.buffer == nil || g.previewLoading || d.discarding {
		return false
	}
	if mods&cui.ModControl != 0 {
		switch key {
		case 'Z':
			g.editorUndo(false)
			return true
		case 'R', 'Y':
			g.editorUndo(true)
			return true
		case 'F':
			g.showEditorSearch()
			return true
		case 'S':
			g.saveEditor()
			return true
		}
		return false
	}
	if !g.editorVim {
		return false
	}
	g.rememberEditor()
	if key == cui.KeyEscape {
		d.buffer.SetInsertMode(false)
		g.updateEditorActions()
		return true
	}
	if d.buffer.InsertMode() {
		return false
	}
	if key == '/' {
		g.showEditorSearch()
		return true
	}
	if key == ':' {
		g.openCommand("")
		return true
	}
	if key == 'q' {
		g.composer.Focus()
		return true
	}
	if key == 'd' {
		g.reloadEditor()
		return true
	}
	stroke := ""
	switch key {
	case cui.KeyUp:
		stroke = "up"
	case cui.KeyDown:
		stroke = "down"
	case cui.KeyLeft:
		stroke = "left"
	case cui.KeyRight:
		stroke = "right"
	case cui.KeyHome:
		stroke = "home"
	case cui.KeyEnd:
		stroke = "end"
	case cui.KeyDelete:
		stroke = "delete"
	default:
		if key >= 32 && key <= 126 {
			stroke = string(rune(key))
		}
	}
	if stroke != "" {
		d.buffer.NormalKey(stroke)
		g.applyEditorBuffer(d)
		return true
	}
	return key != cui.KeyTab
}
func (g *desktop) flushEditorDrafts() {
	if g.closing {
		return
	}
	for _, d := range g.drafts {
		if d.writing || d.discarding || time.Now().Before(d.retryAfter) {
			continue
		}
		doc := d.document
		key := doc.Revision + "\x00" + doc.Content
		if d.durable == key {
			continue
		}
		d.writing = true
		clean := doc.Content == d.saved
		g.work(func(context.Context) func() {
			dir := filepath.Join(g.ws.StateDir, "drafts")
			var err error
			if clean {
				err = ui.RemoveEditorDraft(dir, doc.Path)
			} else {
				err = ui.SaveEditorDraft(dir, doc)
			}
			return func() {
				d.writing = false
				if err != nil {
					d.retryAfter = time.Now().Add(5 * time.Second)
					g.showError(fmt.Errorf("save recovery draft: %w", err))
				} else {
					d.durable = key
					d.retryAfter = time.Time{}
				}
			}
		})
	}
}

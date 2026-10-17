package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"bifrost/internal/ui"
	"bifrost/internal/workspace"
	cui "cui.local/cui"
)

type editorDraft struct {
	discarding bool
	buffer     *ui.EditorBuffer
	durable    string
	writing    bool
	retryAfter time.Time
	document   ui.EditorDocument
	saved      string
	saving     bool
}

// A reused native text buffer must not undo into a different document.
func (g *desktop) setEditorText(text string) {
	g.preview.SetNativeUndo(false)
	g.preview.SetText(text)
	g.preview.SetNativeUndo(false)
}

func (g *desktop) rememberEditor() {
	if draft := g.drafts[g.previewPath]; draft != nil && !g.previewLoading {
		draft.document.Content = g.preview.Text()
		if draft.buffer != nil {
			start, _, _ := g.preview.TextRange()
			draft.buffer.Replace(draft.document.Content, int(start))
		}
		g.updateEditorActions()
	}
}

func (g *desktop) updateEditorActions() {
	draft := g.drafts[g.previewPath]
	editable := draft != nil && !g.previewLoading && !draft.discarding
	g.editorUndoButton.SetEnabled(editable)
	g.editorRedoButton.SetEnabled(editable)
	g.preview.ReadOnly(!editable || (g.editorVim && draft.buffer != nil && !draft.buffer.InsertMode()))
	g.saveSource.SetEnabled(editable && !draft.saving && draft.document.Content != draft.saved)
	if !editable {
		g.editorState.SetText("Read-only preview")
		return
	}
	state := "Saved · Ctrl+S to save"
	if draft.document.Content != draft.saved {
		state = "Unsaved changes · Ctrl+S"
	}
	if draft.saving {
		state = "Saving…"
	}
	if g.editorVim && draft.buffer != nil {
		mode := "NORMAL"
		if draft.buffer.InsertMode() {
			mode = "INSERT"
		}
		state = mode + " · " + state
	}
	g.editorState.SetText(state)
}

func (g *desktop) openEditor(path string) {
	g.viewer.steps = nil
	g.viewer.bar.SetVisible(false)
	g.viewer.path = ""
	g.rememberEditor()
	g.viewRequest++
	request := g.viewRequest
	g.previewPath = path
	g.previewLoading = true
	g.mention.SetEnabled(false)
	if !g.inspectorVisible {
		g.toggleInspector()
	}
	g.showSourcePage()
	g.previewTitle.SetText(shortText(filepath.Base(path), 20))
	g.previewTitle.Tooltip(path)
	g.setEditorText("Loading…")
	g.updateEditorActions()
	if draft := g.drafts[path]; draft != nil {
		g.previewLoading = false
		g.setEditorText(draft.document.Content)
		if draft.buffer != nil {
			g.preview.SetTextRange(uint(draft.buffer.Cursor()), uint(draft.buffer.Cursor()))
		}
		g.decorateSource(path, draft.document.Content)
		g.mention.SetEnabled(true)
		g.updateEditorActions()
		return
	}
	if len(g.drafts) >= 32 {
		for name, draft := range g.drafts {
			if !draft.saving && !draft.writing && draft.document.Content == draft.saved {
				delete(g.drafts, name)
				break
			}
		}
		if len(g.drafts) >= 32 {
			g.previewLoading = false
			g.previewPath = ""
			g.setEditorText("Save or reload an existing draft before opening more files (32-file limit).")
			g.updateEditorActions()
			return
		}
	}
	backend := g.ws.Backend
	g.work(func(ctx context.Context) func() {
		content, revision, err := backend.Read(ctx, path)
		if err == nil && (strings.ContainsRune(content, 0) || !utf8.ValidString(content)) {
			err = errors.New("binary or invalid UTF-8 files cannot be edited")
		}
		if err == nil && len(content) > 2<<20 {
			err = errors.New("file exceeds the 2 MiB editor limit")
		}
		saved := content
		doc := ui.EditorDocument{Path: path, Content: content, Revision: revision}
		if err == nil {
			if restored, e := ui.ReadEditorDraft(filepath.Join(g.ws.StateDir, "drafts"), doc); e == nil {
				doc = restored
				content = doc.Content
			}
		}
		return func() {
			if request != g.viewRequest {
				return
			}
			g.previewLoading = false
			if err != nil {
				g.previewPath = ""
				g.setEditorText(safeText(err.Error()))
				g.updateEditorActions()
				return
			}
			g.drafts[path] = &editorDraft{document: doc, saved: saved, buffer: ui.NewEditorBuffer(doc)}
			g.drafts[path].buffer.SetInsertMode(!g.editorVim)
			g.setEditorText(content)
			g.decorateSource(path, content)
			g.mention.SetEnabled(true)
			g.updateEditorActions()
		}
	})
}

func (g *desktop) saveEditor() {
	g.rememberEditor()
	draft := g.drafts[g.previewPath]
	if draft == nil || draft.saving || draft.discarding || g.previewLoading {
		return
	}
	doc := draft.document
	if len(doc.Content) > 2<<20 {
		g.showError(errors.New("file exceeds the 2 MiB editor limit"))
		return
	}
	draft.saving = true
	g.updateEditorActions()
	backend := g.ws.Backend
	g.work(func(ctx context.Context) func() {
		revision := workspace.Revision(doc.Revision)
		change, err := backend.Write(ctx, doc.Path, &revision, doc.Content)
		return func() {
			draft.saving = false
			if err != nil {
				if errors.Is(err, workspace.ErrConflict) || strings.Contains(err.Error(), "workspace revision conflict") {
					err = errors.New("This file changed on disk. Your draft is retained. Copy it before using Reload to inspect the latest version")
				}
				g.showError(err)
			} else {
				draft.document.Revision = change.After
				draft.saved = doc.Content
				g.status.SetText("Saved " + shortText(doc.Path, 70))
			}
			g.rememberEditor()
			g.updateEditorActions()
		}
	})
}

func (g *desktop) reloadEditor() {
	g.rememberEditor()
	path := g.previewPath
	draft := g.drafts[path]
	if draft == nil || draft.saving || draft.writing || draft.discarding {
		return
	}
	draft.discarding = true
	g.updateEditorActions()
	reload := func() {
		request := g.viewRequest
		g.work(func(context.Context) func() {
			err := ui.RemoveEditorDraft(filepath.Join(g.ws.StateDir, "drafts"), path)
			return func() {
				draft.discarding = false
				if err != nil {
					g.updateEditorActions()
					g.showError(err)
					return
				}
				delete(g.drafts, path)
				if g.previewPath == path {
					// Do not restore the discarded text through rememberEditor.
					g.previewPath = ""
					g.openEditor(path)
				} else if request == g.viewRequest {
					g.updateEditorActions()
				}
			}
		})
	}
	if draft.document.Content == draft.saved {
		reload()
		return
	}
	g.window.Alert("Reload file?", "Discard this file's unsaved draft and read the latest version from disk. Copy the draft first if you want to keep it.", "Discard & reload", func(result int, _ string) {
		if result == cui.DialogAccepted {
			reload()
		} else {
			draft.discarding = false
			g.updateEditorActions()
		}
	})
}

func (g *desktop) hasDrafts() bool {
	g.rememberEditor()
	g.rememberChat()
	for _, tab := range g.chats.items {
		if strings.TrimSpace(tab.draft) != "" {
			return true
		}
	}
	p := &g.workbench.projects
	if p.repositoryValues() != p.repositoryBaseline {
		return true
	}
	if p.loaded && (p.name.Text() != p.status.ProjectName || p.description.Text() != p.status.ProjectDescription || p.settings.Text() != p.defaults) {
		return true
	}
	if g.workbench.settings.dirty || len(g.workbench.settings.pending) > 0 {
		return true
	}
	for _, d := range g.drafts {
		if d.document.Content != d.saved || d.saving {
			return true
		}
	}
	return false
}

func (g *desktop) confirmClose() {
	if !g.hasDrafts() {
		g.beginClose()
		return
	}
	if g.closeDialog {
		return
	}
	g.closeDialog = true
	g.window.Alert("Unsaved changes", "Source recovery drafts will be retained. Unsent messages, unsaved settings and project details will be discarded. Keep working to save them, or close now.", "Keep source drafts & close", func(result int, _ string) {
		g.closeDialog = false
		if result == cui.DialogAccepted {
			g.beginClose()
		}
	})
}

func (g *desktop) beginClose() {
	g.closing = true
	g.cancelTurn()
	g.shell.SetEnabled(false)
	g.workbench.root.SetEnabled(false)
	g.status.SetText("Saving recovery drafts…")
}
func (g *desktop) finishClose() {
	if !g.closing || g.closeFlushing || g.busy {
		return
	}
	for _, d := range g.drafts {
		if d.writing || d.saving || d.discarding {
			return
		}
	}
	var documents []ui.EditorDocument
	var clean []string
	for _, d := range g.drafts {
		if d.document.Content == d.saved {
			clean = append(clean, d.document.Path)
		} else {
			documents = append(documents, d.document)
		}
	}
	g.closeFlushing = true
	g.work(func(context.Context) func() {
		dir := filepath.Join(g.ws.StateDir, "drafts")
		var err error
		for _, doc := range documents {
			err = errors.Join(err, ui.SaveEditorDraft(dir, doc))
		}
		for _, path := range clean {
			err = errors.Join(err, ui.RemoveEditorDraft(dir, path))
		}
		return func() {
			if err != nil {
				g.closing = false
				g.closeFlushing = false
				g.shell.SetEnabled(g.workbench.open == "")
				g.workbench.root.SetEnabled(true)
				g.showError(err)
				return
			}
			g.cancel()
			g.app.Quit()
		}
	})
}

package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"bifrost/internal/workspace"
	cui "cui.local/cui"
)

type fileExplorer struct {
	rows                []cui.Widget
	count, parent, root cui.Widget
	total               int
	selected            string
}

func cleanExplorerPath(path string) string {
	path = filepath.Clean(path)
	if path == ".." {
		return "."
	}
	return path
}

func sortedFiles(entries []workspace.Entry) []workspace.Entry {
	entries = append([]workspace.Entry(nil), entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		a, b := entries[i], entries[j]
		if a.IsDir != b.IsDir {
			return a.IsDir
		}
		if strings.ToLower(a.Name) == strings.ToLower(b.Name) {
			return a.Name < b.Name
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return entries
}

func (g *desktop) buildFileExplorer() {
	header := g.filesPage.Box(cui.Horizontal, 2)
	label(header, "EXPLORER", 9, muted).Expand(true)
	g.explorer.root = iconButton(header, cui.SymbolHome, "Workspace root", func() { g.loadDirectory(".") })
	g.explorer.parent = iconButton(header, cui.SymbolUp, "Parent folder · Left arrow", func() { g.loadDirectory(filepath.Dir(g.cwd)) })
	iconButton(header, cui.SymbolRepeat, "Refresh directory", func() { g.loadDirectory(g.cwd) })
	g.directory = label(g.filesPage, filepath.Base(g.ws.Root), 11, foreground)
	g.directory.Font("", 11, 600)
	// A scrollable native pane keeps the header and workspace navigation fixed.
	viewport := g.filesPage.Split(cui.Vertical, 1)
	viewport.Expand(true)
	viewport.MinSize(1, 80)
	viewport.SetSplitScrollable(0, true)
	viewport.Pane(1).SetVisible(false)
	g.fileList = viewport.Pane(0).Box(cui.Vertical, 1)
	style(g.fileList, rail, foreground, 0, false)
	g.explorer.count = label(g.filesPage, "Loading directory…", 9, muted)
	g.sideSearch.OnKey(func(key int, modifiers uint) bool {
		if g.filesMode && modifiers == 0 && (key == cui.KeyDown || key == cui.KeyEnter) && len(g.visibleFiles) > 0 {
			g.explorer.rows[0].Focus()
			return true
		}
		return false
	})
}

func (g *desktop) updateDirectoryHeader() {
	path := filepath.ToSlash(g.cwd)
	name := filepath.Base(g.ws.Root)
	if path != "." {
		name += " / " + path
	}
	g.directory.SetText(shortText(name, 32))
	g.directory.Tooltip(filepath.Join(g.ws.Root, g.cwd))
	g.explorer.parent.SetEnabled(g.cwd != ".")
	g.explorer.root.SetEnabled(g.cwd != ".")
}

func (g *desktop) renderFiles(query string) {
	g.visibleFiles = nil
	for _, entry := range g.files {
		if query == "" || strings.Contains(strings.ToLower(entry.Name), query) {
			g.visibleFiles = append(g.visibleFiles, entry)
		}
	}
	for len(g.explorer.rows) < len(g.visibleFiles) {
		i := len(g.explorer.rows)
		row := button(g.fileList, "", func() { g.openExplorerEntry(i) })
		row.Font("", 10.5, 400)
		row.MinSize(0, 28)
		row.IconSize(18)
		row.OnKey(func(key int, modifiers uint) bool { return g.explorerKey(i, key, modifiers) })
		g.explorer.rows = append(g.explorer.rows, row)
	}
	for i, row := range g.explorer.rows {
		row.SetVisible(i < len(g.visibleFiles))
		if i >= len(g.visibleFiles) {
			continue
		}
		entry := g.visibleFiles[i]
		kind := fileKind(entry)
		row.SetText(shortText(entry.Name, 30))
		row.Tooltip(entry.Path + "\n" + kind)
		row.Accessibility(entry.Name, kind+" · Enter to open")
		asset, err := fileIcon(kind)
		if err == nil {
			row.SetIcon(asset)
			asset.Close()
		} else {
			row.SetSymbol(cui.SymbolFile)
		}
		bg := uint32(0)
		if entry.Path == g.explorer.selected {
			bg = accentSoft
		}
		style(row, bg, foreground, 6, false, 4)
	}
	g.explorer.count.SetText(fmt.Sprintf("%d of %d items", len(g.visibleFiles), g.explorer.total))
	if g.explorer.total > 1000 {
		g.explorer.count.SetText(fmt.Sprintf("%d matches · first 1,000 of %d items", len(g.visibleFiles), g.explorer.total))
	}
}

func (g *desktop) openExplorerEntry(i int) {
	if i < 0 || i >= len(g.visibleFiles) {
		return
	}
	entry := g.visibleFiles[i]
	if entry.IsDir {
		g.loadDirectory(entry.Path)
		return
	}
	g.explorer.selected = entry.Path
	g.renderFiles(strings.ToLower(strings.TrimSpace(g.sideSearch.Text())))
	g.loadFile(entry.Path)
}

func (g *desktop) explorerKey(i, key int, modifiers uint) bool {
	if modifiers != 0 {
		return false
	}
	next := i
	switch key {
	case cui.KeyUp:
		next--
	case cui.KeyDown:
		next++
	case cui.KeyHome:
		next = 0
	case cui.KeyEnd:
		next = len(g.visibleFiles) - 1
	case cui.KeyLeft:
		if g.cwd != "." {
			g.loadDirectory(filepath.Dir(g.cwd))
		}
		return true
	case cui.KeyRight:
		if i < len(g.visibleFiles) && g.visibleFiles[i].IsDir {
			g.openExplorerEntry(i)
		}
		return true
	default:
		return false // Enter/Space retain native button activation.
	}
	if len(g.visibleFiles) > 0 {
		g.explorer.rows[max(0, min(next, len(g.visibleFiles)-1))].Focus()
	}
	return true
}

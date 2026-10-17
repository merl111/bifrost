package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeEditorUsesTUIUndoNavigationAndDraftRevision(t *testing.T) {
	doc := EditorDocument{Path: "hello.go", Content: "hello 🌉\nsecond\n", Revision: "initial"}
	b := NewEditorBuffer(doc)
	b.SetCursor(len("hello "))
	b.SetInsertMode(false)
	b.NormalKey("x")
	if b.Text() != "hello \nsecond\n" {
		t.Fatalf("delete split Unicode: %q", b.Text())
	}
	b.Undo()
	if b.Text() != doc.Content {
		t.Fatal("undo did not restore original")
	}
	b.Redo()
	if strings.Contains(b.Text(), "🌉") {
		t.Fatal("redo did not apply deletion")
	}
	b.NormalKey("G")
	b.NormalKey("k")
	if b.Cursor() == len(b.Text()) {
		t.Fatal("vertical motion failed")
	}
	b.Find("hello")
	if b.Cursor() != 0 {
		t.Fatalf("search did not wrap: %d", b.Cursor())
	}
	dir := t.TempDir()
	doc.Content = b.Text()
	if err := SaveEditorDraft(dir, doc); err != nil {
		t.Fatal(err)
	}
	restored, err := ReadEditorDraft(dir, EditorDocument{Path: doc.Path, Revision: doc.Revision})
	if err != nil || restored.Content != doc.Content {
		t.Fatalf("restore: %+v %v", restored, err)
	}
	if _, err := ReadEditorDraft(dir, EditorDocument{Path: doc.Path, Revision: "external change"}); err == nil {
		t.Fatal("restored stale draft")
	}
	if err := RemoveEditorDraft(dir, doc.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEditorDraft(dir, doc); !os.IsNotExist(err) {
		t.Fatal("draft removal failed")
	}
}
func TestNativeViewerSymbolAndRanges(t *testing.T) {
	content := "package demo\nfunc Hello() {\n println(1)\n}\n"
	ranges := ViewerHighlights(content, ViewerCommandPayload{Symbol: "Hello", Highlights: []ViewerHighlightRange{{StartLine: 1, EndLine: 1}}})
	if len(ranges) != 2 || ranges[0].StartLine != 2 || ranges[0].EndLine != 4 {
		t.Fatalf("%+v", ranges)
	}
}
func TestNativeTerminalFrameKeepsColorsWideCellsAndCursor(t *testing.T) {
	f := terminalFrame("\x1b[31;1mred\x1b[0m 🌉\nnext", 12, 3, 2, 1, true)
	if !strings.Contains(f.Text, "red 🌉") || !strings.Contains(f.Text, "next") {
		t.Fatalf("frame: %q", f.Text)
	}
	if len(f.Styles) == 0 || f.Styles[0].Start != 0 || f.Styles[0].End != 3 || !f.Styles[0].Bold || f.Styles[0].Foreground == 0 {
		t.Fatalf("style: %+v", f.Styles)
	}
	if !f.CursorVisible || f.Text[f.Cursor:f.Cursor+1] != "x" {
		t.Fatalf("cursor %d in %q", f.Cursor, f.Text)
	}
}
func TestNativeExternalEditorRetainsRootAndArgvChecks(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "file.txt"), []byte("safe"), 0600)
	cmd, err := ExternalEditorCommand(root, "file.txt", []string{"editor", "--wait"}, false)
	if err != nil || len(cmd.Args) != 3 || cmd.Dir != root {
		t.Fatalf("%v %v", cmd, err)
	}
	if _, err := ExternalEditorCommand(root, "../escape", []string{"editor"}, false); err == nil {
		t.Fatal("escaped root")
	}
	if _, err := ExternalEditorCommand(root, "file.txt", []string{"editor"}, true); err == nil {
		t.Fatal("opened SSH path on host")
	}
}

func TestNativeCommandCompletionSharesCyclesAndCatalog(t *testing.T) {
	var completion CommandCompletion
	ctx := CommandContext{Catalog: []MarketplaceEntry{{ID: "openai-example"}}}
	first := completion.Complete("layout ", true, ctx)
	second := completion.Complete(first, true, ctx)
	if first == second || completion.Complete(second, false, ctx) != first {
		t.Fatal("native command cycle diverged")
	}
	if got := completion.Complete("skill install openai", true, ctx); got != "skill install openai-example" {
		t.Fatalf("catalog completion: %q", got)
	}
	if got := completion.Complete("skill install openai-example --scope g", true, ctx); got != "skill install openai-example --scope global" {
		t.Fatalf("scope completion lost ID: %q", got)
	}
}

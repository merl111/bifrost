package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testWorkspace(t *testing.T) (*Workspace, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "root")
	state := filepath.Join(base, "state")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root, state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, root
}

func TestReadListAndRevisionCheckedWrite(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\r\ntwo\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	text, rev, err := w.Read("a.txt")
	if err != nil || text != "one\r\ntwo\r\n" || rev == "" {
		t.Fatalf("read = %q, %q, %v", text, rev, err)
	}
	entries, err := w.List("")
	if err != nil || len(entries) != 1 || entries[0].Name != "a.txt" {
		t.Fatalf("list = %#v, %v", entries, err)
	}
	wrong := Revision("wrong")
	if _, err := w.Write("a.txt", &wrong, "new"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale write error = %v", err)
	}
	c, err := w.Write("a.txt", &rev, text+"tail")
	if err != nil || c.Before != rev || c.After == rev {
		t.Fatalf("write = %#v, %v", c, err)
	}
	got, _, err := w.Read("a.txt")
	if err != nil || got != text+"tail" {
		t.Fatalf("after write = %q, %v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, "a.txt"))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode not preserved: %v %v", info.Mode(), err)
	}
}

func TestEditRejectsAmbiguousReplacement(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("x x"), 0644); err != nil {
		t.Fatal(err)
	}
	_, rev, err := w.Read("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Edit("a.txt", rev, "x", "y"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous edit error = %v", err)
	}
	if _, err := w.Edit("a.txt", rev, "z", "y"); err == nil {
		t.Fatal("missing replacement unexpectedly succeeded")
	}
}

func TestSymlinkEscapeIsRejected(t *testing.T) {
	w, root := testWorkspace(t)
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := w.Read("link/secret.txt"); err == nil {
		t.Fatal("symlink escape read unexpectedly succeeded")
	}
	if _, err := w.Write("link/new.txt", nil, "bad"); err == nil {
		t.Fatal("symlink escape write unexpectedly succeeded")
	}
}

func TestUndoProtectsManualEdit(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	_, rev, err := w.Read("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	c, err := w.Edit("a.txt", rev, "before", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("manual"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.Undo(c.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("undo after manual edit = %v", err)
	}
	b, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(b) != "manual" {
		t.Fatalf("manual edit lost: %q, %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("agent"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.Undo(c.ID); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(b) != "before" {
		t.Fatalf("undo result: %q, %v", b, err)
	}
}

func TestStateMustBeOutsideRootAndBinaryRejected(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, filepath.Join(root, "state")); err == nil {
		t.Fatal("state directory inside root unexpectedly accepted")
	}
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "binary"), []byte("\x00"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.Read("binary"); !errors.Is(err, ErrBinary) {
		t.Fatalf("binary read = %v", err)
	}
	if _, _, err := w.Read("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read = %v", err)
	}
}

func TestApplyPatchPrevalidatesAndCommitsMixedOperations(t *testing.T) {
	w, root := testWorkspace(t)
	for name, content := range map[string]string{"replace.txt": "old", "move.txt": "move", "delete.txt": "gone"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_, replaceRev, err := w.Read("replace.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, moveRev, err := w.Read("move.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, deleteRev, err := w.Read("delete.txt")
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.ApplyPatch(PatchPlan{Operations: []PatchOperation{
		{Kind: "create", Path: "created.txt", Content: "new"},
		{Kind: "replace", Path: "replace.txt", Expected: &replaceRev, Content: "updated"},
		{Kind: "move", Path: "move.txt", To: "nested/moved.txt", Expected: &moveRev},
		{Kind: "delete", Path: "delete.txt", Expected: &deleteRev},
	}})
	if err != nil || result.Status != "committed" || len(result.Changes) != 4 {
		t.Fatalf("patch result=%+v err=%v", result, err)
	}
	for path, want := range map[string]string{"created.txt": "new", "replace.txt": "updated", "nested/moved.txt": "move"} {
		content, err := os.ReadFile(filepath.Join(root, path))
		if err != nil || string(content) != want {
			t.Fatalf("%s = %q err=%v", path, content, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "delete.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete still exists: %v", err)
	}
}

func TestApplyPatchPrevalidationDoesNotPartiallyMutate(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("a"), 0644); err != nil {
		t.Fatal(err)
	}
	_, rev, err := w.Read("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	wrong := Revision("0000000000000000000000000000000000000000000000000000000000000000")
	if _, err := w.ApplyPatch(PatchPlan{Operations: []PatchOperation{
		{Kind: "replace", Path: "a.txt", Expected: &rev, Content: "changed"},
		{Kind: "replace", Path: "a.txt", Expected: &wrong, Content: "bad"},
	}}); err == nil {
		t.Fatal("duplicate path patch unexpectedly succeeded")
	}
	content, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(content) != "a" {
		t.Fatalf("prevalidation changed file: %q err=%v", content, err)
	}
}

func TestPatchRecoveryPreservesUnrelatedManualChange(t *testing.T) {
	w, root := testWorkspace(t)
	before := []byte("before")
	after := []byte("agent")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), before, 0644); err != nil {
		t.Fatal(err)
	}
	id := ChangeID("change_0123456789abcdef0123456789abcdef")
	if err := atomicFile(w.patchBeforePath(id, 0), before, 0600); err != nil {
		t.Fatal(err)
	}
	j := patchJournal{ID: id, Status: "applying", Ops: []patchJournalOp{{
		PatchOperation: PatchOperation{Kind: "replace", Path: "a.txt", Expected: func() *Revision { v := revisionOf(before); return &v }(), Content: string(after)},
		Path:           "a.txt", Before: revisionOf(before), After: revisionOf(after), BeforeExists: true, AfterExists: true, BeforeMode: 0644, AfterMode: 0644,
	}}}
	if err := w.savePatch(j); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("manual"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root, filepath.Join(filepath.Dir(root), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	content, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(content) != "manual" {
		t.Fatalf("manual edit changed during recovery: %q err=%v", content, err)
	}
	recovered, err := w.loadPatch(w.patchPath(id))
	if err != nil || recovered.Status != "partial" {
		t.Fatalf("recovery journal=%+v err=%v", recovered, err)
	}
}

func TestUndoPatchIsRevisionSafeAndRestoresBatch(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	_, rev, err := w.Read("a.txt")
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.ApplyPatch(PatchPlan{Operations: []PatchOperation{{Kind: "replace", Path: "a.txt", Expected: &rev, Content: "after"}, {Kind: "create", Path: "new.txt", Content: "new"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("manual"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := w.UndoPatch(result.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale undo error = %v", err)
	}
	if content, _ := os.ReadFile(filepath.Join(root, "new.txt")); string(content) != "new" {
		t.Fatalf("stale undo partially changed create: %q", content)
	}
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("after"), 0644); err != nil {
		t.Fatal(err)
	}
	undone, err := w.UndoPatch(result.ID)
	if err != nil || undone.Status != "reverted" {
		t.Fatalf("undo result=%+v err=%v", undone, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(content) != "before" {
		t.Fatalf("restored a.txt=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("created file remains after undo: %v", err)
	}
}

func TestUndoPatchRestoresMove(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("move"), 0644); err != nil {
		t.Fatal(err)
	}
	_, rev, err := w.Read("source.txt")
	if err != nil {
		t.Fatal(err)
	}
	result, err := w.ApplyPatch(PatchPlan{Operations: []PatchOperation{{Kind: "move", Path: "source.txt", To: "nested/dest.txt", Expected: &rev}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.UndoPatch(result.ID); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "source.txt")); err != nil || string(content) != "move" {
		t.Fatalf("restored source=%q err=%v", content, err)
	}
	if _, err := os.Stat(filepath.Join(root, "nested", "dest.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("move destination remains: %v", err)
	}
}

func TestPatchRejectsProtectedMetadata(t *testing.T) {
	w, _ := testWorkspace(t)
	for _, path := range []string{".git/config", ".bifrost/settings"} {
		if _, err := w.ApplyPatch(PatchPlan{Operations: []PatchOperation{{Kind: "create", Path: path, Content: "blocked"}}}); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("protected path %q error = %v", path, err)
		}
	}
}

func TestPatchRecoveryMarksHalfMovePartial(t *testing.T) {
	w, root := testWorkspace(t)
	before := []byte("move")
	if err := os.WriteFile(filepath.Join(root, "source.txt"), before, 0644); err != nil {
		t.Fatal(err)
	}
	id := ChangeID("change_abcdefabcdefabcdefabcdefabcdefab")
	if err := os.WriteFile(filepath.Join(root, "dest.txt"), before, 0644); err != nil {
		t.Fatal(err)
	}
	j := patchJournal{ID: id, Status: "applying", Ops: []patchJournalOp{{
		PatchOperation: PatchOperation{Kind: "move", Path: "source.txt", To: "dest.txt"},
		Path:           "source.txt", To: "dest.txt", Before: revisionOf(before), After: revisionOf(before), BeforeExists: true, AfterExists: true,
	}}}
	if err := w.savePatch(j); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := Open(root, filepath.Join(filepath.Dir(root), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	recovered, err := w.loadPatch(w.patchPath(id))
	if err != nil || recovered.Status != "partial" {
		t.Fatalf("half move recovery=%+v err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); err != nil {
		t.Fatal("source disappeared during partial recovery")
	}
}

func TestPatchCreateAndMoveNeverOverwriteAppearedDestination(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "appeared.txt"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.create("appeared.txt", []byte("patch"), 0644); err == nil {
		t.Fatal("create overwrote an existing destination")
	}
	content, _ := os.ReadFile(filepath.Join(root, "appeared.txt"))
	if string(content) != "outside" {
		t.Fatalf("create changed destination: %q", content)
	}
	if err := os.WriteFile(filepath.Join(root, "source.txt"), []byte("source"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := w.moveNoReplace("source.txt", "appeared.txt"); err == nil {
		t.Fatal("move overwrote an existing destination")
	}
	if _, err := os.Stat(filepath.Join(root, "source.txt")); err != nil {
		t.Fatalf("source disappeared after failed move: %v", err)
	}
}

func TestPatchJournalAndBeforeImageReadsAreBounded(t *testing.T) {
	w, root := testWorkspace(t)
	id := ChangeID("change_0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(w.patchPath(id), make([]byte, maxPatchJournalBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.loadPatch(w.patchPath(id)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("journal read error = %v", err)
	}
	before := w.patchBeforePath(id, 0)
	if err := os.WriteFile(before, make([]byte, maxTextBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPatchBefore(before); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("before-image read error = %v", err)
	}
	_ = root
}

func TestDeleteRestoresBeforeImage(t *testing.T) {
	w, root := testWorkspace(t)
	if err := os.WriteFile(filepath.Join(root, "gone.txt"), []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	change, prev, err := w.Delete("gone.txt")
	if err != nil || prev != "keep" {
		t.Fatalf("delete = %q, %v", prev, err)
	}
	if _, err := os.Stat(filepath.Join(root, "gone.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present: %v", err)
	}
	if err := w.Undo(change.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, "gone.txt"))
	if err != nil || string(got) != "keep" {
		t.Fatalf("restored = %q, %v", got, err)
	}
}

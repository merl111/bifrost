package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"bifrost/internal/filelock"
)

func TestChangeHistoryIsBoundToRootAndLocked(t *testing.T) {
	w, root := testWorkspace(t)
	if _, err := Open(root, w.stateDir); !errors.Is(err, filelock.ErrLocked) {
		t.Fatalf("second writer: %v", err)
	}
	state := w.stateDir
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(t.TempDir(), state); err == nil {
		t.Fatal("state reused for another root")
	}
	reopened, err := Open(root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
}

func TestUndoSurvivesRestartAndRejectsCorruptBeforeImage(t *testing.T) {
	w, root := testWorkspace(t)
	os.WriteFile(filepath.Join(root, "empty"), []byte{}, 0600)
	_, rev, err := w.Read("empty")
	if err != nil {
		t.Fatal(err)
	}
	change, err := w.Write("empty", &rev, "next")
	if err != nil || !change.BeforeExists {
		t.Fatalf("empty before-image: %#v %v", change, err)
	}
	state := w.stateDir
	w.Close()
	w, err = Open(root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	image := filepath.Join(state, "before", string(change.ID)+".bin")
	os.WriteFile(image, []byte("corrupted"), 0600)
	if err := w.Undo(change.ID); err == nil {
		t.Fatal("corrupt before-image accepted")
	}
	got, _, _ := w.Read("empty")
	if got != "next" {
		t.Fatal("corrupt undo altered file")
	}
	os.WriteFile(image, []byte{}, 0600)
	if err := w.Undo(change.ID); err != nil {
		t.Fatal(err)
	}
	got, _, err = w.Read("empty")
	if err != nil || got != "" {
		t.Fatalf("restore empty: %q %v", got, err)
	}
	if err := w.Undo(change.ID); err == nil {
		t.Fatal("double undo accepted")
	}
}

func TestMutationRejectsSymlinksAndInvalidText(t *testing.T) {
	w, root := testWorkspace(t)
	os.WriteFile(filepath.Join(root, "target"), []byte("before"), 0600)
	if err := os.Symlink("target", filepath.Join(root, "link")); err != nil {
		t.Skip(err)
	}
	_, rev, err := w.Read("link")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Edit("link", rev, "before", "after"); err == nil {
		t.Fatal("replaced symlink")
	}
	if _, err := w.Write("binary", nil, "\x00"); !errors.Is(err, ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	if err := w.Undo("change_/../../outside"); !errors.Is(err, ErrUnknownChange) {
		t.Fatalf("unsafe ID: %v", err)
	}
	if _, err := w.Edit("target", rev, "", "x"); err == nil {
		t.Fatal("empty replacement accepted")
	}
}

func TestPendingJournalRecovery(t *testing.T) {
	w, root := testWorkspace(t)
	change, err := w.Write("new", nil, "after")
	if err != nil {
		t.Fatal(err)
	}
	jc, err := w.loadChange(change.ID)
	if err != nil {
		t.Fatal(err)
	}
	jc.Status = "applying"
	if err := w.saveChange(jc); err != nil {
		t.Fatal(err)
	}
	state := w.stateDir
	w.Close()
	w, err = Open(root, state)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	jc, err = w.loadChange(change.ID)
	if err != nil || jc.Status != "committed" {
		t.Fatalf("recovered: %#v %v", jc, err)
	}
	if err := w.Undo(change.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new file remains: %v", err)
	}
}

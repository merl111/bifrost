// Package workspace provides revision-checked text file operations rooted at
// one directory. The state directory contains before-images and a small
// journal; it is deliberately separate from the user workspace.
package workspace

import (
	"bifrost/internal/filelock"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"
)

var (
	ErrClosed        = errors.New("workspace is closed")
	ErrConflict      = errors.New("workspace revision conflict")
	ErrAmbiguous     = errors.New("replacement is ambiguous")
	ErrInvalidPath   = errors.New("invalid workspace path")
	ErrBinary        = errors.New("file is not text")
	ErrTooLarge      = errors.New("file exceeds 8 MiB text limit")
	ErrUnknownChange = errors.New("unknown workspace change")
)

// Revision is the SHA-256 digest of a file's bytes, encoded as lowercase hex.
// It is an alias so callers can pass ordinary string revisions without casts.
// A missing file is represented by a nil expected revision in Write.
type Revision = string

type ChangeID string

// Entry is one immediate child returned by List.
type Entry struct {
	Name  string
	Path  string
	Mode  fs.FileMode
	Size  int64
	IsDir bool
}

// Change describes one committed file replacement. Before and After are
// content revisions; BeforeExists is false for a newly created file.
type Change struct {
	ID           ChangeID `json:"id"`
	Path         string   `json:"path"`
	Before       Revision `json:"before"`
	After        Revision `json:"after"`
	BeforeExists bool     `json:"before_exists"`
	AfterExists  bool     `json:"after_exists"`
	Mode         uint32   `json:"mode"`
	Status       string   `json:"status"`
}

type journalChange struct {
	Change
	BeforeMode uint32 `json:"before_mode"`
}

// Workspace owns a rooted file descriptor and a state directory. Methods are
// safe for concurrent callers, though revision checks only provide protection
// against changes observed at operation time; filesystem-wide CAS is not
// promised for external writers.
const maxTextBytes = 8 << 20

type Workspace struct {
	lock     *filelock.Lock
	mu       sync.Mutex
	root     *os.Root
	stateDir string
	closed   bool
}

func Open(root, stateDir string) (*Workspace, error) {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("workspace: root and state directory are required")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, fmt.Errorf("workspace state: %w", err)
	}
	for _, name := range []string{"changes", "before", "patches", "patch-before"} {
		if err := os.MkdirAll(filepath.Join(stateDir, name), 0700); err != nil {
			return nil, fmt.Errorf("workspace state: %w", err)
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	stateReal, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return nil, fmt.Errorf("workspace state: %w", err)
	}
	rel, err := filepath.Rel(rootReal, stateReal)
	if err != nil {
		return nil, fmt.Errorf("workspace state: %w", err)
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return nil, fmt.Errorf("workspace state directory must be outside workspace root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("workspace root: %w", err)
	}
	lock, err := filelock.Acquire(filepath.Join(stateDir, "workspace.lock"))
	if err != nil {
		r.Close()
		return nil, err
	}
	// A change history belongs to exactly one canonical workspace.
	binding := filepath.Join(stateDir, "root.json")
	data, err := os.ReadFile(binding)
	if errors.Is(err, fs.ErrNotExist) {
		data, _ = json.Marshal(rootReal)
		err = atomicFile(binding, data, 0600)
	} else if err == nil {
		var bound string
		if json.Unmarshal(data, &bound) != nil || bound != rootReal {
			err = errors.New("workspace state belongs to another root")
		}
	}
	if err != nil {
		r.Close()
		lock.Close()
		return nil, err
	}
	w := &Workspace{root: r, stateDir: stateDir, lock: lock}
	if err := w.recoverPending(); err != nil {
		r.Close()
		lock.Close()
		return nil, err
	}
	if err := w.recoverPatches(); err != nil {
		r.Close()
		lock.Close()
		return nil, err
	}
	return w, nil
}

func (w *Workspace) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return errors.Join(w.root.Close(), w.lock.Close())
}

func (w *Workspace) List(name string) ([]Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return nil, err
	}
	p, err := cleanPath(name, true)
	if err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(w.root.FS(), p)
	if err != nil {
		return nil, fmt.Errorf("list %q: %w", name, err)
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			return nil, fmt.Errorf("list %q: %w", name, err)
		}
		child := e.Name()
		if p != "." {
			child = p + "/" + child
		}
		out = append(out, Entry{Name: e.Name(), Path: child, Mode: info.Mode(), Size: info.Size(), IsDir: info.IsDir()})
	}
	return out, nil
}

func (w *Workspace) Read(name string) (string, Revision, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return "", "", err
	}
	p, err := cleanPath(name, false)
	if err != nil {
		return "", "", err
	}
	b, _, err := w.currentText(p)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return "", "", fmt.Errorf("read %q: %w", name, ErrBinary)
	}
	return string(b), revisionOf(b), nil
}

func (w *Workspace) Edit(name string, expected Revision, oldText, newText string) (Change, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return Change{}, err
	}
	p, err := cleanPath(name, false)
	if err != nil {
		return Change{}, err
	}
	b, mode, err := w.currentText(p)
	if err != nil {
		return Change{}, err
	}
	if got := revisionOf(b); got != expected {
		return Change{}, conflict(expected, got)
	}
	if oldText == "" {
		return Change{}, errors.New("replacement text must not be empty")
	}
	if err := w.mutationPath(p); err != nil {
		return Change{}, err
	}
	if !validText([]byte(newText)) {
		return Change{}, ErrBinary
	}
	count := strings.Count(string(b), oldText)
	if count != 1 {
		if count > 1 {
			return Change{}, fmt.Errorf("%w: %d matches", ErrAmbiguous, count)
		}
		return Change{}, fmt.Errorf("replacement text not found")
	}
	after := strings.Replace(string(b), oldText, newText, 1)
	return w.commit(p, b, mode, []byte(after))
}

// Write replaces a file after checking expectedRevision. A nil expected
// revision is an explicit must-not-exist precondition.
func (w *Workspace) Write(name string, expectedRevision *Revision, content string) (Change, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return Change{}, err
	}
	p, err := cleanPath(name, false)
	if err != nil {
		return Change{}, err
	}
	if err := w.mutationPath(p); err != nil {
		return Change{}, err
	}
	if !validText([]byte(content)) {
		return Change{}, ErrBinary
	}
	if len(content) > maxTextBytes {
		return Change{}, ErrTooLarge
	}
	b, mode, exists, err := w.current(p)
	if err != nil {
		return Change{}, err
	}
	if expectedRevision == nil {
		if exists {
			return Change{}, conflict("<missing>", revisionOf(b))
		}
		mode = 0644
	} else {
		if !exists {
			return Change{}, conflict(*expectedRevision, "<missing>")
		}
		if got := revisionOf(b); got != *expectedRevision {
			return Change{}, conflict(*expectedRevision, got)
		}
	}
	return w.commit(p, b, mode, []byte(content))
}

// Delete removes a regular file after saving its before-image. Undo restores
// that image only while the path is still absent.
func (w *Workspace) Delete(name string) (Change, string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return Change{}, "", err
	}
	p, err := cleanPath(name, false)
	if err != nil {
		return Change{}, "", err
	}
	if err := w.mutationPath(p); err != nil {
		return Change{}, "", err
	}
	b, mode, exists, err := w.current(p)
	if err != nil {
		return Change{}, "", err
	}
	if !exists {
		return Change{}, "", fmt.Errorf("delete %q: %w", name, fs.ErrNotExist)
	}
	if !validText(b) {
		return Change{}, "", ErrBinary
	}
	prev := string(b)
	change, err := w.commitDelete(p, b, mode)
	if err != nil {
		return Change{}, "", err
	}
	return change, prev, nil
}

func (w *Workspace) commitDelete(p string, before []byte, mode fs.FileMode) (Change, error) {
	id, err := newChangeID()
	if err != nil {
		return Change{}, err
	}
	jc := journalChange{Change: Change{ID: id, Path: p, Before: revisionOf(before), After: revisionOf(nil), BeforeExists: true, AfterExists: false, Mode: uint32(mode.Perm()), Status: "prepared"}, BeforeMode: uint32(mode.Perm())}
	if err := atomicFile(filepath.Join(w.stateDir, "before", string(id)+".bin"), before, 0600); err != nil {
		return Change{}, fmt.Errorf("save before-image: %w", err)
	}
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	jc.Status = "applying"
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	if err := w.root.Remove(p); err != nil {
		return Change{}, fmt.Errorf("delete %q: %w", p, err)
	}
	jc.Status = "committed"
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	return jc.Change, nil
}

// Undo restores the before-image only when the file still has the revision
// produced by the original change. A manual edit therefore remains intact and
// is reported as a conflict.
func (w *Workspace) Undo(id ChangeID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return err
	}
	jc, err := w.loadChange(id)
	if err != nil {
		return err
	}
	if jc.Status != "committed" {
		return fmt.Errorf("change is %s, not committed", jc.Status)
	}
	if err := w.mutationPath(jc.Path); err != nil {
		return err
	}
	b, _, exists, err := w.current(jc.Path)
	if err != nil {
		return err
	}
	if !jc.AfterExists {
		if exists {
			return conflict("<missing>", revisionOf(b))
		}
		if !jc.BeforeExists {
			return errors.New("deleted change has no before-image")
		}
		before, err := os.ReadFile(filepath.Join(w.stateDir, "before", string(jc.ID)+".bin"))
		if err != nil {
			return fmt.Errorf("read before-image: %w", err)
		}
		if revisionOf(before) != jc.Before {
			return errors.New("before-image checksum mismatch")
		}
		if _, err := w.replace(jc.Path, before, fs.FileMode(jc.BeforeMode)); err != nil {
			return err
		}
		jc.Status = "reverted"
		return w.saveChange(jc)
	}
	if !exists || revisionOf(b) != jc.After {
		got := Revision("<missing>")
		if exists {
			got = revisionOf(b)
		}
		return conflict(jc.After, got)
	}
	if jc.BeforeExists {
		before, err := os.ReadFile(filepath.Join(w.stateDir, "before", string(id)+".bin"))
		if err != nil {
			return fmt.Errorf("read before-image: %w", err)
		}
		if revisionOf(before) != jc.Before {
			return errors.New("before-image checksum mismatch")
		}
		if _, err := w.replace(jc.Path, before, fs.FileMode(jc.BeforeMode)); err != nil {
			return err
		}
	} else if err := w.root.Remove(jc.Path); err != nil {
		return fmt.Errorf("undo remove %q: %w", jc.Path, err)
	}
	jc.Status = "reverted"
	if err := w.saveChange(jc); err != nil {
		return err
	}
	return nil
}

func (w *Workspace) currentText(p string) ([]byte, fs.FileMode, error) {
	b, mode, exists, err := w.current(p)
	if err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, fmt.Errorf("read %q: %w", p, fs.ErrNotExist)
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return nil, 0, fmt.Errorf("read %q: %w", p, ErrBinary)
	}
	return b, mode, nil
}

func (w *Workspace) current(p string) ([]byte, fs.FileMode, bool, error) {
	info, err := w.root.Stat(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, 0, false, nil
		}
		return nil, 0, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, false, fmt.Errorf("%q is not a regular file", p)
	}
	if info.Size() > maxTextBytes {
		return nil, 0, false, ErrTooLarge
	}
	f, err := w.root.Open(p)
	if err != nil {
		return nil, 0, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTextBytes+1))
	if len(b) > maxTextBytes {
		return nil, 0, false, ErrTooLarge
	}
	if err != nil {
		return nil, 0, false, err
	}
	return b, info.Mode().Perm(), true, nil
}

func (w *Workspace) commit(p string, before []byte, mode fs.FileMode, after []byte) (Change, error) {
	if len(after) > maxTextBytes {
		return Change{}, ErrTooLarge
	}
	id, err := newChangeID()
	if err != nil {
		return Change{}, err
	}
	beforeExists := before != nil
	jc := journalChange{Change: Change{ID: id, Path: p, Before: revisionOf(before), After: revisionOf(after), BeforeExists: beforeExists, AfterExists: true, Mode: uint32(mode.Perm()), Status: "prepared"}, BeforeMode: uint32(mode.Perm())}
	if beforeExists {
		if err := atomicFile(filepath.Join(w.stateDir, "before", string(id)+".bin"), before, 0600); err != nil {
			return Change{}, fmt.Errorf("save before-image: %w", err)
		}
	}
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	jc.Status = "applying"
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	if _, err := w.replace(p, after, mode); err != nil {
		return Change{}, err
	}
	jc.Status = "committed"
	if err := w.saveChange(jc); err != nil {
		return Change{}, err
	}
	return jc.Change, nil
}

func (w *Workspace) replace(p string, data []byte, mode fs.FileMode) (Revision, error) {
	parent := filepath.Dir(p)
	if parent == "." {
		parent = "."
	}
	if err := w.root.MkdirAll(parent, 0755); err != nil {
		return "", err
	}
	tmp := filepath.Join(parent, ".bifrost-tmp-"+randomSuffix())
	f, err := w.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = w.root.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return "", err
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := w.root.Chmod(tmp, mode.Perm()); err != nil {
		return "", err
	}
	if err := w.root.Rename(tmp, p); err != nil {
		return "", err
	}
	ok = true
	dir, err := w.root.Open(parent)
	if err != nil {
		return "", err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return "", err
	}
	return revisionOf(data), nil
}

func (w *Workspace) saveChange(jc journalChange) error {
	b, err := json.Marshal(jc)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(w.stateDir, "changes", string(jc.ID)+".json"), b, 0600)
}

func (w *Workspace) loadChange(id ChangeID) (journalChange, error) {
	if !validChangeID(id) {
		return journalChange{}, ErrUnknownChange
	}
	b, err := os.ReadFile(filepath.Join(w.stateDir, "changes", string(id)+".json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return journalChange{}, ErrUnknownChange
		}
		return journalChange{}, err
	}
	var jc journalChange
	if err := json.Unmarshal(b, &jc); err != nil {
		return journalChange{}, fmt.Errorf("decode change: %w", err)
	}
	if err := validateChange(jc); err != nil {
		return journalChange{}, err
	}
	if jc.ID != id {
		return journalChange{}, ErrUnknownChange
	}
	return jc, nil
}

func (w *Workspace) recoverPending() error {
	entries, err := os.ReadDir(filepath.Join(w.stateDir, "changes"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(w.stateDir, "changes", e.Name()))
		if err != nil {
			return err
		}
		var jc journalChange
		if err := json.Unmarshal(b, &jc); err != nil {
			return fmt.Errorf("decode change %s: %w", e.Name(), err)
		}
		if err := validateChange(jc); err != nil {
			return err
		}
		if string(jc.ID)+".json" != e.Name() {
			return errors.New("change ID does not match filename")
		}
		if jc.Status != "prepared" && jc.Status != "applying" {
			continue
		}
		current, _, exists, err := w.current(jc.Path)
		if err != nil {
			return err
		}
		switch {
		case !jc.AfterExists && !exists:
			jc.Status = "committed"
		case exists && revisionOf(current) == jc.After:
			jc.Status = "committed"
		default:
			jc.Status = "partial"
		}
		if err := w.saveChange(jc); err != nil {
			return err
		}
	}
	return nil
}

func (w *Workspace) check() error {
	if w.closed {
		return ErrClosed
	}
	return nil
}

func cleanPath(name string, directory bool) (string, error) {
	if name == "" {
		if directory {
			return ".", nil
		}
		return "", ErrInvalidPath
	}
	if name != "." && !fs.ValidPath(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, name)
	}
	if filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return "", fmt.Errorf("%w: %q", ErrInvalidPath, name)
	}
	if !directory && name == "." {
		return "", ErrInvalidPath
	}
	return filepath.FromSlash(name), nil
}

func revisionOf(b []byte) Revision {
	sum := sha256.Sum256(b)
	return Revision(hex.EncodeToString(sum[:]))
}

func conflict(expected, got Revision) error {
	return fmt.Errorf("%w: expected %s, got %s", ErrConflict, expected, got)
}

func newChangeID() (ChangeID, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return ChangeID("change_" + hex.EncodeToString(b[:])), nil
}

func randomSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "fallback"
	}
	return hex.EncodeToString(b[:])
}

func atomicFile(name string, data []byte, mode fs.FileMode) error {
	tmp := name + ".tmp-" + randomSuffix()
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = f.Close()
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		return err
	}
	ok = true
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func validText(b []byte) bool { return utf8.Valid(b) && !strings.ContainsRune(string(b), 0) }

func validChangeID(id ChangeID) bool {
	s := string(id)
	if len(s) != len("change_")+32 || !strings.HasPrefix(s, "change_") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(s, "change_"))
	return err == nil
}

func validateChange(jc journalChange) error {
	if !validChangeID(jc.ID) {
		return ErrUnknownChange
	}
	if _, err := cleanPath(jc.Path, false); err != nil {
		return err
	}
	for _, rev := range []string{jc.Before, jc.After} {
		if len(rev) != 64 {
			return errors.New("invalid change revision")
		}
		if _, err := hex.DecodeString(rev); err != nil {
			return err
		}
	}
	switch jc.Status {
	case "prepared", "applying", "committed", "partial", "reverted":
	default:
		return errors.New("invalid change status")
	}
	return nil
}

// Reading links inside the root is supported; replacing a link would replace
// its inode rather than its target, so mutation requires an explicit real path.
func (w *Workspace) mutationPath(p string) error {
	if p == ".git" || strings.HasPrefix(p, ".git"+string(filepath.Separator)) || p == ".bifrost" || strings.HasPrefix(p, ".bifrost"+string(filepath.Separator)) {
		return fmt.Errorf("%w: protected metadata path %q", ErrInvalidPath, p)
	}
	info, err := w.root.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%q is not a regular file; edit its real path", p)
	}
	return nil
}

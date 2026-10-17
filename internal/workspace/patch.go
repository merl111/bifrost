package workspace

// Patch transactions provide a bounded, revision-checked batch boundary for
// callers that need to create, replace, delete, or move several files. The
// filesystem operations are individually atomic; the transaction journal
// makes interruption recoverable, but cannot make unrelated external writes
// participate in a cross-file filesystem CAS.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxPatchOperations   = 128
	maxPatchContentBytes = 16 << 20
	maxPatchPathBytes    = 4096
	maxPatchJournalBytes = maxPatchContentBytes + 4<<20
)

func (w *Workspace) patchMutationPath(path string) error {
	if path == ".git" || strings.HasPrefix(path, ".git"+string(filepath.Separator)) || path == ".bifrost" || strings.HasPrefix(path, ".bifrost"+string(filepath.Separator)) {
		return fmt.Errorf("%w: protected metadata path %q", ErrInvalidPath, path)
	}
	return w.mutationPath(path)
}

type PatchOperation struct {
	Kind     string    `json:"kind"` // create, replace, delete, move
	Path     string    `json:"path"`
	To       string    `json:"to,omitempty"`
	Expected *Revision `json:"expected_revision,omitempty"`
	Content  string    `json:"content,omitempty"`
	Mode     uint32    `json:"mode,omitempty"`
}

type PatchPlan struct {
	Operations []PatchOperation `json:"operations"`
}

type PatchChange struct {
	Kind         string   `json:"kind"`
	Path         string   `json:"path"`
	To           string   `json:"to,omitempty"`
	Before       Revision `json:"before"`
	After        Revision `json:"after"`
	BeforeExists bool     `json:"before_exists"`
	AfterExists  bool     `json:"after_exists"`
}

type PatchResult struct {
	ID      ChangeID      `json:"id"`
	Status  string        `json:"status"`
	Changes []PatchChange `json:"changes"`
}

type preparedPatch struct {
	PatchOperation
	path, to       string
	before         []byte
	beforeMode     fs.FileMode
	beforeExists   bool
	after          []byte
	afterMode      fs.FileMode
	afterExists    bool
	beforeRevision Revision
	afterRevision  Revision
}

type patchJournal struct {
	ID     ChangeID         `json:"id"`
	Status string           `json:"status"`
	Ops    []patchJournalOp `json:"operations"`
}

type patchJournalOp struct {
	PatchOperation
	Path         string   `json:"path"`
	To           string   `json:"to,omitempty"`
	Before       Revision `json:"before"`
	After        Revision `json:"after"`
	BeforeExists bool     `json:"before_exists"`
	AfterExists  bool     `json:"after_exists"`
	BeforeMode   uint32   `json:"before_mode"`
	AfterMode    uint32   `json:"after_mode"`
}

// ApplyPatch validates the entire plan and its revisions before changing any
// file. A committed result is durable in the state journal. If a later file
// operation fails, earlier operations are rolled back when their current
// revisions still match the transaction's after-images.
func (w *Workspace) ApplyPatch(plan PatchPlan) (PatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return PatchResult{}, err
	}
	prepared, err := w.preparePatch(plan)
	if err != nil {
		return PatchResult{}, err
	}
	id, err := newChangeID()
	if err != nil {
		return PatchResult{}, err
	}
	j := patchJournal{ID: id, Status: "prepared", Ops: make([]patchJournalOp, len(prepared))}
	for i, p := range prepared {
		j.Ops[i] = patchJournalOp{PatchOperation: p.PatchOperation, Path: p.path, To: p.to,
			Before: p.beforeRevision, After: p.afterRevision, BeforeExists: p.beforeExists,
			AfterExists: p.afterExists, BeforeMode: uint32(p.beforeMode.Perm()), AfterMode: uint32(p.afterMode.Perm())}
		if p.beforeExists {
			if err := atomicFile(w.patchBeforePath(id, i), p.before, 0600); err != nil {
				return PatchResult{}, fmt.Errorf("save patch before-image: %w", err)
			}
		}
	}
	if err := w.savePatch(j); err != nil {
		return PatchResult{}, err
	}
	j.Status = "applying"
	if err := w.savePatch(j); err != nil {
		return PatchResult{}, err
	}
	for i, p := range prepared {
		if err := w.revalidatePrepared(p); err != nil {
			rolledBack := w.rollbackPatch(j)
			if rolledBack {
				j.Status = "rolledback"
			} else {
				j.Status = "partial"
			}
			_ = w.savePatch(j)
			return PatchResult{ID: id, Status: j.Status}, fmt.Errorf("patch operation %d changed before apply: %w", i, err)
		}
		if err := w.applyPrepared(p); err != nil {
			rolledBack := w.rollbackPatch(j)
			if rolledBack {
				j.Status = "rolledback"
			} else {
				j.Status = "partial"
			}
			_ = w.savePatch(j)
			return PatchResult{ID: id, Status: j.Status}, fmt.Errorf("patch operation %d: %w", i, err)
		}
	}
	j.Status = "committed"
	if err := w.savePatch(j); err != nil {
		return PatchResult{ID: id, Status: "applying"}, err
	}
	return patchResult(j), nil
}

// UndoPatch restores a committed transaction only when every affected path
// still has the transaction's after-image. All paths and before-images are
// validated before the first restore, so a stale batch cannot partially undo
// newer work.
func (w *Workspace) UndoPatch(id ChangeID) (PatchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return PatchResult{}, err
	}
	if !validChangeID(id) {
		return PatchResult{}, ErrUnknownChange
	}
	j, err := w.loadPatch(w.patchPath(id))
	if err != nil {
		return PatchResult{}, err
	}
	if j.Status != "committed" {
		return PatchResult{}, fmt.Errorf("patch is %s, not committed", j.Status)
	}
	if err := w.validatePatchUndo(j); err != nil {
		return PatchResult{}, err
	}
	j.Status = "reverting"
	if err := w.savePatch(j); err != nil {
		return PatchResult{}, err
	}
	for i := len(j.Ops) - 1; i >= 0; i-- {
		if err := w.undoPatchOp(j, i); err != nil {
			j.Status = "partial"
			_ = w.savePatch(j)
			return PatchResult{ID: j.ID, Status: j.Status}, fmt.Errorf("undo patch operation %d: %w", i, err)
		}
	}
	j.Status = "reverted"
	if err := w.savePatch(j); err != nil {
		return PatchResult{ID: j.ID, Status: "reverting"}, err
	}
	return patchResult(j), nil
}

func (w *Workspace) validatePatchUndo(j patchJournal) error {
	for i, op := range j.Ops {
		if err := w.patchMutationPath(op.Path); err != nil {
			return err
		}
		if op.Kind == "move" {
			if err := w.patchMutationPath(op.To); err != nil {
				return err
			}
			source, _, sourceExists, sourceErr := w.current(op.Path)
			dest, _, destExists, destErr := w.current(op.To)
			if sourceErr != nil || destErr != nil {
				return errors.Join(sourceErr, destErr)
			}
			if sourceExists || !samePatchState(dest, destExists, op.After, true) {
				return conflict(op.After, revisionOf(dest))
			}
			_ = source
			continue
		}
		data, _, exists, err := w.current(op.Path)
		if err != nil {
			return err
		}
		if !samePatchState(data, exists, op.After, op.AfterExists) {
			return conflict(op.After, revisionOf(data))
		}
		if op.BeforeExists {
			before, err := readPatchBefore(w.patchBeforePath(j.ID, i))
			if err != nil {
				return fmt.Errorf("read patch before-image: %w", err)
			}
			if revisionOf(before) != op.Before {
				return errors.New("patch before-image checksum mismatch")
			}
		}
	}
	return nil
}

func (w *Workspace) undoPatchOp(j patchJournal, index int) error {
	op := j.Ops[index]
	if op.Kind == "move" {
		if err := w.moveNoReplace(op.To, op.Path); err != nil {
			return err
		}
		return syncRootDirs(w.root, filepath.Dir(op.To), filepath.Dir(op.Path))
	}
	if op.BeforeExists {
		before, err := readPatchBefore(w.patchBeforePath(j.ID, index))
		if err != nil {
			return err
		}
		_, err = w.replace(op.Path, before, fs.FileMode(op.BeforeMode))
		return err
	}
	if err := w.root.Remove(op.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncRootDirs(w.root, filepath.Dir(op.Path))
}

func (w *Workspace) preparePatch(plan PatchPlan) ([]preparedPatch, error) {
	if len(plan.Operations) == 0 || len(plan.Operations) > maxPatchOperations {
		return nil, fmt.Errorf("patch must contain 1..%d operations", maxPatchOperations)
	}
	seen := make(map[string]bool, len(plan.Operations)*2)
	out := make([]preparedPatch, 0, len(plan.Operations))
	contentBytes := 0
	for _, op := range plan.Operations {
		if op.Kind != "create" && op.Kind != "replace" && op.Kind != "delete" && op.Kind != "move" {
			return nil, fmt.Errorf("unsupported patch operation %q", op.Kind)
		}
		if len(op.Path) > maxPatchPathBytes || len(op.To) > maxPatchPathBytes {
			return nil, errors.New("patch path exceeds 4096 bytes")
		}
		contentBytes += len(op.Content)
		if contentBytes > maxPatchContentBytes {
			return nil, ErrTooLarge
		}
		path, err := cleanPath(op.Path, false)
		if err != nil {
			return nil, err
		}
		if seen[path] {
			return nil, fmt.Errorf("patch path appears more than once: %q", path)
		}
		seen[path] = true
		if err := w.patchMutationPath(path); err != nil {
			return nil, err
		}
		if op.Mode&^uint32(0777) != 0 {
			return nil, errors.New("patch mode must contain only permission bits")
		}
		if op.Kind != "move" && op.To != "" {
			return nil, fmt.Errorf("patch operation %q does not accept to", op.Kind)
		}
		if op.Kind != "create" && op.Kind != "replace" && op.Content != "" {
			return nil, fmt.Errorf("patch operation %q does not accept content", op.Kind)
		}
		if op.Kind == "delete" && op.Mode != 0 {
			return nil, errors.New("delete does not accept mode")
		}
		if op.Kind == "move" && op.Mode != 0 {
			return nil, errors.New("move does not accept mode")
		}
		to := ""
		if op.Kind == "move" {
			to, err = cleanPath(op.To, false)
			if err != nil {
				return nil, fmt.Errorf("move destination: %w", err)
			}
			if path == to || seen[to] {
				return nil, fmt.Errorf("patch path appears more than once: %q", to)
			}
			seen[to] = true
			if err := w.patchMutationPath(to); err != nil {
				return nil, err
			}
		}
		before, mode, exists, err := w.current(path)
		if err != nil {
			return nil, err
		}
		p := preparedPatch{PatchOperation: op, path: path, to: to, before: before, beforeMode: mode,
			beforeExists: exists, beforeRevision: revisionOf(before)}
		switch op.Kind {
		case "create":
			if op.Expected != nil || exists {
				return nil, conflict("<missing>", revisionOf(before))
			}
			if err := validPatchContent(op.Content); err != nil {
				return nil, err
			}
			p.after, p.afterMode, p.afterExists = []byte(op.Content), patchMode(op.Mode, 0644), true
		case "replace":
			if op.Expected == nil {
				return nil, errors.New("replace requires expected_revision")
			}
			if err := checkPatchRevision(*op.Expected, before, exists); err != nil {
				return nil, err
			}
			if err := validPatchContent(op.Content); err != nil {
				return nil, err
			}
			p.after, p.afterMode, p.afterExists = []byte(op.Content), patchMode(op.Mode, uint32(mode.Perm())), true
		case "delete":
			if op.Expected == nil {
				return nil, errors.New("delete requires expected_revision")
			}
			if err := checkPatchRevision(*op.Expected, before, exists); err != nil {
				return nil, err
			}
			p.afterMode, p.afterExists = 0, false
		case "move":
			if op.Expected == nil {
				return nil, errors.New("move requires expected_revision")
			}
			if err := checkPatchRevision(*op.Expected, before, exists); err != nil {
				return nil, err
			}
			_, _, destinationExists, err := w.current(to)
			if err != nil {
				return nil, err
			}
			if destinationExists {
				return nil, conflict("<missing>", revisionOf(nil))
			}
			p.after, p.afterMode, p.afterExists = before, mode, true
		}
		p.afterRevision = revisionOf(p.after)
		out = append(out, p)
	}
	return out, nil
}

func validPatchContent(content string) error {
	if len(content) > maxTextBytes {
		return ErrTooLarge
	}
	if !validText([]byte(content)) {
		return ErrBinary
	}
	return nil
}

func patchMode(value, fallback uint32) fs.FileMode {
	if value == 0 {
		value = fallback
	}
	return fs.FileMode(value & 0777)
}

func checkPatchRevision(expected Revision, before []byte, exists bool) error {
	if !validRevision(expected) {
		return errors.New("invalid expected patch revision")
	}
	if !exists {
		return conflict(expected, "<missing>")
	}
	if got := revisionOf(before); got != expected {
		return conflict(expected, got)
	}
	return nil
}

func validRevision(value Revision) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (w *Workspace) applyPrepared(p preparedPatch) error {
	switch p.Kind {
	case "create":
		return w.create(p.path, p.after, p.afterMode)
	case "replace":
		_, err := w.replace(p.path, p.after, p.afterMode)
		return err
	case "delete":
		if err := w.root.Remove(p.path); err != nil {
			return err
		}
		return syncRootDirs(w.root, filepath.Dir(p.path))
	case "move":
		if err := w.root.MkdirAll(filepath.Dir(p.to), 0755); err != nil {
			return err
		}
		if err := w.moveNoReplace(p.path, p.to); err != nil {
			return err
		}
		return syncRootDirs(w.root, filepath.Dir(p.path), filepath.Dir(p.to))
	default:
		return errors.New("unknown patch operation")
	}
}

func (w *Workspace) revalidatePrepared(p preparedPatch) error {
	before, _, exists, err := w.current(p.path)
	if err != nil {
		return err
	}
	if p.Kind == "create" {
		if exists {
			return conflict("<missing>", revisionOf(before))
		}
	} else if err := checkPatchRevision(p.beforeRevision, before, exists); err != nil {
		return err
	}
	if p.Kind == "move" {
		_, _, destinationExists, err := w.current(p.to)
		if err != nil {
			return err
		}
		if destinationExists {
			return conflict("<missing>", revisionOf(nil))
		}
	}
	return nil
}

func syncRootDirs(root *os.Root, dirs ...string) error {
	seen := map[string]bool{}
	for _, name := range dirs {
		if seen[name] {
			continue
		}
		seen[name] = true
		d, err := root.Open(name)
		if err != nil {
			return err
		}
		err = d.Sync()
		_ = d.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// create writes a new file without replacing a file that appeared after
// preparation. OpenFile(O_EXCL) makes the destination check part of the
// filesystem operation rather than a separate TOCTOU check.
func (w *Workspace) create(path string, data []byte, mode fs.FileMode) error {
	if err := w.root.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := w.root.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		_ = w.root.Remove(path)
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return syncRootDirs(w.root, filepath.Dir(path))
}

// moveNoReplace implements a crash-recoverable move for regular files. Link
// refuses an existing destination, then removing the source completes the
// move. If the process stops between those operations recovery sees both
// names and marks the transaction partial instead of overwriting either file.
func (w *Workspace) moveNoReplace(from, to string) error {
	if err := w.root.Link(from, to); err != nil {
		return err
	}
	if err := w.root.Remove(from); err != nil {
		return err
	}
	return nil
}

func (w *Workspace) rollbackPatch(j patchJournal) bool {
	ok := true
	for i := len(j.Ops) - 1; i >= 0; i-- {
		op := j.Ops[i]
		if op.Kind == "move" {
			if !w.rollbackMove(op) {
				ok = false
			}
			continue
		}
		current, mode, exists, err := w.current(op.Path)
		if err != nil {
			ok = false
			continue
		}
		if exists && op.BeforeExists && revisionOf(current) == op.Before {
			continue
		}
		if !exists && !op.BeforeExists {
			continue
		}
		if !samePatchState(current, exists, op.After, op.AfterExists) {
			_ = mode
			ok = false
			continue
		}
		before := []byte(nil)
		if op.BeforeExists {
			var readErr error
			before, readErr = readPatchBefore(w.patchBeforePath(j.ID, i))
			if readErr != nil || revisionOf(before) != op.Before {
				ok = false
				continue
			}
		}
		if op.BeforeExists {
			if _, err := w.replace(op.Path, before, fs.FileMode(op.BeforeMode)); err != nil {
				ok = false
			}
		} else if err := w.root.Remove(op.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			ok = false
		} else if !op.BeforeExists {
			if err := syncRootDirs(w.root, filepath.Dir(op.Path)); err != nil {
				ok = false
			}
		}
	}
	return ok
}

func (w *Workspace) rollbackMove(op patchJournalOp) bool {
	source, _, sourceExists, sourceErr := w.current(op.Path)
	dest, _, destExists, destErr := w.current(op.To)
	if sourceErr != nil || destErr != nil {
		return false
	}
	if sourceExists && revisionOf(source) == op.Before {
		// A destination appearing while the source remains is a partial move,
		// not an untouched operation. Preserve both files for manual recovery.
		return !destExists
	}
	if !samePatchState(nil, sourceExists, revisionOf(nil), false) || !samePatchState(dest, destExists, op.After, true) {
		return false
	}
	if err := w.moveNoReplace(op.To, op.Path); err != nil {
		return false
	}
	return syncRootDirs(w.root, filepath.Dir(op.To), filepath.Dir(op.Path)) == nil
}

func samePatchState(data []byte, exists bool, revision Revision, expectedExists bool) bool {
	if exists != expectedExists {
		return false
	}
	return !exists || revisionOf(data) == revision
}

func patchResult(j patchJournal) PatchResult {
	result := PatchResult{ID: j.ID, Status: j.Status, Changes: make([]PatchChange, len(j.Ops))}
	for i, op := range j.Ops {
		result.Changes[i] = PatchChange{Kind: op.Kind, Path: op.Path, To: op.To, Before: op.Before, After: op.After, BeforeExists: op.BeforeExists, AfterExists: op.AfterExists}
	}
	return result
}

func (w *Workspace) patchPath(id ChangeID) string {
	return filepath.Join(w.stateDir, "patches", string(id)+".json")
}

func (w *Workspace) patchBeforePath(id ChangeID, index int) string {
	return filepath.Join(w.stateDir, "patch-before", fmt.Sprintf("%s-%03d.bin", id, index))
}

func (w *Workspace) savePatch(j patchJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicFile(w.patchPath(j.ID), b, 0600)
}

func (w *Workspace) loadPatch(name string) (patchJournal, error) {
	b, err := readBoundedPatchFile(name, maxPatchJournalBytes)
	if err != nil {
		return patchJournal{}, err
	}
	var j patchJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return patchJournal{}, err
	}
	if !validChangeID(j.ID) || len(j.Ops) == 0 || len(j.Ops) > maxPatchOperations {
		return patchJournal{}, errors.New("invalid patch journal")
	}
	if j.Status != "prepared" && j.Status != "applying" && j.Status != "committed" && j.Status != "rolledback" && j.Status != "partial" && j.Status != "reverting" && j.Status != "reverted" {
		return patchJournal{}, errors.New("invalid patch journal status")
	}
	for _, op := range j.Ops {
		if op.Kind != "create" && op.Kind != "replace" && op.Kind != "delete" && op.Kind != "move" {
			return patchJournal{}, errors.New("invalid patch journal operation")
		}
		if _, err := cleanPath(op.Path, false); err != nil {
			return patchJournal{}, err
		}
		if op.Kind == "move" {
			if _, err := cleanPath(op.To, false); err != nil {
				return patchJournal{}, err
			}
		}
	}
	return j, nil
}

func readPatchBefore(path string) ([]byte, error) {
	return readBoundedPatchFile(path, maxTextBytes)
}

func readBoundedPatchFile(path string, limit int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, ErrTooLarge
	}
	return b, nil
}

func (w *Workspace) recoverPatches() error {
	entries, err := os.ReadDir(filepath.Join(w.stateDir, "patches"))
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		j, err := w.loadPatch(filepath.Join(w.stateDir, "patches", entry.Name()))
		if err != nil {
			return err
		}
		if strings.TrimSuffix(entry.Name(), ".json") != string(j.ID) {
			return errors.New("patch ID does not match filename")
		}
		if j.Status != "prepared" && j.Status != "applying" && j.Status != "reverting" {
			continue
		}
		if j.Status == "reverting" && patchReverted(w, j) {
			j.Status = "reverted"
		} else if j.Status == "reverting" && patchApplied(w, j) {
			j.Status = "committed"
		} else if patchApplied(w, j) {
			j.Status = "committed"
		} else if w.rollbackPatch(j) {
			j.Status = "rolledback"
		} else {
			j.Status = "partial"
		}
		if err := w.savePatch(j); err != nil {
			return err
		}
	}
	return nil
}

func patchReverted(w *Workspace, j patchJournal) bool {
	for _, op := range j.Ops {
		if op.Kind == "move" {
			source, _, sourceExists, sourceErr := w.current(op.Path)
			_, _, destExists, destErr := w.current(op.To)
			if sourceErr != nil || destErr != nil || !sourceExists || destExists || revisionOf(source) != op.Before {
				return false
			}
			continue
		}
		data, _, exists, err := w.current(op.Path)
		if err != nil || !samePatchState(data, exists, op.Before, op.BeforeExists) {
			return false
		}
	}
	return true
}

func patchApplied(w *Workspace, j patchJournal) bool {
	for _, op := range j.Ops {
		if op.Kind == "move" {
			_, _, sourceExists, sourceErr := w.current(op.Path)
			dest, _, destExists, destErr := w.current(op.To)
			if sourceErr != nil || destErr != nil || sourceExists || !samePatchState(dest, destExists, op.After, true) {
				return false
			}
			continue
		}
		data, _, exists, err := w.current(op.Path)
		if err != nil || !samePatchState(data, exists, op.After, op.AfterExists) {
			return false
		}
	}
	return true
}

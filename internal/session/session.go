// Package session stores bounded, durable conversation snapshots.
//
// A session is deliberately a single JSON snapshot.  Each operation takes a
// short-lived advisory lock, so callers never hold a lock while a model or a
// tool is running.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"bifrost/internal/atomicfile"
	"bifrost/internal/domain"
	"bifrost/internal/filelock"
	"bifrost/internal/provider"
)

const (
	schemaVersion = 1
	// MaxSnapshotBytes bounds both reads and writes, including JSON framing.
	MaxSnapshotBytes = 16 << 20
	MaxMessages      = 4096
	MaxToolCalls     = 256
	MaxFingerprint   = 1024
)

var (
	ErrNotFound          = errors.New("session not found")
	ErrRevisionConflict  = errors.New("session revision conflict")
	ErrBindingMismatch   = errors.New("session binding mismatch")
	ErrUnsupportedSchema = errors.New("unsupported session schema")
	ErrCorrupt           = errors.New("corrupt session snapshot")
	ErrTooLarge          = errors.New("session snapshot too large")
	ErrInvalid           = errors.New("invalid session")
)

type Status string

const (
	StatusCompleted   Status = "completed"
	StatusInterrupted Status = "interrupted"
	StatusError       Status = "error"
)

// Session is the caller-facing representation of a conversation snapshot.
// Provider fingerprints are caller-supplied, non-secret identifiers such as a
// provider base URL and model name. API keys must never be put in this field.
type Session struct {
	ID                  domain.ID
	Root                string
	ProviderFingerprint string
	Messages            []provider.Message
	CreatedAt           time.Time
	UpdatedAt           time.Time
	Revision            uint64
	Status              Status
}

// Store owns one user-selected state directory. It does not hold a daemon
// lock; every operation acquires the session's lock and releases it before
// returning.
type Store struct {
	stateDir string
	sessions string
}

func New(stateDir string) (*Store, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, fmt.Errorf("session state directory is empty")
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("resolve session state directory: %w", err)
	}
	s := &Store{stateDir: filepath.Clean(abs)}
	s.sessions = filepath.Join(s.stateDir, "sessions")
	if err := os.MkdirAll(s.sessions, 0o700); err != nil {
		return nil, fmt.Errorf("create session state directory: %w", err)
	}
	return s, nil
}

// Create starts an interrupted (and therefore resumable) session.
func (s *Store) Create(root, fingerprint string) (Session, error) {
	if s == nil {
		return Session{}, fmt.Errorf("nil session store")
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return Session{}, err
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return Session{}, err
	}
	id, err := domain.NewID(domain.KindSession)
	if err != nil {
		return Session{}, fmt.Errorf("create session id: %w", err)
	}
	now := time.Now().UTC()
	sess := Session{ID: id, Root: root, ProviderFingerprint: fingerprint,
		Messages: []provider.Message{}, CreatedAt: now, UpdatedAt: now,
		Revision: 1, Status: StatusInterrupted}
	if err := validateSession(sess); err != nil {
		return Session{}, err
	}
	return sess, s.withLock(id, func() error {
		path := s.path(id)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s", ErrInvalid, id)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check session %s: %w", id, err)
		}
		return atomicWrite(path, sess)
	})
}

func (s *Store) Load(id domain.ID) (Session, error) {
	if s == nil {
		return Session{}, fmt.Errorf("nil session store")
	}
	if err := validateID(id); err != nil {
		return Session{}, err
	}
	var sess Session
	err := s.withLock(id, func() error {
		var err error
		sess, err = s.read(id)
		return err
	})
	return sess, err
}

// Save replaces a snapshot if expectedRevision is still current. On success
// the supplied session is advanced in the returned value.
func (s *Store) Save(sess Session, expectedRevision uint64) (Session, error) {
	if s == nil {
		return Session{}, fmt.Errorf("nil session store")
	}
	if err := validateID(sess.ID); err != nil {
		return Session{}, err
	}
	if expectedRevision == 0 {
		return Session{}, fmt.Errorf("%w: expected revision must be positive", ErrRevisionConflict)
	}
	var saved Session
	err := s.withLock(sess.ID, func() error {
		current, err := s.read(sess.ID)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return fmt.Errorf("%w: got %d, want %d", ErrRevisionConflict, expectedRevision, current.Revision)
		}
		if sess.Root != current.Root || sess.ProviderFingerprint != current.ProviderFingerprint {
			return fmt.Errorf("%w: session %s", ErrBindingMismatch, sess.ID)
		}
		if !sess.CreatedAt.Equal(current.CreatedAt) {
			return fmt.Errorf("%w: created time changed", ErrInvalid)
		}
		sess.Revision = current.Revision + 1
		sess.UpdatedAt = time.Now().UTC()
		if err := validateSession(sess); err != nil {
			return err
		}
		if err := atomicWrite(s.path(sess.ID), sess); err != nil {
			return err
		}
		saved = cloneSession(sess)
		return nil
	})
	if err != nil {
		return Session{}, err
	}
	return saved, nil
}

func (s *Store) List(root string) ([]Session, error) {
	if s == nil {
		return nil, fmt.Errorf("nil session store")
	}
	root, err := canonicalRoot(root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.sessions)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	result := make([]Session, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id, err := domain.ParseID(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, fmt.Errorf("list session %q: %w", entry.Name(), err)
		}
		if err := validateID(id); err != nil {
			return nil, err
		}
		var sess Session
		if err := s.withLock(id, func() error {
			var err error
			sess, err = s.read(id)
			return err
		}); err != nil {
			return nil, err
		}
		if sess.Root == root {
			result = append(result, sess)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

// ValidateBinding checks the explicit root/provider binding before a caller
// resumes a session. It does not perform any model or tool action.
func ValidateBinding(sess Session, root, fingerprint string) error {
	canonical, err := canonicalRoot(root)
	if err != nil {
		return err
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return err
	}
	if sess.Root != canonical || sess.ProviderFingerprint != fingerprint {
		return fmt.Errorf("%w: session %s", ErrBindingMismatch, sess.ID)
	}
	return nil
}

func (s *Store) ValidateBinding(sess Session, root, fingerprint string) error {
	return ValidateBinding(sess, root, fingerprint)
}

func (s *Store) read(id domain.ID) (Session, error) {
	b, err := readBounded(s.path(id))
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("read session %s: %w", id, err)
	}
	if len(b) > MaxSnapshotBytes {
		return Session{}, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, id, MaxSnapshotBytes)
	}
	sess, err := decode(b)
	if err != nil {
		return Session{}, fmt.Errorf("%w: %s: %w", ErrCorrupt, id, err)
	}
	return sess, nil
}

func (s *Store) path(id domain.ID) string     { return filepath.Join(s.sessions, id.String()+".json") }
func (s *Store) lockPath(id domain.ID) string { return filepath.Join(s.sessions, id.String()+".lock") }

func (s *Store) withLock(id domain.ID, fn func() error) error {
	lock, err := acquireWithWait(s.lockPath(id))
	if err != nil {
		return err
	}
	opErr := fn()
	closeErr := lock.Close()
	return errors.Join(opErr, closeErr)
}

func acquireWithWait(path string) (*filelock.Lock, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err := filelock.Acquire(path)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, filelock.ErrLocked) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func validateID(id domain.ID) error {
	kind, err := id.Kind()
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if kind != domain.KindSession {
		return fmt.Errorf("%w: id %s is %s, want %s", ErrInvalid, id, kind, domain.KindSession)
	}
	return nil
}

func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("%w: root is empty", ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root: %v", ErrInvalid, err)
	}
	abs = filepath.Clean(abs)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("%w: resolve root %q: %v", ErrInvalid, root, err)
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", fmt.Errorf("%w: stat root: %v", ErrInvalid, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%w: root is not a directory", ErrInvalid)
	}
	return filepath.Clean(real), nil
}

func readBounded(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxSnapshotBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSnapshotBytes {
		return nil, ErrTooLarge
	}
	return b, nil
}

func validateFingerprint(fp string) error {
	if strings.TrimSpace(fp) == "" {
		return fmt.Errorf("%w: provider fingerprint is empty", ErrInvalid)
	}
	if len(fp) > MaxFingerprint || strings.ContainsAny(fp, "\r\n") {
		return fmt.Errorf("%w: provider fingerprint is invalid", ErrInvalid)
	}
	return nil
}

func validateSession(s Session) error {
	if err := validateID(s.ID); err != nil {
		return err
	}
	canonical, err := canonicalRoot(s.Root)
	if err != nil {
		return err
	}
	if canonical != filepath.Clean(s.Root) {
		return fmt.Errorf("%w: root is not canonical", ErrInvalid)
	}
	if err := validateFingerprint(s.ProviderFingerprint); err != nil {
		return err
	}
	if s.CreatedAt.IsZero() || s.UpdatedAt.IsZero() || s.UpdatedAt.Before(s.CreatedAt) {
		return fmt.Errorf("%w: invalid timestamps", ErrInvalid)
	}
	if s.Revision == 0 {
		return fmt.Errorf("%w: revision is zero", ErrInvalid)
	}
	switch s.Status {
	case StatusCompleted, StatusInterrupted, StatusError:
	default:
		return fmt.Errorf("%w: unsupported status %q", ErrInvalid, s.Status)
	}
	if len(s.Messages) > MaxMessages {
		return fmt.Errorf("%w: message count exceeds %d", ErrTooLarge, MaxMessages)
	}
	if err := validateMessages(s.Messages); err != nil {
		return err
	}
	return nil
}

func validateMessages(messages []provider.Message) error {
	pending := make(map[string]struct{})
	seenCalls := make(map[string]struct{})
	for i, m := range messages {
		switch m.Role {
		case provider.RoleSystem, provider.RoleUser:
			if len(pending) != 0 {
				return fmt.Errorf("%w: message %d occurs before tool results", ErrInvalid, i)
			}
			if len(m.ToolCalls) != 0 || m.ToolCallID != "" {
				return fmt.Errorf("%w: message %d role %s has tool fields", ErrInvalid, i, m.Role)
			}
		case provider.RoleAssistant:
			if len(pending) != 0 || m.ToolCallID != "" {
				return fmt.Errorf("%w: invalid assistant message %d", ErrInvalid, i)
			}
			if len(m.ToolCalls) > MaxToolCalls {
				return fmt.Errorf("%w: too many tool calls", ErrTooLarge)
			}
			for _, call := range m.ToolCalls {
				if strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.Name) == "" || len(call.Arguments) == 0 || !json.Valid(call.Arguments) {
					return fmt.Errorf("%w: invalid tool call in message %d", ErrInvalid, i)
				}
				if _, ok := seenCalls[call.ID]; ok {
					return fmt.Errorf("%w: duplicate tool call %q", ErrInvalid, call.ID)
				}
				seenCalls[call.ID] = struct{}{}
				pending[call.ID] = struct{}{}
			}
		case provider.RoleTool:
			if len(m.ToolCalls) != 0 || strings.TrimSpace(m.ToolCallID) == "" {
				return fmt.Errorf("%w: invalid tool message %d", ErrInvalid, i)
			}
			if _, ok := pending[m.ToolCallID]; !ok {
				return fmt.Errorf("%w: tool result %q has no preceding call", ErrInvalid, m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		default:
			return fmt.Errorf("%w: unsupported message role %q", ErrInvalid, m.Role)
		}
	}
	if len(pending) != 0 {
		return fmt.Errorf("%w: missing tool results", ErrInvalid)
	}
	return nil
}

type snapshot struct {
	Version             int               `json:"version"`
	ID                  string            `json:"id"`
	Root                string            `json:"root"`
	ProviderFingerprint string            `json:"provider_fingerprint"`
	Messages            []snapshotMessage `json:"messages"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	Revision            uint64            `json:"revision"`
	Status              Status            `json:"status"`
}

type snapshotMessage struct {
	Role       provider.Role      `json:"role"`
	Content    string             `json:"content"`
	ToolCalls  []snapshotToolCall `json:"tool_calls"`
	ToolCallID string             `json:"tool_call_id"`
	ToolName   string             `json:"tool_name,omitempty"`
	Images     []provider.Image   `json:"images,omitempty"`
	Opaque     json.RawMessage    `json:"opaque,omitempty"`
}

type snapshotToolCall struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments json.RawMessage `json:"arguments"`
}

func encode(s Session) ([]byte, error) {
	if err := validateSession(s); err != nil {
		return nil, err
	}
	w := snapshot{Version: schemaVersion, ID: s.ID.String(), Root: s.Root,
		ProviderFingerprint: s.ProviderFingerprint, CreatedAt: s.CreatedAt.UTC(),
		UpdatedAt: s.UpdatedAt.UTC(), Revision: s.Revision, Status: s.Status,
		Messages: make([]snapshotMessage, len(s.Messages))}
	for i, m := range s.Messages {
		w.Messages[i] = snapshotMessage{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolName: m.ToolName, Images: cloneImages(m.Images), Opaque: append(json.RawMessage(nil), m.Opaque...),
			ToolCalls: make([]snapshotToolCall, len(m.ToolCalls))}
		for j, c := range m.ToolCalls {
			w.Messages[i].ToolCalls[j] = snapshotToolCall{ID: c.ID, Name: c.Name, Namespace: c.Namespace, Arguments: append(json.RawMessage(nil), c.Arguments...)}
		}
	}
	b, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode session: %w", err)
	}
	b = append(b, '\n')
	if len(b) > MaxSnapshotBytes {
		return nil, fmt.Errorf("%w: encoded session exceeds %d bytes", ErrTooLarge, MaxSnapshotBytes)
	}
	return b, nil
}

func decode(b []byte) (Session, error) {
	if len(b) > MaxSnapshotBytes {
		return Session{}, ErrTooLarge
	}
	dec := json.NewDecoder(bufio.NewReaderSize(bytes.NewReader(b), 4096))
	dec.DisallowUnknownFields()
	var w snapshot
	if err := dec.Decode(&w); err != nil {
		return Session{}, err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return Session{}, errors.New("multiple JSON values")
		}
		return Session{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return Session{}, err
	}
	for _, field := range []string{"version", "id", "root", "provider_fingerprint", "messages", "created_at", "updated_at", "revision", "status"} {
		if _, ok := fields[field]; !ok {
			return Session{}, fmt.Errorf("missing field %q", field)
		}
	}
	if string(fields["messages"]) == "null" {
		return Session{}, errors.New("messages must be an array")
	}
	if w.Version != schemaVersion {
		return Session{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, w.Version, schemaVersion)
	}
	id, err := domain.ParseID(w.ID)
	if err != nil {
		return Session{}, err
	}
	s := Session{ID: id, Root: w.Root, ProviderFingerprint: w.ProviderFingerprint,
		CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, Revision: w.Revision, Status: w.Status,
		Messages: make([]provider.Message, len(w.Messages))}
	for i, m := range w.Messages {
		s.Messages[i] = provider.Message{Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID, ToolName: m.ToolName, Images: cloneImages(m.Images), Opaque: append(json.RawMessage(nil), m.Opaque...),
			ToolCalls: make([]provider.ToolCall, len(m.ToolCalls))}
		for j, c := range m.ToolCalls {
			s.Messages[i].ToolCalls[j] = provider.ToolCall{ID: c.ID, Name: c.Name, Namespace: c.Namespace, Arguments: append(json.RawMessage(nil), c.Arguments...)}
		}
	}
	if err := validateSession(s); err != nil {
		return Session{}, err
	}
	return s, nil
}

func atomicWrite(path string, s Session) error {
	b, err := encode(s)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, b)
}

func cloneSession(s Session) Session {
	s.Messages = append([]provider.Message(nil), s.Messages...)
	for i := range s.Messages {
		s.Messages[i].Images = cloneImages(s.Messages[i].Images)
		s.Messages[i].Opaque = append(json.RawMessage(nil), s.Messages[i].Opaque...)
		s.Messages[i].ToolCalls = append([]provider.ToolCall(nil), s.Messages[i].ToolCalls...)
		for j := range s.Messages[i].ToolCalls {
			s.Messages[i].ToolCalls[j].Arguments = append(json.RawMessage(nil), s.Messages[i].ToolCalls[j].Arguments...)
		}
	}
	return s
}

func cloneImages(images []provider.Image) []provider.Image {
	out := append([]provider.Image(nil), images...)
	for i := range out {
		out[i].Data = append([]byte(nil), out[i].Data...)
	}
	return out
}

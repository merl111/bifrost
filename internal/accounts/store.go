// Package accounts stores provider account metadata and credentials locally.
// Metadata is intentionally separate from credentials: callers can use List
// and Get for UI without ever serializing a bearer token or API key.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"bifrost/internal/filelock"
)

type Kind string

const (
	KindChatGPT      Kind = "chatgpt"
	KindCursor       Kind = "cursor"
	KindCursorNative Kind = "cursor-native"
	KindClaude       Kind = "claude"
	KindOpenRouter   Kind = "openrouter"
)

// Account contains non-secret account metadata. It is safe to marshal this
// value for display. Credentials are kept in a separate 0600 file.
type Account struct {
	ID         string    `json:"id"`
	Generation string    `json:"generation,omitempty"`
	Name       string    `json:"name"`
	Kind       Kind      `json:"kind"`
	ConfigDir  string    `json:"config_dir,omitempty"`
	APIKeyEnv  string    `json:"api_key_env,omitempty"`
	Email      string    `json:"email,omitempty"`
	Subject    string    `json:"subject,omitempty"`
	Issuer     string    `json:"issuer,omitempty"`
	ClientID   string    `json:"client_id,omitempty"`
	HostID     string    `json:"host_id,omitempty"`
	Scopes     []string  `json:"scopes,omitempty"`
	ExpiresAt  time.Time `json:"expires_at,omitempty"`
	CreatedAt  time.Time `json:"created_at,omitempty"`
	UpdatedAt  time.Time `json:"updated_at,omitempty"`
}

// Credentials is only read from or written to the protected credential file.
// It deliberately has no JSON tags that would make it part of Account.
type Credentials struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	APIKey       string
	Scopes       []string
	ExpiresAt    time.Time
	SavedAt      time.Time
}

// Credential is kept as a singular alias for callers that model one token
// set per account.
type Credential = Credentials

var validName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var ErrIdentityMismatch = errors.New("accounts: account identity does not match existing record")

type Store struct {
	dir   string
	mu    sync.RWMutex
	locks map[string]*sync.Mutex
}

func New(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("accounts: directory is required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("accounts: create directory: %w", err)
	}
	return &Store{dir: dir, locks: make(map[string]*sync.Mutex)}, nil
}

func (s *Store) accountLock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[id]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[id] = l
	}
	return l
}

func (s *Store) metadataPath(id string) string { return filepath.Join(s.dir, id+".json") }
func (s *Store) secretPath(id string) string   { return filepath.Join(s.dir, id+".secret.json") }

func validateID(id string) error {
	if !validID.MatchString(id) || id == "." || id == ".." || strings.Contains(id, "..") {
		return errors.New("accounts: invalid account id")
	}
	return nil
}
func validateAccount(a Account) error {
	if err := validateID(a.ID); err != nil {
		return err
	}
	if !validName.MatchString(a.Name) {
		return errors.New("accounts: invalid account name")
	}
	switch a.Kind {
	case KindChatGPT, KindCursor, KindCursorNative, KindClaude, KindOpenRouter:
	default:
		return errors.New("accounts: unsupported account kind")
	}
	if strings.ContainsAny(a.ConfigDir, "\r\n") || strings.ContainsAny(a.APIKeyEnv, "\r\n") {
		return errors.New("accounts: invalid account field")
	}
	return nil
}

func (s *Store) List() ([]Account, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("accounts: list: %w", err)
	}
	out := make([]Account, 0)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".secret.json") {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		b, err := readPrivate(path, 1<<20)
		if err != nil {
			return nil, fmt.Errorf("accounts: read metadata: %w", err)
		}
		var a Account
		if err := json.Unmarshal(b, &a); err != nil {
			return nil, fmt.Errorf("accounts: decode metadata: %w", err)
		}
		if err := validateAccount(a); err != nil {
			return nil, err
		}
		if strings.TrimSuffix(e.Name(), ".json") != a.ID {
			return nil, errors.New("accounts: metadata filename does not match account id")
		}
		a.Scopes = append([]string(nil), a.Scopes...)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get accepts either an account ID (Get(id)) or a kind/name pair
// (Get(kind, name)). The flexible form keeps CLI callers from having to know
// how IDs are persisted.
func (s *Store) Get(key any, names ...string) (Account, error) {
	if kind, ok := key.(Kind); ok {
		if len(names) != 1 {
			return Account{}, errors.New("accounts: kind/name lookup requires a name")
		}
		return s.GetByName(kind, names[0])
	}
	id, ok := key.(string)
	if ok && len(names) == 1 {
		return s.GetByName(Kind(id), names[0])
	}
	if !ok || len(names) != 0 {
		return Account{}, errors.New("accounts: invalid account lookup")
	}
	if err := validateID(id); err != nil {
		return Account{}, err
	}
	b, err := readPrivate(s.metadataPath(id), 1<<20)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Account{}, os.ErrNotExist
		}
		return Account{}, fmt.Errorf("accounts: read metadata: %w", err)
	}
	var a Account
	if err := json.Unmarshal(b, &a); err != nil {
		return Account{}, fmt.Errorf("accounts: decode metadata: %w", err)
	}
	if err := validateAccount(a); err != nil {
		return Account{}, err
	}
	return a, nil
}

// GetByName resolves the stable user-facing alias without exposing secrets.
func (s *Store) GetByName(kind Kind, name string) (Account, error) {
	if !validName.MatchString(name) {
		return Account{}, errors.New("accounts: invalid account name")
	}
	list, err := s.List()
	if err != nil {
		return Account{}, err
	}
	for _, a := range list {
		if a.Kind == kind && a.Name == name {
			return a, nil
		}
	}
	return Account{}, os.ErrNotExist
}

// StableID provides a safe deterministic ID for a kind/name alias. Callers
// may use this when creating a new record, then preserve the returned ID.
func StableID(kind Kind, name string) (string, error) {
	if !validName.MatchString(name) {
		return "", errors.New("accounts: invalid account name")
	}
	if kind != KindChatGPT && kind != KindCursor && kind != KindCursorNative && kind != KindClaude && kind != KindOpenRouter {
		return "", errors.New("accounts: unsupported account kind")
	}
	id := strings.ToLower(string(kind)) + "-" + name
	if len(id) > 128 {
		return "", errors.New("accounts: account id is too long")
	}
	return id, nil
}

func (s *Store) Save(a Account, creds ...Credentials) error {
	if err := validateAccount(a); err != nil {
		return err
	}
	l := s.accountLock(a.ID)
	l.Lock()
	defer l.Unlock()
	fileLock, err := acquireFileLock(context.Background(), filepath.Join(s.dir, a.ID+".account.lock"))
	if err != nil {
		return err
	}
	defer fileLock.Close()
	generationProvided := a.Generation != ""
	now := time.Now().UTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	}
	if a.Generation == "" {
		a.Generation, _ = newUUID()
	}
	a.UpdatedAt = now
	if old, readErr := readPrivate(s.metadataPath(a.ID), 1<<20); readErr == nil {
		var prev Account
		if json.Unmarshal(old, &prev) == nil {
			if prev.Kind != a.Kind || prev.Name != a.Name {
				return ErrIdentityMismatch
			}
			if prev.Subject != "" && a.Subject != "" && (prev.Subject != a.Subject || prev.Issuer != a.Issuer || prev.ClientID != a.ClientID) {
				return ErrIdentityMismatch
			}
		}
		if !generationProvided {
			a.Generation = prev.Generation
		}
	}
	a.Scopes = append([]string(nil), a.Scopes...)
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return fmt.Errorf("accounts: encode metadata: %w", err)
	}
	var c Credentials
	if len(creds) > 0 {
		c = creds[0]
	} else if existing, readErr := readPrivate(s.secretPath(a.ID), 1<<20); readErr == nil {
		_ = json.Unmarshal(existing, &c)
	}
	if c.SavedAt.IsZero() {
		c.SavedAt = now
	}
	cb, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("accounts: encode credentials: %w", err)
	}
	if err := atomicPrivateWrite(s.secretPath(a.ID), cb, 0600); err != nil {
		return fmt.Errorf("accounts: save credentials: %w", err)
	}
	if err := atomicPrivateWrite(s.metadataPath(a.ID), b, 0600); err != nil {
		return fmt.Errorf("accounts: save metadata: %w", err)
	}
	return nil
}

// LoadCredentials is the only API that reads secret material.
func (s *Store) LoadCredentials(id string) (Credentials, error) {
	if err := validateID(id); err != nil {
		return Credentials{}, err
	}
	b, err := readPrivate(s.secretPath(id), 1<<20)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Credentials{}, os.ErrNotExist
		}
		return Credentials{}, fmt.Errorf("accounts: read credentials: %w", err)
	}
	var c Credentials
	if err := json.Unmarshal(b, &c); err != nil {
		return Credentials{}, errors.New("accounts: decode credentials")
	}
	return c, nil
}

// UpdateCredentials atomically rotates credentials without changing metadata.
func (s *Store) UpdateCredentials(id string, c Credentials) error {
	if err := validateID(id); err != nil {
		return err
	}
	l := s.accountLock(id)
	l.Lock()
	defer l.Unlock()
	fileLock, err := acquireFileLock(context.Background(), filepath.Join(s.dir, id+".account.lock"))
	if err != nil {
		return err
	}
	defer fileLock.Close()
	if _, err := os.Stat(s.metadataPath(id)); err != nil {
		return err
	}
	if c.SavedAt.IsZero() {
		c.SavedAt = time.Now().UTC()
	}
	b, err := json.Marshal(c)
	if err != nil {
		return errors.New("accounts: encode credentials")
	}
	return atomicPrivateWrite(s.secretPath(id), b, 0600)
}

// Refresh serializes refresh/rotation work for one account. The callback must
// return the complete replacement credentials, including any rotated token.
func (s *Store) Refresh(id string, fn func(Credentials) (Credentials, error)) error {
	return s.RefreshContext(context.Background(), id, fn)
}

// RefreshContext is Refresh with cancellation while waiting for a process
// holding the account's rotation lock.
func (s *Store) RefreshContext(ctx context.Context, id string, fn func(Credentials) (Credentials, error)) error {
	if fn == nil {
		return errors.New("accounts: refresh callback is nil")
	}
	if err := validateID(id); err != nil {
		return err
	}
	l := s.accountLock(id)
	l.Lock()
	defer l.Unlock()
	accountFileLock, err := acquireFileLock(ctx, filepath.Join(s.dir, id+".account.lock"))
	if err != nil {
		return err
	}
	defer accountFileLock.Close()
	fl, err := acquireFileLock(ctx, filepath.Join(s.dir, id+".refresh.lock"))
	if err != nil {
		return err
	}
	defer fl.Close()
	b, err := readPrivate(s.secretPath(id), 1<<20)
	if err != nil {
		return err
	}
	var old Credentials
	if err := json.Unmarshal(b, &old); err != nil {
		return errors.New("accounts: decode credentials")
	}
	next, err := fn(old)
	if err != nil {
		return err
	}
	if next.SavedAt.IsZero() {
		next.SavedAt = time.Now().UTC()
	}
	out, err := json.Marshal(next)
	if err != nil {
		return errors.New("accounts: encode credentials")
	}
	return atomicPrivateWrite(s.secretPath(id), out, 0600)
}

// RefreshBound rechecks the immutable account identity while holding the
// account lock, then rotates credentials. It prevents a refresh callback from
// replacing credentials after a concurrent reauthentication changed identity.
func (s *Store) RefreshBound(ctx context.Context, expected Account, fn func(Credentials) (Credentials, error)) error {
	if err := validateID(expected.ID); err != nil {
		return err
	}
	if fn == nil {
		return errors.New("accounts: refresh callback is nil")
	}
	l := s.accountLock(expected.ID)
	l.Lock()
	defer l.Unlock()
	accountFileLock, err := acquireFileLock(ctx, filepath.Join(s.dir, expected.ID+".account.lock"))
	if err != nil {
		return err
	}
	defer accountFileLock.Close()
	actual, err := s.Get(expected.ID)
	if err != nil {
		return err
	}
	if !sameIdentity(actual, expected) {
		return ErrIdentityMismatch
	}
	fl, err := acquireFileLock(ctx, filepath.Join(s.dir, expected.ID+".refresh.lock"))
	if err != nil {
		return err
	}
	defer fl.Close()
	b, err := readPrivate(s.secretPath(expected.ID), 1<<20)
	if err != nil {
		return err
	}
	var old Credentials
	if err := json.Unmarshal(b, &old); err != nil {
		return errors.New("accounts: decode credentials")
	}
	next, err := fn(old)
	if err != nil {
		return err
	}
	if next.SavedAt.IsZero() {
		next.SavedAt = time.Now().UTC()
	}
	out, err := json.Marshal(next)
	if err != nil {
		return errors.New("accounts: encode credentials")
	}
	return atomicPrivateWrite(s.secretPath(expected.ID), out, 0600)
}

func sameIdentity(a, b Account) bool {
	return a.ID == b.ID && a.Kind == b.Kind && a.Name == b.Name && a.Generation == b.Generation && a.Subject == b.Subject && a.Issuer == b.Issuer && a.ClientID == b.ClientID
}

// ClearCredentials revokes no remote session; it retains metadata and client
// registration so a later login can reuse the issued client ID.
func (s *Store) ClearCredentials(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	l := s.accountLock(id)
	l.Lock()
	defer l.Unlock()
	fileLock, err := acquireFileLock(context.Background(), filepath.Join(s.dir, id+".account.lock"))
	if err != nil {
		return err
	}
	defer fileLock.Close()
	b, err := json.Marshal(Credentials{SavedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	return atomicPrivateWrite(s.secretPath(id), b, 0600)
}

func acquireFileLock(ctx context.Context, path string) (*filelock.Lock, error) {
	for {
		l, err := filelock.Acquire(path)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, filelock.ErrLocked) {
			return nil, err
		}
		t := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func (s *Store) Remove(id string) error {
	if err := validateID(id); err != nil {
		return err
	}
	l := s.accountLock(id)
	l.Lock()
	defer l.Unlock()
	fileLock, err := acquireFileLock(context.Background(), filepath.Join(s.dir, id+".account.lock"))
	if err != nil {
		return err
	}
	defer fileLock.Close()
	if err := os.Remove(s.metadataPath(id)); err != nil {
		return err
	}
	if err := os.Remove(s.secretPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// HostID returns a stable per-store host identifier, creating it atomically on
// first use. It is suitable for ext_agent_host_id in Sign in with ChatGPT.
func (s *Store) HostID() (string, error) {
	p := filepath.Join(s.dir, "host-id")
	lock, err := acquireFileLock(context.Background(), p+".lock")
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if b, err := readPrivate(p, 256); err == nil {
		id := strings.TrimSpace(string(b))
		if id != "" {
			return id, nil
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	// RFC 4122 version 4, variant 1 UUID formatting keeps this host ID
	// recognizable while remaining opaque and stable.
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	id := fmt.Sprintf("urn:uuid:%s-%s-%s-%s-%s", hex.EncodeToString(b[:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:]))
	if err := atomicPrivateWrite(p, []byte(id+"\n"), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:])), nil
}

func atomicPrivateWrite(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".account-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func readPrivate(path string, limit int64) ([]byte, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("accounts: refusing symlinked account file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("accounts: account file exceeds size limit")
	}
	return b, nil
}

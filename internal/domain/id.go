package domain

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Kind string

const (
	KindProject  Kind = "prj"
	KindRepo     Kind = "repo"
	KindTask     Kind = "task"
	KindSession  Kind = "ses"
	KindTurn     Kind = "turn"
	KindCall     Kind = "call"
	KindOp       Kind = "op"
	KindEnv      Kind = "env"
	KindProcess  Kind = "proc"
	KindArtifact Kind = "artifact"
	KindChange   Kind = "change"
	KindApproval Kind = "approval"
	KindEvent    Kind = "evt"
)

var (
	ErrInvalidID = errors.New("invalid domain id")
	validID      = regexp.MustCompile(`^[a-z][a-z0-9]*_[0-9a-f]{32}$`)
)

// ID is an opaque, filename-safe identifier. Its prefix identifies the entity kind.
type ID string

func NewID(kind Kind) (ID, error) {
	if !validKind(kind) {
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidID, kind)
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate %s id: %w", kind, err)
	}
	return ID(string(kind) + "_" + hex.EncodeToString(b)), nil
}

func ParseID(value string) (ID, error) {
	if !validID.MatchString(value) {
		return "", fmt.Errorf("%w: %q", ErrInvalidID, value)
	}
	prefix := value[:strings.IndexByte(value, '_')]
	if !validKind(Kind(prefix)) {
		return "", fmt.Errorf("%w: unknown kind %q", ErrInvalidID, prefix)
	}
	return ID(value), nil
}

func (id ID) String() string { return string(id) }

func (id ID) Validate() error {
	_, err := ParseID(string(id))
	return err
}

func (id ID) Kind() (Kind, error) {
	if err := id.Validate(); err != nil {
		return "", err
	}
	return Kind(string(id)[:strings.IndexByte(string(id), '_')]), nil
}

func validKind(kind Kind) bool {
	switch kind {
	case KindProject, KindRepo, KindTask, KindSession, KindTurn, KindCall, KindOp,
		KindEnv, KindProcess, KindArtifact, KindChange, KindApproval, KindEvent:
		return true
	default:
		return false
	}
}

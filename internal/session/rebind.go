package session

import (
	"fmt"
	"time"

	"bifrost/internal/domain"
	"bifrost/internal/provider"
)

// Rebind is an explicit account/model switch. Unlike Save it changes the
// execution binding while retaining the conversation ID and readable history.
// Callers must hold the session turn lease so no turn executes during a switch.
func (s *Store) Rebind(id domain.ID, fingerprint string, expectedRevision uint64) (Session, error) {
	if err := validateID(id); err != nil {
		return Session{}, err
	}
	if err := validateFingerprint(fingerprint); err != nil {
		return Session{}, err
	}
	var saved Session
	err := s.withLock(id, func() error {
		current, err := s.read(id)
		if err != nil {
			return err
		}
		if current.Revision != expectedRevision {
			return fmt.Errorf("%w: conversation changed during account switch", ErrRevisionConflict)
		}
		if current.Revision == 1 && len(current.Messages) == 0 {
			current.Status = StatusCompleted
		}
		current.ProviderFingerprint = fingerprint
		current.Messages = provider.PortableHistory(current.Messages)
		current.Revision++
		current.UpdatedAt = time.Now().UTC()
		if err := validateSession(current); err != nil {
			return err
		}
		if err := atomicWrite(s.path(id), current); err != nil {
			return err
		}
		saved = cloneSession(current)
		return nil
	})
	return saved, err
}

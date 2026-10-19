package session

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"bifrost/internal/domain"
	"bifrost/internal/provider"
)

// Export uses the versioned persistence schema, including opaque provider data.
func (s *Store) Export(id domain.ID, out io.Writer) error {
	current, err := s.Load(id)
	if err != nil {
		return err
	}
	data, err := encode(current)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	return err
}

// Import validates an entire snapshot before creating a fresh local identity.
// Import never executes embedded tool calls or promotes an interrupted turn.
func (s *Store) Import(in io.Reader, root string) (Session, error) {
	data, err := io.ReadAll(io.LimitReader(in, MaxSnapshotBytes+1))
	if err != nil {
		return Session{}, err
	}
	source, err := decode(data)
	if err != nil {
		return Session{}, err
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		return Session{}, err
	}
	if source.Root != canonical {
		return Session{}, errors.New("import root differs from selected workspace")
	}
	created, err := s.Create(canonical, source.ProviderFingerprint)
	if err != nil {
		return Session{}, err
	}
	created.Messages = cloneSession(source).Messages
	created.Status = source.Status
	return s.Save(created, created.Revision)
}

// Compact forks a conversation using a caller-reviewed summary. The source
// remains intact; retained turns never split an assistant/tool result batch.
func (s *Store) Compact(id domain.ID, summary string, keepTurns int) (Session, error) {
	source, err := s.Load(id)
	if err != nil {
		return Session{}, err
	}
	messages, err := CompactedMessages(source, summary, keepTurns)
	if err != nil {
		return Session{}, err
	}
	created, err := s.Create(source.Root, source.ProviderFingerprint)
	if err != nil {
		return Session{}, err
	}
	created.Messages = messages
	created.Status = StatusCompleted
	return s.Save(created, created.Revision)
}

// CompactedMessages previews a summary and whole retained turns without creating a session.
// Opaque continuations and executable tool calls become portable transcript text.
func CompactedMessages(source Session, summary string, keepTurns int) ([]provider.Message, error) {
	if strings.TrimSpace(summary) == "" || len(summary) > 256<<10 {
		return nil, errors.New("summary must contain 1..262144 bytes")
	}
	if keepTurns < 0 || keepTurns > 100 {
		return nil, errors.New("keep turns must be 0..100")
	}
	if source.Status != StatusCompleted {
		return nil, errors.New("only complete sessions can be compacted")
	}
	cut := len(source.Messages)
	seen := 0
	for i := len(source.Messages) - 1; i >= 0; i-- {
		m := source.Messages[i]
		// Agent tool observations use this marker and belong to the preceding
		// turn. A real user prompt may also carry images, so image presence alone
		// must not make it disappear from compaction's turn boundary accounting.
		if m.Role == provider.RoleUser && !isToolImageObservation(m) {
			seen++
			if seen <= keepTurns {
				cut = i
			} else {
				break
			}
		}
	}
	messages := []provider.Message{{Role: provider.RoleUser, Content: fmt.Sprintf("Summary of earlier conversation (source %s):\n%s", source.ID, summary)}}
	if cut < len(source.Messages) {
		messages = append(messages, provider.PortableHistory(cloneSession(source).Messages[cut:])...)
	}
	if err := validateMessages(messages); err != nil {
		return nil, err
	}

	return messages, nil
}

func isToolImageObservation(m provider.Message) bool {
	return m.Role == provider.RoleUser && len(m.Images) > 0 && strings.TrimSpace(m.Content) == "Visual observations from the preceding tools."
}

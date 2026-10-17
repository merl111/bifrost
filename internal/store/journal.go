package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bifrost/internal/domain"
)

const journalSchemaVersion = 1

type Event struct {
	SchemaVersion int             `json:"schema_version"`
	Sequence      uint64          `json:"sequence"`
	ID            domain.ID       `json:"event_id"`
	Type          string          `json:"type"`
	ProjectID     domain.ID       `json:"project_id,omitempty"`
	Timestamp     time.Time       `json:"timestamp"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type Journal struct {
	mu              sync.Mutex
	path            string
	file            *os.File
	lock            *fileLock
	next            uint64
	closed          bool
	needsSeparator  bool
	unhealthy       bool
	unsafeAppendErr error
}

func OpenJournal(path string) (*Journal, error) {
	if path == "" {
		return nil, errors.New("journal path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	lock, err := acquireLock(path + ".lock")
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("open journal %q: %w", path, err)
	}
	j := &Journal{path: path, file: f, lock: lock}
	if err := j.recover(); err != nil {
		_ = f.Close()
		_ = lock.Close()
		return nil, err
	}
	return j, nil
}

func (j *Journal) Append(event Event) (Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return Event{}, errors.New("journal is closed")
	}
	if j.unhealthy {
		return Event{}, fmt.Errorf("journal is unhealthy; reopen required: %w", j.unsafeAppendErr)
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = journalSchemaVersion
	}
	if event.SchemaVersion != journalSchemaVersion {
		return Event{}, fmt.Errorf("%w: journal event schema %d", ErrUnsupportedSchema, event.SchemaVersion)
	}
	if event.Type == "" {
		return Event{}, errors.New("event type is empty")
	}
	if event.ID == "" {
		id, err := domain.NewID(domain.KindEvent)
		if err != nil {
			return Event{}, err
		}
		event.ID = id
	}
	if err := event.ID.Validate(); err != nil {
		return Event{}, fmt.Errorf("event id: %w", err)
	}
	if kind, _ := event.ID.Kind(); kind != domain.KindEvent {
		return Event{}, fmt.Errorf("event id has kind %s", kind)
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	} else {
		event.Timestamp = event.Timestamp.UTC()
	}
	event.Sequence = j.next + 1
	line, err := json.Marshal(event)
	if err != nil {
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	line = append(line, '\n')
	if j.needsSeparator {
		if n, err := j.file.Write([]byte{'\n'}); err != nil || n != 1 {
			if err == nil {
				err = io.ErrShortWrite
			}
			return Event{}, j.markUnhealthy(fmt.Errorf("append journal separator: %w", err))
		}
	}
	if n, err := j.file.Write(line); err != nil || n != len(line) {
		if err == nil {
			err = io.ErrShortWrite
		}
		return Event{}, j.markUnhealthy(fmt.Errorf("append event: %w", err))
	}
	if err := j.file.Sync(); err != nil {
		return Event{}, j.markUnhealthy(fmt.Errorf("sync event journal: %w", err))
	}
	j.next = event.Sequence
	j.needsSeparator = false
	return event, nil
}

func (j *Journal) Replay() ([]Event, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, errors.New("journal is closed")
	}
	return j.replayLocked()
}

func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	fileErr := j.file.Close()
	lockErr := j.lock.Close()
	if fileErr != nil {
		return fileErr
	}
	return lockErr
}

func (j *Journal) recover() error {
	events, err := j.replayLocked()
	if err != nil {
		return err
	}
	if len(events) > 0 {
		j.next = events[len(events)-1].Sequence
	}
	return nil
}

func (j *Journal) markUnhealthy(err error) error {
	j.unhealthy = true
	j.unsafeAppendErr = err
	return err
}

func (j *Journal) replayLocked() ([]Event, error) {
	j.needsSeparator = false
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("seek journal: %w", err)
	}
	reader := bufio.NewReader(j.file)
	var events []Event
	var offset int64
	var expected uint64 = 1
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		hasNewline := len(line) > 0 && line[len(line)-1] == '\n'
		data := line
		if hasNewline {
			data = data[:len(data)-1]
		}
		var event Event
		decodeErr := json.Unmarshal(data, &event)
		if decodeErr != nil {
			if !hasNewline && errors.Is(err, io.EOF) && isTruncatedJSON(decodeErr) {
				if truncErr := j.file.Truncate(offset); truncErr != nil {
					return nil, fmt.Errorf("truncate incomplete final journal record: %w", truncErr)
				}
				if syncErr := j.file.Sync(); syncErr != nil {
					return nil, fmt.Errorf("sync recovered journal: %w", syncErr)
				}
				break
			}
			return nil, fmt.Errorf("journal corruption at byte %d: %w", offset, decodeErr)
		}
		if event.SchemaVersion != journalSchemaVersion {
			return nil, fmt.Errorf("journal record %d: %w: schema %d", expected, ErrUnsupportedSchema, event.SchemaVersion)
		}
		if event.Sequence != expected {
			return nil, fmt.Errorf("journal sequence at byte %d: got %d, want %d", offset, event.Sequence, expected)
		}
		if err := event.ID.Validate(); err != nil {
			return nil, fmt.Errorf("journal record %d event id: %w", expected, err)
		}
		if kind, _ := event.ID.Kind(); kind != domain.KindEvent {
			return nil, fmt.Errorf("journal record %d event id kind %s", expected, kind)
		}
		events = append(events, event)
		expected++
		offset += int64(len(line))
		if errors.Is(err, io.EOF) {
			if !hasNewline {
				j.needsSeparator = true
			}
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read journal: %w", err)
		}
	}
	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return nil, fmt.Errorf("seek journal end: %w", err)
	}
	return events, nil
}

func isTruncatedJSON(err error) bool {
	return strings.Contains(err.Error(), "unexpected end of JSON input") || strings.Contains(err.Error(), "unexpected EOF")
}

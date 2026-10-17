package tasks

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bifrost/internal/atomicfile"
	"bifrost/internal/domain"
	"bifrost/internal/store"
)

// Monitor is a read-only view of a scheduler state directory. It does not
// acquire the scheduler lock or reinterpret active tasks as interrupted, so
// it is safe to use while another process is running the scheduler.
type Monitor struct {
	snapshotDir string
	journalPath string
	runs        []*Monitor
	empty       bool
}

type cancelRequest struct {
	Version int       `json:"version"`
	ID      domain.ID `json:"id"`
}

const (
	maxMonitorLine     = 1 << 20
	maxAggregateEvents = 2048
	maxMonitorPageSize = 1000
)

func OpenMonitor(stateDir string) (*Monitor, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("tasks: monitor state directory is required")
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if info, err := os.Stat(filepath.Join(abs, "snapshots")); err == nil && info.IsDir() {
		return &Monitor{snapshotDir: filepath.Join(abs, "snapshots"), journalPath: filepath.Join(abs, "events.jsonl")}, nil
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &Monitor{empty: true}, nil
		}
		return nil, fmt.Errorf("tasks: open monitor: %w", err)
	}
	var runs []*Monitor
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		run, runErr := OpenMonitor(filepath.Join(abs, entry.Name()))
		if runErr == nil {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 {
		return &Monitor{empty: true}, nil
	}
	return &Monitor{runs: runs}, nil
}

func (m *Monitor) List() ([]Summary, error) {
	tasks, err := m.readTasks()
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(tasks))
	for _, task := range tasks {
		out = append(out, summary(task))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Monitor) Status(id domain.ID) (Summary, error) {
	tasks, err := m.readTasks()
	if err != nil {
		return Summary{}, err
	}
	task, ok := tasks[id]
	if !ok {
		return Summary{}, ErrNotFound
	}
	return summary(task), nil
}

// Cancel writes a bounded, atomic cancellation request for a queued or active
// task. A scheduler in another process consumes the request; completed tasks
// are rejected so an old request cannot affect future work.
func (m *Monitor) Cancel(id domain.ID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	run, err := m.findRun(id)
	if err != nil {
		return err
	}
	tasks, err := run.readTasks()
	if err != nil {
		return err
	}
	task, ok := tasks[id]
	if !ok {
		return ErrNotFound
	}
	if task.Status != StatusQueued && task.Status != StatusActive {
		return fmt.Errorf("tasks: task is no longer running")
	}
	stateDir := filepath.Dir(run.snapshotDir)
	cancelDir := filepath.Join(stateDir, "cancel")
	if err := os.MkdirAll(cancelDir, 0o700); err != nil {
		return fmt.Errorf("tasks: create cancellation directory: %w", err)
	}
	b, err := json.Marshal(cancelRequest{Version: 1, ID: id})
	if err != nil {
		return err
	}
	if err := atomicfile.Write(filepath.Join(cancelDir, string(id)+".json"), append(b, '\n')); err != nil {
		return fmt.Errorf("tasks: write cancellation request: %w", err)
	}
	return nil
}

func (m *Monitor) findRun(id domain.ID) (*Monitor, error) {
	if len(m.runs) > 0 {
		for _, run := range m.runs {
			if found, err := run.findRun(id); err == nil {
				return found, nil
			}
		}
		return nil, ErrNotFound
	}
	tasks, err := m.readTasks()
	if err != nil {
		return nil, err
	}
	if _, ok := tasks[id]; !ok {
		return nil, ErrNotFound
	}
	return m, nil
}

func (m *Monitor) Tree(root domain.ID) ([]Summary, error) {
	tasks, err := m.readTasks()
	if err != nil {
		return nil, err
	}
	if _, ok := tasks[root]; !ok {
		return nil, ErrNotFound
	}
	var out []Summary
	var walk func(domain.ID)
	walk = func(id domain.ID) {
		if task, ok := tasks[id]; ok {
			out = append(out, summary(task))
			for _, child := range task.Children {
				walk(child)
			}
		}
	}
	walk(root)
	return out, nil
}

func (m *Monitor) Events(after uint64, limit int) ([]Event, uint64, error) {
	if limit < 0 {
		return nil, after, errors.New("tasks: event limit must be non-negative")
	}
	if limit == 0 {
		limit = 100
	}
	if limit > maxMonitorPageSize {
		limit = maxMonitorPageSize
	}
	if m.empty {
		if after != 0 {
			return nil, after, errors.New("tasks: aggregate event cursors are unsupported; select a run")
		}
		return nil, 0, nil
	}
	if len(m.runs) > 0 {
		if after != 0 {
			return nil, after, errors.New("tasks: aggregate event cursors are unsupported; select a run")
		}
		var combined []Event
		for _, run := range m.runs {
			var cursor uint64
			for {
				events, next, err := run.Events(cursor, maxMonitorPageSize)
				if err != nil {
					return nil, 0, err
				}
				combined = append(combined, events...)
				if len(combined) > maxAggregateEvents {
					sort.SliceStable(combined, func(i, j int) bool { return eventBefore(combined[i], combined[j]) })
					combined = combined[len(combined)-maxAggregateEvents:]
				}
				if len(events) == 0 || next <= cursor {
					break
				}
				cursor = next
			}
		}
		sort.SliceStable(combined, func(i, j int) bool { return eventBefore(combined[i], combined[j]) })
		if len(combined) > limit {
			combined = combined[len(combined)-limit:]
		}
		return combined, 0, nil
	}
	f, err := os.Open(m.journalPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, after, nil
		}
		return nil, after, err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 128<<10)
	var out []Event
	cursor := after
	var expected uint64 = 1
	for {
		line, complete, readErr := readMonitorLine(reader)
		if len(line) == 0 && readErr == nil {
			break
		}
		data := line
		if complete {
			data = data[:len(data)-1]
		}
		var event store.Event
		if err := json.Unmarshal(data, &event); err != nil {
			if !complete && errors.Is(readErr, io.EOF) {
				break // writer may be between appending and its final newline
			}
			return nil, after, fmt.Errorf("tasks: monitor journal corruption: %w", err)
		}
		if event.Sequence != expected {
			return nil, after, fmt.Errorf("tasks: monitor journal sequence: got %d, want %d", event.Sequence, expected)
		}
		expected++
		if event.Sequence > after {
			var decoded Event
			if err := json.Unmarshal(event.Payload, &decoded); err == nil {
				decoded.Sequence, decoded.ID, decoded.Time = event.Sequence, event.ID, event.Timestamp
				out = append(out, decoded)
				cursor = event.Sequence
				if len(out) >= limit {
					break
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, after, readErr
		}
	}
	return out, cursor, nil
}

func readMonitorLine(reader *bufio.Reader) (line []byte, complete bool, err error) {
	for {
		chunk, readErr := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMonitorLine {
			return nil, false, errors.New("tasks: monitor journal line exceeds 1 MiB")
		}
		if readErr == nil {
			return line, true, nil
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			return line, false, io.EOF
		}
		return nil, false, readErr
	}
}

func eventBefore(a, b Event) bool {
	if !a.Time.Equal(b.Time) {
		return a.Time.Before(b.Time)
	}
	if a.TaskID != b.TaskID {
		return a.TaskID < b.TaskID
	}
	return a.Sequence < b.Sequence
}

func (m *Monitor) readTasks() (map[domain.ID]Task, error) {
	if m.empty {
		return map[domain.ID]Task{}, nil
	}
	if len(m.runs) > 0 {
		out := make(map[domain.ID]Task)
		for _, run := range m.runs {
			tasks, err := run.readTasks()
			if err != nil {
				return nil, err
			}
			for id, task := range tasks {
				out[id] = task
			}
		}
		return out, nil
	}
	entries, err := os.ReadDir(m.snapshotDir)
	if err != nil {
		return nil, err
	}
	out := make(map[domain.ID]Task)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, err := readBoundedFile(filepath.Join(m.snapshotDir, entry.Name()), maxSnapshotSize)
		if err != nil {
			return nil, err
		}
		var snap snapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			return nil, fmt.Errorf("tasks: decode monitor snapshot: %w", err)
		}
		if snap.Version != snapshotVersion {
			return nil, fmt.Errorf("tasks: unsupported monitor snapshot version %d", snap.Version)
		}
		if err := snap.Task.ID.Validate(); err != nil {
			return nil, err
		}
		out[snap.Task.ID] = snap.Task
	}
	return out, nil
}

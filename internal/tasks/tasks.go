// Package tasks schedules bounded caller-supplied work and records its
// lifecycle durably. It deliberately knows nothing about providers or the
// agent package; callers decide how a task executes.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bifrost/internal/atomicfile"
	"bifrost/internal/domain"
	"bifrost/internal/filelock"
	"bifrost/internal/gitops"
	"bifrost/internal/process"
	"bifrost/internal/store"
)

const (
	snapshotVersion = 1
	maxEventText    = 64 << 10
	maxTaskOutput   = 8 << 20
	maxSnapshotSize = 16 << 20
)

type Mode string

const (
	ModeWrite    Mode = "write"
	ModeReadOnly Mode = "readonly"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusActive      Status = "active"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
	StatusInterrupted Status = "interrupted"
)

var (
	ErrClosed    = errors.New("tasks: scheduler is closed")
	ErrNotFound  = errors.New("tasks: task not found")
	ErrQueueFull = errors.New("tasks: queue is full")
	ErrDepth     = errors.New("tasks: maximum task depth exceeded")
	ErrChildren  = errors.New("tasks: maximum child count exceeded")
	ErrBudget    = errors.New("tasks: task result exceeds configured budget")
)

type TokenUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type Result struct {
	Output    string     `json:"output,omitempty"`
	ToolCalls int        `json:"tool_calls"`
	Tokens    TokenUsage `json:"tokens"`
	Steps     int        `json:"steps"`
}

type TaskSpec struct {
	TaskID           domain.ID     `json:"task_id,omitempty"`
	ParentID         domain.ID     `json:"parent_id,omitempty"`
	Root             string        `json:"root"`
	Prompt           string        `json:"prompt"`
	Model            string        `json:"model,omitempty"`
	Profile          string        `json:"profile,omitempty"`
	Mode             Mode          `json:"mode,omitempty"`
	BaseRef          string        `json:"base_ref,omitempty"`
	WorktreePath     string        `json:"worktree_path,omitempty"`
	PreserveWorktree bool          `json:"preserve_worktree,omitempty"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	StepBudget       int           `json:"step_budget,omitempty"`
	MaxOutputBytes   int           `json:"max_output_bytes,omitempty"`
}

type Task struct {
	ID               domain.ID     `json:"id"`
	ParentID         domain.ID     `json:"parent_id,omitempty"`
	Root             string        `json:"root"`
	RepositoryRoot   string        `json:"repository_root,omitempty"`
	Prompt           string        `json:"prompt"`
	Model            string        `json:"model,omitempty"`
	Profile          string        `json:"profile,omitempty"`
	Mode             Mode          `json:"mode"`
	WorktreeRoot     string        `json:"worktree_root,omitempty"`
	PreserveWorktree bool          `json:"preserve_worktree"`
	Timeout          time.Duration `json:"timeout,omitempty"`
	StepBudget       int           `json:"step_budget,omitempty"`
	MaxOutputBytes   int           `json:"max_output_bytes,omitempty"`
	Status           Status        `json:"status"`
	Error            string        `json:"error,omitempty"`
	Result           Result        `json:"result"`
	Children         []domain.ID   `json:"children,omitempty"`
	CreatedAt        time.Time     `json:"created_at"`
	StartedAt        time.Time     `json:"started_at,omitempty"`
	UpdatedAt        time.Time     `json:"updated_at"`
	FinishedAt       time.Time     `json:"finished_at,omitempty"`
	Revision         uint64        `json:"revision"`
}

type Summary struct {
	Task
	Elapsed time.Duration `json:"elapsed"`
}

type Event struct {
	Sequence uint64      `json:"sequence"`
	ID       domain.ID   `json:"id"`
	TaskID   domain.ID   `json:"task_id"`
	ParentID domain.ID   `json:"parent_id,omitempty"`
	Type     string      `json:"type"`
	Message  string      `json:"message,omitempty"`
	Usage    *TokenUsage `json:"usage,omitempty"`
	Step     int         `json:"step,omitempty"`
	Time     time.Time   `json:"time"`
}

type Runner func(context.Context, TaskSpec, func(Event)) (Result, error)

type Options struct {
	MaxConcurrency    int
	MaxQueue          int
	MaxDepth          int
	MaxChildren       int
	MaxOutputBytes    int
	MaxEventsPerTask  int
	PreserveWorktrees bool
}

type Scheduler struct {
	mu          sync.Mutex
	wg          sync.WaitGroup
	cond        *sync.Cond
	stateDir    string
	snapshotDir string
	worktreeDir string
	runner      Runner
	opts        Options
	lock        *filelock.Lock
	journal     *store.Journal
	tasks       map[domain.ID]*Task
	queue       []domain.ID
	running     int
	slotHeld    map[domain.ID]bool
	yielded     map[domain.ID]bool
	runCancel   map[domain.ID]context.CancelFunc
	contexts    map[domain.ID]context.Context
	done        map[domain.ID]chan struct{}
	events      map[domain.ID]int
	cancelStop  chan struct{}
	cancelDone  chan struct{}
	closed      bool
}

type snapshot struct {
	Version int  `json:"version"`
	Task    Task `json:"task"`
}

func New(stateDir string, runner Runner, opts Options) (*Scheduler, error) {
	if strings.TrimSpace(stateDir) == "" || runner == nil {
		return nil, errors.New("tasks: state directory and runner are required")
	}
	if opts.MaxConcurrency <= 0 {
		opts.MaxConcurrency = 1
	}
	if opts.MaxConcurrency > 32 {
		return nil, errors.New("tasks: max concurrency must be <= 32")
	}
	if opts.MaxQueue <= 0 {
		opts.MaxQueue = 64
	}
	if opts.MaxDepth <= 0 {
		opts.MaxDepth = 4
	}
	if opts.MaxChildren <= 0 {
		opts.MaxChildren = 8
	}
	if opts.MaxOutputBytes <= 0 {
		opts.MaxOutputBytes = 1 << 20
	}
	if opts.MaxOutputBytes > maxTaskOutput {
		return nil, errors.New("tasks: max output must be <= 8 MiB")
	}
	if opts.MaxEventsPerTask <= 0 {
		opts.MaxEventsPerTask = 10000
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Join(abs, "snapshots", "worktrees"), 0o700); err != nil {
		return nil, fmt.Errorf("tasks: create state: %w", err)
	}
	lock, err := filelock.Acquire(filepath.Join(abs, "scheduler.lock"))
	if err != nil {
		return nil, err
	}
	journal, err := store.OpenJournal(filepath.Join(abs, "events.jsonl"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	s := &Scheduler{stateDir: abs, snapshotDir: filepath.Join(abs, "snapshots"), worktreeDir: filepath.Join(abs, "snapshots", "worktrees"), runner: runner, opts: opts, lock: lock, journal: journal, tasks: make(map[domain.ID]*Task), runCancel: make(map[domain.ID]context.CancelFunc), contexts: make(map[domain.ID]context.Context), done: make(map[domain.ID]chan struct{}), events: make(map[domain.ID]int), slotHeld: make(map[domain.ID]bool), yielded: make(map[domain.ID]bool), cancelStop: make(chan struct{}), cancelDone: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	if err := s.load(); err != nil {
		_ = journal.Close()
		_ = lock.Close()
		return nil, err
	}
	go s.loop()
	go s.cancelLoop()
	return s, nil
}

// RecoverRuns marks orphaned active/queued turn snapshots interrupted. A live
// scheduler keeps its lock and is skipped without touching its state.
func RecoverRuns(stateDir string) error {
	entries, err := os.ReadDir(stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id, parseErr := domain.ParseID(entry.Name())
		kind, kindErr := id.Kind()
		if parseErr != nil || kindErr != nil || kind != domain.KindTurn {
			continue
		}
		runDir := filepath.Join(stateDir, entry.Name())
		lock, lockErr := filelock.Acquire(filepath.Join(runDir, "scheduler.lock"))
		if errors.Is(lockErr, filelock.ErrLocked) {
			continue
		}
		if lockErr != nil {
			return lockErr
		}
		recoverErr := recoverRun(runDir)
		_ = lock.Close()
		if recoverErr != nil {
			return recoverErr
		}
	}
	return nil
}

func recoverRun(runDir string) error {
	snapshotDir := filepath.Join(runDir, "snapshots")
	entries, err := os.ReadDir(snapshotDir)
	if err != nil {
		return err
	}
	journal, err := store.OpenJournal(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return err
	}
	defer journal.Close()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(snapshotDir, entry.Name())
		b, err := readBoundedFile(path, maxSnapshotSize)
		if err != nil {
			return err
		}
		var snap snapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			return fmt.Errorf("tasks: decode snapshot: %w", err)
		}
		if snap.Version != snapshotVersion {
			return fmt.Errorf("tasks: unsupported snapshot version %d", snap.Version)
		}
		if snap.Task.Status != StatusActive && snap.Task.Status != StatusQueued {
			continue
		}
		snap.Task.Status = StatusInterrupted
		snap.Task.Error = "scheduler restarted while task was active or queued"
		snap.Task.UpdatedAt = time.Now().UTC()
		snap.Task.Revision++
		b, err = json.Marshal(snap)
		if err != nil {
			return err
		}
		if err := atomicfile.Write(path, b); err != nil {
			return err
		}
		payload, _ := json.Marshal(Event{TaskID: snap.Task.ID, ParentID: snap.Task.ParentID, Type: "task.interrupted", Message: snap.Task.Error})
		if _, err := journal.Append(store.Event{Type: "task.interrupted", Payload: payload, Timestamp: time.Now().UTC()}); err != nil {
			return err
		}
	}
	return nil
}

func readBoundedFile(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("tasks: file %s exceeds %d bytes", path, max)
	}
	return b, nil
}

func (s *Scheduler) cancelLoop() {
	defer close(s.cancelDone)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-s.cancelStop:
			return
		case <-ticker.C:
			s.consumeCancelRequests()
		}
	}
}

func (s *Scheduler) consumeCancelRequests() {
	entries, err := os.ReadDir(filepath.Join(s.stateDir, "cancel"))
	if err != nil {
		return
	}
	processed := 0
	for _, entry := range entries {
		if processed >= 128 {
			return
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		processed++
		path := filepath.Join(s.stateDir, "cancel", entry.Name())
		idText := strings.TrimSuffix(entry.Name(), ".json")
		id, parseErr := domain.ParseID(idText)
		kind, kindErr := id.Kind()
		if parseErr != nil || kindErr != nil || kind != domain.KindTask {
			_ = os.Remove(path)
			continue
		}
		b, readErr := os.ReadFile(path)
		var request cancelRequest
		valid := readErr == nil && json.Unmarshal(b, &request) == nil && request.Version == 1 && request.ID == id
		if !valid {
			_ = os.Remove(path)
			continue
		}
		s.mu.Lock()
		if task := s.tasks[id]; task != nil && (task.Status == StatusQueued || task.Status == StatusActive) {
			s.cancelLocked(id)
		}
		s.mu.Unlock()
		_ = os.Remove(path)
	}
}

func (s *Scheduler) load() error {
	entries, err := os.ReadDir(s.snapshotDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		b, readErr := readBoundedFile(filepath.Join(s.snapshotDir, entry.Name()), maxSnapshotSize)
		if readErr != nil {
			return readErr
		}
		var snap snapshot
		if err := json.Unmarshal(b, &snap); err != nil {
			return fmt.Errorf("tasks: decode snapshot: %w", err)
		}
		if snap.Version != snapshotVersion {
			return fmt.Errorf("tasks: unsupported snapshot version %d", snap.Version)
		}
		if err := snap.Task.ID.Validate(); err != nil {
			return err
		}
		t := snap.Task
		if t.Status == StatusActive || t.Status == StatusQueued {
			t.Status = StatusInterrupted
			t.Error = "scheduler restarted while task was active or queued"
			t.UpdatedAt = time.Now().UTC()
			t.Revision++
			if err := s.writeSnapshot(&t); err != nil {
				return err
			}
			if err := s.appendEventLocked(t, "task.interrupted", t.Error, nil, 0); err != nil {
				return err
			}
		}
		s.tasks[t.ID] = &t
		s.done[t.ID] = make(chan struct{})
		if t.Status == StatusCompleted || t.Status == StatusFailed || t.Status == StatusCanceled || t.Status == StatusInterrupted {
			close(s.done[t.ID])
		}
	}
	return nil
}

func (s *Scheduler) SpawnParent(ctx context.Context, spec TaskSpec) (Task, error) {
	spec.ParentID = ""
	return s.spawn(ctx, spec)
}
func (s *Scheduler) SpawnChild(ctx context.Context, parent domain.ID, spec TaskSpec) (Task, error) {
	spec.ParentID = parent
	return s.spawn(ctx, spec)
}

// RegisterParent records an already-running external turn as an active graph
// parent without consuming a scheduler execution slot. The caller must later
// call CompleteParent; children may be spawned while this parent is active.
func (s *Scheduler) RegisterParent(ctx context.Context, spec TaskSpec) (Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Task{}, ErrClosed
	}
	spec.ParentID = ""
	if err := s.validateSpecLocked(spec); err != nil {
		return Task{}, err
	}
	root, err := filepath.Abs(spec.Root)
	if err != nil {
		return Task{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Task{}, err
	}
	id, err := domain.NewID(domain.KindTask)
	if err != nil {
		return Task{}, err
	}
	mode := spec.Mode
	if mode == "" {
		mode = ModeReadOnly
	}
	maxOutput := spec.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = s.opts.MaxOutputBytes
	}
	now := time.Now().UTC()
	task := Task{ID: id, Root: root, RepositoryRoot: root, Prompt: spec.Prompt, Model: spec.Model, Profile: spec.Profile, Mode: mode, PreserveWorktree: spec.PreserveWorktree || s.opts.PreserveWorktrees, Timeout: spec.Timeout, StepBudget: spec.StepBudget, MaxOutputBytes: maxOutput, Status: StatusActive, CreatedAt: now, StartedAt: now, UpdatedAt: now, Revision: 1}
	parentCtx, cancel := context.WithCancel(ctx)
	s.tasks[id] = &task
	s.contexts[id] = parentCtx
	s.runCancel[id] = cancel
	s.done[id] = make(chan struct{})
	if err := s.writeSnapshot(&task); err != nil {
		delete(s.tasks, id)
		delete(s.contexts, id)
		delete(s.runCancel, id)
		delete(s.done, id)
		_ = cancel
		return Task{}, err
	}
	if err := s.appendEventLocked(task, "task.active", "external parent registered", nil, 0); err != nil {
		delete(s.tasks, id)
		delete(s.contexts, id)
		delete(s.runCancel, id)
		delete(s.done, id)
		_ = os.Remove(filepath.Join(s.snapshotDir, string(id)+".json"))
		return Task{}, err
	}
	return task, nil
}

// CompleteParent closes an externally registered parent and records its final
// result. It does not affect scheduler concurrency accounting.
func (s *Scheduler) CompleteParent(id domain.ID, result Result, runErr error) error {
	s.mu.Lock()
	t := s.tasks[id]
	if t == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	if t.Status != StatusActive {
		s.mu.Unlock()
		return errors.New("tasks: parent is not active")
	}
	s.mu.Unlock()
	s.finish(id, result, runErr)
	return nil
}

func (s *Scheduler) spawn(ctx context.Context, spec TaskSpec) (Task, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Task{}, ErrClosed
	}
	if err := s.validateSpecLocked(spec); err != nil {
		return Task{}, err
	}
	if len(s.queue) >= s.opts.MaxQueue {
		return Task{}, ErrQueueFull
	}
	id, err := domain.NewID(domain.KindTask)
	if err != nil {
		return Task{}, err
	}
	mode := spec.Mode
	if mode == "" {
		mode = ModeWrite
	}
	root, err := filepath.Abs(spec.Root)
	if err != nil {
		return Task{}, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return Task{}, err
	}
	if spec.ParentID != "" {
		parent := s.tasks[spec.ParentID]
		allowed := false
		if parent != nil {
			allowed = pathWithin(root, parent.RepositoryRoot) || pathWithin(root, parent.Root)
		}
		if !allowed {
			return Task{}, errors.New("tasks: child root must be within its parent repository")
		}
	}
	maxOutput := spec.MaxOutputBytes
	if maxOutput == 0 {
		maxOutput = s.opts.MaxOutputBytes
	}
	task := Task{ID: id, ParentID: spec.ParentID, Root: root, RepositoryRoot: root, Prompt: spec.Prompt, Model: spec.Model, Profile: spec.Profile, Mode: mode, PreserveWorktree: spec.PreserveWorktree || s.opts.PreserveWorktrees, Timeout: spec.Timeout, StepBudget: spec.StepBudget, MaxOutputBytes: maxOutput, Status: StatusQueued, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Revision: 1}
	if mode == ModeWrite {
		ref := spec.BaseRef
		if ref == "" {
			ref = "HEAD"
		}
		_, err := gitops.Open(root)
		if err != nil {
			return Task{}, fmt.Errorf("tasks: open git root: %w", err)
		}
		path := spec.WorktreePath
		if path == "" {
			path = filepath.Join(s.worktreeDir, string(id))
		}
		if err := ensureUnder(s.worktreeDir, path); err != nil {
			return Task{}, err
		}
		if err := createWorktree(ctx, root, path, ref); err != nil {
			return Task{}, err
		}
		task.WorktreeRoot = path
		task.Root = path
	}
	s.tasks[id] = &task
	taskCtx, taskCancel := context.WithCancel(ctx)
	s.contexts[id] = taskCtx
	// Keep a cancellation function installed while the task is queued. The
	// loop can dequeue a task just before another goroutine calls Cancel; a
	// queued task must still be canceled before execute starts.
	s.runCancel[id] = taskCancel
	s.done[id] = make(chan struct{})
	s.queue = append(s.queue, id)
	if spec.ParentID != "" {
		p := s.tasks[spec.ParentID]
		p.Children = append(p.Children, id)
		p.UpdatedAt = time.Now().UTC()
		p.Revision++
		if err := s.writeSnapshot(p); err != nil {
			return Task{}, err
		}
	}
	if err := s.writeSnapshot(&task); err != nil {
		return Task{}, err
	}
	_ = s.appendEventLocked(task, "task.queued", "", nil, 0)
	s.cond.Signal()
	return task, nil
}

func (s *Scheduler) validateSpecLocked(spec TaskSpec) error {
	if strings.TrimSpace(spec.Root) == "" || strings.TrimSpace(spec.Prompt) == "" {
		return errors.New("tasks: root and prompt are required")
	}
	if spec.Mode != ModeWrite && spec.Mode != ModeReadOnly && spec.Mode != "" {
		return errors.New("tasks: invalid mode")
	}
	if spec.ParentID != "" {
		p, ok := s.tasks[spec.ParentID]
		if !ok {
			return ErrNotFound
		}
		if taskDepth(s.tasks, p.ID) >= s.opts.MaxDepth {
			return ErrDepth
		}
		if len(p.Children) >= s.opts.MaxChildren {
			return ErrChildren
		}
		if p.Status != StatusQueued && p.Status != StatusActive {
			return errors.New("tasks: parent is no longer running")
		}
	}
	if spec.StepBudget < 0 || spec.StepBudget > 100 {
		return errors.New("tasks: invalid step budget")
	}
	if spec.Timeout < 0 || spec.Timeout > 24*time.Hour {
		return errors.New("tasks: invalid timeout")
	}
	if spec.MaxOutputBytes < 0 || spec.MaxOutputBytes > maxTaskOutput {
		return errors.New("tasks: invalid output budget")
	}
	if err := validateOverride(spec.Model, 256, "model"); err != nil {
		return err
	}
	if err := validateOverride(spec.Profile, 128, "profile"); err != nil {
		return err
	}
	return nil
}

func validateOverride(value string, max int, name string) error {
	if len(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("tasks: invalid %s override", name)
	}
	return nil
}

func (s *Scheduler) loop() {
	for {
		s.mu.Lock()
		for !s.closed && (s.running >= s.opts.MaxConcurrency || len(s.queue) == 0) {
			s.cond.Wait()
		}
		if s.closed {
			s.mu.Unlock()
			return
		}
		id := s.queue[0]
		s.queue = s.queue[1:]
		t := s.tasks[id]
		s.running++
		s.slotHeld[id] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.execute(id, t)
		}()
	}
}

func (s *Scheduler) execute(id domain.ID, t *Task) {
	s.mu.Lock()
	if s.closed || t.Status != StatusQueued {
		if t.Status == StatusQueued {
			t.Status = StatusCanceled
			t.Error = "canceled before start"
			t.UpdatedAt = time.Now().UTC()
			t.FinishedAt = t.UpdatedAt
			t.Revision++
			_ = s.writeSnapshot(t)
			_ = s.appendEventLocked(*t, "task.canceled", t.Error, nil, 0)
			closeIfOpen(s.done[id])
		}
		if s.slotHeld[id] {
			s.running--
			delete(s.slotHeld, id)
		}
		delete(s.runCancel, id)
		delete(s.contexts, id)
		s.cond.Signal()
		s.mu.Unlock()
		return
	}
	t.Status = StatusActive
	t.StartedAt = time.Now().UTC()
	t.UpdatedAt = t.StartedAt
	t.Revision++
	_ = s.writeSnapshot(t)
	_ = s.appendEventLocked(*t, "task.active", "", nil, 0)
	task := *t
	parentCtx := s.contexts[id]
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	ctx, cancel := context.WithCancel(parentCtx)
	queuedCancel := s.runCancel[id]
	s.runCancel[id] = func() {
		if queuedCancel != nil {
			queuedCancel()
		}
		cancel()
	}
	s.mu.Unlock()
	if task.ParentID != "" {
		go s.watchParent(ctx, task.ParentID, cancel)
	}
	if taskSpec, err := s.specFor(task); err != nil {
		s.finish(id, Result{}, err)
	} else {
		if taskSpec.Timeout > 0 {
			var timeoutCancel context.CancelFunc
			ctx, timeoutCancel = context.WithTimeout(ctx, taskSpec.Timeout)
			defer timeoutCancel()
		}
		result, runErr := s.runner(ctx, taskSpec, func(e Event) {
			if e.TaskID == "" {
				e.TaskID = id
			}
			s.emitTaskEvent(e)
		})
		s.finish(id, result, runErr)
	}
}

func (s *Scheduler) watchParent(ctx context.Context, parent domain.ID, cancel context.CancelFunc) {
	s.mu.Lock()
	done := s.done[parent]
	s.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
	s.mu.Lock()
	p := s.tasks[parent]
	if p != nil && (p.Status == StatusCanceled || p.Status == StatusInterrupted) {
		cancel()
	}
	s.mu.Unlock()
}

func (s *Scheduler) specFor(t Task) (TaskSpec, error) {
	return TaskSpec{TaskID: t.ID, ParentID: t.ParentID, Root: t.Root, Prompt: t.Prompt, Model: t.Model, Profile: t.Profile, Mode: t.Mode, WorktreePath: t.WorktreeRoot, PreserveWorktree: t.PreserveWorktree, Timeout: t.Timeout, StepBudget: t.StepBudget, MaxOutputBytes: t.MaxOutputBytes}, nil
}

func (s *Scheduler) finish(id domain.ID, result Result, runErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.tasks[id]
	if t == nil {
		return
	}
	if result.ToolCalls < 0 || result.ToolCalls > 64 || result.Steps < 0 || (t.StepBudget > 0 && result.Steps > t.StepBudget) || len(result.Output) > t.MaxOutputBytes || result.Tokens.InputTokens < 0 || result.Tokens.OutputTokens < 0 || result.Tokens.TotalTokens < 0 {
		runErr = ErrBudget
	}
	t.Result = result
	t.FinishedAt = time.Now().UTC()
	t.UpdatedAt = t.FinishedAt
	t.Revision++
	if runErr == nil {
		t.Status = StatusCompleted
	} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		t.Status = StatusCanceled
		t.Error = runErr.Error()
	} else {
		t.Status = StatusFailed
		t.Error = runErr.Error()
	}
	_ = s.writeSnapshot(t)
	_ = s.appendEventLocked(*t, "task."+string(t.Status), t.Error, nil, 0)
	if cancel := s.runCancel[id]; cancel != nil {
		cancel()
	}
	delete(s.runCancel, id)
	delete(s.contexts, id)
	if s.slotHeld[id] {
		s.running--
		delete(s.slotHeld, id)
	}
	delete(s.yielded, id)
	closeIfOpen(s.done[id])
	if t.Mode == ModeWrite && !t.PreserveWorktree && t.WorktreeRoot != "" {
		_, _ = removeWorktree(context.Background(), t.RepositoryRoot, t.WorktreeRoot)
	}
	s.cond.Signal()
}

func (s *Scheduler) emitTaskEvent(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.recordEventLocked(e)
}

// RecordEvent records progress for an active task that is running outside the
// scheduler worker pool, such as a CLI parent turn.
func (s *Scheduler) RecordEvent(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return s.recordEventLocked(e)
}

func (s *Scheduler) recordEventLocked(e Event) error {
	if s.closed {
		return ErrClosed
	}
	if e.TaskID == "" {
		return ErrNotFound
	}
	if len(e.Message) > maxEventText {
		e.Message = e.Message[:maxEventText]
	}
	if s.events[e.TaskID] >= s.opts.MaxEventsPerTask {
		return nil
	}
	t := s.tasks[e.TaskID]
	if t == nil {
		return ErrNotFound
	}
	if e.Step > 0 {
		t.Result.Steps = e.Step
	}
	if e.Usage != nil {
		t.Result.Tokens = *e.Usage
	}
	lower := strings.ToLower(e.Type + " " + e.Message)
	if strings.Contains(lower, "tool") && strings.Contains(lower, "started") {
		t.Result.ToolCalls++
	}
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.ParentID == "" {
		e.ParentID = t.ParentID
	}
	if err := s.appendEventLocked(*t, e.Type, e.Message, e.Usage, e.Step); err != nil {
		return err
	}
	s.events[e.TaskID]++
	// Usage can arrive once per response, while a tool completion is a useful
	// durable checkpoint. Avoid rewriting snapshots for every text delta.
	checkpoint := e.Usage != nil || strings.Contains(lower, "tool") && strings.Contains(lower, "finished")
	if checkpoint {
		t.UpdatedAt = time.Now().UTC()
		t.Revision++
		if err := s.writeSnapshot(t); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) appendEventLocked(t Task, typ, message string, usage *TokenUsage, step int) error {
	if !strings.HasPrefix(typ, "task.") {
		typ = "task." + typ
	}
	payload, _ := json.Marshal(Event{TaskID: t.ID, ParentID: t.ParentID, Type: typ, Message: message, Usage: usage, Step: step})
	_, err := s.journal.Append(store.Event{Type: typ, Payload: payload, Timestamp: time.Now().UTC()})
	return err
}

func (s *Scheduler) writeSnapshot(t *Task) error {
	b, err := json.Marshal(snapshot{Version: snapshotVersion, Task: *t})
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(s.snapshotDir, string(t.ID)+".json"), b)
}

func (s *Scheduler) Status(id domain.ID) (Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return Summary{}, ErrNotFound
	}
	t := s.tasks[id]
	return summary(*t), nil
}

// ParentContext returns the cancellation context owned by an externally
// registered parent. It lets the caller run the parent agent under the same
// cancellation tree as its children.
func (s *Scheduler) ParentContext(id domain.ID) (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if _, ok := s.tasks[id]; !ok {
		return nil, ErrNotFound
	}
	ctx := s.contexts[id]
	if ctx == nil {
		return nil, errors.New("tasks: parent context is no longer active")
	}
	return ctx, nil
}

func (s *Scheduler) isDescendantLocked(parent, id domain.ID) bool {
	if parent == "" || id == "" || parent == id {
		return false
	}
	for current := id; current != ""; {
		t := s.tasks[current]
		if t == nil {
			return false
		}
		if t.ParentID == parent {
			return true
		}
		current = t.ParentID
	}
	return false
}

// StatusFor limits a task identity to the subtree rooted at parent. Empty
// parent preserves the scheduler-wide administrative view.
func (s *Scheduler) StatusFor(parent, id domain.ID) (Summary, error) {
	if parent == "" {
		return s.Status(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[parent]; !ok {
		return Summary{}, ErrNotFound
	}
	if !s.isDescendantLocked(parent, id) {
		return Summary{}, errors.New("tasks: task is outside parent subtree")
	}
	return summary(*s.tasks[id]), nil
}
func (s *Scheduler) List() []Summary {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Summary, 0, len(s.tasks))
	for _, t := range s.tasks {
		out = append(out, summary(*t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
func (s *Scheduler) Tree(root domain.ID) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[root]; !ok {
		return nil, ErrNotFound
	}
	var out []Summary
	var walk func(domain.ID)
	walk = func(id domain.ID) {
		if t := s.tasks[id]; t != nil {
			out = append(out, summary(*t))
			for _, child := range t.Children {
				walk(child)
			}
		}
	}
	walk(root)
	return out, nil
}
func (s *Scheduler) Events(after uint64, limit int) ([]Event, uint64, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	raw, err := s.journal.Replay()
	if err != nil {
		return nil, after, err
	}
	out := make([]Event, 0, limit)
	var cursor = after
	for _, e := range raw {
		if e.Sequence <= after {
			continue
		}
		var ev Event
		if json.Unmarshal(e.Payload, &ev) != nil {
			continue
		}
		ev.Sequence = e.Sequence
		ev.ID = e.ID
		ev.Time = e.Timestamp
		out = append(out, ev)
		cursor = e.Sequence
		if len(out) >= limit {
			break
		}
	}
	return out, cursor, nil
}
func (s *Scheduler) Wait(ctx context.Context, id domain.ID) (Summary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	done, ok := s.done[id]
	s.mu.Unlock()
	if !ok {
		return Summary{}, ErrNotFound
	}
	select {
	case <-done:
		return s.Status(id)
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	}
}

// WaitChild temporarily yields the parent's scheduler slot while it waits.
// This prevents a parent at MaxConcurrency from deadlocking on a queued child.
func (s *Scheduler) WaitChild(ctx context.Context, parent, child domain.ID) (Summary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	p := s.tasks[parent]
	c := s.tasks[child]
	if p == nil || c == nil {
		s.mu.Unlock()
		return Summary{}, ErrNotFound
	}
	owned := false
	for _, id := range p.Children {
		if id == child {
			owned = true
			break
		}
	}
	if !owned {
		s.mu.Unlock()
		return Summary{}, errors.New("tasks: parent does not own child")
	}
	if p.Status == StatusActive && s.slotHeld[parent] {
		s.slotHeld[parent] = false
		s.yielded[parent] = true
		s.running--
		s.cond.Signal()
	}
	done := s.done[child]
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if s.yielded[parent] {
			// A child may have acquired the yielded slot before a bounded
			// wait timeout. Do not resume the parent until capacity returns;
			// otherwise running would exceed MaxConcurrency. Cancellation of
			// the parent is the one case where the runner must finish without
			// reacquiring a slot.
			for !s.closed && s.running >= s.opts.MaxConcurrency && !errors.Is(ctx.Err(), context.Canceled) {
				s.cond.Wait()
			}
			delete(s.yielded, parent)
			if !s.closed && s.running < s.opts.MaxConcurrency && !errors.Is(ctx.Err(), context.Canceled) {
				s.slotHeld[parent] = true
				s.running++
				s.cond.Signal()
			}
		}
		s.mu.Unlock()
	}()
	select {
	case <-done:
		return s.Status(child)
	case <-ctx.Done():
		return Summary{}, ctx.Err()
	}
}
func (s *Scheduler) Cancel(id domain.ID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	_, ok := s.tasks[id]
	if !ok {
		return ErrNotFound
	}
	s.cancelLocked(id)
	return nil
}

// CancelFor limits cancellation to descendants of parent. This is used for
// model-facing task tools so a child cannot cancel siblings or its ancestors.
func (s *Scheduler) CancelFor(parent, id domain.ID) error {
	if parent == "" {
		return s.Cancel(id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if _, ok := s.tasks[parent]; !ok {
		return ErrNotFound
	}
	if !s.isDescendantLocked(parent, id) {
		return errors.New("tasks: task is outside parent subtree")
	}
	s.cancelLocked(id)
	return nil
}
func (s *Scheduler) cancelLocked(id domain.ID) {
	t := s.tasks[id]
	if t == nil {
		return
	}
	for _, child := range t.Children {
		s.cancelLocked(child)
	}
	if t.Status == StatusQueued {
		if cancel := s.runCancel[id]; cancel != nil {
			cancel()
		}
		for i, q := range s.queue {
			if q == id {
				s.queue = append(s.queue[:i], s.queue[i+1:]...)
				break
			}
		}
		t.Status = StatusCanceled
		t.Error = "canceled before start"
		t.UpdatedAt = time.Now().UTC()
		t.FinishedAt = t.UpdatedAt
		t.Revision++
		_ = s.writeSnapshot(t)
		_ = s.appendEventLocked(*t, "task.canceled", t.Error, nil, 0)
		closeIfOpen(s.done[id])
		delete(s.runCancel, id)
		delete(s.contexts, id)
		return
	}
	if cancel := s.runCancel[id]; cancel != nil {
		cancel()
	}
}
func (s *Scheduler) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	close(s.cancelStop)
	for _, cancel := range s.runCancel {
		cancel()
	}
	for _, id := range s.queue {
		if t := s.tasks[id]; t != nil && t.Status == StatusQueued {
			t.Status = StatusCanceled
			t.Error = "scheduler closed before start"
			t.UpdatedAt = time.Now().UTC()
			t.FinishedAt = t.UpdatedAt
			t.Revision++
			_ = s.writeSnapshot(t)
			_ = s.appendEventLocked(*t, "task.canceled", t.Error, nil, 0)
			closeIfOpen(s.done[id])
			if cancel := s.runCancel[id]; cancel != nil {
				cancel()
			}
			delete(s.runCancel, id)
			delete(s.contexts, id)
		}
	}
	s.queue = nil
	s.cond.Broadcast()
	s.mu.Unlock()
	<-s.cancelDone
	s.wg.Wait()
	return errors.Join(s.journal.Close(), s.lock.Close())
}

func summary(t Task) Summary {
	elapsed := time.Duration(0)
	end := t.FinishedAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	if !t.StartedAt.IsZero() {
		elapsed = end.Sub(t.StartedAt)
	}
	return Summary{Task: t, Elapsed: elapsed}
}
func taskDepth(tasks map[domain.ID]*Task, id domain.ID) int {
	depth := 0
	for {
		t := tasks[id]
		if t == nil || t.ParentID == "" {
			return depth
		}
		depth++
		id = t.ParentID
	}
}
func ensureUnder(root, path string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// Resolve the nearest existing ancestor so a caller cannot escape through
	// a symlink in the otherwise lexical state/worktrees path.
	ancestor := abs
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			resolvedAncestor, resolveErr := filepath.EvalSymlinks(ancestor)
			if resolveErr != nil {
				return resolveErr
			}
			suffix, relErr := filepath.Rel(ancestor, abs)
			if relErr != nil {
				return relErr
			}
			resolved := filepath.Join(resolvedAncestor, suffix)
			rel, relErr := filepath.Rel(root, resolved)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errors.New("tasks: worktree path must be under state worktrees")
			}
			return nil
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
		ancestor = parent
	}
	// The filesystem root is always an existing ancestor, so this is only a
	// defensive fallback for unusual platforms.
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("tasks: worktree path must be under state worktrees")
	}
	return nil
}

func pathWithin(path, root string) bool {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(root) == "" {
		return false
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gitops deliberately restricts worktree paths to the repository. Scheduler
// worktrees live below its state directory, so these two calls use the same
// bounded argv/process policy while retaining gitops for repository checks
// and status inspection.
func createWorktree(ctx context.Context, repo, path, ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n\t") {
		return errors.New("tasks: invalid base ref")
	}
	result, err := process.Run(ctx, process.Spec{Argv: []string{"git", "-C", repo, "worktree", "add", path, ref}, Dir: repo, MaxOutputBytes: 2 << 20})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("tasks: git worktree add exited with status %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	return nil
}

func removeWorktree(ctx context.Context, repo, path string) (bool, error) {
	wt, err := gitops.Open(path)
	if err != nil {
		return false, err
	}
	status, err := wt.Status(ctx)
	if err != nil {
		return false, err
	}
	if len(status.Entries) > 0 {
		return false, fmt.Errorf("worktree is dirty: %s", path)
	}
	result, err := process.Run(ctx, process.Spec{Argv: []string{"git", "-C", repo, "worktree", "remove", "--", path}, Dir: repo, MaxOutputBytes: 2 << 20})
	if err != nil {
		return false, err
	}
	if result.ExitCode != 0 {
		return false, fmt.Errorf("tasks: git worktree remove exited with status %d: %s", result.ExitCode, strings.TrimSpace(string(result.Stderr)))
	}
	return true, nil
}
func closeIfOpen(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

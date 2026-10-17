package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bifrost/internal/domain"
)

func TestSchedulerParentChildEventsAndRestartRecovery(t *testing.T) {
	root := t.TempDir()
	started := make(chan struct{})
	release := make(chan struct{})
	runner := func(ctx context.Context, spec TaskSpec, emit func(Event)) (Result, error) {
		if spec.Prompt == "parent" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		emit(Event{TaskID: spec.ParentID, Type: "progress", Message: spec.Prompt})
		return Result{Output: spec.Prompt, Steps: 1}, nil
	}
	s, err := New(filepath.Join(root, "state"), runner, Options{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	parent, err := s.SpawnParent(context.Background(), TaskSpec{Root: root, Prompt: "parent", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	child, err := s.SpawnChild(context.Background(), parent.ID, TaskSpec{Root: root, Prompt: "child", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if got, err := s.Wait(context.Background(), parent.ID); err != nil || got.Status != StatusCompleted {
		t.Fatalf("parent wait = %#v, %v", got, err)
	}
	if got, err := s.Wait(context.Background(), child.ID); err != nil || got.Status != StatusCompleted || got.Result.Output != "child" {
		t.Fatalf("child wait = %#v, %v", got, err)
	}
	events, cursor, err := s.Events(0, 100)
	if err != nil || cursor == 0 || len(events) < 4 {
		t.Fatalf("events = %d, cursor %d, err %v", len(events), cursor, err)
	}
	for _, event := range events {
		if len(event.Type) < len("task.") || event.Type[:len("task.")] != "task." {
			t.Fatalf("event type lacks namespace: %q", event.Type)
		}
	}
	if _, _, err := s.Events(cursor, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// A process killed while a snapshot is active has no opportunity to call
	// Close. New marks it interrupted and records that recovery in the journal.
	state := filepath.Join(root, "restart")
	if err := os.MkdirAll(filepath.Join(state, "snapshots", "worktrees"), 0o700); err != nil {
		t.Fatal(err)
	}
	id, err := domain.NewID(domain.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := Task{ID: id, Root: root, RepositoryRoot: root, Prompt: "crashed", Mode: ModeReadOnly, Status: StatusActive, CreatedAt: now, StartedAt: now, UpdatedAt: now, Revision: 2}
	b, _ := json.Marshal(snapshot{Version: snapshotVersion, Task: task})
	if err := os.WriteFile(filepath.Join(state, "snapshots", string(id)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(state, func(context.Context, TaskSpec, func(Event)) (Result, error) {
		return Result{}, errors.New("must not replay")
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := recovered.Status(id)
	if err != nil || got.Status != StatusInterrupted {
		t.Fatalf("recovered status = %#v, %v", got, err)
	}
	if ev, _, err := recovered.Events(0, 100); err != nil || !hasEvent(ev, id, "task.interrupted") {
		t.Fatalf("recovery event missing: %#v, %v", ev, err)
	}
	_ = recovered.Close()
}

func TestSchedulerWorktreeIsolationAndPreservation(t *testing.T) {
	repo := initGitRepo(t)
	state := filepath.Join(t.TempDir(), "state")
	var seen TaskSpec
	s, err := New(state, func(_ context.Context, spec TaskSpec, _ func(Event)) (Result, error) {
		seen = spec
		if spec.Root == repo {
			return Result{}, errors.New("write task was not isolated")
		}
		if err := os.WriteFile(filepath.Join(spec.Root, "task.txt"), []byte("isolated"), 0o600); err != nil {
			return Result{}, err
		}
		return Result{Output: "ok"}, nil
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.SpawnParent(context.Background(), TaskSpec{Root: repo, Prompt: "write", PreserveWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Wait(context.Background(), task.ID)
	if err != nil || got.Status != StatusCompleted {
		t.Fatalf("wait = %#v, %v", got, err)
	}
	if seen.Root != task.WorktreeRoot || seen.Root == repo {
		t.Fatalf("runner root = %q, task worktree = %q, repo = %q", seen.Root, task.WorktreeRoot, repo)
	}
	if _, err := os.Stat(filepath.Join(task.WorktreeRoot, "task.txt")); err != nil {
		t.Fatalf("isolated output missing: %v", err)
	}
	if _, err := os.Stat(repo); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestSchedulerConcurrencyAndCancellation(t *testing.T) {
	var current, maximum atomic.Int32
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	s, err := New(t.TempDir(), func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		n := current.Add(1)
		for {
			old := maximum.Load()
			if n <= old || maximum.CompareAndSwap(old, n) {
				break
			}
		}
		started <- struct{}{}
		defer current.Add(-1)
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, Options{MaxConcurrency: 2, MaxQueue: 8})
	if err != nil {
		t.Fatal(err)
	}
	var ids []domain.ID
	for i := 0; i < 4; i++ {
		task, spawnErr := s.SpawnParent(context.Background(), TaskSpec{Root: t.TempDir(), Prompt: fmt.Sprintf("%d", i), Mode: ModeReadOnly})
		if spawnErr != nil {
			t.Fatal(spawnErr)
		}
		ids = append(ids, task.ID)
	}
	<-started
	<-started
	if maximum.Load() != 2 {
		t.Fatalf("maximum concurrency = %d", maximum.Load())
	}
	if err := s.Cancel(ids[0]); err != nil {
		t.Fatal(err)
	}
	close(release)
	for _, id := range ids {
		if _, err := s.Wait(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Status(ids[0]); got.Status != StatusCanceled {
		t.Fatalf("canceled status = %s", got.Status)
	}
	_ = s.Close()
}

func TestMonitorReadsLiveSchedulerWithoutRestartMutation(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	state := filepath.Join(t.TempDir(), "state")
	s, err := New(state, func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		close(started)
		select {
		case <-release:
			return Result{}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	task, err := s.SpawnParent(context.Background(), TaskSpec{Root: t.TempDir(), Prompt: "live", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	monitor, err := OpenMonitor(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := monitor.Status(task.ID)
	if err != nil || got.Status != StatusActive {
		t.Fatalf("live monitor status = %#v, %v", got, err)
	}
	close(release)
	if _, err := s.Wait(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestMonitorCancelRequestCancelsExternalParentAndChild(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	started := make(chan struct{})
	s, err := New(state, func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	parent, err := s.RegisterParent(context.Background(), TaskSpec{Root: root, Prompt: "external", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, err := s.ParentContext(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChild(context.Background(), parent.ID, TaskSpec{Root: root, Prompt: "child", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	monitor, err := OpenMonitor(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := monitor.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("monitor cancellation did not reach parent")
	}
	childDone, err := s.Wait(context.Background(), child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if childDone.Status != StatusCanceled {
		t.Fatalf("child status = %s", childDone.Status)
	}
	if err := s.CompleteParent(parent.ID, Result{}, context.Canceled); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Wait(context.Background(), parent.ID); err != nil || got.Status != StatusCanceled {
		t.Fatalf("parent status = %#v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(state, "cancel", string(parent.ID)+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel request was not consumed: %v", err)
	}
	_ = s.Close()
}

func TestRecordEventUpdatesLiveUsageAndSteps(t *testing.T) {
	s, err := New(t.TempDir(), func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	task, err := s.RegisterParent(context.Background(), TaskSpec{Root: t.TempDir(), Prompt: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	usage := &TokenUsage{InputTokens: 3, OutputTokens: 5, TotalTokens: 8}
	if err := s.RecordEvent(Event{TaskID: task.ID, Type: "usage", Usage: usage, Step: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Status(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Result.Steps != 2 || got.Result.Tokens != *usage {
		t.Fatalf("live result = %#v", got.Result)
	}
	events, _, err := s.Events(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, event := range events {
		if event.TaskID == task.ID && event.Type == "task.usage" && event.Usage != nil && event.Usage.TotalTokens == 8 && event.Step == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("usage event missing: %#v", events)
	}
	_ = s.CompleteParent(task.ID, Result{}, context.Canceled)
}

func TestRecoverRunsMarksOrphansAndSkipsLiveSchedulers(t *testing.T) {
	root := t.TempDir()
	turnID, err := domain.NewID(domain.KindTurn)
	if err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(root, string(turnID))
	if err := os.MkdirAll(filepath.Join(runDir, "snapshots", "worktrees"), 0o700); err != nil {
		t.Fatal(err)
	}
	taskID, err := domain.NewID(domain.KindTask)
	if err != nil {
		t.Fatal(err)
	}
	task := Task{ID: taskID, Root: root, RepositoryRoot: root, Prompt: "orphan", Mode: ModeReadOnly, Status: StatusActive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), Revision: 1}
	b, _ := json.Marshal(snapshot{Version: snapshotVersion, Task: task})
	if err := os.WriteFile(filepath.Join(runDir, "snapshots", string(taskID)+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecoverRuns(root); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenMonitor(runDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := recovered.Status(taskID)
	if err != nil || got.Status != StatusInterrupted {
		t.Fatalf("orphan status = %#v, %v", got, err)
	}

	liveTurn, err := domain.NewID(domain.KindTurn)
	if err != nil {
		t.Fatal(err)
	}
	liveDir := filepath.Join(root, string(liveTurn))
	started := make(chan struct{})
	live, err := New(liveDir, func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	liveTask, err := live.SpawnParent(context.Background(), TaskSpec{Root: root, Prompt: "live", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	if err := RecoverRuns(root); err != nil {
		t.Fatal(err)
	}
	status, err := live.Status(liveTask.ID)
	if err != nil || status.Status != StatusActive {
		t.Fatalf("live status changed during recovery = %#v, %v", status, err)
	}
	_ = live.Cancel(liveTask.ID)
	_, _ = live.Wait(context.Background(), liveTask.ID)
	_ = live.Close()
}

func TestWaitChildYieldsParentConcurrencySlot(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	s, err := New(t.TempDir(), func(ctx context.Context, spec TaskSpec, _ func(Event)) (Result, error) {
		if spec.Prompt == "parent" {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}
		return Result{Output: spec.Prompt}, nil
	}, Options{MaxConcurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	parent, err := s.SpawnParent(context.Background(), TaskSpec{Root: root, Prompt: "parent", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	child, err := s.SpawnChild(context.Background(), parent.ID, TaskSpec{Root: root, Prompt: "child", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	childDone := make(chan error, 1)
	go func() {
		_, waitErr := s.WaitChild(context.Background(), parent.ID, child.ID)
		childDone <- waitErr
	}()
	select {
	case err := <-childDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("parent held the only scheduler slot while waiting for child")
	}
	close(release)
	if _, err := s.Wait(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
}

func TestParentContextScopeAndChildRootBoundary(t *testing.T) {
	s, err := New(t.TempDir(), func(ctx context.Context, _ TaskSpec, _ func(Event)) (Result, error) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}, Options{MaxConcurrency: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	root := t.TempDir()
	parent, err := s.RegisterParent(context.Background(), TaskSpec{Root: root, Prompt: "parent", Mode: ModeWrite})
	if err != nil {
		t.Fatal(err)
	}
	parentCtx, err := s.ParentContext(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.SpawnChild(context.Background(), parent.ID, TaskSpec{Root: root, Prompt: "child", Mode: ModeReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SpawnChild(context.Background(), parent.ID, TaskSpec{Root: t.TempDir(), Prompt: "escape", Mode: ModeReadOnly}); err == nil {
		t.Fatal("child outside parent repository was accepted")
	}
	if _, err := s.StatusFor(parent.ID, parent.ID); err == nil {
		t.Fatal("parent status was visible through descendant scope")
	}
	if err := s.CancelFor(parent.ID, parent.ID); err == nil {
		t.Fatal("parent cancellation was permitted through descendant scope")
	}
	if _, err := s.StatusFor(parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.CancelFor(parent.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentCtx.Done():
		t.Fatal("canceling child canceled external parent")
	default:
	}
	if err := s.Cancel(parent.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-parentCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("canceling parent did not cancel parent context")
	}
}

func hasEvent(events []Event, id domain.ID, typ string) bool {
	for _, event := range events {
		if event.TaskID == id && event.Type == typ {
			return true
		}
	}
	return false
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Bifrost Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--", "README"}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

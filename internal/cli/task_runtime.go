package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
	"bifrost/internal/ui"
)

// TaskRuntime owns one turn's isolated durable task graph. It is deliberately
// separate from the session store: closing a turn runtime closes only its
// scheduler and never replays a parent agent turn.
type TaskRuntime struct {
	scheduler  *tasks.Scheduler
	stateDir   string
	allowWrite bool
	once       sync.Once
	closeErr   error
}

// OpenTaskRuntime creates a per-turn task graph below <state>/tasks/<turn>.
// Remote backends are rejected until child execution can be placed in the
// same remote environment; silently falling back to host paths would violate
// the caller's workspace boundary.
func OpenTaskRuntime(ctx context.Context, opts options, cfg *agent.Config, maxConcurrent int) (*TaskRuntime, error) {
	if cfg == nil || (cfg.Provider == nil && cfg.RunExternal == nil) {
		return nil, errors.New("task runtime: configured provider is required")
	}
	if cfg.Backend != nil && cfg.Backend.Identity().Kind != "" && cfg.Backend.Identity().Kind != "local" {
		return nil, errors.New("task runtime: remote child spawning is unsupported")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}
	if maxConcurrent > 32 {
		return nil, errors.New("task runtime: max concurrency must be <= 32")
	}
	if err := tasks.RecoverRuns(filepath.Join(opts.statePath(), "tasks")); err != nil {
		return nil, fmt.Errorf("task runtime: recover previous runs: %w", err)
	}
	turnID, err := domain.NewID(domain.KindTurn)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(opts.statePath(), "tasks", string(turnID))
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("task runtime: create state: %w", err)
	}
	var scheduler *tasks.Scheduler
	resolver, err := taskProviderResolver(opts)
	if err != nil {
		return nil, err
	}
	agentResolver, err := taskAgentResolver(opts)
	if err != nil {
		return nil, err
	}
	profiles, err := taskProfileInstructions(opts)
	if err != nil {
		return nil, err
	}
	runner := NewTaskRunner(TaskRunnerConfig{Agent: *cfg, SchedulerFunc: func() *tasks.Scheduler { return scheduler }, AllowWrite: cfg.WriteEnabled, ResolveProvider: resolver, ResolveAgent: agentResolver, ProfileInstructions: profiles})
	scheduler, err = tasks.New(stateDir, runner, tasks.Options{MaxConcurrency: maxConcurrent, PreserveWorktrees: true})
	if err != nil {
		return nil, err
	}
	return &TaskRuntime{scheduler: scheduler, stateDir: stateDir, allowWrite: cfg.WriteEnabled}, nil
}

func (r *TaskRuntime) Scheduler() *tasks.Scheduler {
	if r == nil {
		return nil
	}
	return r.scheduler
}

func (r *TaskRuntime) StateDir() string {
	if r == nil {
		return ""
	}
	return r.stateDir
}

// RegisterParent reserves no worker slot for the caller's already-running
// turn, while making it available as the parent ID for task tools.
func (r *TaskRuntime) RegisterParent(ctx context.Context, cfg agent.Config, prompt string) (tasks.Task, error) {
	if r == nil || r.scheduler == nil {
		return tasks.Task{}, errors.New("task runtime is unavailable")
	}
	mode := tasks.ModeReadOnly
	if r.allowWrite {
		mode = tasks.ModeWrite
	}
	return r.scheduler.RegisterParent(ctx, tasks.TaskSpec{Root: cfg.Root, Prompt: prompt, Mode: mode, Model: cfg.ModelName, Profile: cfg.ProfileName, StepBudget: cfg.MaxSteps})
}

// ParentContext is the derived context used by the scheduler for an external
// parent turn. Canceling the parent task therefore cancels the root agent and
// all children, rather than merely marking a graph node canceled.
func (r *TaskRuntime) ParentContext(id domain.ID) (context.Context, error) {
	if r == nil || r.scheduler == nil {
		return nil, errors.New("task runtime is unavailable")
	}
	return r.scheduler.ParentContext(id)
}

func taskProviderResolver(opts options) (TaskProviderResolver, error) {
	settings, err := opts.settings()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, model, profile string) (provider.Client, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if profile == "" {
			return nil, errors.New("task runtime: provider profile is required")
		}
		p, ok := settings.Providers[profile]
		if !ok {
			return nil, fmt.Errorf("task runtime: unknown provider profile %q", profile)
		}
		if model == "" {
			model = p.Model
		}
		if model == "" {
			return nil, errors.New("task runtime: provider profile has no model")
		}
		if p.Kind == "cursor" || p.Kind == "claude" || p.Kind == "cursor-native" {
			return nil, errors.New("external agent profiles cannot be used as model overrides")
		}
		p.Model = model
		var child agent.Config
		_, err := bindRuntimeProvider(opts, &child, p)
		if err != nil {
			return nil, fmt.Errorf("task runtime: create provider: %w", err)
		}
		return child.Provider, nil
	}, nil
}

func taskAgentResolver(opts options) (TaskAgentResolver, error) {
	settings, err := opts.settings()
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, model, profile string) (agent.Config, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		if profile == "" {
			return agent.Config{}, errors.New("task runtime: provider profile is required")
		}
		p, ok := settings.Providers[profile]
		if !ok {
			return agent.Config{}, fmt.Errorf("task runtime: unknown provider profile %q", profile)
		}
		if model != "" {
			p.Model = model
		}
		if p.Model == "" {
			return agent.Config{}, errors.New("task runtime: provider profile has no model")
		}
		var resolved agent.Config
		_, err := bindRuntimeProvider(opts, &resolved, p)
		if err != nil {
			return agent.Config{}, fmt.Errorf("task runtime: create profile: %w", err)
		}
		resolved.ModelName, resolved.ProfileName = p.Model, profile
		return resolved, nil
	}, nil
}

func taskProfileInstructions(opts options) (string, error) {
	settings, err := opts.settings()
	if err != nil {
		return "", err
	}
	if len(settings.Providers) == 0 {
		return "", nil
	}
	profiles := make([]string, 0, len(settings.Providers))
	for name, p := range settings.Providers {
		mode := "native"
		if p.Kind == "cursor" || p.Kind == "claude" {
			mode = "external"
		}
		profiles = append(profiles, fmt.Sprintf("%s (%s, model=%s)", name, mode, p.Model))
	}
	sort.Strings(profiles)
	return "Available delegation profiles (use the profile field when spawning a child): " + strings.Join(profiles, "; "), nil
}

func (r *TaskRuntime) CompleteParent(id domain.ID, result tasks.Result, runErr error) error {
	if r == nil || r.scheduler == nil {
		return errors.New("task runtime is unavailable")
	}
	return r.scheduler.CompleteParent(id, result, runErr)
}

// AttachParent binds task tools to an externally registered parent. It is
// safe to call after OpenTaskRuntime because the scheduler is already live.
func (r *TaskRuntime) AttachParent(cfg *agent.Config, parent domain.ID) {
	if r == nil || cfg == nil {
		return
	}
	AttachTaskRuntime(cfg, func() *tasks.Scheduler { return r.scheduler }, parent, r.allowWrite)
}

// MonitorOption connects the live scheduler to the UI without opening a
// second scheduler lock or converting active snapshots to interrupted.
func (r *TaskRuntime) MonitorOption() ui.Option {
	return ui.WithTaskMonitor(ui.TaskMonitorCallbacks{
		Refresh: func(context.Context) ([]tasks.Summary, error) {
			if r == nil || r.scheduler == nil {
				return nil, errors.New("task runtime is unavailable")
			}
			return r.scheduler.List(), nil
		},
		Events: func(_ context.Context, cursor uint64, limit int) ([]tasks.Event, uint64, error) {
			if r == nil || r.scheduler == nil {
				return nil, cursor, errors.New("task runtime is unavailable")
			}
			return r.scheduler.Events(cursor, limit)
		},
		Cancel: func(_ context.Context, id domain.ID) error {
			if r == nil || r.scheduler == nil {
				return errors.New("task runtime is unavailable")
			}
			return r.scheduler.Cancel(id)
		},
	})
}

func (r *TaskRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.scheduler != nil {
			r.closeErr = r.scheduler.Close()
		}
	})
	return r.closeErr
}

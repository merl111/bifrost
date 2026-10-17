package cli

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/session"
	"bifrost/internal/tasks"
	"bifrost/internal/ui"
)

type taskMonitorState struct {
	mu     sync.Mutex
	active *TaskRuntime
	parent domain.ID
	cancel context.CancelFunc
}

func (m *taskMonitorState) option(opts options) ui.Option {
	return ui.WithTaskMonitor(m.callbacks(opts))
}

func (m *taskMonitorState) callbacks(opts options) ui.TaskMonitorCallbacks {
	return ui.TaskMonitorCallbacks{
		Refresh: func(context.Context) ([]tasks.Summary, error) {
			r, e := tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks"))
			if e != nil {
				return nil, e
			}
			return r.List()
		},
		Events: func(_ context.Context, c uint64, n int) ([]tasks.Event, uint64, error) {
			r, e := tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks"))
			if e != nil {
				return nil, c, e
			}
			return r.Events(c, n)
		},
		Cancel: func(_ context.Context, id domain.ID) error {
			m.mu.Lock()
			active := m.active
			parent := m.parent
			cancel := m.cancel
			m.mu.Unlock()
			if active != nil {
				if id == parent && cancel != nil {
					cancel()
				}
				if err := active.Scheduler().Cancel(id); err == nil {
					return nil
				} else if !errors.Is(err, tasks.ErrNotFound) {
					return err
				}
			}
			r, err := tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks"))
			if err != nil {
				return err
			}
			return r.Cancel(id)
		},
	}
}

func runMonitoredSessionTurn(ctx context.Context, opts options, st *session.Store, current session.Session, cfg agent.Config, prompt string, monitor *taskMonitorState, maxAgents int) (updated session.Session, result agent.Result, err error) {
	cfg, err = routeAgent(ctx, opts, cfg, current.Messages, prompt)
	if err != nil {
		return current, result, err
	}
	if cfg.OnEvent != nil {
		settings, loadErr := opts.settings()
		if loadErr == nil {
			cfg.OnEvent(agent.Event{Kind: "connection", Provider: settings.Providers[cfg.ProfileName].Kind, Profile: cfg.ProfileName, Model: cfg.ModelName})
		}
	}
	if maxAgents == 0 {
		return runSessionTurn(ctx, opts.statePath(), st, current, cfg, prompt)
	}
	runtime, err := OpenTaskRuntime(ctx, opts, &cfg, maxAgents)
	if err != nil {
		return current, result, err
	}
	defer runtime.Close()
	turnCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	parent, err := runtime.RegisterParent(turnCtx, cfg, prompt)
	if err != nil {
		return current, result, err
	}
	parentCtx, err := runtime.ParentContext(parent.ID)
	if err != nil {
		return current, result, err
	}
	runtime.AttachParent(&cfg, parent.ID)
	previousEvent := cfg.OnEvent
	cfg.OnEvent = func(event agent.Event) {
		var usage *tasks.TokenUsage
		if event.Usage != nil {
			usage = &tasks.TokenUsage{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens, TotalTokens: event.Usage.TotalTokens}
		}
		_ = runtime.Scheduler().RecordEvent(tasks.Event{TaskID: parent.ID, Type: event.Kind, Message: event.Text, Usage: usage, Step: event.Step})
		if previousEvent != nil {
			previousEvent(event)
		}
	}
	if monitor != nil {
		monitor.mu.Lock()
		monitor.active = runtime
		monitor.parent = parent.ID
		monitor.cancel = cancel
		monitor.mu.Unlock()
		defer func() { monitor.mu.Lock(); monitor.cancel = nil; monitor.mu.Unlock() }()
	}
	if cfg.OnEvent != nil {
		cfg.OnEvent(agent.Event{Kind: "status", Text: "task " + string(parent.ID) + " · sub-agent monitoring available in :tasks"})
	}
	updated, result, err = runSessionTurn(parentCtx, opts.statePath(), st, current, cfg, prompt)
	completionErr := runtime.CompleteParent(parent.ID, tasks.Result{Output: result.Text, Steps: result.Steps, ToolCalls: result.ToolCalls, Tokens: tasks.TokenUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, TotalTokens: result.Usage.TotalTokens}}, err)
	return updated, result, errors.Join(err, completionErr)
}

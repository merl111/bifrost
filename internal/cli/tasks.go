package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
	"bifrost/internal/tasktools"
)

// TaskProviderResolver supplies a checked provider profile for a child task.
// API keys remain inside the resolver and are never placed in TaskSpec.
type TaskProviderResolver func(context.Context, string, string) (provider.Client, error)

// TaskAgentResolver resolves a complete saved profile. Unlike the legacy
// provider resolver it can return an external agent runner as well as a native
// provider client.
type TaskAgentResolver func(context.Context, string, string) (agent.Config, error)

type TaskRunnerConfig struct {
	Agent               agent.Config
	Scheduler           *tasks.Scheduler
	SchedulerFunc       func() *tasks.Scheduler
	AllowWrite          bool
	ResolveProvider     TaskProviderResolver
	ResolveAgent        TaskAgentResolver
	ProfileInstructions string
}

// AttachTaskTools binds task tools to the current task identity. Existing
// task tools are replaced, preventing a child from retaining its parent's
// binding and spawning outside the current graph edge.
func AttachTaskTools(cfg *agent.Config, scheduler *tasks.Scheduler, parent domain.ID, allowWrite bool) {
	if cfg == nil || scheduler == nil {
		return
	}
	filtered := make([]agent.Tool, 0, len(cfg.ExtraTools)+4)
	for _, tool := range cfg.ExtraTools {
		switch tool.Definition.Name {
		case "spawn_agent", "task_status", "task_wait", "task_cancel":
		default:
			filtered = append(filtered, tool)
		}
	}
	cfg.ExtraTools = append(filtered, tasktools.ToolsWithOptions(scheduler, parent, tasktools.Options{AllowWrite: allowWrite, DefaultProfile: cfg.ChildProfile, DefaultModel: cfg.ChildModel})...)
}

// AttachTaskRuntime is the lazy form used when the scheduler's runner needs
// the agent configuration during construction. The callback is evaluated
// only when attaching tools; it does not claim a scheduler execution slot.
func AttachTaskRuntime(cfg *agent.Config, scheduler func() *tasks.Scheduler, parent domain.ID, allowWrite bool) {
	if scheduler != nil {
		AttachTaskTools(cfg, scheduler(), parent, allowWrite)
	}
}

// NewTaskRunner adapts the agent loop to the durable task runner contract.
// Each invocation receives the scheduler-created isolated root and a fresh
// task-tool binding. Model overrides are passed through the provider request;
// profile overrides require an explicit resolver.
func NewTaskRunner(options TaskRunnerConfig) tasks.Runner {
	return func(ctx context.Context, spec tasks.TaskSpec, emit func(tasks.Event)) (tasks.Result, error) {
		if err := validateTaskOverride(spec.Model, 256, "model"); err != nil {
			return tasks.Result{}, err
		}
		if err := validateTaskOverride(spec.Profile, 128, "profile"); err != nil {
			return tasks.Result{}, err
		}
		cfg := options.Agent
		cfg.DecisionUsage, cfg.DecisionNote = provider.Usage{}, ""
		if options.ProfileInstructions != "" {
			cfg.Instructions += "\n" + options.ProfileInstructions
		}
		cfg.Root = spec.Root
		cfg.Backend = nil
		cfg.History = nil
		cfg.RunHook = nil
		if options.Agent.StateDir != "" && spec.TaskID != "" {
			cfg.StateDir = filepath.Join(options.Agent.StateDir, "tasks", "children", string(spec.TaskID))
			if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
				return tasks.Result{}, fmt.Errorf("task runner: create child state: %w", err)
			}
		}
		if spec.Mode == tasks.ModeReadOnly {
			cfg.WriteEnabled = false
			cfg.ExecEnabled = false
		}
		if spec.StepBudget > 0 && (cfg.MaxSteps <= 0 || spec.StepBudget < cfg.MaxSteps) {
			cfg.MaxSteps = spec.StepBudget
		}
		if spec.MaxOutputBytes > 0 && (cfg.MaxOutputBytes <= 0 || spec.MaxOutputBytes < cfg.MaxOutputBytes) {
			cfg.MaxOutputBytes = spec.MaxOutputBytes
		}
		if spec.Profile != "" {
			// A selected profile replaces both inherited execution modes. This is
			// essential when a native parent delegates to Claude/Cursor or vice
			// versa; root, permissions and budgets remain those of the child.
			cfg.Provider, cfg.RunExternal = nil, nil
			cfg.ExternalToolBridge = false
			if options.ResolveAgent != nil {
				resolved, err := options.ResolveAgent(ctx, spec.Model, spec.Profile)
				if err != nil {
					return tasks.Result{}, fmt.Errorf("task runner: resolve agent profile: %w", err)
				}
				cfg.Provider, cfg.RunExternal = resolved.Provider, resolved.RunExternal
				cfg.ExternalToolBridge = resolved.ExternalToolBridge
				cfg.ModelName, cfg.ProfileName = resolved.ModelName, spec.Profile
			} else {
				if options.ResolveProvider == nil {
					return tasks.Result{}, errors.New("task runner: provider profile override is unavailable")
				}
				client, err := options.ResolveProvider(ctx, spec.Model, spec.Profile)
				if err != nil {
					return tasks.Result{}, fmt.Errorf("task runner: resolve provider profile: %w", err)
				}
				cfg.Provider = client
			}
			if spec.Model != "" {
				cfg.ModelName = spec.Model
			}
		} else if spec.Model != "" {
			cfg.ModelName = spec.Model
			if cfg.Provider != nil {
				cfg.Provider = modelProvider{Client: cfg.Provider, Model: spec.Model}
			}
		}
		if cfg.Provider == nil && cfg.RunExternal == nil {
			return tasks.Result{}, errors.New("task runner: provider is not configured")
		}
		// Child tools may hold browser/MCP/skill leases or mutate shared state;
		// the child receives only the scheduler tools bound to its own ID.
		cfg.ExtraTools = nil
		scheduler := options.Scheduler
		if scheduler == nil && options.SchedulerFunc != nil {
			scheduler = options.SchedulerFunc()
		}
		AttachTaskTools(&cfg, scheduler, spec.TaskID, options.AllowWrite && spec.Mode != tasks.ModeReadOnly)
		cfg.OnEvent = func(event agent.Event) {
			if emit != nil && (strings.TrimSpace(event.Text) != "" || event.Usage != nil || event.Step > 0) {
				var usage *tasks.TokenUsage
				if event.Usage != nil {
					converted := tasks.TokenUsage{InputTokens: event.Usage.InputTokens, OutputTokens: event.Usage.OutputTokens, TotalTokens: event.Usage.TotalTokens}
					usage = &converted
				}
				emit(tasks.Event{TaskID: spec.TaskID, Type: event.Kind, Message: event.Text, Usage: usage, Step: event.Step})
			}
		}
		result, err := agent.Run(ctx, cfg, spec.Prompt)
		return tasks.Result{Output: result.Text, ToolCalls: result.ToolCalls, Steps: result.Steps, Tokens: tasks.TokenUsage{InputTokens: result.Usage.InputTokens, OutputTokens: result.Usage.OutputTokens, TotalTokens: result.Usage.TotalTokens}}, err
	}
}

func validateTaskOverride(value string, max int, name string) error {
	if len(value) > max || strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("task runner: invalid %s override", name)
	}
	return nil
}

type modelProvider struct {
	provider.Client
	Model string
}

func (p modelProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	if req.Model == "" {
		req.Model = p.Model
	}
	return p.Client.Complete(ctx, req)
}

// runTasks is intentionally read-only unless an active scheduler is supplied
// by runtime wiring. Monitor commands can therefore inspect a live process.
func runTasks(ctx context.Context, opts options, args []string, stdout io.Writer, active ...*tasks.Scheduler) error {
	if len(args) == 0 {
		return errors.New("usage: tasks list | status ID | tree ID | events [CURSOR] [LIMIT] | wait ID | cancel ID")
	}
	monitor, err := tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks"))
	if err != nil {
		return err
	}
	write := func(value any) error { return writeJSON(stdout, value) }
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("tasks list takes no arguments")
		}
		items, err := monitor.List()
		if err != nil {
			return err
		}
		return write(items)
	case "status", "tree":
		if len(args) != 2 {
			return fmt.Errorf("tasks %s requires ID", args[0])
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			return err
		}
		if args[0] == "status" {
			item, err := monitor.Status(id)
			if err != nil {
				return err
			}
			return write(item)
		}
		items, err := monitor.Tree(id)
		if err != nil {
			return err
		}
		return write(items)
	case "events":
		eventMonitor := monitor
		pagingArgs := args[1:]
		if len(pagingArgs) > 0 && pagingArgs[0] == "--run" {
			if len(pagingArgs) < 2 || len(pagingArgs) > 5 {
				return errors.New("tasks events --run TURN_ID accepts CURSOR and LIMIT")
			}
			runID, parseErr := domain.ParseID(pagingArgs[1])
			if parseErr != nil {
				return parseErr
			}
			kind, kindErr := runID.Kind()
			if kindErr != nil || kind != domain.KindTurn {
				return errors.New("tasks events --run requires a turn ID")
			}
			eventMonitor, err = tasks.OpenMonitor(filepath.Join(opts.statePath(), "tasks", string(runID)))
			if err != nil {
				return err
			}
			pagingArgs = pagingArgs[2:]
		} else if len(pagingArgs) > 2 {
			return errors.New("tasks events accepts CURSOR and LIMIT")
		}
		cursor, limit, err := parseMonitorPaging(pagingArgs)
		if err != nil {
			return err
		}
		events, next, err := eventMonitor.Events(cursor, limit)
		if err != nil {
			return err
		}
		return write(struct {
			Cursor uint64        `json:"cursor"`
			Events []tasks.Event `json:"events"`
		}{Cursor: next, Events: events})
	case "wait", "cancel":
		if len(args) != 2 {
			return fmt.Errorf("tasks %s requires ID", args[0])
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			return err
		}
		if args[0] == "cancel" && (len(active) == 0 || active[0] == nil) {
			if err := monitor.Cancel(id); err != nil {
				return err
			}
			return write(map[string]any{"id": id, "canceled": true})
		}
		if args[0] == "wait" && (len(active) == 0 || active[0] == nil) {
			return waitViaMonitor(ctx, monitor, id, stdout)
		}
		if len(active) == 0 || active[0] == nil {
			return errors.New("task mutation requires an active scheduler in this process")
		}
		if args[0] == "cancel" {
			if err := active[0].Cancel(id); err != nil {
				return err
			}
			return write(map[string]any{"id": id, "canceled": true})
		}
		item, err := active[0].Wait(ctx, id)
		if err != nil {
			return err
		}
		return write(item)
	default:
		return fmt.Errorf("unknown tasks command %q", args[0])
	}
}

func waitViaMonitor(ctx context.Context, monitor *tasks.Monitor, id domain.ID, stdout io.Writer) error {
	if monitor == nil {
		return errors.New("task monitor is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bounded, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		item, err := monitor.Status(id)
		if err != nil {
			return err
		}
		switch item.Status {
		case tasks.StatusCompleted, tasks.StatusFailed, tasks.StatusCanceled, tasks.StatusInterrupted:
			return writeJSON(stdout, item)
		}
		select {
		case <-bounded.Done():
			return bounded.Err()
		case <-ticker.C:
		}
	}
}

func parseMonitorPaging(args []string) (uint64, int, error) {
	var cursor uint64
	limit := 100
	if len(args) > 0 {
		if _, err := fmt.Sscan(args[0], &cursor); err != nil {
			return 0, 0, errors.New("events cursor must be an integer")
		}
	}
	if len(args) > 1 {
		if _, err := fmt.Sscan(args[1], &limit); err != nil || limit < 1 || limit > 1000 {
			return 0, 0, errors.New("events limit must be 1..1000")
		}
	}
	return cursor, limit, nil
}

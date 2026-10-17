package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"bifrost/internal/agent"
	"bifrost/internal/daemon"
	"bifrost/internal/ui"
)

// runDaemon is kept separate from top-level dispatch so the caller can add a
// daemon case without changing the existing command parser.
func runDaemon(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: daemon serve|run|status|jobs|attach|cancel|stop")
	}
	cfg := daemon.Config{StateDir: opts.statePath()}
	switch args[0] {
	case "serve":
		if len(args) != 1 {
			return errors.New("daemon serve takes no arguments")
		}
		s, err := daemon.Start(ctx, cfg, daemonAgentHandler(opts, stderr))
		if err != nil {
			return err
		}
		defer s.Close()
		select {
		case <-s.Done():
			if err := ctx.Err(); err != nil {
				return err
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	case "run":
		if len(args) < 2 {
			return errors.New("usage: daemon run PROMPT...")
		}
		c, err := openDaemon(ctx, opts)
		if err != nil {
			return err
		}
		defer c.Close()
		requestArgs := append([]string(nil), args[1:]...)
		id, err := c.Submit(ctx, daemon.AgentRequest{Root: opts.root, Project: opts.project, Repository: opts.repository, Args: requestArgs})
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, map[string]string{"id": id})
		}
		_, _ = fmt.Fprintln(stdout, id)
		return nil
	case "status":
		if len(args) != 2 {
			return errors.New("usage: daemon status JOB_ID")
		}
		c, err := daemon.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		j, err := c.Status(ctx, args[1])
		if err != nil {
			return err
		}
		return writeDaemonValue(stdout, opts.json, j)
	case "jobs":
		if len(args) != 1 {
			return errors.New("daemon jobs takes no arguments")
		}
		c, err := daemon.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		jobs, err := c.List(ctx)
		if err != nil {
			return err
		}
		return writeDaemonValue(stdout, opts.json, jobs)
	case "attach":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: daemon attach JOB_ID [CURSOR]")
		}
		c, err := daemon.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		var cursor uint64
		if len(args) == 3 {
			if _, err := fmt.Sscan(args[2], &cursor); err != nil {
				return errors.New("invalid event cursor")
			}
		}
		return c.Attach(ctx, args[1], cursor, func(ev daemon.Event) error {
			if opts.json {
				return json.NewEncoder(stdout).Encode(ev)
			}
			_, err := fmt.Fprintf(stdout, "%d\t%s\t%s\n", ev.Cursor, ev.Type, string(ev.Payload))
			return err
		})
	case "cancel":
		if len(args) != 2 {
			return errors.New("usage: daemon cancel JOB_ID")
		}
		c, err := daemon.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Cancel(ctx, args[1]); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "cancelling", args[1])
		return nil
	case "stop":
		if len(args) != 1 {
			return errors.New("daemon stop takes no arguments")
		}
		c, err := daemon.Dial(ctx, cfg)
		if err != nil {
			return err
		}
		defer c.Close()
		return c.Shutdown(ctx)
	case "start":
		if len(args) != 1 {
			return errors.New("daemon start takes no arguments")
		}
		return startDaemon(ctx, opts)
	default:
		return fmt.Errorf("unknown daemon command %q", args[0])
	}
}

func writeDaemonValue(out io.Writer, jsonMode bool, value any) error {
	if jsonMode {
		return writeJSON(out, value)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(b))
	return err
}

func daemonAgentHandler(base options, stderr io.Writer) daemon.Handler {
	return func(ctx context.Context, req daemon.AgentRequest, emit func(daemon.Event) error) (json.RawMessage, error) {
		opts := base
		if req.Root != "" {
			opts.root = req.Root
		}
		if req.Project != "" {
			opts.project = req.Project
		}
		if req.Repository != "" {
			opts.repository = req.Repository
		}
		setup, err := configureAgentSetup(ctx, opts, req.Args, stderr, true)
		if err != nil {
			return nil, err
		}
		defer setup.cleanup()
		if req.SessionID != "" {
			setup.sessionID = req.SessionID
		}
		cfg := setup.cfg
		cfg.OnEvent = func(ev agent.Event) {
			payloadValue := map[string]any{"kind": ev.Kind, "text": ev.Text, "provider": ev.Provider, "profile": ev.Profile, "model": ev.Model}
			if ev.ID != "" {
				payloadValue["id"] = ev.ID
			}
			if ev.Detail != "" {
				payloadValue["detail"] = ev.Detail
			}
			if ev.Step > 0 {
				payloadValue["step"] = ev.Step
			}
			payload, _ := json.Marshal(payloadValue)
			_ = emit(daemon.Event{Type: "agent." + ev.Kind, Payload: payload})
		}
		prompt := req.Prompt
		if strings.TrimSpace(prompt) == "" {
			prompt = strings.Join(setup.rest, " ")
		}
		if strings.TrimSpace(prompt) == "" {
			return nil, errors.New("daemon request requires a prompt")
		}
		st, err := sessionStore(opts)
		if err != nil {
			return nil, err
		}
		current, err := loadOrCreateSession(st, setup.sessionID, setup.cfg.Root, setup.fingerprint)
		if err != nil {
			return nil, err
		}
		cfg = addResumeNotice(cfg, current, stderr)
		_, result, err := runMonitoredSessionTurn(ctx, opts, st, current, cfg, prompt, nil, setup.maxAgents)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

// runDaemonAgent is a small direct helper for callers that already have a
// foreground output stream; daemon jobs use daemonAgentHandler instead.
func runDaemonAgent(ctx context.Context, opts options, args []string, out, stderr io.Writer) error {
	if daemon, prompt, err := daemonPrompt(args); err != nil {
		return err
	} else if daemon {
		return submitDaemonPrompt(ctx, opts, prompt, out)
	}
	return runAgent(ctx, opts, args, out, stderr)
}

func daemonPrompt(args []string) (bool, []string, error) {
	remaining := append([]string(nil), args...)
	seen := false
	for i := 0; i < len(remaining); {
		arg := remaining[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			break
		}
		name, value, hasValue := arg, "", false
		if equal := strings.IndexByte(arg, '='); equal >= 0 {
			name, value, hasValue = arg[:equal], arg[equal+1:], true
		}
		if name == "--daemon" {
			if hasValue && value != "true" {
				break
			}
			if seen {
				return false, nil, errors.New("duplicate --daemon")
			}
			seen = true
			remaining = append(remaining[:i], remaining[i+1:]...)
			continue
		}
		if daemonFlagTakesValue(name) && !hasValue {
			if i+1 >= len(remaining) {
				break
			}
			i += 2
			continue
		}
		if !daemonBooleanFlag(name) {
			break
		}
		i++
	}
	if seen && len(remaining) == 0 {
		return false, nil, errors.New("--daemon requires agent arguments")
	}
	return seen, remaining, nil
}

func daemonFlagTakesValue(name string) bool {
	switch name {
	case "--base-url", "--model", "--max-steps", "--skill", "--mcp-config", "--session", "--provider", "--profile", "--api-key-env", "--ssh", "--agents", "--account":
		return true
	default:
		return false
	}
}

func daemonBooleanFlag(name string) bool {
	switch name {
	case "--allow-write", "--allow-exec", "--browser", "--desktop":
		return true
	default:
		return false
	}
}

func submitDaemonPrompt(ctx context.Context, opts options, args []string, out io.Writer) error {
	c, err := openDaemon(ctx, opts)
	if err != nil {
		return err
	}
	defer c.Close()
	id, err := c.Submit(ctx, daemon.AgentRequest{Root: opts.root, Project: opts.project, Repository: opts.repository, Args: append([]string(nil), args...)})
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, map[string]string{"id": id})
	}
	_, err = fmt.Fprintln(out, id)
	return err
}

// daemonChatRunner adapts the daemon's durable event stream to the TUI chat
// runner contract. The caller owns the returned cleanup function. A daemon is
// started on demand when no server is listening for the selected state dir.
func daemonChatRunner(ctx context.Context, opts options, args []string, sessionID string) (ui.ChatRunner, func(), error) {
	c, err := openDaemon(ctx, opts)
	if err != nil {
		return nil, func() {}, err
	}
	return func(turnCtx context.Context, prompt string, emit func(ui.ChatEvent)) error {
		id, err := c.Submit(turnCtx, daemon.AgentRequest{Root: opts.root, Project: opts.project, Repository: opts.repository, Args: append([]string(nil), args...), Prompt: prompt, SessionID: sessionID})
		if err != nil {
			return err
		}
		err = c.Attach(turnCtx, id, 0, func(event daemon.Event) error {
			if !strings.HasPrefix(event.Type, "agent.") {
				return nil
			}
			var payload struct {
				Kind     string `json:"kind"`
				Text     string `json:"text"`
				ID       string `json:"id"`
				Detail   string `json:"detail"`
				Step     int    `json:"step"`
				Provider string `json:"provider"`
				Profile  string `json:"profile"`
				Model    string `json:"model"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return err
			}
			kind := payload.Kind
			if kind == "" {
				kind = strings.TrimPrefix(event.Type, "agent.")
			}
			emit(toChatEvent(agent.Event{Kind: kind, Text: payload.Text, ID: payload.ID, Detail: payload.Detail, Step: payload.Step, Provider: payload.Provider, Profile: payload.Profile, Model: payload.Model}, opts.root))
			return nil
		})
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = c.Cancel(cancelCtx, id)
				cancel()
			}
			return err
		}
		statusCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		status, statusErr := c.Status(statusCtx, id)
		cancel()
		if statusErr != nil {
			return statusErr
		}
		if status.Status != "completed" {
			if status.Error != "" {
				return errors.New(status.Error)
			}
			return fmt.Errorf("daemon job %s", status.Status)
		}
		return nil
	}, func() { _ = c.Close() }, nil
}

func openDaemon(ctx context.Context, opts options) (*daemon.Client, error) {
	c, err := daemon.Dial(ctx, daemon.Config{StateDir: opts.statePath()})
	if err == nil {
		return c, nil
	}
	if startErr := startDaemon(ctx, opts); startErr != nil {
		return nil, startErr
	}
	return daemon.Dial(ctx, daemon.Config{StateDir: opts.statePath()})
}

func startDaemon(ctx context.Context, opts options) error {
	executable := os.Getenv("BIFROST_DAEMON_EXECUTABLE")
	if executable == "" {
		var err error
		executable, err = os.Executable()
		if err != nil {
			return fmt.Errorf("locate bifrost executable: %w", err)
		}
	}
	configPath, err := opts.configFile()
	if err != nil {
		return err
	}
	args := []string{"--state-dir", opts.statePath(), "--config", configPath}
	if opts.root != "" {
		args = append(args, "--root", opts.root)
	}
	if opts.project != "" {
		args = append(args, "--project", opts.project)
	}
	if opts.repository != "" {
		args = append(args, "--repository", opts.repository)
	}
	if opts.environment != "" {
		args = append(args, "--environment", opts.environment)
	}
	args = append(args, "daemon", "serve")
	cmd := exec.Command(executable, args...)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = os.Stderr
	configureDetachedCommand(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	if err := waitDaemon(ctx, opts.statePath()); err != nil {
		terminateDetachedCommand(cmd)
		return err
	}
	return nil
}

func waitDaemon(ctx context.Context, stateDir string) error {
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		c, err := daemon.Dial(ctx, daemon.Config{StateDir: stateDir})
		if err == nil {
			_ = c.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("daemon did not become ready: %w", err)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

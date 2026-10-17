// Package hooks runs explicitly trusted declarative argv hooks. It never
// interprets shell syntax or dispatches hooks recursively.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"bifrost/internal/process"
)

type Event string

const (
	BeforeTurn Event = "before-turn"
	AfterTurn  Event = "after-turn"
	BeforeTool Event = "before-tool"
	AfterTool  Event = "after-tool"
)

type Hook struct {
	Name           string        `json:"name"`
	Event          Event         `json:"event"`
	Command        []string      `json:"command"`
	Timeout        time.Duration `json:"timeout"`
	MaxOutputBytes int           `json:"max_output_bytes"`
}
type Config struct {
	Trusted bool
	Hooks   []Hook
}
type Result struct {
	Hook    Hook
	Process process.Result
}
type Runner struct {
	hooks   map[Event][]Hook
	running atomic.Bool
}

func New(cfg Config) (*Runner, error) {
	if !cfg.Trusted {
		return nil, errors.New("hooks require explicit trusted configuration")
	}
	r := &Runner{hooks: map[Event][]Hook{}}
	for _, h := range cfg.Hooks {
		if err := validate(h); err != nil {
			return nil, err
		}
		if h.Timeout <= 0 {
			h.Timeout = 30 * time.Second
		}
		if h.MaxOutputBytes <= 0 {
			h.MaxOutputBytes = 1 << 20
		}
		h.Command = append([]string(nil), h.Command...)
		r.hooks[h.Event] = append(r.hooks[h.Event], h)
	}
	return r, nil
}
func (r *Runner) Run(ctx context.Context, event Event, input any) ([]Result, error) {
	if r == nil {
		return nil, errors.New("hooks runner is nil")
	}
	if _, ok := r.hooks[event]; !ok {
		return nil, nil
	}
	if !r.running.CompareAndSwap(false, true) {
		return nil, errors.New("recursive or concurrent hook dispatch is forbidden")
	}
	defer r.running.Store(false)
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(r.hooks[event]))
	for _, h := range r.hooks[event] {
		hookCtx, cancel := context.WithTimeout(ctx, h.Timeout)
		env := append(os.Environ(), "BIFROST_HOOK_EVENT="+string(event), "BIFROST_HOOK_INPUT="+string(payload))
		pr, runErr := process.Run(hookCtx, process.Spec{Argv: h.Command, Env: env, MaxOutputBytes: h.MaxOutputBytes})
		cancel()
		out = append(out, Result{Hook: h, Process: pr})
		if runErr != nil {
			return out, fmt.Errorf("hook %s: %w", h.Name, runErr)
		}
		if pr.ExitCode != 0 {
			return out, fmt.Errorf("hook %s exited %d", h.Name, pr.ExitCode)
		}
	}
	return out, nil
}
func validate(h Hook) error {
	if strings.TrimSpace(h.Name) == "" {
		return errors.New("hook name is required")
	}
	switch h.Event {
	case BeforeTurn, AfterTurn, BeforeTool, AfterTool:
	default:
		return fmt.Errorf("hook %q has unsupported event %q", h.Name, h.Event)
	}
	if len(h.Command) == 0 || h.Command[0] == "" {
		return fmt.Errorf("hook %q command is required", h.Name)
	}
	for _, a := range h.Command {
		if a == "" || strings.IndexByte(a, 0) >= 0 {
			return fmt.Errorf("hook %q command contains empty or NUL argument", h.Name)
		}
	}
	return nil
}

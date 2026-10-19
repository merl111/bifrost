// Package tasktools exposes the scheduler as a small set of agent tools.
// The scheduler itself deliberately does not import agent, avoiding a cycle.
package tasktools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/tasks"
)

const (
	maxPrompt = 64 << 10
	maxArgs   = 128 << 10
)

// Tools returns task graph tools bound to a scheduler. parent identifies the
// currently executing task; an empty ID makes spawn_agent create a root task.
type Options struct {
	DefaultProfile, DefaultModel string
	AllowWrite                   bool
}

// Tools returns task graph tools bound to a scheduler. The default is
// readonly child work; callers must opt into write tasks explicitly.
func Tools(s *tasks.Scheduler, parent domain.ID) []agent.Tool {
	return ToolsWithOptions(s, parent, Options{})
}

func ToolsWithOptions(s *tasks.Scheduler, parent domain.ID, options Options) []agent.Tool {
	if s == nil {
		return nil
	}
	return []agent.Tool{
		{Definition: definition("spawn_agent", "Start a bounded child task (or a root task when no parent is bound).", `{"type":"object","properties":{"prompt":{"type":"string","minLength":1,"maxLength":65536},"root":{"type":"string"},"model":{"type":"string"},"profile":{"type":"string"},"mode":{"type":"string","enum":["write","readonly"]},"base_ref":{"type":"string"},"timeout_ms":{"type":"integer","minimum":0,"maximum":86400000},"step_budget":{"type":"integer","minimum":0,"maximum":100},"max_output_bytes":{"type":"integer","minimum":0,"maximum":8388608},"preserve_worktree":{"type":"boolean"}},"required":["prompt"],"additionalProperties":false}`), Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in spawnInput
			if err := decode(raw, &in); err != nil {
				return "", err
			}
			if strings.TrimSpace(in.Prompt) == "" {
				return "", errors.New("tasktools: prompt is required")
			}
			if len(in.Prompt) > maxPrompt {
				return "", errors.New("tasktools: prompt is too large")
			}
			mode := tasks.Mode(in.Mode)
			if mode == "" {
				mode = tasks.ModeReadOnly
			}
			if mode == tasks.ModeWrite && !options.AllowWrite {
				return "", errors.New("tasktools: write tasks require explicit permission")
			}
			root := in.Root
			if root == "" && parent != "" {
				current, statusErr := s.Status(parent)
				if statusErr != nil {
					return "", statusErr
				}
				root = current.RepositoryRoot
				if root == "" {
					root = current.Root
				}
			}
			in.applyDefaults(options)
			spec := tasks.TaskSpec{ParentID: parent, Root: root, Prompt: in.Prompt, Model: in.Model, Profile: in.Profile, Mode: mode, BaseRef: in.BaseRef, Timeout: time.Duration(in.TimeoutMS) * time.Millisecond, StepBudget: in.StepBudget, MaxOutputBytes: in.MaxOutputBytes, PreserveWorktree: in.PreserveWorktree}
			var task tasks.Task
			var err error
			if parent == "" {
				task, err = s.SpawnParent(ctx, spec)
			} else {
				task, err = s.SpawnChild(ctx, parent, spec)
			}
			if err != nil {
				return "", err
			}
			return marshal(task)
		}},
		{Definition: definition("task_status", "Inspect one task summary.", `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), Call: func(_ context.Context, raw json.RawMessage) (string, error) {
			var in idInput
			if err := decode(raw, &in); err != nil {
				return "", err
			}
			id, err := domain.ParseID(in.ID)
			if err != nil {
				return "", err
			}
			out, err := s.StatusFor(parent, id)
			if err != nil {
				return "", err
			}
			return marshal(out)
		}},
		{Definition: definition("task_wait", "Wait for a task to finish, with an optional timeout.", `{"type":"object","properties":{"id":{"type":"string"},"timeout_ms":{"type":"integer","minimum":0,"maximum":86400000}},"required":["id"],"additionalProperties":false}`), Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in waitInput
			if err := decode(raw, &in); err != nil {
				return "", err
			}
			id, err := domain.ParseID(in.ID)
			if err != nil {
				return "", err
			}
			waitCtx := ctx
			if in.TimeoutMS < 0 || in.TimeoutMS > 86400000 {
				return "", errors.New("tasktools: timeout_ms must be 0..86400000")
			}
			if ctx == nil {
				ctx = context.Background()
			}
			if in.TimeoutMS > 0 {
				var cancel context.CancelFunc
				waitCtx, cancel = context.WithTimeout(ctx, time.Duration(in.TimeoutMS)*time.Millisecond)
				defer cancel()
			}
			var out tasks.Summary
			if parent != "" {
				out, err = s.WaitChild(waitCtx, parent, id)
			} else {
				out, err = s.Wait(waitCtx, id)
			}
			if err != nil {
				return "", err
			}
			return marshal(out)
		}},
		{Definition: definition("task_cancel", "Cancel a task and all of its descendants.", `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`), Call: func(_ context.Context, raw json.RawMessage) (string, error) {
			var in idInput
			if err := decode(raw, &in); err != nil {
				return "", err
			}
			id, err := domain.ParseID(in.ID)
			if err != nil {
				return "", err
			}
			if err := s.CancelFor(parent, id); err != nil {
				return "", err
			}
			return marshal(map[string]any{"id": id, "canceled": true})
		}},
	}
}

type spawnInput struct {
	Prompt           string `json:"prompt"`
	Root             string `json:"root"`
	Model            string `json:"model"`
	Profile          string `json:"profile"`
	Mode             string `json:"mode"`
	BaseRef          string `json:"base_ref"`
	TimeoutMS        int    `json:"timeout_ms"`
	StepBudget       int    `json:"step_budget"`
	MaxOutputBytes   int    `json:"max_output_bytes"`
	PreserveWorktree bool   `json:"preserve_worktree"`
}

func (in *spawnInput) applyDefaults(options Options) {
	requestedProfile := in.Profile
	if in.Profile == "" {
		in.Profile = options.DefaultProfile
	}
	if in.Model == "" && (requestedProfile == "" || requestedProfile == options.DefaultProfile) {
		in.Model = options.DefaultModel
	}
}

type idInput struct {
	ID string `json:"id"`
}
type waitInput struct {
	ID        string `json:"id"`
	TimeoutMS int    `json:"timeout_ms"`
}

func definition(name, description, schema string) provider.ToolDefinition {
	return provider.ToolDefinition{Name: name, Description: description, Parameters: json.RawMessage(schema)}
}

func decode(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || len(raw) > maxArgs || string(raw) == "null" {
		return errors.New("tasktools: arguments must be a JSON object")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("tasktools: invalid arguments: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("tasktools: arguments must contain one JSON object")
	}
	return nil
}

func marshal(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

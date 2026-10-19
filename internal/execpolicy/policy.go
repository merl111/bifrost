// Package execpolicy authorizes individual agent shell executions. It is not an OS sandbox.
package execpolicy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
)

type Settings struct {
	Mode  string     `json:"mode"`
	Allow [][]string `json:"allow,omitempty"`
}

func (s Settings) Validate() error {
	if s.Mode != "" && s.Mode != "allow" && s.Mode != "ask" && s.Mode != "rules" {
		return errors.New("execution.mode must be allow, ask or rules")
	}
	if len(s.Allow) > 128 {
		return errors.New("too many execution rules")
	}
	for _, argv := range s.Allow {
		if len(argv) == 0 || len(argv) > 128 {
			return errors.New("execution rules must contain 1..128 arguments")
		}
		for _, arg := range argv {
			if strings.ContainsRune(arg, 0) || len(arg) > 4096 {
				return errors.New("invalid execution rule argument")
			}
		}
	}
	return nil
}

type Request struct {
	Argv      []string
	Root, CWD string
	Reply     chan bool
	Done      <-chan struct{}
}
type Confirm func(*Request)
type Authorizer func(context.Context, []string, string, string) error

func (s Settings) Authorizer(confirm Confirm) Authorizer {
	return func(ctx context.Context, argv []string, root, cwd string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Validate(); err != nil {
			return err
		}
		if s.Mode == "" || s.Mode == "allow" {
			return nil
		}
		if s.Mode == "rules" && s.matches(argv) {
			return nil
		}
		if confirm == nil {
			return errors.New("command needs approval; run in an interactive workspace or configure execution rules")
		}
		request := &Request{Argv: slices.Clone(argv), Root: root, CWD: cwd, Reply: make(chan bool, 1), Done: ctx.Done()}
		confirm(request)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case allowed := <-request.Reply:
			if err := ctx.Err(); err != nil {
				return err
			}
			if !allowed {
				return fmt.Errorf("command denied by user")
			}
			return nil
		}
	}
}
func (s Settings) matches(argv []string) bool {
	for _, allowed := range s.Allow {
		if slices.Equal(argv, allowed) {
			return true
		}
	}
	// Only these exact argument vectors are built-in rules. Shell scripts, pipelines,
	// substitutions and interpreter commands require explicit approval.
	for _, allowed := range [][]string{{"pwd"}, {"ls"}, {"ls", "-la"}, {"rg", "--files"}} {
		if slices.Equal(argv, allowed) {
			return true
		}
	}
	return false
}

// WithLifetime prevents detached shell requests from approving after their turn ends.
func WithLifetime(lifetime context.Context, authorize Authorizer) Authorizer {
	return func(ctx context.Context, argv []string, root, cwd string) error {
		bounded, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		defer cancel()
		if err := lifetime.Err(); err != nil {
			return err
		}
		err := authorize(bounded, argv, root, cwd)
		if ended := lifetime.Err(); ended != nil {
			return ended
		}
		return err
	}
}

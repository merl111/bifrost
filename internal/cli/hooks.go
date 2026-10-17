package cli

import (
	"context"
	"errors"

	"bifrost/internal/agent"
	"bifrost/internal/hooks"
)

// configureHooks connects the trusted, machine-local hook settings to the
// agent callback. Hooks are trusted only because they come from the explicit
// machine-local settings file; command execution still requires ExecEnabled.
func configureHooks(opts options, cfg *agent.Config) error {
	if cfg == nil {
		return errors.New("agent config is nil")
	}
	settings, err := opts.settings()
	if err != nil {
		return err
	}

	if len(settings.Hooks) == 0 {
		return nil
	}
	if !cfg.ExecEnabled {
		return errors.New("configured hooks require --allow-exec")
	}
	runner, err := hooks.New(hooks.Config{Trusted: true, Hooks: settings.Hooks})
	if err != nil {
		return err
	}
	cfg.RunHook = func(ctx context.Context, event string, input any) error {
		_, err := runner.Run(ctx, hooks.Event(event), input)
		return err
	}
	return nil
}

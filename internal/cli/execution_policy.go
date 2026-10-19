package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/execpolicy"
	"bifrost/internal/ui"
	"context"
	"errors"
)

func configureExecutionPolicy(cfg *agent.Config, settings config.Settings, confirm execpolicy.Confirm) error {
	cfg.RequireExecApproval = settings.Execution.Mode != "" && settings.Execution.Mode != "allow"
	if cfg.ExecEnabled && cfg.RunExternal != nil && !cfg.ExternalToolBridge && cfg.RequireExecApproval {
		return errors.New("command approval requires a native provider or Cursor Native; vendor CLI tools cannot be intercepted")
	}
	cfg.AuthorizeExec = settings.Execution.Authorizer(confirm)
	return nil
}
func prepareWorkspaceTurn(ctx context.Context, opts options, cfg agent.Config, emit func(ui.ChatEvent)) (agent.Config, error) {
	settings, err := opts.settings()
	if err != nil {
		return cfg, err
	}
	if err = configureExecutionPolicy(&cfg, settings, func(request *execpolicy.Request) { emit(ui.ChatEvent{Kind: "approval", Approval: request}) }); err != nil {
		return cfg, err
	}
	cfg.AuthorizeExec = execpolicy.WithLifetime(ctx, cfg.AuthorizeExec)
	if err = loadDefaultSkills(opts, &cfg); err != nil {
		return cfg, err
	}
	applyWorkspaceProjectContext(opts, &cfg)
	attachWorkspaceViewerTools(&cfg, emit)
	return cfg, nil
}

package agent

import (
	"bifrost/internal/environment"
	"context"
	"encoding/json"
	"errors"
	"time"
)

// runAgentCommand checks capability and policy before starting the command deadline.
func runAgentCommand(ctx context.Context, cfg Config, w environment.Backend, args map[string]json.RawMessage, limit int) string {
	if !cfg.ExecEnabled {
		return toolErrorLimit(errors.New("command execution is disabled"), limit)
	}
	var argv []string
	rawArgv, ok := args["argv"]
	if !ok || json.Unmarshal(rawArgv, &argv) != nil || len(argv) == 0 {
		return toolErrorLimit(errors.New("argv is required"), limit)
	}
	if _, ok := args["cwd"]; ok {
		return toolErrorLimit(errors.New("cwd is fixed to the workspace root"), limit)
	}
	if cfg.AuthorizeExec != nil {
		if err := cfg.AuthorizeExec(ctx, argv, cfg.Root, ""); err != nil {
			return toolErrorLimit(err, limit)
		}
	}
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	res, err := w.Exec(commandCtx, environment.ExecRequest{Argv: argv, MaxOutputBytes: limit})
	if err != nil {
		return toolErrorLimit(err, limit)
	}
	return boundedJSON(map[string]any{"argv": res.Argv, "exit_code": res.ExitCode, "stdout": string(res.Stdout), "stderr": string(res.Stderr), "stdout_truncated": res.StdoutTruncated, "stderr_truncated": res.StderrTruncated, "cancelled": res.Cancelled}, limit)
}

// runExternalTurn also covers profiles selected by subagents and decision routing.
func runExternalTurn(ctx context.Context, cfg Config, prompt string) (Result, error) {
	if cfg.ExecEnabled && cfg.RequireExecApproval && !cfg.ExternalToolBridge {
		return Result{}, errors.New("vendor CLI command execution cannot use Bifrost approval policy")
	}
	return cfg.RunExternal(ctx, cfg, prompt)
}

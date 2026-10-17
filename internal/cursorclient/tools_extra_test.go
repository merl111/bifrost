package cursorclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bifrost/internal/process"
)

func TestPreviouslyDeniedToolsReturnResults(t *testing.T) {
	env := &ToolEnv{Root: t.TempDir(), Files: denyFiles{}, AllowExec: true}
	names := []string{
		"diagnostics_args", "canvas_diagnostics_args", "mcp_args", "list_mcp_resources_exec_args",
		"read_mcp_resource_exec_args", "mcp_state_exec_args", "record_screen_args", "computer_use_args",
		"write_shell_stdin_args", "force_background_shell_args", "force_background_subagent_args",
		"subagent_args", "subagent_await_args", "execute_hook_args", "smart_mode_classifier_args",
		"shell_allowlist_precheck_args", "mcp_allowlist_precheck_args", "web_fetch_allowlist_precheck_args",
		"git_diff_request", "conversation_search_args", "agent_store_conflict_args", "adopt_args",
	}
	for _, name := range names {
		req := execRequest(t, name)
		reply := env.exec(context.Background(), req)
		raw, err := reply.Encode()
		if err != nil {
			t.Fatalf("%s encode: %v", name, err)
		}
		if strings.Contains(string(raw), "this Cursor tool is not executed") {
			t.Fatalf("%s is still a generic denial", name)
		}
	}
}

func TestFetchToolReadsHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello fetch"))
	}))
	defer srv.Close()
	env := &ToolEnv{Root: t.TempDir(), Files: denyFiles{}}
	args, _ := New("FetchArgs")
	args.Set("url", srv.URL)
	req := execRequest(t, "fetch_args")
	req.Set("fetch_args", args)
	reply := env.exec(context.Background(), req)
	success := reply.Message("exec_client_message").Message("fetch_result").Message("success")
	if success.String("content") != "hello fetch" || success.Int("status_code") != 200 {
		t.Fatalf("fetch = %#v", success.fields)
	}
}

func TestGitDiffUsesWorkspaceGit(t *testing.T) {
	env := &ToolEnv{Root: t.TempDir(), Files: gitFiles{}, AllowExec: true}
	reply := env.exec(context.Background(), execRequest(t, "git_diff_request"))
	response := reply.Message("exec_client_message").Message("git_diff_response")
	if response.String("head_sha") != "abc" || !response.Bool("has_uncommitted_changes") {
		t.Fatalf("diff response = %#v", response.fields)
	}
	if response.Message("diff") == nil || len(response.Message("diff").Messages("diffs")) != 1 {
		t.Fatal("missing git diff")
	}
}

func TestBackgroundShellReturnsID(t *testing.T) {
	env := &ToolEnv{Root: t.TempDir(), Files: scriptFiles{stdout: "bg\n"}, AllowExec: true}
	args, _ := New("BackgroundShellSpawnArgs")
	args.Set("command", "echo bg")
	args.Set("tool_call_id", "call-1")
	req := execRequest(t, "background_shell_spawn_args")
	req.Set("background_shell_spawn_args", args)
	reply := env.exec(context.Background(), req)
	id := reply.Message("exec_client_message").Message("background_shell_spawn_result").Message("success").Int("shell_id")
	if id == 0 {
		t.Fatal("missing shell id")
	}
	forceArgs, _ := New("ForceBackgroundShellArgs")
	forceArgs.Set("tool_call_id", "call-1")
	force := execRequest(t, "force_background_shell_args")
	force.Set("force_background_shell_args", forceArgs)
	forced := env.exec(context.Background(), force)
	if forced.Message("exec_client_message").Message("force_background_shell_result").Int("status") != 1 {
		t.Fatal("background shell was not accepted")
	}
	time.Sleep(20 * time.Millisecond)
}

func TestShellPrecheckFollowsExecPermission(t *testing.T) {
	env := &ToolEnv{Root: t.TempDir(), Files: denyFiles{}}
	reply := env.exec(context.Background(), execRequest(t, "shell_allowlist_precheck_args"))
	if reply.Message("exec_client_message").Message("shell_allowlist_precheck_result").Bool("allowlisted") {
		t.Fatal("exec is disabled")
	}
	env = &ToolEnv{Root: env.Root, Files: denyFiles{}, AllowExec: true}
	reply = env.exec(context.Background(), execRequest(t, "shell_allowlist_precheck_args"))
	if !reply.Message("exec_client_message").Message("shell_allowlist_precheck_result").Bool("allowlisted") {
		t.Fatal("exec is enabled")
	}
}

func execRequest(t *testing.T, name string) *Value {
	t.Helper()
	argsType := map[string]string{
		"diagnostics_args": "DiagnosticsArgs", "canvas_diagnostics_args": "CanvasDiagnosticsArgs",
		"mcp_args": "McpArgs", "list_mcp_resources_exec_args": "ListMcpResourcesExecArgs",
		"read_mcp_resource_exec_args": "ReadMcpResourceExecArgs", "mcp_state_exec_args": "McpStateExecArgs",
		"record_screen_args": "RecordScreenArgs", "computer_use_args": "ComputerUseArgs",
		"write_shell_stdin_args": "WriteShellStdinArgs", "force_background_shell_args": "ForceBackgroundShellArgs",
		"force_background_subagent_args": "ForceBackgroundSubagentArgs", "subagent_args": "SubagentArgs",
		"subagent_await_args": "SubagentAwaitArgs", "execute_hook_args": "ExecuteHookArgs",
		"smart_mode_classifier_args": "SmartModeClassifierArgs", "shell_allowlist_precheck_args": "ShellAllowlistPrecheckArgs",
		"mcp_allowlist_precheck_args": "McpAllowlistPrecheckArgs", "web_fetch_allowlist_precheck_args": "WebFetchAllowlistPrecheckArgs",
		"git_diff_request": "GetDiffRequest", "conversation_search_args": "ConversationSearchArgs",
		"agent_store_conflict_args": "AgentStoreConflictArgs", "adopt_args": "AdoptArgs",
		"fetch_args": "FetchArgs", "background_shell_spawn_args": "BackgroundShellSpawnArgs",
	}[name]
	args, err := New(argsType)
	if err != nil {
		t.Fatal(err)
	}
	req, err := New("ExecServerMessage")
	if err != nil {
		t.Fatal(err)
	}
	req.Set("exec_id", name)
	req.Set(name, args)
	return req
}

type gitFiles struct{ denyFiles }

func (gitFiles) Exec(_ context.Context, argv []string, _ string, _ int) (process.Result, error) {
	if len(argv) < 2 {
		return process.Result{ExitCode: 1}, nil
	}
	switch argv[1] {
	case "diff":
		return process.Result{Stdout: []byte("diff --git a/a b/a\n")}, nil
	case "status":
		return process.Result{Stdout: []byte(" M a\n")}, nil
	case "rev-parse":
		return process.Result{Stdout: []byte("abc\n")}, nil
	default:
		return process.Result{ExitCode: 1}, nil
	}
}

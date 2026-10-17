package cursorclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type backgroundShell struct {
	id         int64
	toolCallID string
	command    string
	cwd        string
	done       chan struct{}
}

func moveResultField(reply *Value, from, to string) *Value {
	msg := reply.Message("exec_client_message")
	if msg == nil || msg.fields == nil {
		return reply
	}
	child, ok := msg.fields[from].(*Value)
	if !ok {
		return reply
	}
	delete(msg.fields, from)
	msg.Set(to, child)
	return reply
}

func oneofReply(req *Value, field, resultType, oneof, childType string, fill func(*Value)) *Value {
	result, err := New(resultType)
	if err != nil {
		return denyExec(req, err.Error())
	}
	child, err := New(childType)
	if err != nil {
		return denyExec(req, err.Error())
	}
	if fill != nil {
		fill(child)
	}
	result.Set(oneof, child)
	return execReply(req, field, result)
}

func (e *ToolEnv) diagnostics(req, args *Value) *Value {
	path := args.String("path")
	return oneofReply(req, "diagnostics_result", "DiagnosticsResult", "error", "DiagnosticsError", func(v *Value) {
		v.Set("path", path)
		v.Set("error", "language-server diagnostics are not connected")
	})
}

func (e *ToolEnv) canvasDiagnostics(req, args *Value) *Value {
	return oneofReply(req, "canvas_diagnostics_result", "CanvasDiagnosticsResult", "success", "CanvasDiagnosticsSuccess", func(v *Value) {
		v.Set("path", args.String("path"))
	})
}

func (e *ToolEnv) mcpCall(req, args *Value) *Value {
	name := args.String("server_identifier")
	if name == "" {
		name = args.String("provider_identifier")
	}
	if name == "" {
		name = args.String("name")
	}
	return oneofReply(req, "mcp_result", "McpResult", "server_not_found", "McpServerNotFound", func(v *Value) {
		v.Set("name", name)
	})
}

func (e *ToolEnv) listMcpResources(req *Value) *Value {
	return oneofReply(req, "list_mcp_resources_exec_result", "ListMcpResourcesExecResult", "success", "ListMcpResourcesSuccess", nil)
}

func (e *ToolEnv) readMcpResource(req, args *Value) *Value {
	return oneofReply(req, "read_mcp_resource_exec_result", "ReadMcpResourceExecResult", "not_found", "ReadMcpResourceNotFound", func(v *Value) {
		v.Set("uri", args.String("uri"))
	})
}

func (e *ToolEnv) mcpState(req *Value) *Value {
	return oneofReply(req, "mcp_state_exec_result", "McpStateExecResult", "success", "McpStateSuccess", nil)
}

func (e *ToolEnv) fetch(ctx context.Context, req, args *Value) *Value {
	rawURL := strings.TrimSpace(args.String("url"))
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return oneofReply(req, "fetch_result", "FetchResult", "error", "FetchError", func(v *Value) {
			v.Set("url", rawURL)
			v.Set("error", "only http and https URLs can be fetched")
		})
	}
	callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(callCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return oneofReply(req, "fetch_result", "FetchResult", "error", "FetchError", func(v *Value) {
			v.Set("url", rawURL)
			v.Set("error", err.Error())
		})
	}
	httpReq.Header.Set("Accept", "text/plain, text/markdown, application/json, text/html;q=0.5")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return oneofReply(req, "fetch_result", "FetchResult", "error", "FetchError", func(v *Value) {
			v.Set("url", rawURL)
			v.Set("error", err.Error())
		})
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err != nil {
		return oneofReply(req, "fetch_result", "FetchResult", "error", "FetchError", func(v *Value) {
			v.Set("url", rawURL)
			v.Set("error", err.Error())
		})
	}
	content := string(body)
	if len(body) == 256<<10 {
		content += "\n[response truncated]"
	}
	return oneofReply(req, "fetch_result", "FetchResult", "success", "FetchSuccess", func(v *Value) {
		v.Set("url", rawURL)
		v.Set("content", content)
		v.Set("status_code", int64(resp.StatusCode))
		v.Set("content_type", resp.Header.Get("Content-Type"))
	})
}

func (e *ToolEnv) recordScreen(req *Value) *Value {
	return oneofReply(req, "record_screen_result", "RecordScreenResult", "failure", "RecordScreenFailure", func(v *Value) {
		v.Set("error", "screen recording is not available in this session")
	})
}

func (e *ToolEnv) computerUse(req *Value) *Value {
	return oneofReply(req, "computer_use_result", "ComputerUseResult", "error", "ComputerUseError", func(v *Value) {
		v.Set("error", "computer use is not available in this session")
	})
}

func (e *ToolEnv) backgroundShell(ctx context.Context, req, args *Value) *Value {
	command := args.String("command")
	cwd := args.String("working_directory")
	if !e.AllowExec {
		return oneofReply(req, "background_shell_spawn_result", "BackgroundShellSpawnResult", "permission_denied", "ShellPermissionDenied", func(v *Value) {
			v.Set("command", command)
			v.Set("working_directory", cwd)
			v.Set("error", "command execution is disabled")
		})
	}
	if strings.TrimSpace(command) == "" {
		return oneofReply(req, "background_shell_spawn_result", "BackgroundShellSpawnResult", "rejected", "ShellRejected", func(v *Value) {
			v.Set("command", command)
			v.Set("working_directory", cwd)
			v.Set("reason", "command is empty")
		})
	}
	if _, err := relativePath(e.Root, cwd); err != nil {
		return oneofReply(req, "background_shell_spawn_result", "BackgroundShellSpawnResult", "rejected", "ShellRejected", func(v *Value) {
			v.Set("command", command)
			v.Set("working_directory", cwd)
			v.Set("reason", "working directory is outside the workspace")
		})
	}
	e.shellMu.Lock()
	e.shellSeq++
	id := e.shellSeq
	if e.shells == nil {
		e.shells = map[int64]*backgroundShell{}
	}
	session := &backgroundShell{id: id, toolCallID: args.String("tool_call_id"), command: command, cwd: cwd, done: make(chan struct{})}
	e.shells[id] = session
	e.shellMu.Unlock()
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	go func() {
		defer cancel()
		defer close(session.done)
		_ = e.runShell(runCtx, args)
	}()
	return oneofReply(req, "background_shell_spawn_result", "BackgroundShellSpawnResult", "success", "BackgroundShellSpawnSuccess", func(v *Value) {
		v.Set("shell_id", id)
		v.Set("command", command)
		v.Set("working_directory", cwd)
	})
}

func (e *ToolEnv) writeShellStdin(req, args *Value) *Value {
	id := args.Int("shell_id")
	e.shellMu.Lock()
	_, ok := e.shells[id]
	e.shellMu.Unlock()
	if !ok {
		return oneofReply(req, "write_shell_stdin_result", "WriteShellStdinResult", "error", "WriteShellStdinError", func(v *Value) {
			v.Set("error", "shell is not running")
		})
	}
	return oneofReply(req, "write_shell_stdin_result", "WriteShellStdinResult", "error", "WriteShellStdinError", func(v *Value) {
		v.Set("error", "interactive stdin is not attached to background shells")
	})
}

func (e *ToolEnv) forceBackgroundShell(req, args *Value) *Value {
	status := int64(2)
	e.shellMu.Lock()
	for _, session := range e.shells {
		if args.String("tool_call_id") == "" || session.toolCallID == args.String("tool_call_id") {
			status = 1
			break
		}
	}
	e.shellMu.Unlock()
	result, err := New("ForceBackgroundShellResult")
	if err != nil {
		return denyExec(req, err.Error())
	}
	result.Set("status", status)
	return execReply(req, "force_background_shell_result", result)
}

func (e *ToolEnv) forceBackgroundSubagent(req *Value) *Value {
	result, err := New("ForceBackgroundSubagentResult")
	if err != nil {
		return denyExec(req, err.Error())
	}
	result.Set("status", int64(2))
	return execReply(req, "force_background_subagent_result", result)
}

func (e *ToolEnv) subagent(req, args *Value) *Value {
	return oneofReply(req, "subagent_result", "SubagentResult", "error", "SubagentError", func(v *Value) {
		v.Set("error", "sub-agents are not available in this Cursor session")
		if id := args.String("resume_agent_id"); id != "" {
			v.Set("agent_id", id)
		}
	})
}

func (e *ToolEnv) awaitSubagent(req, args *Value) *Value {
	return oneofReply(req, "subagent_await_result", "SubagentAwaitResult", "not_found", "SubagentAwaitNotFound", func(v *Value) {
		v.Set("agent_id", args.String("agent_id"))
	})
}

func (e *ToolEnv) executeHook(req, args *Value) *Value {
	response, err := New("ExecuteHookResponse")
	if err != nil {
		return denyExec(req, err.Error())
	}
	if inner := args.Message("request"); inner != nil && inner.Oneof("request") == "pre_tool_use" {
		item, err := New("PreToolUseRequestResponse")
		if err == nil {
			item.Set("permission", "allow")
			response.Set("pre_tool_use", item)
		}
	}
	result, err := New("ExecuteHookResult")
	if err != nil {
		return denyExec(req, err.Error())
	}
	result.Set("response", response)
	return execReply(req, "execute_hook_result", result)
}

func (e *ToolEnv) smartMode(req *Value) *Value {
	return oneofReply(req, "smart_mode_classifier_result", "SmartModeClassifierResult", "success", "SmartModeClassifierSuccess", func(v *Value) {
		v.Set("decision", int64(1))
	})
}

func (e *ToolEnv) shellPrecheck(req *Value) *Value {
	return e.boolPrecheck(req, "shell_allowlist_precheck_result", "ShellAllowlistPrecheckResult", e.AllowExec)
}

func (e *ToolEnv) boolPrecheck(req *Value, field, resultType string, allow bool) *Value {
	result, err := New(resultType)
	if err != nil {
		return denyExec(req, err.Error())
	}
	result.Set("allowlisted", allow)
	return execReply(req, field, result)
}

func (e *ToolEnv) gitDiff(ctx context.Context, req, args *Value) *Value {
	response, err := New("GetDiffResponse")
	if err != nil {
		return denyExec(req, err.Error())
	}
	diffText := ""
	head := ""
	dirty := false
	if e.Files != nil {
		limit := 128 << 10
		if n := args.Int("max_response_bytes"); n > 0 && int(n) < limit {
			limit = int(n)
		}
		rel, relErr := relativePath(e.Root, args.String("cwd"))
		if relErr == nil {
			runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if res, err := e.Files.Exec(runCtx, []string{"git", "diff", "--no-ext-diff"}, rel, limit); err == nil && res.ExitCode == 0 {
				diffText = string(res.Stdout)
			}
			if res, err := e.Files.Exec(runCtx, []string{"git", "status", "--porcelain"}, rel, 64<<10); err == nil && res.ExitCode == 0 && strings.TrimSpace(string(res.Stdout)) != "" {
				dirty = true
			}
			if res, err := e.Files.Exec(runCtx, []string{"git", "rev-parse", "HEAD"}, rel, 128); err == nil && res.ExitCode == 0 {
				head = strings.TrimSpace(string(res.Stdout))
			}
		}
	}
	if diffText != "" {
		chunk, err := New("FileDiff_Chunk")
		file, fileErr := New("FileDiff")
		git, gitErr := New("GitDiff")
		if err == nil && fileErr == nil && gitErr == nil {
			chunk.Set("content", diffText)
			file.Set("chunks", []*Value{chunk})
			git.Set("diffs", []*Value{file})
			response.Set("diff", git)
		}
	}
	if head != "" {
		response.Set("head_sha", head)
	}
	response.Set("has_uncommitted_changes", dirty || diffText != "")
	return execReply(req, "git_diff_response", response)
}

func (e *ToolEnv) conversationSearch(req *Value) *Value {
	return oneofReply(req, "conversation_search_result", "ConversationSearchResult", "success", "ConversationSearchSuccess", func(v *Value) {
		v.Set("truncated", false)
	})
}

func (e *ToolEnv) agentStoreConflicts(req *Value) *Value {
	return oneofReply(req, "agent_store_conflict_result", "AgentStoreConflictResult", "success", "AgentStoreConflictSuccess", func(v *Value) {
		v.Set("gap", false)
	})
}

func (e *ToolEnv) adopt(req, args *Value) *Value {
	result, err := New("AdoptResult")
	if err != nil {
		return denyExec(req, err.Error())
	}
	result.Set("source_agent_id", args.String("source_agent_id"))
	result.Set("error", "adopting another agent is not available in this session")
	return execReply(req, "adopt_result", result)
}

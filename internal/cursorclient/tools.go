package cursorclient

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"bifrost/internal/process"
	"bifrost/internal/workspace"
)

// Files is the rooted workspace surface used for Cursor execution requests.
type Files interface {
	List(context.Context, string) ([]workspace.Entry, error)
	Read(context.Context, string) (string, workspace.Revision, error)
	Search(context.Context, string) (workspace.SearchResult, error)
	Write(context.Context, string, *workspace.Revision, string) (workspace.Change, error)
	Exec(context.Context, []string, string, int) (process.Result, error)
}

// Deleter removes a rooted file and returns its previous text.
type Deleter interface {
	Delete(context.Context, string) (string, error)
}

// ToolEnv bounds local execution for one Cursor turn.
type ToolEnv struct {
	Root       string
	Files      Files
	AllowWrite bool
	AllowExec  bool
	MaxOutput  int
	blobs      map[string][]byte
	seen       map[string]struct{}
	shellMu    sync.Mutex
	shells     map[int64]*backgroundShell
	shellSeq   int64
}

func (e *ToolEnv) prepare() {
	if e.blobs == nil {
		e.blobs = map[string][]byte{}
	}
	if e.seen == nil {
		e.seen = map[string]struct{}{}
	}
	if e.MaxOutput <= 0 || e.MaxOutput > 1<<20 {
		e.MaxOutput = 64 << 10
	}
}

func (e *ToolEnv) handle(ctx context.Context, msg *Value) ([]*Value, []byte, bool, Event, bool) {
	e.prepare()
	switch msg.Oneof("message") {
	case "exec_server_message":
		req := msg.Message("exec_server_message")
		if req != nil && req.Oneof("message") == "shell_stream_args" {
			replies := e.shellStream(ctx, req, req.Message("shell_stream_args"))
			return replies, nil, false, Event{}, len(replies) > 0
		}
		reply := e.exec(ctx, req)
		if reply == nil {
			return nil, nil, false, Event{}, false
		}
		return []*Value{reply}, nil, false, Event{}, true
	case "exec_server_control_message":
		return nil, nil, false, Event{}, false
	case "conversation_checkpoint_update":
		raw, err := msg.Message("conversation_checkpoint_update").Encode()
		if err != nil || len(raw) > 4<<20 {
			return nil, nil, false, Event{}, false
		}
		return nil, raw, false, Event{}, false
	case "kv_server_message":
		return []*Value{e.kv(msg.Message("kv_server_message"))}, nil, false, Event{}, true
	case "interaction_query":
		return []*Value{e.interaction(msg.Message("interaction_query"))}, nil, false, Event{Kind: "status", Text: "declined a Cursor interaction request"}, true
	case "interaction_update":
		ev, ended := interactionEvent(msg.Message("interaction_update"))
		return nil, nil, ended, ev, false
	default:
		return nil, nil, false, Event{}, false
	}
}

func (e *ToolEnv) exec(ctx context.Context, req *Value) *Value {
	e.prepare()
	if req == nil {
		return nil
	}
	name := req.Oneof("message")
	if name == "" || name == "span_context" || name == "accept_hook_additional_contexts" {
		return nil
	}
	if key := execKey(req); key != "" {
		if _, ok := e.seen[key]; ok {
			return denyExec(req, "duplicate execution id")
		}
		e.seen[key] = struct{}{}
	}
	args := req.Message(name)
	switch name {
	case "shell_args", "mini_swe_agent_bash_args":
		return e.shell(ctx, req, args)
	case "pi_bash_args":
		return e.piBash(ctx, req, args)
	case "read_args":
		return e.read(ctx, req, args)
	case "redacted_read_args":
		return moveResultField(e.read(ctx, req, args), "read_result", "redacted_read_result")
	case "pi_read_args":
		return e.piRead(ctx, req, args)
	case "write_args":
		return e.write(ctx, req, args, args.String("file_text"))
	case "pi_write_args":
		return e.piWrite(ctx, req, args)
	case "pi_edit_args":
		return e.piEdit(ctx, req, args)
	case "delete_args":
		return e.delete(ctx, req, args)
	case "grep_args":
		return e.grep(ctx, req, args)
	case "pi_grep_args":
		return e.piGrep(ctx, req, args)
	case "ls_args":
		return e.ls(ctx, req, args)
	case "request_context_args":
		return e.requestContext(ctx, req, args)
	case "diagnostics_args":
		return e.diagnostics(req, args)
	case "canvas_diagnostics_args":
		return e.canvasDiagnostics(req, args)
	case "mcp_args":
		return e.mcpCall(req, args)
	case "list_mcp_resources_exec_args":
		return e.listMcpResources(req)
	case "read_mcp_resource_exec_args":
		return e.readMcpResource(req, args)
	case "mcp_state_exec_args":
		return e.mcpState(req)
	case "fetch_args":
		return e.fetch(ctx, req, args)
	case "record_screen_args":
		return e.recordScreen(req)
	case "computer_use_args":
		return e.computerUse(req)
	case "background_shell_spawn_args":
		return e.backgroundShell(ctx, req, args)
	case "write_shell_stdin_args":
		return e.writeShellStdin(req, args)
	case "force_background_shell_args":
		return e.forceBackgroundShell(req, args)
	case "force_background_subagent_args":
		return e.forceBackgroundSubagent(req)
	case "subagent_args":
		return e.subagent(req, args)
	case "subagent_await_args":
		return e.awaitSubagent(req, args)
	case "execute_hook_args":
		return e.executeHook(req, args)
	case "smart_mode_classifier_args":
		return e.smartMode(req)
	case "shell_allowlist_precheck_args":
		return e.shellPrecheck(req)
	case "mcp_allowlist_precheck_args":
		return e.boolPrecheck(req, "mcp_allowlist_precheck_result", "McpAllowlistPrecheckResult", true)
	case "web_fetch_allowlist_precheck_args":
		return e.boolPrecheck(req, "web_fetch_allowlist_precheck_result", "WebFetchAllowlistPrecheckResult", true)
	case "git_diff_request":
		return e.gitDiff(ctx, req, args)
	case "conversation_search_args":
		return e.conversationSearch(req)
	case "agent_store_conflict_args":
		return e.agentStoreConflicts(req)
	case "adopt_args":
		return e.adopt(req, args)
	case "pi_ls_args":
		text, err := e.listText(ctx, args.String("path"), int(args.Int("limit")))
		return e.piText(ctx, req, text, err)
	case "pi_find_args":
		text, err := e.findText(ctx, args)
		return e.piText(ctx, req, text, err)
	default:
		return denyExec(req, "this Cursor tool is not executed by Bifrost")
	}
}

func (e *ToolEnv) requestContext(ctx context.Context, req, args *Value) *Value {
	envMsg, err := New("RequestContextEnv")
	if err != nil {
		return denyExec(req, "request context is unavailable")
	}
	root := filepath.Clean(e.Root)
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" {
		shell = "/bin/sh"
	}
	zone := time.Local.String()
	if zone == "" || zone == "Local" {
		if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
			zone = tz
		} else {
			zone = "UTC"
		}
	}
	envMsg.Set("os_version", runtime.GOOS+"/"+runtime.GOARCH)
	envMsg.Set("shell", shell)
	envMsg.Set("sandbox_enabled", false)
	envMsg.Set("sandbox_supported", false)
	envMsg.Set("computer_use_supported", false)
	envMsg.Set("time_zone", zone)
	if root != "" && root != "." {
		envMsg.Set("workspace_paths", []string{root})
		envMsg.Set("project_folder", root)
		envMsg.Set("process_working_directory", root)
		if id := notesSessionID(args); id != "" {
			envMsg.Set("agent_conversation_notes_folder", filepath.Join(root, "agent-notes", id))
			envMsg.Set("agent_shared_notes_folder", filepath.Join(root, "agent-notes", "shared"))
		}
	}
	contextMsg, err := New("RequestContext")
	if err != nil {
		return denyExec(req, "request context is unavailable")
	}
	contextMsg.Set("env", envMsg)
	if repo := e.gitRepo(ctx); repo != nil {
		contextMsg.Set("git_repos", []*Value{repo})
	}
	for _, name := range []string{
		"git_repo_info_complete",
		"git_status_info_complete",
		"rules_info_complete",
		"env_info_complete",
		"mcp_info_complete",
		"repository_info_complete",
		"custom_subagents_info_complete",
		"agent_skills_info_complete",
		"mcp_file_system_info_complete",
	} {
		contextMsg.Set(name, true)
	}
	success, err := New("RequestContextSuccess")
	if err != nil {
		return denyExec(req, "request context is unavailable")
	}
	success.Set("request_context", contextMsg)
	result, err := New("RequestContextResult")
	if err != nil {
		return denyExec(req, "request context is unavailable")
	}
	result.Set("success", success)
	return execReply(req, "request_context_result", result)
}

func (e *ToolEnv) gitRepo(ctx context.Context) *Value {
	if e.Files == nil || strings.TrimSpace(e.Root) == "" {
		return nil
	}
	branch := ""
	status := ""
	if e.AllowExec {
		runCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if res, err := e.Files.Exec(runCtx, []string{"git", "rev-parse", "--abbrev-ref", "HEAD"}, ".", 4096); err == nil && res.ExitCode == 0 {
			branch = strings.TrimSpace(string(res.Stdout))
			if branch == "HEAD" {
				branch = ""
			}
		}
		if res, err := e.Files.Exec(runCtx, []string{"git", "status", "--short", "--branch"}, ".", e.MaxOutput); err == nil && res.ExitCode == 0 {
			status = strings.TrimSpace(string(res.Stdout))
		}
	}
	if branch == "" {
		if text, _, err := e.Files.Read(ctx, ".git/HEAD"); err == nil {
			line := strings.TrimSpace(text)
			const prefix = "ref: refs/heads/"
			if strings.HasPrefix(line, prefix) {
				branch = strings.TrimPrefix(line, prefix)
			}
		}
	}
	if branch == "" && status == "" {
		return nil
	}
	repo, err := New("GitRepoInfo")
	if err != nil {
		return nil
	}
	repo.Set("path", filepath.Clean(e.Root))
	if branch != "" {
		repo.Set("branch_name", branch)
	}
	if status != "" {
		repo.Set("status", status)
	}
	return repo
}

func notesSessionID(args *Value) string {
	if args == nil {
		return ""
	}
	id := strings.TrimSpace(args.String("notes_session_id"))
	if id == "" || len(id) > 80 {
		return ""
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return ""
		}
	}
	return id
}

func (e *ToolEnv) shell(ctx context.Context, req, args *Value) *Value {
	return shellOutcome(req, e.runShell(ctx, args))
}

func (e *ToolEnv) shellStream(ctx context.Context, req, args *Value) []*Value {
	e.prepare()
	if req != nil {
		if key := execKey(req); key != "" {
			if _, ok := e.seen[key]; ok {
				return []*Value{denyExec(req, "duplicate execution id")}
			}
			e.seen[key] = struct{}{}
		}
	}
	return shellStreamMessages(req, e.runShell(ctx, args))
}

type shellRun struct {
	kind, command, cwd, reason, stdout, stderr string
	elapsed, exitCode                          int
}

func (e *ToolEnv) runShell(ctx context.Context, args *Value) shellRun {
	command := args.String("command")
	cwd := args.String("working_directory")
	ran := shellRun{command: command, cwd: cwd}
	if !e.AllowExec {
		ran.kind = "permission_denied"
		ran.reason = "command execution is disabled"
		return ran
	}
	if strings.TrimSpace(command) == "" {
		ran.kind = "rejected"
		ran.reason = "command is empty"
		return ran
	}
	rel, err := relativePath(e.Root, cwd)
	if err != nil {
		ran.kind = "rejected"
		ran.reason = "working directory is outside the workspace"
		return ran
	}
	timeout := boundedTimeout(args)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	started := time.Now()
	res, err := e.Files.Exec(runCtx, []string{"sh", "-c", command}, rel, e.MaxOutput)
	ran.elapsed = int(time.Since(started) / time.Millisecond)
	ran.stdout, ran.stderr = string(res.Stdout), string(res.Stderr)
	ran.exitCode = res.ExitCode
	if res.StdoutTruncated || res.StderrTruncated {
		ran.stderr += "\n[output truncated]"
	}
	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		ran.kind = "timeout"
		ran.elapsed = int(timeout / time.Millisecond)
		ran.exitCode = 124
	case err != nil && res.ExitCode < 0:
		ran.kind = "spawn_error"
		ran.reason = err.Error()
		ran.exitCode = 127
	case err != nil:
		ran.kind = "failure"
		ran.reason = err.Error()
		if ran.exitCode < 0 {
			ran.exitCode = 1
		}
	case res.ExitCode != 0:
		ran.kind = "failure"
	default:
		ran.kind = "success"
		ran.exitCode = 0
	}
	return ran
}

func shellStreamMessages(req *Value, ran shellRun) []*Value {
	switch ran.kind {
	case "permission_denied":
		item, _ := New("ShellPermissionDenied")
		item.Set("command", ran.command)
		item.Set("working_directory", ran.cwd)
		item.Set("error", ran.reason)
		event, _ := New("ShellStream")
		event.Set("permission_denied", item)
		return []*Value{execReply(req, "shell_stream", event)}
	case "rejected":
		item, _ := New("ShellRejected")
		item.Set("command", ran.command)
		item.Set("working_directory", ran.cwd)
		item.Set("reason", ran.reason)
		event, _ := New("ShellStream")
		event.Set("rejected", item)
		return []*Value{execReply(req, "shell_stream", event)}
	}
	var out []*Value
	start, _ := New("ShellStreamStart")
	started, _ := New("ShellStream")
	started.Set("start", start)
	out = append(out, execReply(req, "shell_stream", started))
	if ran.stdout != "" {
		chunk, _ := New("ShellStreamStdout")
		chunk.Set("data", ran.stdout)
		event, _ := New("ShellStream")
		event.Set("stdout", chunk)
		out = append(out, execReply(req, "shell_stream", event))
	}
	if ran.stderr != "" || ran.reason != "" {
		text := ran.stderr
		if ran.reason != "" {
			if text != "" {
				text += "\n"
			}
			text += ran.reason
		}
		chunk, _ := New("ShellStreamStderr")
		chunk.Set("data", text)
		event, _ := New("ShellStream")
		event.Set("stderr", chunk)
		out = append(out, execReply(req, "shell_stream", event))
	}
	exit, _ := New("ShellStreamExit")
	exit.Set("code", int64(ran.exitCode))
	exit.Set("cwd", ran.cwd)
	exit.Set("local_execution_time_ms", int64(ran.elapsed))
	if ran.kind == "timeout" {
		exit.Set("aborted", true)
	}
	event, _ := New("ShellStream")
	event.Set("exit", exit)
	out = append(out, execReply(req, "shell_stream", event))
	return out
}

func (e *ToolEnv) piBash(ctx context.Context, req, args *Value) *Value {
	if !e.AllowExec {
		return piResult(req, false, "command execution is disabled")
	}
	command := args.String("command")
	if strings.TrimSpace(command) == "" {
		return piResult(req, false, "command is empty")
	}
	timeout := boundedTimeout(args)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	res, err := e.Files.Exec(runCtx, []string{"sh", "-c", command}, ".", e.MaxOutput)
	text := string(res.Stdout)
	if res.Stderr != nil {
		if text != "" {
			text += "\n"
		}
		text += string(res.Stderr)
	}
	if err != nil && res.ExitCode < 0 {
		return piResult(req, false, err.Error())
	}
	if res.ExitCode != 0 {
		return piResult(req, false, fmt.Sprintf("exit %d\n%s", res.ExitCode, text))
	}
	return piResult(req, true, text)
}

func (e *ToolEnv) read(ctx context.Context, req, args *Value) *Value {
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return readOutcome(req, "rejected", args.String("path"), "path is outside the workspace", "")
	}
	text, err := e.readText(ctx, rel)
	if err != nil {
		return readErr(req, rel, err)
	}
	offset := int(args.Int("offset"))
	limit := int(args.Int("limit"))
	body, lines, truncated := sliceLines(text, offset, limit)
	result, _ := New("ReadResult")
	ok, _ := New("ReadSuccess")
	ok.Set("path", rel)
	ok.Set("content", body)
	ok.Set("total_lines", int64(lines))
	ok.Set("file_size", int64(len(text)))
	if truncated {
		ok.Set("truncated", true)
	}
	if offset > 0 || limit > 0 {
		ok.Set("range_applied", true)
	}
	result.Set("success", ok)
	return execReply(req, "read_result", result)
}

func (e *ToolEnv) piRead(ctx context.Context, req, args *Value) *Value {
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return piResult(req, false, "path is outside the workspace")
	}
	text, err := e.readText(ctx, rel)
	if err != nil {
		return piResult(req, false, err.Error())
	}
	body, _, _ := sliceLines(text, int(args.Int("offset")), int(args.Int("limit")))
	return piResult(req, true, body)
}

func (e *ToolEnv) write(ctx context.Context, req, args *Value, content string) *Value {
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return writeOutcome(req, "rejected", args.String("path"), "path is outside the workspace")
	}
	if !e.AllowWrite {
		return writeOutcome(req, "permission_denied", rel, "file writes are disabled")
	}
	if len(args.Bytes("file_bytes")) > 0 && content == "" {
		return writeOutcome(req, "rejected", rel, "binary writes are not accepted")
	}
	change, err := e.writeText(ctx, rel, content)
	if err != nil {
		kind := "error"
		if errors.Is(err, workspace.ErrBinary) {
			kind = "rejected"
		}
		return writeOutcome(req, kind, rel, err.Error())
	}
	result, _ := New("WriteResult")
	ok, _ := New("WriteSuccess")
	ok.Set("path", rel)
	ok.Set("lines_created", int64(strings.Count(content, "\n")+1))
	ok.Set("file_size", int64(len(content)))
	if args.Bool("return_file_content_after_write") {
		ok.Set("file_content_after_write", content)
	}
	_ = change
	result.Set("success", ok)
	return execReply(req, "write_result", result)
}

func (e *ToolEnv) piWrite(ctx context.Context, req, args *Value) *Value {
	if !e.AllowWrite {
		return piResult(req, false, "file writes are disabled")
	}
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return piResult(req, false, "path is outside the workspace")
	}
	if _, err := e.writeText(ctx, rel, args.String("content")); err != nil {
		return piResult(req, false, err.Error())
	}
	return piResult(req, true, "wrote "+rel)
}

func (e *ToolEnv) piEdit(ctx context.Context, req, args *Value) *Value {
	if !e.AllowWrite {
		return piResult(req, false, "file edits are disabled")
	}
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return piResult(req, false, "path is outside the workspace")
	}
	text, err := e.readText(ctx, rel)
	if err != nil {
		return piResult(req, false, err.Error())
	}
	original := text
	for _, edit := range args.Messages("edits") {
		old, newText := edit.String("old_text"), edit.String("new_text")
		if old == "" || !strings.Contains(text, old) {
			return piResult(req, false, "edit text was not found")
		}
		if strings.Count(text, old) != 1 {
			return piResult(req, false, "edit text matched more than once")
		}
		text = strings.Replace(text, old, newText, 1)
	}
	if _, err := e.writeText(ctx, rel, text); err != nil {
		return piResult(req, false, err.Error())
	}
	return piResult(req, true, unifiedSnippet(rel, original, text))
}

func (e *ToolEnv) delete(ctx context.Context, req, args *Value) *Value {
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return deleteOutcome(req, "rejected", args.String("path"), "path is outside the workspace", "")
	}
	if !e.AllowWrite {
		return deleteOutcome(req, "rejected", rel, "file deletion is disabled", "")
	}
	deleter, ok := e.Files.(Deleter)
	if !ok {
		return deleteOutcome(req, "rejected", rel, "this environment cannot delete files", "")
	}
	prev, err := deleter.Delete(ctx, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return deleteOutcome(req, "file_not_found", rel, "", "")
		}
		return deleteOutcome(req, "error", rel, err.Error(), "")
	}
	result, _ := New("DeleteResult")
	okMsg, _ := New("DeleteSuccess")
	okMsg.Set("path", rel)
	okMsg.Set("deleted_file", rel)
	okMsg.Set("file_size", int64(len(prev)))
	okMsg.Set("prev_content", prev)
	result.Set("success", okMsg)
	return execReply(req, "delete_result", result)
}

func (e *ToolEnv) grep(ctx context.Context, req, args *Value) *Value {
	pattern := args.String("pattern")
	if pattern == "" {
		return grepError(req, "pattern is empty")
	}
	res, err := e.Files.Search(ctx, pattern)
	if err != nil {
		return grepError(req, err.Error())
	}
	limit := int(args.Int("head_limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	glob := args.String("glob")
	prefix, _ := relativePath(e.Root, args.String("path"))
	files := map[string]*Value{}
	var total int
	for _, match := range res.Matches {
		if prefix != "" && prefix != "." && match.Path != prefix && !strings.HasPrefix(match.Path, prefix+"/") {
			continue
		}
		if glob != "" {
			ok, _ := filepath.Match(glob, filepath.Base(match.Path))
			if !ok {
				continue
			}
		}
		if total >= limit {
			break
		}
		total++
		file := files[match.Path]
		if file == nil {
			file, _ = New("GrepFileMatch")
			file.Set("file", match.Path)
			files[match.Path] = file
		}
		line, _ := New("GrepContentMatch")
		line.Set("line_number", int64(match.Line))
		line.Set("content", match.Text)
		file.Set("matches", append(file.Messages("matches"), line))
	}
	mode := args.String("output_mode")
	if mode == "" {
		mode = "content"
	}
	union, _ := New("GrepUnionResult")
	switch mode {
	case "files_with_matches", "files":
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		filesResult, _ := New("GrepFilesResult")
		filesResult.Set("files", names)
		filesResult.Set("total_files", int64(len(names)))
		union.Set("files", filesResult)
	case "count":
		count, _ := New("GrepCountResult")
		count.Set("total_matches", int64(total))
		count.Set("total_files", int64(len(files)))
		union.Set("count", count)
	default:
		content, _ := New("GrepContentResult")
		var matches []*Value
		for _, file := range files {
			matches = append(matches, file)
		}
		content.Set("matches", matches)
		content.Set("total_matched_lines", int64(total))
		if res.Truncated {
			content.Set("client_truncated", true)
		}
		union.Set("content", content)
	}
	success, _ := New("GrepSuccess")
	success.Set("pattern", pattern)
	success.Set("path", args.String("path"))
	success.Set("output_mode", mode)
	success.Set("workspace_results", map[string]any{".": union})
	result, _ := New("GrepResult")
	result.Set("success", success)
	return execReply(req, "grep_result", result)
}

func (e *ToolEnv) piGrep(ctx context.Context, req, args *Value) *Value {
	res, err := e.Files.Search(ctx, args.String("pattern"))
	if err != nil {
		return piResult(req, false, err.Error())
	}
	var b strings.Builder
	for _, match := range res.Matches {
		fmt.Fprintf(&b, "%s:%d:%s\n", match.Path, match.Line, match.Text)
	}
	return piResult(req, true, b.String())
}

func (e *ToolEnv) ls(ctx context.Context, req, args *Value) *Value {
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return lsOutcome(req, "rejected", args.String("path"), "path is outside the workspace")
	}
	if rel == "" {
		rel = "."
	}
	entries, err := e.Files.List(ctx, rel)
	if err != nil {
		return lsOutcome(req, "error", rel, err.Error())
	}
	node, _ := New("LsDirectoryTreeNode")
	node.Set("abs_path", rel)
	node.Set("children_were_processed", true)
	var files []*Value
	var dirs []*Value
	for _, entry := range entries {
		if ignored(entry.Name, args.Strings("ignore")) {
			continue
		}
		if entry.IsDir {
			child, _ := New("LsDirectoryTreeNode")
			child.Set("abs_path", entry.Path)
			dirs = append(dirs, child)
			continue
		}
		file, _ := New("LsDirectoryTreeNode_File")
		file.Set("name", entry.Name)
		files = append(files, file)
	}
	node.Set("children_dirs", dirs)
	node.Set("children_files", files)
	node.Set("num_files", int64(len(files)))
	success, _ := New("LsSuccess")
	success.Set("directory_tree_root", node)
	result, _ := New("LsResult")
	result.Set("success", success)
	return execReply(req, "ls_result", result)
}

func (e *ToolEnv) listText(ctx context.Context, path string, limit int) (string, error) {
	rel, err := relativePath(e.Root, path)
	if err != nil {
		return "", err
	}
	if rel == "" {
		rel = "."
	}
	entries, err := e.Files.List(ctx, rel)
	if err != nil {
		return "", err
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	var b strings.Builder
	for i, entry := range entries {
		if i >= limit {
			b.WriteString("...\n")
			break
		}
		if entry.IsDir {
			b.WriteString(entry.Name + "/\n")
		} else {
			b.WriteString(entry.Name + "\n")
		}
	}
	return b.String(), nil
}

func (e *ToolEnv) findText(ctx context.Context, args *Value) (string, error) {
	pattern := args.String("pattern")
	rel, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return "", err
	}
	if rel == "" {
		rel = "."
	}
	limit := int(args.Int("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var b strings.Builder
	var n int
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if n >= limit || depth > 8 || ctx.Err() != nil {
			return ctx.Err()
		}
		entries, err := e.Files.List(ctx, dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if n >= limit {
				break
			}
			if pattern == "" || strings.Contains(entry.Name, pattern) {
				fmt.Fprintf(&b, "%s\n", entry.Path)
				n++
			}
			if entry.IsDir {
				if err := walk(entry.Path, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(rel, 0); err != nil && !errors.Is(err, context.Canceled) {
		return "", err
	}
	return b.String(), nil
}

func (e *ToolEnv) piText(ctx context.Context, req *Value, text string, err error) *Value {
	if err != nil {
		return piResult(req, false, err.Error())
	}
	return piResult(req, true, text)
}

func (e *ToolEnv) readText(ctx context.Context, rel string) (string, error) {
	text, _, err := e.Files.Read(ctx, rel)
	return text, err
}

func (e *ToolEnv) writeText(ctx context.Context, rel, content string) (workspace.Change, error) {
	current, rev, err := e.Files.Read(ctx, rel)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return e.Files.Write(ctx, rel, nil, content)
		}
		return workspace.Change{}, err
	}
	_ = current
	return e.Files.Write(ctx, rel, &rev, content)
}

func (e *ToolEnv) kv(req *Value) *Value {
	reply, _ := New("KvClientMessage")
	reply.Set("id", req.Int("id"))
	switch req.Oneof("message") {
	case "get_blob_args":
		id := string(req.Message("get_blob_args").Bytes("blob_id"))
		result, _ := New("GetBlobResult")
		// A missing blob is an empty result. Reporting "blob not found" makes
		// the server end the stream with internal: blob not found.
		if data, ok := e.blobs[id]; ok {
			result.Set("blob_data", append([]byte(nil), data...))
		}
		reply.Set("get_blob_result", result)
	case "set_blob_args":
		args := req.Message("set_blob_args")
		id := string(args.Bytes("blob_id"))
		result, _ := New("SetBlobResult")
		if err := e.rememberBlob(id, args.Bytes("blob_data")); err != nil {
			errMsg, _ := New("Error2")
			errMsg.Set("message", err.Error())
			result.Set("error", errMsg)
		}
		reply.Set("set_blob_result", result)
	}
	return wrapClient("kv_client_message", reply)
}

func (e *ToolEnv) interaction(req *Value) *Value {
	reply, _ := New("InteractionResponse")
	reply.Set("id", req.Int("id"))
	name := req.Oneof("query")
	if name == "" {
		return wrapClient("interaction_response", reply)
	}
	resultName, resultType := interactionResult(name)
	if resultName == "" {
		return wrapClient("interaction_response", reply)
	}
	result, err := New(resultType)
	if err != nil {
		return wrapClient("interaction_response", reply)
	}
	fillDenial(result, "Bifrost declined this Cursor interaction")
	reply.Set(resultName, result)
	return wrapClient("interaction_response", reply)
}

func interactionResult(queryName string) (string, string) {
	spec := mustSchema("InteractionResponse")
	stem := strings.TrimSuffix(queryName, "_query")
	for _, candidate := range []string{stem + "_response", stem + "_result", strings.TrimSuffix(stem, "_args") + "_result"} {
		if f, ok := spec.byName[candidate]; ok && f.message {
			return f.name, f.ref
		}
	}
	return "", ""
}

func interactionEvent(msg *Value) (Event, bool) {
	if msg == nil {
		return Event{}, false
	}
	switch msg.Oneof("message") {
	case "text_delta":
		return Event{Kind: "delta", Text: msg.Message("text_delta").String("text")}, false
	case "thinking_delta":
		return Event{Kind: "thinking", Text: msg.Message("thinking_delta").String("text")}, false
	case "turn_ended":
		end := msg.Message("turn_ended")
		return Event{Kind: "usage", InputTokens: int(end.Int("input_tokens")), OutputTokens: int(end.Int("output_tokens"))}, true
	case "tool_call_started":
		return Event{Kind: "tool", Text: "tool started"}, false
	case "tool_call_completed":
		return Event{Kind: "tool", Text: "tool completed"}, false
	default:
		return Event{}, false
	}
}

func execReply(req *Value, field string, result *Value) *Value {
	out, _ := New("ExecClientMessage")
	if n := req.Int("id"); n != 0 {
		out.Set("id", n)
	}
	if id := req.String("exec_id"); id != "" {
		out.Set("exec_id", id)
	}
	out.Set(field, result)
	return wrapClient("exec_client_message", out)
}

func wrapClient(field string, msg *Value) *Value {
	out, _ := New("AgentClientMessage")
	out.Set(field, msg)
	return out
}

func denyExec(req *Value, reason string) *Value {
	name := req.Oneof("message")
	field, ok := resultField(name)
	out, _ := New("ExecClientMessage")
	if n := req.Int("id"); n != 0 {
		out.Set("id", n)
	}
	if id := req.String("exec_id"); id != "" {
		out.Set("exec_id", id)
	}
	if ok {
		result, err := New(field.ref)
		if err == nil {
			fillDenial(result, reason)
			out.Set(field.name, result)
		}
	}
	return wrapClient("exec_client_message", out)
}

func resultField(argsName string) (fieldSpec, bool) {
	spec := mustSchema("ExecClientMessage")
	stem := strings.TrimSuffix(argsName, "_args")
	for _, candidate := range []string{stem + "_result", stem, strings.TrimSuffix(argsName, "_request") + "_response"} {
		if f, ok := spec.byName[candidate]; ok && f.message {
			return f, true
		}
	}
	return fieldSpec{}, false
}

func fillDenial(result *Value, reason string) {
	spec := mustSchema(result.Type)
	for _, name := range []string{"rejected", "permission_denied", "error", "failure"} {
		f, ok := spec.byName[name]
		if !ok || !f.message {
			continue
		}
		child, err := New(f.ref)
		if err != nil {
			return
		}
		setReason(child, reason)
		result.Set(name, child)
		return
	}
	setReason(result, reason)
}

func setReason(v *Value, reason string) {
	spec := mustSchema(v.Type)
	for _, name := range []string{"reason", "error", "client_visible_error", "message"} {
		if _, ok := spec.byName[name]; ok {
			v.Set(name, reason)
			return
		}
	}
}

func shellOutcome(req *Value, ran shellRun) *Value {
	result, _ := New("ShellResult")
	kind, command, cwd, reason, stdout, stderr, elapsed := ran.kind, ran.command, ran.cwd, ran.reason, ran.stdout, ran.stderr, ran.elapsed
	switch kind {
	case "success":
		ok, _ := New("ShellSuccess")
		ok.Set("command", command)
		ok.Set("working_directory", cwd)
		ok.Set("stdout", stdout)
		ok.Set("stderr", stderr)
		ok.Set("execution_time", int64(elapsed))
		result.Set("success", ok)
	case "timeout":
		item, _ := New("ShellTimeout")
		item.Set("command", command)
		item.Set("working_directory", cwd)
		item.Set("timeout_ms", int64(elapsed))
		result.Set("timeout", item)
	case "spawn_error":
		item, _ := New("ShellSpawnError")
		item.Set("command", command)
		item.Set("working_directory", cwd)
		item.Set("error", reason)
		result.Set("spawn_error", item)
	case "permission_denied":
		item, _ := New("ShellPermissionDenied")
		item.Set("command", command)
		item.Set("working_directory", cwd)
		item.Set("error", reason)
		result.Set("permission_denied", item)
	case "rejected":
		item, _ := New("ShellRejected")
		item.Set("command", command)
		item.Set("working_directory", cwd)
		item.Set("reason", reason)
		result.Set("rejected", item)
	default:
		item, _ := New("ShellFailure")
		item.Set("command", command)
		item.Set("working_directory", cwd)
		code := ran.exitCode
		if code == 0 {
			code = 1
		}
		item.Set("exit_code", int64(code))
		item.Set("stdout", stdout)
		item.Set("stderr", stderr)
		item.Set("execution_time", int64(elapsed))
		result.Set("failure", item)
	}
	return execReply(req, "shell_result", result)
}

func readErr(req *Value, path string, err error) *Value {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return readOutcome(req, "file_not_found", path, "", "")
	case errors.Is(err, workspace.ErrBinary):
		return readOutcome(req, "invalid_file", path, "file is binary", "")
	default:
		return readOutcome(req, "error", path, err.Error(), "")
	}
}

func readOutcome(req *Value, kind, path, reason, content string) *Value {
	result, _ := New("ReadResult")
	switch kind {
	case "file_not_found":
		item, _ := New("ReadFileNotFound")
		item.Set("path", path)
		result.Set("file_not_found", item)
	case "invalid_file":
		item, _ := New("ReadInvalidFile")
		item.Set("path", path)
		item.Set("reason", reason)
		result.Set("invalid_file", item)
	case "rejected":
		item, _ := New("ReadRejected")
		item.Set("path", path)
		item.Set("reason", reason)
		result.Set("rejected", item)
	default:
		item, _ := New("ReadError")
		item.Set("path", path)
		item.Set("error", reason)
		result.Set("error", item)
	}
	_ = content
	return execReply(req, "read_result", result)
}

func writeOutcome(req *Value, kind, path, reason string) *Value {
	result, _ := New("WriteResult")
	switch kind {
	case "permission_denied":
		item, _ := New("WritePermissionDenied")
		item.Set("path", path)
		item.Set("error", reason)
		result.Set("permission_denied", item)
	case "rejected":
		item, _ := New("WriteRejected")
		item.Set("path", path)
		item.Set("reason", reason)
		result.Set("rejected", item)
	default:
		item, _ := New("WriteError")
		item.Set("path", path)
		item.Set("error", reason)
		result.Set("error", item)
	}
	return execReply(req, "write_result", result)
}

func deleteOutcome(req *Value, kind, path, reason, prev string) *Value {
	result, _ := New("DeleteResult")
	switch kind {
	case "file_not_found":
		item, _ := New("DeleteFileNotFound")
		item.Set("path", path)
		result.Set("file_not_found", item)
	case "rejected":
		item, _ := New("DeleteRejected")
		item.Set("path", path)
		item.Set("reason", reason)
		result.Set("rejected", item)
	default:
		item, _ := New("DeleteError")
		item.Set("path", path)
		item.Set("error", reason)
		result.Set("error", item)
	}
	_ = prev
	return execReply(req, "delete_result", result)
}

func grepError(req *Value, reason string) *Value {
	result, _ := New("GrepResult")
	item, _ := New("GrepError")
	item.Set("error", reason)
	result.Set("error", item)
	return execReply(req, "grep_result", result)
}

func lsOutcome(req *Value, kind, path, reason string) *Value {
	result, _ := New("LsResult")
	switch kind {
	case "rejected":
		item, _ := New("LsRejected")
		item.Set("path", path)
		item.Set("reason", reason)
		result.Set("rejected", item)
	default:
		item, _ := New("LsError")
		item.Set("path", path)
		item.Set("error", reason)
		result.Set("error", item)
	}
	return execReply(req, "ls_result", result)
}

func piResult(req *Value, ok bool, text string) *Value {
	field, found := resultField(req.Oneof("message"))
	if !found {
		return denyExec(req, text)
	}
	result, err := New(field.ref)
	if err != nil {
		return denyExec(req, text)
	}
	spec := mustSchema(result.Type)
	kind := "error"
	if ok {
		kind = "success"
	}
	f, exists := spec.byName[kind]
	if !exists || !f.message {
		fillDenial(result, text)
	} else {
		child, err := New(f.ref)
		if err != nil {
			return denyExec(req, text)
		}
		if ok {
			if _, has := mustSchema(child.Type).byName["output"]; has {
				child.Set("output", text)
			}
		} else {
			setReason(child, text)
		}
		result.Set(kind, child)
	}
	return execReply(req, field.name, result)
}

func execKey(req *Value) string {
	if id := req.String("exec_id"); id != "" {
		return "exec:" + id
	}
	if n := req.Int("id"); n != 0 {
		return fmt.Sprintf("id:%d", n)
	}
	return ""
}

func relativePath(root, p string) (string, error) {
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return "", errors.New("workspace root is required")
	}
	p = strings.TrimSpace(p)
	if p == "" || p == "." {
		return ".", nil
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, filepath.Clean(p))
	}
	p = filepath.Clean(p)
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes the workspace")
	}
	return rel, nil
}

func boundedTimeout(args *Value) time.Duration {
	var d time.Duration
	if n := args.Int("timeout"); n > 0 {
		if n > 120 {
			d = time.Duration(n) * time.Millisecond
		} else {
			d = time.Duration(n) * time.Second
		}
	} else if f, ok := args.Get("timeout").(float64); ok && f > 0 {
		d = time.Duration(f * float64(time.Second))
	}
	if hard := args.Int("hard_timeout"); hard > 0 {
		hd := time.Duration(hard) * time.Millisecond
		if hard <= 120 {
			hd = time.Duration(hard) * time.Second
		}
		if d == 0 || hd < d {
			d = hd
		}
	}
	if d <= 0 {
		d = 30 * time.Second
	}
	if d > 120*time.Second {
		return 120 * time.Second
	}
	return d
}

func sliceLines(text string, offset, limit int) (string, int, bool) {
	lines := strings.Split(text, "\n")
	total := len(lines)
	if offset < 0 {
		offset = 0
	}
	if offset > 0 {
		if offset > total {
			return "", total, true
		}
		lines = lines[offset-1:]
	}
	truncated := false
	if limit > 0 && len(lines) > limit {
		lines = lines[:limit]
		truncated = true
	}
	return strings.Join(lines, "\n"), total, truncated
}

func ignored(name string, patterns []string) bool {
	for _, pattern := range patterns {
		if pattern == "" {
			continue
		}
		if ok, _ := filepath.Match(pattern, name); ok || name == pattern {
			return true
		}
	}
	return false
}

func unifiedSnippet(path, before, after string) string {
	if before == after {
		return "unchanged " + path
	}
	return "updated " + path
}

package cursorclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bifrost/internal/process"
	"bifrost/internal/workspace"
)

func TestSchemaLoads(t *testing.T) {
	if _, err := schemas(); err != nil {
		t.Fatal(err)
	}
	msg, err := New("ShellArgs")
	if err != nil {
		t.Fatal(err)
	}
	msg.Set("command", "echo hi")
	msg.Set("timeout", int64(30))
	msg.Set("simple_commands", []string{"echo"})
	raw, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode("ShellArgs", raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.String("command") != "echo hi" || decoded.Int("timeout") != 30 || len(decoded.Strings("simple_commands")) != 1 {
		t.Fatalf("round trip = %#v", decoded.fields)
	}
}

func TestRunKeepsInstructionsOutOfSystemPromptOption(t *testing.T) {
	msg, err := buildRun(RunInput{Prompt: "test", Model: "auto", Instructions: "Follow AGENTS.md"}, "conv", "run")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode("AgentClientMessage", raw)
	if err != nil {
		t.Fatal(err)
	}
	run := decoded.Message("run_request")
	if run.String("custom_system_prompt") != "" {
		t.Fatalf("custom system prompt = %q", run.String("custom_system_prompt"))
	}
	text := run.Message("action").Message("user_message_action").Message("user_message").String("text")
	if text != "Follow AGENTS.md\n\ntest" {
		t.Fatalf("user text = %q", text)
	}
}

func TestNewRunCarriesEmptyConversationState(t *testing.T) {
	msg, err := buildRun(RunInput{Prompt: "test", Model: "auto"}, "conv", "run")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode("AgentClientMessage", raw)
	if err != nil {
		t.Fatal(err)
	}
	state := decoded.Message("run_request").Message("conversation_state")
	if state == nil || state.Type != "ConversationStateStructure" {
		t.Fatal("run request omitted conversation state")
	}
	encoded, err := state.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) != 0 {
		t.Fatalf("new conversation state = %x", encoded)
	}
}

func TestUnmodifiedBytesRoundTrip(t *testing.T) {
	msg, err := New("ConversationStateStructure")
	if err != nil {
		t.Fatal(err)
	}
	msg.Set("agent_type", "local")
	raw, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, 0xfa, 0x0c, 0x02, 'z', 'z')
	decoded, err := Decode("ConversationStateStructure", raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := decoded.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(raw) {
		t.Fatalf("checkpoint bytes changed\n got %x\nwant %x", again, raw)
	}
}

func TestLoginPollAndRefresh(t *testing.T) {
	var polls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/poll"):
			if r.URL.Query().Get("verifier") == "" || r.URL.Query().Get("uuid") == "" {
				t.Errorf("poll query = %s", r.URL.RawQuery)
			}
			polls++
			if polls == 1 {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "access", "refreshToken": "refresh", "authId": "user"})
		case strings.HasSuffix(r.URL.Path, "/auth/exchange_user_api_key"):
			if r.Header.Get("Authorization") != "Bearer key-1" {
				t.Errorf("authorization = %s", r.Header.Get("Authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"accessToken": "from-key", "refreshToken": "from-key-refresh"})
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["grant_type"] != "refresh_token" || body["client_id"] != productionClientID || body["refresh_token"] != "refresh" {
				t.Errorf("refresh body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "next", "refresh_token": "rotated"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	auth := Auth{HTTP: srv.Client(), API: srv.URL, Website: srv.URL, PollInterval: func(int) time.Duration { return time.Millisecond }, MaxAttempts: 3}
	pending, err := auth.BeginLogin()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pending.URL, "mode=login") || !strings.Contains(pending.URL, "redirectTarget=cli") || strings.Contains(pending.URL, "accessToken") {
		t.Fatalf("login url = %s", pending.URL)
	}
	tok, err := auth.Poll(context.Background(), pending)
	if err != nil || tok.AccessToken != "access" || tok.RefreshToken != "refresh" {
		t.Fatalf("poll = %#v, %v", tok, err)
	}
	exchanged, err := auth.ExchangeAPIKey(context.Background(), "key-1")
	if err != nil || exchanged.AccessToken != "from-key" {
		t.Fatalf("exchange = %#v, %v", exchanged, err)
	}
	refreshed, err := auth.Refresh(context.Background(), "refresh")
	if err != nil || refreshed.AccessToken != "next" || refreshed.RefreshToken != "rotated" {
		t.Fatalf("refresh = %#v, %v", refreshed, err)
	}
}

func TestRunSSEExecutesAndDenies(t *testing.T) {
	var mu sync.Mutex
	var seqs []int64
	var sawDenied bool
	appended := make(chan struct{}, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/RunSSE"):
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			flusher, _ := w.(http.Flusher)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-appended:
			case <-r.Context().Done():
				return
			}
			exec := mustServerExec("computer_use_args", "e1")
			raw, err := exec.Encode()
			if err != nil {
				t.Errorf("encode exec: %v", err)
				return
			}
			_ = writeEnvelope(w, 0, raw)
			if flusher != nil {
				flusher.Flush()
			}
			select {
			case <-appended:
			case <-r.Context().Done():
				return
			}
			delta := textDelta("Hello")
			draw, _ := delta.Encode()
			_ = writeEnvelope(w, 0, draw)
			_ = writeEnvelope(w, 0x02, []byte("{}"))
		case strings.HasSuffix(r.URL.Path, "/BidiAppend"):
			body, _ := io.ReadAll(r.Body)
			msg, err := Decode("BidiAppendRequest", body)
			if err != nil {
				t.Errorf("append decode: %v", err)
				return
			}
			mu.Lock()
			seqs = append(seqs, msg.Int("append_seqno"))
			mu.Unlock()
			if msg.Int("append_seqno") == 1 {
				payload, err := decodeHexMessage(msg.String("data"))
				if err != nil {
					t.Errorf("hex: %v", err)
				} else if payload.Message("exec_client_message").Message("computer_use_result") == nil {
					t.Errorf("missing denial: %#v", payload.fields)
				} else {
					sawDenied = true
				}
			}
			appended <- struct{}{}
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := Client{HTTP: srv.Client(), API: srv.URL, AccessToken: "token"}
	out, err := client.Run(context.Background(), RunInput{
		Prompt:     "hi",
		Model:      "auto",
		Root:       t.TempDir(),
		Files:      denyFiles{},
		AllowWrite: false,
		AllowExec:  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Text != "Hello" || !sawDenied {
		t.Fatalf("text %q denied %v", out.Text, sawDenied)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != 2 || seqs[0] != 0 || seqs[1] != 1 {
		t.Fatalf("sequence = %v", seqs)
	}
}

func TestShellStreamRunsCommand(t *testing.T) {
	root := t.TempDir()
	env := &ToolEnv{Root: root, Files: scriptFiles{stdout: "ok\n", code: 0}, AllowExec: true}
	args, err := New("ShellArgs")
	if err != nil {
		t.Fatal(err)
	}
	args.Set("command", "echo ok")
	req, err := New("ExecServerMessage")
	if err != nil {
		t.Fatal(err)
	}
	req.Set("exec_id", "sh1")
	req.Set("shell_stream_args", args)
	msg, err := New("AgentServerMessage")
	if err != nil {
		t.Fatal(err)
	}
	msg.Set("exec_server_message", req)
	replies, _, _, _, send := env.handle(context.Background(), msg)
	if !send || len(replies) < 2 {
		t.Fatalf("replies = %d send %v", len(replies), send)
	}
	var sawOut bool
	var exitCode int64 = -1
	for _, reply := range replies {
		stream := reply.Message("exec_client_message").Message("shell_stream")
		if stream.Message("stdout").String("data") == "ok\n" {
			sawOut = true
		}
		if exit := stream.Message("exit"); exit != nil {
			exitCode = exit.Int("code")
		}
	}
	if !sawOut || exitCode != 0 {
		t.Fatalf("stdout %v exit %d", sawOut, exitCode)
	}
}

func TestRequestContextReturnsWorkspace(t *testing.T) {
	root := t.TempDir()
	env := &ToolEnv{Root: root, Files: denyFiles{}}
	args, err := New("RequestContextArgs")
	if err != nil {
		t.Fatal(err)
	}
	args.Set("notes_session_id", "session-1")
	req, err := New("ExecServerMessage")
	if err != nil {
		t.Fatal(err)
	}
	req.Set("exec_id", "ctx")
	req.Set("request_context_args", args)
	reply := env.exec(context.Background(), req)
	result := reply.Message("exec_client_message").Message("request_context_result")
	if result.Oneof("result") != "success" {
		t.Fatalf("result = %s", result.Oneof("result"))
	}
	got := result.Message("success").Message("request_context")
	envMsg := got.Message("env")
	if envMsg.String("project_folder") != root || len(envMsg.Strings("workspace_paths")) != 1 {
		t.Fatalf("env = %#v", envMsg.fields)
	}
	if envMsg.String("agent_conversation_notes_folder") != filepath.Join(root, "agent-notes", "session-1") {
		t.Fatalf("notes folder = %s", envMsg.String("agent_conversation_notes_folder"))
	}
	if !got.Bool("env_info_complete") || !got.Bool("git_repo_info_complete") {
		t.Fatal("request context did not mark collection complete")
	}
}

func TestDuplicateExecIsNotRunTwice(t *testing.T) {
	files := &countingFiles{}
	env := &ToolEnv{Root: t.TempDir(), Files: files, AllowExec: true}
	args, _ := New("ShellArgs")
	args.Set("command", "echo hi")
	req, _ := New("ExecServerMessage")
	req.Set("exec_id", "same")
	req.Set("shell_args", args)
	first := env.exec(context.Background(), req)
	second := env.exec(context.Background(), req)
	if files.calls != 1 {
		t.Fatalf("exec calls = %d", files.calls)
	}
	if first.Message("exec_client_message").Message("shell_result").Oneof("result") == "rejected" {
		t.Fatal("first call was rejected")
	}
	if second.Message("exec_client_message").Message("shell_result").Oneof("result") != "rejected" {
		t.Fatalf("duplicate = %s", second.Message("exec_client_message").Message("shell_result").Oneof("result"))
	}
}

func TestWorkspaceFileTools(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("alpha beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Open(root, filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	env := &ToolEnv{Root: root, Files: wsFiles{ws, root}, AllowWrite: true, AllowExec: true}
	read := env.exec(context.Background(), execReq("read_args", "r1", map[string]any{"path": "note.txt"}))
	if !strings.Contains(read.Message("exec_client_message").Message("read_result").Message("success").String("content"), "alpha") {
		t.Fatalf("read = %#v", read.Message("exec_client_message").Message("read_result").fields)
	}
	wrote := env.exec(context.Background(), execReq("write_args", "w1", map[string]any{"path": "note.txt", "file_text": "gamma\n"}))
	if wrote.Message("exec_client_message").Message("write_result").Oneof("result") != "success" {
		t.Fatalf("write = %s", wrote.Message("exec_client_message").Message("write_result").Oneof("result"))
	}
	found := env.exec(context.Background(), execReq("grep_args", "g1", map[string]any{"pattern": "gamma", "path": "."}))
	if found.Message("exec_client_message").Message("grep_result").Oneof("result") != "success" {
		t.Fatalf("grep = %s", found.Message("exec_client_message").Message("grep_result").Oneof("result"))
	}
	listed := env.exec(context.Background(), execReq("ls_args", "l1", map[string]any{"path": "."}))
	if listed.Message("exec_client_message").Message("ls_result").Message("success") == nil {
		t.Fatal("ls failed")
	}
	ran := env.exec(context.Background(), execReq("shell_args", "s1", map[string]any{"command": "echo hi", "working_directory": root}))
	if !strings.Contains(ran.Message("exec_client_message").Message("shell_result").Message("success").String("stdout"), "hi") {
		t.Fatalf("shell = %#v", ran.Message("exec_client_message").Message("shell_result").fields)
	}
	removed := env.exec(context.Background(), execReq("delete_args", "d1", map[string]any{"path": filepath.Join(root, "note.txt")}))
	if removed.Message("exec_client_message").Message("delete_result").Oneof("result") != "success" {
		t.Fatalf("delete = %s", removed.Message("exec_client_message").Message("delete_result").Oneof("result"))
	}
	denied := env.exec(context.Background(), execReq("computer_use_args", "c1", map[string]any{"tool_call_id": "cu"}))
	if denied.Message("exec_client_message").Message("computer_use_result") == nil {
		t.Fatal("computer use was not denied")
	}
}

func execReq(field, id string, values map[string]any) *Value {
	spec := mustSchema("ExecServerMessage").byName[field]
	args, err := New(spec.ref)
	if err != nil {
		panic(err)
	}
	for name, value := range values {
		args.Set(name, value)
	}
	req, err := New("ExecServerMessage")
	if err != nil {
		panic(err)
	}
	req.Set("exec_id", id)
	req.Set(field, args)
	return req
}

func TestMalformedFrame(t *testing.T) {
	_, _, err := readEnvelope(strings.NewReader("\x00\x00\x00\x00\x10"))
	if err == nil {
		t.Fatal("expected truncated frame error")
	}
}

func mustServerExec(field, id string) *Value {
	args, _ := New("ComputerUseArgs")
	args.Set("tool_call_id", "call")
	exec, _ := New("ExecServerMessage")
	exec.Set("exec_id", id)
	exec.Set(field, args)
	msg, _ := New("AgentServerMessage")
	msg.Set("exec_server_message", exec)
	return msg
}

func textDelta(text string) *Value {
	delta, _ := New("TextDeltaUpdate")
	delta.Set("text", text)
	update, _ := New("InteractionUpdate")
	update.Set("text_delta", delta)
	msg, _ := New("AgentServerMessage")
	msg.Set("interaction_update", update)
	return msg
}

func decodeHexMessage(data string) (*Value, error) {
	raw, err := hex.DecodeString(data)
	if err != nil {
		return nil, err
	}
	return Decode("AgentClientMessage", raw)
}

type scriptFiles struct {
	denyFiles
	stdout string
	code   int
}

func (s scriptFiles) Exec(context.Context, []string, string, int) (process.Result, error) {
	return process.Result{ExitCode: s.code, Stdout: []byte(s.stdout)}, nil
}

type denyFiles struct{}

func (denyFiles) List(context.Context, string) ([]workspace.Entry, error) { return nil, nil }
func (denyFiles) Read(context.Context, string) (string, workspace.Revision, error) {
	return "", "", nil
}
func (denyFiles) Search(context.Context, string) (workspace.SearchResult, error) {
	return workspace.SearchResult{}, nil
}
func (denyFiles) Write(context.Context, string, *workspace.Revision, string) (workspace.Change, error) {
	return workspace.Change{}, nil
}
func (denyFiles) Exec(context.Context, []string, string, int) (process.Result, error) {
	return process.Result{}, nil
}

type countingFiles struct{ calls int }

func (c *countingFiles) List(context.Context, string) ([]workspace.Entry, error) { return nil, nil }
func (c *countingFiles) Read(context.Context, string) (string, workspace.Revision, error) {
	return "", "", nil
}
func (c *countingFiles) Search(context.Context, string) (workspace.SearchResult, error) {
	return workspace.SearchResult{}, nil
}
func (c *countingFiles) Write(context.Context, string, *workspace.Revision, string) (workspace.Change, error) {
	return workspace.Change{}, nil
}
func (c *countingFiles) Exec(context.Context, []string, string, int) (process.Result, error) {
	c.calls++
	return process.Result{}, nil
}

type wsFiles struct {
	ws   *workspace.Workspace
	root string
}

func (w wsFiles) List(_ context.Context, path string) ([]workspace.Entry, error) {
	return w.ws.List(path)
}
func (w wsFiles) Read(_ context.Context, path string) (string, workspace.Revision, error) {
	return w.ws.Read(path)
}
func (w wsFiles) Search(ctx context.Context, query string) (workspace.SearchResult, error) {
	return w.ws.Search(ctx, query)
}
func (w wsFiles) Write(_ context.Context, path string, expected *workspace.Revision, content string) (workspace.Change, error) {
	return w.ws.Write(path, expected, content)
}
func (w wsFiles) Delete(_ context.Context, path string) (string, error) {
	_, prev, err := w.ws.Delete(path)
	return prev, err
}
func (w wsFiles) Exec(ctx context.Context, argv []string, cwd string, maxOutput int) (process.Result, error) {
	dir := w.root
	if cwd != "" && cwd != "." {
		dir = filepath.Join(w.root, cwd)
	}
	return process.Run(ctx, process.Spec{Argv: argv, Dir: dir, MaxOutputBytes: maxOutput})
}

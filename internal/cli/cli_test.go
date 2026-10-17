package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func invoke(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestProjectAndFileCommandsUseIsolatedState(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "repo")
	state := filepath.Join(base, "state")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello\r\n"), 0640); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := invoke(t, "--state-dir", state, "project", "add", "demo", "--root", root)
	if err != nil || !strings.Contains(stdout, "created demo") {
		t.Fatalf("project add = %q, %v", stdout, err)
	}
	secondRepo := filepath.Join(base, "api")
	if err := os.Mkdir(secondRepo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secondRepo, "api.txt"), []byte("api\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := invoke(t, "project", "add-repo", "demo", "--alias", "api", "--root", secondRepo, "--state-dir", state); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = invoke(t, "--state-dir", state, "--project", "demo", "--repository", "api", "--json", "file", "read", "api.txt")
	if err != nil || !strings.Contains(stdout, "\"api\\n\"") {
		t.Fatalf("added repository selection = %q, %v", stdout, err)
	}
	stdout, _, err = invoke(t, "--state-dir", state, "--project", "demo", "--json", "exec", "--", "pwd")
	if err != nil {
		t.Fatal(err)
	}
	var execResult struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(stdout), &execResult); err != nil || strings.TrimSpace(execResult.Stdout) != root {
		t.Fatalf("project-bound exec = %q, %v", stdout, err)
	}
	stdout, _, err = invoke(t, "project", "list", "--state-dir", state, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var projects []struct{ Name string }
	if err := json.Unmarshal([]byte(stdout), &projects); err != nil || len(projects) != 1 || projects[0].Name != "demo" {
		t.Fatalf("project list = %q, %v", stdout, err)
	}
	stdout, _, err = invoke(t, "--state-dir", state, "--project", "demo", "--json", "file", "read", "hello.txt")
	if err != nil {
		t.Fatal(err)
	}
	var read struct {
		Text     string `json:"text"`
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(stdout), &read); err != nil || read.Text != "hello\r\n" || read.Revision == "" {
		t.Fatalf("file read = %q, %v", stdout, err)
	}
	stdout, _, err = invoke(t, "--state-dir", state, "--project", "demo", "--json", "file", "edit", "hello.txt", "--expected", read.Revision, "--old", "hello", "--new", "updated")
	if err != nil {
		t.Fatal(err)
	}
	var change struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &change); err != nil || !strings.HasPrefix(change.ID, "change_") {
		t.Fatalf("file edit = %q, %v", stdout, err)
	}
	if _, _, err := invoke(t, "--state-dir", state, "--project", "demo", "file", "undo", change.ID); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil || string(b) != "hello\r\n" {
		t.Fatalf("undo = %q, %v", b, err)
	}
	if info, err := os.Stat(filepath.Join(root, "hello.txt")); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode after undo = %v, %v", info.Mode(), err)
	}
}

func TestWriteRequiresPreconditionAndVersionGoesToStdout(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	state := filepath.Join(base, "state")
	if err := os.Mkdir(root, 0755); err != nil {
		t.Fatal(err)
	}
	stdout, _, err := invoke(t, "--version")
	if err != nil || !strings.Contains(stdout, "bifrost "+Version) {
		t.Fatalf("version = %q, %v", stdout, err)
	}
	if _, _, err := invoke(t, "--state-dir", state, "--root", root, "file", "write", "new.txt", "--content", "x"); err == nil {
		t.Fatal("unguarded write unexpectedly succeeded")
	}
	if _, _, err := invoke(t, "--state-dir", state, "--root", root, "file", "write", "new.txt", "--must-not-exist", "--content", "--help"); err != nil {
		t.Fatal(err)
	}
	content, readErr := os.ReadFile(filepath.Join(root, "new.txt"))
	if readErr != nil || string(content) != "--help" {
		t.Fatalf("content beginning with flag = %q, %v", content, readErr)
	}
	if _, _, err := invoke(t, "--state-dir", state, "--root", root, "file", "write", "new.txt", "--must-not-exist", "--content", "y"); err == nil {
		t.Fatalf("existing must-not-exist write = %v", err)
	}
	secondRoot := filepath.Join(base, "second-root")
	if err := os.Mkdir(secondRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := invoke(t, "--state-dir", state, "--root", secondRoot, "file", "write", "other.txt", "--must-not-exist", "--content", "value"); err != nil {
		t.Fatal(err)
	}
	workspaceStates, err := filepath.Glob(filepath.Join(state, "workspace", "*"))
	if err != nil || len(workspaceStates) != 2 {
		t.Fatalf("root-specific workspace state = %v, %v", workspaceStates, err)
	}
}

func TestExecUsesArgvAndReturnsExitStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell command fixture uses sh")
	}
	root := t.TempDir()
	stdout, stderr, err := invoke(t, "--root", root, "exec", "--", "sh", "-c", "printf out; printf err >&2; exit 7")
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 7 || stdout != "out" || stderr != "err" {
		t.Fatalf("exec = stdout %q stderr %q err %v", stdout, stderr, err)
	}
}

func TestGlobalFlagsRespectExecBoundaryAndRejectDuplicates(t *testing.T) {
	global, command, err := extractGlobalFlags([]string{"exec", "--", "sh", "-c", "echo", "--root", "--json"})
	if err != nil || len(global) != 0 || strings.Join(command, "\x00") != "exec\x00--\x00sh\x00-c\x00echo\x00--root\x00--json" {
		t.Fatalf("exec boundary = global=%#v command=%#v err=%v", global, command, err)
	}
	if _, _, err := extractGlobalFlags([]string{"--root", "/tmp", "--root", "/other", "doctor"}); err == nil {
		t.Fatal("duplicate global flag unexpectedly accepted")
	}
	if _, _, err := extractGlobalFlags([]string{"doctor", "--state-dir", "--json"}); err == nil {
		t.Fatal("missing global flag value unexpectedly accepted")
	}
	_, command, err = extractGlobalFlags([]string{"file", "write", "p", "--content", "--root"})
	if err != nil || strings.Join(command, "\x00") != "file\x00write\x00p\x00--content\x00--root" {
		t.Fatalf("subcommand value = command=%#v err=%v", command, err)
	}
	_, command, err = extractGlobalFlags([]string{"mcp", "call", "--args", "--json"})
	if err != nil || strings.Join(command, "\x00") != "mcp\x00call\x00--args\x00--json" {
		t.Fatalf("MCP args value = command=%#v err=%v", command, err)
	}
}

func TestDoctorReportsAvailabilityWithoutRunningPrograms(t *testing.T) {
	previous := execLookPath
	t.Cleanup(func() { execLookPath = previous })
	execLookPath = func(name string) (string, error) {
		if name == "git" || name == "chromium" {
			return "/fake/" + name, nil
		}
		return "", os.ErrNotExist
	}
	stdout, _, err := invoke(t, "doctor", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"available": true`) || !strings.Contains(stdout, "Executable availability only") {
		t.Fatalf("doctor output = %s", stdout)
	}
}

func TestMCPAndBrowserValidateExplicitInputs(t *testing.T) {
	if err := runMCP(context.Background(), options{}, []string{"inspect"}, ioDiscard{}, ioDiscard{}); err == nil {
		t.Fatal("MCP inspect without endpoint unexpectedly succeeded")
	}
	if err := runBrowser(context.Background(), []string{"probe", "--url", "not-a-url"}, ioDiscard{}); err == nil {
		t.Fatal("browser probe accepted invalid URL")
	}
}

func TestRunRequiresEnvironmentConfigurationAndDoesNotAcceptKeyFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("BIFROST_BASE_URL", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("BIFROST_MODEL", "")
	t.Setenv("OPENAI_MODEL", "")
	if _, _, err := invoke(t, "run", "say hello"); err == nil || !strings.Contains(err.Error(), "MODEL") {
		t.Fatalf("missing run environment = %v", err)
	}
	if _, _, err := invoke(t, "run", "--api-key", "secret", "say hello"); err == nil {
		t.Fatal("literal API key flag unexpectedly accepted")
	}
}

func TestRunWiresReadEditExecAndFinalThroughLocalHTTP(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local HTTP fixture unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			writeProviderResponse(w, providerToolResponse("read-1", "read_file", `{"path":"note.txt"}`))
		case 2:
			var tool map[string]any
			if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &tool); err != nil {
				t.Error(err)
			}
			revision, _ := tool["revision"].(string)
			writeProviderResponse(w, providerToolResponse("edit-1", "edit_file", `{"path":"note.txt","expected_revision":"`+revision+`","old_text":"old","new_text":"new"}`))
		case 3:
			writeProviderResponse(w, providerToolResponse("exec-1", "exec", `{"argv":["sh","-c","printf cli-run"]}`))
		default:
			writeProviderResponse(w, providerFinalResponse("completed"))
		}
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()
	t.Setenv("BIFROST_BASE_URL", server.URL)
	t.Setenv("BIFROST_MODEL", "fixture")
	stdout, stderr, err := invoke(t, "--root", root, "--state-dir", state, "--allow-write", "--allow-exec", "--json", "run", "update the note")
	if err != nil {
		t.Fatalf("run error: %v (stderr=%s)", err, stderr)
	}
	var result struct {
		Text      string `json:"Text"`
		Steps     int    `json:"Steps"`
		ToolCalls int    `json:"ToolCalls"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("run output=%q: %v", stdout, err)
	}
	if result.Text != "completed" || result.Steps != 4 || result.ToolCalls != 3 || calls.Load() != 4 {
		t.Fatalf("run result=%+v calls=%d", result, calls.Load())
	}
	content, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(content) != "new\n" {
		t.Fatalf("run file=%q err=%v", content, err)
	}
}

func providerToolResponse(id, name, arguments string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments}}}}, "finish_reason": "tool_calls"}}}
}

func providerFinalResponse(text string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": text}, "finish_reason": "stop"}}}
}

func writeProviderResponse(w http.ResponseWriter, value any) {
	_ = json.NewEncoder(w).Encode(value)
}

type ioDiscard struct{}

func (ioDiscard) Write([]byte) (int, error) { return 0, nil }

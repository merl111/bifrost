package agentbackend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeExecutable(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-agent")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nset -eu\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCursorLoadIncludesMCPServers(t *testing.T) {
	root := t.TempDir()
	seen := filepath.Join(t.TempDir(), "load")
	bin := fakeExecutable(t, `while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
    *'"method":"authenticate"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}' ;;
    *'"method":"session/load"'*) printf '%s' "$line" > "`+seen+`"; printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{}}' ;;
    *'"method":"session/prompt"'*) printf '%s\n' '{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}'; exit 0 ;;
  esac
	done`)
	r, err := New(Config{Kind: KindCursorACP, Executable: bin, Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "resume", "s1", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"mcpServers":[]`) {
		t.Fatalf("session/load params=%s", b)
	}
}

func TestCursorACPStreamingAndAccountIsolation(t *testing.T) {
	root, account := t.TempDir(), t.TempDir()
	envFile := filepath.Join(t.TempDir(), "env")
	bin := fakeExecutable(t, `printf '%s\n%s\n%s' "$CURSOR_CONFIG_DIR" "$CURSOR_DATA_DIR" "${XDG_CONFIG_HOME-}" > "`+envFile+`"
while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
    *'"method":"authenticate"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}' ;;
    *'"method":"session/new"'*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"text":"hello"}}}}'
      printf '%s\n' '{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}'
      exit 0 ;;
  esac
done`)
	r, err := New(Config{Kind: KindCursorACP, Executable: bin, Root: root, AccountDir: account})
	if err != nil {
		t.Fatal(err)
	}
	var got string
	result, err := r.Run(context.Background(), "say hello", "", func(e Event) {
		if e.Kind == "text" {
			got += e.Text
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "s1" || result.Text != "hello" || got != "hello" {
		t.Fatalf("result=%#v streamed=%q", result, got)
	}
	b, err := os.ReadFile(envFile)
	if err != nil || string(b) != account+"\n"+account+"\n"+account {
		t.Fatalf("cursor account env=%q err=%v", b, err)
	}
}

func TestClaudeStreamResumeAndSanitizeCredentials(t *testing.T) {
	root, account := t.TempDir(), t.TempDir()
	argsFile, envFile := filepath.Join(t.TempDir(), "args"), filepath.Join(t.TempDir(), "env")
	bin := fakeExecutable(t, `printf '%s' "$*" > "`+argsFile+`"
printf '%s' "${CLAUDE_CONFIG_DIR-}" > "`+envFile+`"
printf '%s\n' '{"type":"system","session_id":"new"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","session_id":"new","result":"done","usage":{"input_tokens":2,"output_tokens":3}}'`)
	old := os.Getenv("ANTHROPIC_API_KEY")
	if err := os.Setenv("ANTHROPIC_API_KEY", "must-not-pass"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Setenv("ANTHROPIC_API_KEY", old) })
	r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: root, AccountDir: account})
	if err != nil {
		t.Fatal(err)
	}
	result, err := r.Run(context.Background(), "--dangerous-looking-prompt", "old-session", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "new" || result.Text != "done" || result.Usage.TotalTokens != 5 {
		t.Fatalf("result=%#v", result)
	}
	b, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(b), "--resume old-session") || !strings.Contains(string(b), "-p -- --dangerous-looking-prompt") || strings.Contains(string(b), "dangerously-skip") {
		t.Fatalf("claude args=%q", b)
	}
	b, _ = os.ReadFile(envFile)
	if string(b) != account {
		t.Fatalf("claude account env=%q", b)
	}
}

func TestClaudeRequiresTerminalResult(t *testing.T) {
	bin := fakeExecutable(t, `printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}'`)
	r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "finish", "", nil); err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected terminal-result error, got %v", err)
	}
}

func TestClaudeRequiresSuccessfulTerminalSubtype(t *testing.T) {
	for name, line := range map[string]string{
		"missing": `{"type":"result","session_id":"s1","result":"partial"}`,
		"unknown": `{"type":"result","subtype":"completed","session_id":"s1","result":"partial"}`,
	} {
		t.Run(name, func(t *testing.T) {
			bin := fakeExecutable(t, `printf '%s\n' '`+line+`'`)
			r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.Run(context.Background(), "finish", "", nil)
			if err == nil || result.StopReason == "success" {
				t.Fatalf("expected unsuccessful subtype error, result=%#v err=%v", result, err)
			}
		})
	}
}

func TestClaudeDoesNotHangOnDescendantPipeAfterExit(t *testing.T) {
	bin := fakeExecutable(t, `printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"partial"}]}}'
sleep 30 &
exit 0`)
	r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.Run(context.Background(), "finish", "", nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected failed terminal result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run hung after direct child exited")
	}
}

func TestClaudeCancellationKillsProcessGroup(t *testing.T) {
	bin := fakeExecutable(t, `sleep 30 & wait`)
	r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = r.Run(ctx, "wait", "", nil)
	if err == nil || ctx.Err() == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("cancellation err=%v duration=%s", err, time.Since(start))
	}
}

func TestCursorPermissionUsesACPShapeAndRejectsUnknownRequests(t *testing.T) {
	root := t.TempDir()
	responseFile := filepath.Join(t.TempDir(), "response")
	bin := fakeExecutable(t, `while IFS= read -r line; do
  case "$line" in
    *'"method":"initialize"'*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{}}' ;;
    *'"method":"authenticate"'*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{}}' ;;
    *'"method":"session/new"'*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"sessionId":"s1"}}' ;;
    *'"method":"session/prompt"'*)
      printf '%s\n' '{"jsonrpc":"2.0","id":77,"method":"cursor/ask_question","params":{}}'
      printf '%s\n' '{"jsonrpc":"2.0","id":99,"method":"session/request_permission","params":{"sessionId":"s1","toolCall":{"toolCallId":"t1","kind":"edit","title":"edit"},"options":[{"optionId":"allow_once","kind":"allow_once","name":"Allow"},{"optionId":"reject_once","kind":"reject_once","name":"Reject"}]}}'
      seen=0
      while IFS= read -r reply; do
        case "$reply" in *'"id":77'*|*'"id":99'*) printf '%s' "$reply" > "`+responseFile+`"; seen=$((seen+1));; esac
        if [ "$seen" -ge 2 ]; then printf '%s\n' '{"jsonrpc":"2.0","id":4,"result":{"stopReason":"end_turn"}}'; exit 0; fi
      done ;;
  esac
done`)
	r, err := New(Config{Kind: KindCursorACP, Executable: bin, Root: root, AllowWrite: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), "edit", "", nil); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(responseFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"reject_once"`) && !strings.Contains(string(b), `"code":-32601`) {
		t.Fatalf("ACP responses=%q", b)
	}
}

func TestEnvironmentScrubsProviderOverrides(t *testing.T) {
	for name, value := range map[string]string{
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN": "refresh",
		"CLAUDE_CODE_OAUTH_SCOPES":        "scope",
		"ANTHROPIC_PROFILE":               "wrong-profile",
		"PROVIDER_MANAGED_BY_HOST":        "1",
		"CLAUDE_CODE_USE_ANTHROPIC_AWS":   "1",
		"ANTHROPIC_BASE_URL":              "https://wrong.example",
		"XDG_CONFIG_HOME":                 "/wrong/config",
	} {
		old, had := os.LookupEnv(name)
		if err := os.Setenv(name, value); err != nil {
			t.Fatal(err)
		}
		got := environment(Config{Kind: KindClaudeCLI, AccountDir: t.TempDir()})
		if had {
			_ = os.Setenv(name, old)
		} else {
			_ = os.Unsetenv(name)
		}
		for _, entry := range got {
			if strings.HasPrefix(entry, name+"=") {
				t.Fatalf("inherited provider override %s", entry)
			}
		}
	}
}

func TestRejectsUnsafeCLIArguments(t *testing.T) {
	for _, arg := range []string{"--dangerously-skip-permissions", "--force", "--bare", "--api-key=secret", "--permission-mode=bypassPermissions"} {
		if _, err := New(Config{Kind: KindClaudeCLI, Root: t.TempDir(), Args: []string{arg}}); err == nil {
			t.Fatalf("accepted unsafe argument %q", arg)
		}
	}
}

func TestClaudeDrainsBoundedRedactedStderr(t *testing.T) {
	bin := fakeExecutable(t, `i=0; while [ "$i" -lt 100 ]; do printf '%s\n' 'ANTHROPIC_API_KEY=secret-value' >&2; i=$((i+1)); done
printf '%s\n' '{"type":"result","subtype":"success","session_id":"s1","result":"ok"}'`)
	r, err := New(Config{Kind: KindClaudeCLI, Executable: bin, Root: t.TempDir(), MaxOutputBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	result, err := r.Run(context.Background(), "ok", "", func(event Event) { events = append(events, event) })
	if err != nil || result.SessionID != "s1" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	for _, event := range events {
		if strings.Contains(event.Text, "secret-value") {
			t.Fatal("stderr secret leaked in event")
		}
	}
}

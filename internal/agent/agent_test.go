package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"bifrost/internal/agent"
	"bifrost/internal/provider"
)

func TestRunReadEditFinalThroughHTTP(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("old value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	serverURL, closeServer := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role      string            `json:"role"`
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			writeJSON(w, toolResponse("read-1", "read_file", `{"path":"note.txt"}`))
		case 2:
			if len(req.Messages) == 0 {
				t.Error("missing messages")
			}
			if len(req.Messages) < 4 || req.Messages[len(req.Messages)-2].Role != "assistant" || len(req.Messages[len(req.Messages)-2].ToolCalls) != 1 || req.Messages[len(req.Messages)-1].Role != "tool" {
				t.Errorf("tool call messages were not preserved: %+v", req.Messages)
			}
			var tool map[string]any
			if err := json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &tool); err != nil {
				t.Errorf("tool content: %v", err)
			}
			rev, _ := tool["revision"].(string)
			if rev == "" {
				t.Error("read result omitted revision")
			}
			writeJSON(w, toolResponse("edit-1", "edit_file", `{"path":"note.txt","expected_revision":"`+rev+`","old_text":"old value","new_text":"new value"}`))
		default:
			writeJSON(w, finalResponse("updated"))
		}
	}))
	defer closeServer()
	client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: serverURL + "/v1", Model: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Run(context.Background(), agent.Config{Provider: client, Root: root, StateDir: state, WriteEnabled: true, MaxSteps: 5}, "update the note")
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "updated" || result.Steps != 3 || result.ToolCalls != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new value\n" {
		t.Fatalf("file = %q", got)
	}
}

func TestRunDeniesWritesAndExecByDefault(t *testing.T) {
	for _, tc := range []struct{ name, tool, args string }{
		{"write", "write_file", `{"path":"new.txt","expected_revision":null,"content":"secret"}`},
		{"exec", "exec", `{"argv":["touch","bad.txt"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, state := t.TempDir(), t.TempDir()
			var calls atomic.Int32
			serverURL, closeServer := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					writeJSON(w, toolResponse("call-1", tc.tool, tc.args))
					return
				}
				writeJSON(w, finalResponse("permission handled"))
			}))
			defer closeServer()
			client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: serverURL, Model: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := agent.Run(context.Background(), agent.Config{Provider: client, Root: root, StateDir: state, MaxSteps: 3}, "do it")
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != "permission handled" || result.ToolCalls != 1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(root, "new.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("write happened: %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "bad.txt")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("exec happened: %v", err)
			}
		})
	}
}

func TestRunMaxSteps(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	serverURL, closeServer := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, toolResponse("read", "read_file", `{"path":"missing"}`))
	}))
	defer closeServer()
	client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: serverURL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := agent.Run(context.Background(), agent.Config{Provider: client, Root: root, StateDir: state, MaxSteps: 2}, "loop")
	if !errors.Is(err, agent.ErrMaxSteps) || result.Steps != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestRunCancellation(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	serverURL, closeServer := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer closeServer()
	client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: serverURL, Model: "fixture", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = agent.Run(ctx, agent.Config{Provider: client, Root: root, StateDir: state}, "cancel")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenAIValidationAndBoundedResponse(t *testing.T) {
	if _, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: "http://example.com", Model: "x"}); err == nil {
		t.Fatal("accepted non-loopback HTTP")
	}
	serverURL, closeServer := localServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"too large"}}]}`))
	}))
	defer closeServer()
	client, err := provider.NewOpenAI(provider.OpenAIConfig{BaseURL: serverURL, Model: "x", MaxResponseBytes: 8})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Complete(context.Background(), provider.Request{})
	if err == nil {
		t.Fatal("accepted oversized response")
	}
}

func toolResponse(id, name, args string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": args}}}}, "finish_reason": "tool_calls"}}}
}

func finalResponse(text string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": text}, "finish_reason": "stop"}}}
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func localServer(t *testing.T, handler http.Handler) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(listener) }()
	return "http://" + listener.Addr().String(), func() { _ = srv.Close() }
}

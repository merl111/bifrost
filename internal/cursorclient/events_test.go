package cursorclient

import (
	"bifrost/internal/process"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestThinkingAndToolPresentation(t *testing.T) {
	var stream eventStream
	a := stream.normalize(Event{Kind: "thinking", Text: "Inspect "})
	b := stream.normalize(Event{Kind: "thinking", Text: "the code."})
	if a.ID == "" || a.ID != b.ID || b.Text != "Inspect the code." {
		t.Fatalf("fragmented thinking: %+v %+v", a, b)
	}
	stream.normalize(Event{Kind: "tool", Text: "Read"})
	c := stream.normalize(Event{Kind: "thinking", Text: "Next block"})
	if c.ID == a.ID || c.Text != "Next block" {
		t.Fatalf("merged unrelated blocks: %+v", c)
	}
	args, _ := New("ReadArgs")
	args.Set("path", "main.go")
	call, _ := New("ReadToolCall")
	call.Set("args", args)
	tool, _ := New("ToolCall")
	tool.Set("read_tool_call", call)
	update, _ := New("ToolCallStartedUpdate")
	update.Set("tool_call", tool)
	update.Set("call_id", "read-1")
	ev := toolInteraction(update, false)
	if ev.ID != "read-1" || !strings.Contains(ev.Text, "main.go") || !strings.Contains(ev.Detail, "main.go") {
		t.Fatalf("useless tool: %+v", ev)
	}
}

func TestRunBudgetsBeforeExecution(t *testing.T) {
	for _, test := range []struct {
		name  string
		steps bool
	}{{"tool", false}, {"model step", true}} {
		t.Run(test.name, func(t *testing.T) {
			started := make(chan struct{}, 1)
			files := &budgetFiles{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/BidiAppend") {
					io.Copy(io.Discard, r.Body)
					select {
					case started <- struct{}{}:
					default:
					}
					return
				}
				if !strings.HasSuffix(r.URL.Path, "/RunSSE") {
					http.NotFound(w, r)
					return
				}
				w.Header().Set("Content-Type", "application/connect+proto")
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				select {
				case <-started:
				case <-r.Context().Done():
					return
				}
				send := func(v *Value) {
					raw, err := v.Encode()
					if err != nil {
						t.Error(err)
						return
					}
					writeEnvelope(w, 0, raw)
				}
				// Bookkeeping does not consume the one-tool budget.
				wrapper, _ := New("AgentServerMessage")
				wrapper.Set("exec_server_message", execReq("request_context_args", "context", nil))
				send(wrapper)
				for i, id := range []string{"one", "two"} {
					if test.steps {
						step, _ := New("StepStartedUpdate")
						step.Set("step_id", int64(i+1))
						update, _ := New("InteractionUpdate")
						update.Set("step_started", step)
						wrapper, _ := New("AgentServerMessage")
						wrapper.Set("interaction_update", update)
						send(wrapper)
					}
					wrapper, _ := New("AgentServerMessage")
					wrapper.Set("exec_server_message", execReq("shell_args", id, map[string]any{"command": "echo safe"}))
					send(wrapper)
				}
				writeEnvelope(w, 2, []byte("{}"))
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			maxTools := 1
			if test.steps {
				maxTools = 10
			}
			out, err := (Client{HTTP: srv.Client(), API: srv.URL, AccessToken: "fixture"}).Run(ctx, RunInput{Prompt: "fixture", Root: t.TempDir(), Files: files, AllowExec: true, MaxTools: maxTools, MaxSteps: 1})
			if err == nil || !strings.Contains(err.Error(), "budget exceeded") || files.calls != 1 || out.ToolCalls != 1 {
				t.Fatalf("budget allowed execution: out=%+v calls=%d err=%v", out, files.calls, err)
			}
		})
	}
}

// Request-context runs bounded Git reads. Count only the requested shell action.
type budgetFiles struct{ countingFiles }

func (f *budgetFiles) Exec(ctx context.Context, argv []string, dir string, limit int) (process.Result, error) {
	if len(argv) > 0 && argv[0] != "git" {
		f.calls++
	}
	return process.Result{}, nil
}

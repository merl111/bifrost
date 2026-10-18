package cursorclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The server deliberately waits for stream_close, not shell exit. Previously
// this completed the command successfully and then hung forever.
func TestRunContinuesAfterShellStreamClose(t *testing.T) {
	appends := make(chan *Value, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/BidiAppend") {
			raw, _ := io.ReadAll(r.Body)
			request, err := Decode("BidiAppendRequest", raw)
			if err != nil {
				t.Error(err)
				return
			}
			msg, err := decodeHexMessage(request.String("data"))
			if err != nil {
				t.Error(err)
				return
			}
			appends <- msg
			return
		}
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-appends:
		case <-r.Context().Done():
			return
		}
		for _, field := range []string{"shell_stream_args", "read_args"} {
			msg := mustServerExec(field, field)
			req := msg.Message("exec_server_message")
			req.Set("id", int64(42))
			if field == "shell_stream_args" {
				req.Message(field).Set("command", "printf ready")
			}
			if field == "read_args" {
				req.Message(field).Set("path", "main.go")
			}
			raw, _ := msg.Encode()
			writeEnvelope(w, 0, raw)
			w.(http.Flusher).Flush()
			sawResult := false
			for {
				var reply *Value
				select {
				case reply = <-appends:
				case <-r.Context().Done():
					return
				}
				if result := reply.Message("exec_client_message"); result != nil {
					sawResult = true
					if result.Int("id") != 42 {
						t.Error("result lost execution identity")
					}
				}
				if close := reply.Message("exec_client_control_message").Message("stream_close"); close != nil {
					if !sawResult || close.Int("id") != 42 {
						t.Error("stream closed before results or for wrong execution")
					}
					break
				}
			}
		}
		raw, _ := textDelta("Here is the code explanation.").Encode()
		writeEnvelope(w, 0, raw)
		writeEnvelope(w, 2, []byte("{}"))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c := Client{HTTP: srv.Client(), API: srv.URL, AccessToken: "fixture"}
	out, err := c.Run(ctx, RunInput{Prompt: "Explain main.go", Root: t.TempDir(), Files: scriptFiles{stdout: "ready", code: 0}, AllowExec: true})
	if err != nil || out.Text != "Here is the code explanation." || out.ToolCalls != 2 {
		t.Fatalf("turn failed to continue: %+v, %v", out, err)
	}
}

func TestExecutionHeartbeatIsJoinedAndFailureCancels(t *testing.T) {
	req, _ := New("ExecServerMessage")
	req.Set("id", int64(93))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sent := make(chan struct{}, 10)
	finish := executionHeartbeat(ctx, req, time.Millisecond, func(msg *Value) error {
		if msg.Message("exec_client_control_message").Message("heartbeat").Int("id") != 93 {
			t.Error("wrong heartbeat identity")
		}
		sent <- struct{}{}
		return nil
	}, cancel)
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("no heartbeat")
	}
	if err := finish(); err != nil {
		t.Fatal(err)
	}
	// finish joins the goroutine; draining sent now cannot race a later write.
	for len(sent) > 0 {
		<-sent
	}
	select {
	case <-sent:
		t.Fatal("heartbeat after finish")
	case <-time.After(5 * time.Millisecond):
	}
	want := errors.New("append failed")
	finish = executionHeartbeat(ctx, req, time.Millisecond, func(*Value) error { return want }, cancel)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("failed heartbeat did not cancel execution")
	}
	if err := finish(); !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

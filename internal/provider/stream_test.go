package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStreamTextAndFragmentedToolCall(t *testing.T) {
	serverURL, closeServer := streamFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream {
			t.Errorf("stream request = %+v, err=%v", req, err)
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`)
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":" world"}}]}`)
		writeSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_","type":"function","function":{"name":"look","arguments":"{\"x\":"}}]}}]}`)
		writeSSE(w, `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"up","arguments":"1}"}}]}}]}`)
		writeSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		writeSSE(w, "[DONE]")
	})
	defer closeServer()
	client, err := NewOpenAI(OpenAIConfig{BaseURL: serverURL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	result, err := client.Stream(context.Background(), Request{}, func(delta string) error {
		text.WriteString(delta)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello world" || result.Message.Content != "Hello world" {
		t.Fatalf("text callback=%q result=%q", text.String(), result.Message.Content)
	}
	if result.FinishReason != "tool_calls" || len(result.Message.ToolCalls) != 1 {
		t.Fatalf("result = %+v", result)
	}
	call := result.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "lookup" || string(call.Arguments) != `{"x":1}` {
		t.Fatalf("tool call = %+v", call)
	}
}

func TestStreamCancellationAndCallbackError(t *testing.T) {
	serverURL, closeServer := streamFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"first"}}]}`)
		flusher, _ := w.(http.Flusher)
		if flusher != nil {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	defer closeServer()
	client, err := NewOpenAI(OpenAIConfig{BaseURL: serverURL, Model: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = client.Stream(ctx, Request{}, func(string) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}

	callbackErr := errors.New("stop deltas")
	_, err = client.Stream(context.Background(), Request{}, func(string) error { return callbackErr })
	if !errors.Is(err, callbackErr) {
		t.Fatalf("callback error = %v", err)
	}
}

func TestStreamRejectsPrematureEOFMalformedAndMultipleChoices(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"premature", "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", "before [DONE]"},
		{"malformed", "data: not-json\n\ndata: [DONE]\n\n", "invalid SSE event"},
		{"multiple", "data: {\"choices\":[{\"index\":0,\"delta\":{}},{\"index\":1,\"delta\":{}}]}\n\ndata: [DONE]\n\n", "exactly one choice"},
		{"missing-finish", "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n", "no terminal finish"},
		{"length", "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n", "incomplete response"},
		{"tool-arguments", "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"id\",\"type\":\"function\",\"function\":{\"name\":\"tool\",\"arguments\":\"[]\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", "tool arguments must be a JSON object"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			serverURL, closeServer := streamFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) })
			defer closeServer()
			client, err := NewOpenAI(OpenAIConfig{BaseURL: serverURL, Model: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Stream(context.Background(), Request{}, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStreamLimits(t *testing.T) {
	serverURL, closeServer := streamFixture(t, func(w http.ResponseWriter, r *http.Request) {
		for i := 0; i < 33; i++ {
			writeSSE(w, fmt.Sprintf(`{"choices":[{"index":%d,"delta":{"tool_calls":[{"index":%d,"id":"id","type":"function","function":{"name":"tool","arguments":"{}"}}]}}]}`, 0, i))
		}
		writeSSE(w, `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)
		writeSSE(w, "[DONE]")
	})
	defer closeServer()
	client, err := NewOpenAI(OpenAIConfig{BaseURL: serverURL, Model: "fixture", MaxResponseBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Stream(context.Background(), Request{}, nil); err == nil || !strings.Contains(err.Error(), "too many tool calls") {
		t.Fatalf("limit error = %v", err)
	}

	largeURL, closeLarge := streamFixture(t, func(w http.ResponseWriter, r *http.Request) {
		writeSSE(w, `{"choices":[{"index":0,"delta":{"content":"01234567890123456789"}}]}`)
	})
	defer closeLarge()
	limited, err := NewOpenAI(OpenAIConfig{BaseURL: largeURL, Model: "fixture", MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := limited.Stream(context.Background(), Request{}, nil); err == nil || !strings.Contains(err.Error(), "configured limit") {
		t.Fatalf("body limit error = %v", err)
	}
}

func TestStreamDoneDoesNotReadPastTerminalEvent(t *testing.T) {
	body := []byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	reader := &terminalReader{data: body}
	client := &OpenAI{maxBody: 1 << 20}
	result, err := client.parseSSE(context.Background(), reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "stop" || reader.readAfterDone {
		t.Fatalf("result=%+v readAfterDone=%v", result, reader.readAfterDone)
	}
}

func TestStreamSingleLineIsBoundedBeforeBuffering(t *testing.T) {
	client := &OpenAI{maxBody: 32}
	_, err := client.parseSSE(context.Background(), io.LimitReader(strings.NewReader(strings.Repeat("x", 100)), 33), nil)
	if err == nil || !strings.Contains(err.Error(), "configured limit") {
		t.Fatalf("error = %v", err)
	}
}

func writeSSE(w http.ResponseWriter, data string) {
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func streamFixture(t *testing.T, handler http.HandlerFunc) (string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(listener) }()
	return "http://" + listener.Addr().String(), func() { _ = srv.Close() }
}

type terminalReader struct {
	data          []byte
	readAfterDone bool
}

func (r *terminalReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		r.readAfterDone = true
		return 0, errors.New("read after terminal event")
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestCompatibleStreamRetainsUsageOnlyTerminalChunk(t *testing.T) {
	c, err := NewOpenAI(OpenAIConfig{BaseURL: "http://localhost:1", Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	body := `data: {"choices":[{"index":0,"delta":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":12,"completion_tokens":3,"total_tokens":15}}

data: [DONE]

`
	result, err := c.parseSSE(context.Background(), strings.NewReader(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 12 || result.Usage.OutputTokens != 3 || result.Usage.TotalTokens != 15 {
		t.Fatal(result.Usage)
	}
}

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAIResponsesCompleteAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path = %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "fixture" {
			t.Errorf("body = %#v", body)
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"call_id\":\"c1\",\"name\":\"look\",\"delta\":\"{\\\"x\\\":1}\"}\n\n")
			fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":2,\"output_tokens\":3,\"total_tokens\":5}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp_1","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]},{"type":"function_call","call_id":"c1","name":"look","arguments":"{\"x\":1}"}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`)
	}))
	defer srv.Close()
	c, err := NewOpenAIResponses(ResponsesConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x", Images: []Image{{MediaType: "image/png", Data: []byte("png")}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "hello" || len(result.Message.ToolCalls) != 1 || result.Usage.TotalTokens != 5 {
		t.Fatalf("result = %+v", result)
	}
	var got strings.Builder
	result, err = c.Stream(context.Background(), Request{}, func(s string) error { got.WriteString(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "hi" || result.Message.Content != "hi" || len(result.Message.ToolCalls) != 1 || result.Message.ToolCalls[0].ID != "c1" {
		t.Fatalf("stream = %+v text=%q", result, got.String())
	}
}

func TestOpenAIResponsesStreamMergesFunctionCallIdentity(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"look\",\"namespace\":\"bifrost\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"x\\\":1}\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"look\",\"namespace\":\"bifrost\",\"arguments\":\"{\\\"x\\\":1}\"}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n")
	}))
	defer srv.Close()
	c, err := NewOpenAIResponses(ResponsesConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Stream(context.Background(), Request{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.ToolCalls) != 1 {
		t.Fatalf("calls = %+v", result.Message.ToolCalls)
	}
	call := result.Message.ToolCalls[0]
	if call.ID != "call_1" || call.Name != "look" || call.Namespace != "bifrost" || string(call.Arguments) != `{"x":1}` {
		t.Fatalf("call = %+v", call)
	}
	prefixed, err := responsesToolCall(responsesOutput{Type: "function_call", CallID: "c", Name: "bifrost.look", Namespace: "bifrost", Arguments: `{"x":1}`})
	if err != nil {
		t.Fatal(err)
	}
	if prefixed.Name != "look" || prefixed.Namespace != "bifrost" || prefixed.ID != "c" {
		t.Fatalf("prefixed = %+v", prefixed)
	}
}

func TestAnthropicCompleteAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("missing anthropic-version")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] == true {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m1\",\"usage\":{\"input_tokens\":4}}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\"}}\n\n")
			fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"thinking\",\"signature\":\"stream-sig\"}}\n\n")
			fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"id":"m1","stop_reason":"tool_use","content":[{"type":"text","text":"look"},{"type":"tool_use","id":"t1","name":"look","input":{"x":1}}],"usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	defer srv.Close()
	c, err := NewAnthropic(AnthropicConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "tool_calls" || len(result.Message.ToolCalls) != 1 || result.Usage.TotalTokens != 6 {
		t.Fatalf("result = %+v", result)
	}
	var got strings.Builder
	result, err = c.Stream(context.Background(), Request{}, func(s string) error { got.WriteString(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "hello" || result.FinishReason != "stop" || len(result.Message.Opaque) == 0 {
		t.Fatalf("stream = %+v text=%q", result, got.String())
	}
}

func TestGeminiCompleteAndStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "secret" {
			t.Error("missing Gemini key")
		}
		if strings.Contains(r.URL.Path, "streamGenerateContent") || r.URL.RawQuery == "alt=sse" {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":2,\"totalTokenCount\":3}}\n\n")
			return
		}
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"look","args":{"x":1},"id":"c1"},"thoughtSignature":"sig"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	defer srv.Close()
	c, err := NewGemini(GeminiConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	if result.FinishReason != "tool_calls" || len(result.Message.ToolCalls) != 1 || result.Usage.TotalTokens != 3 || len(result.Message.Opaque) == 0 {
		t.Fatalf("result = %+v", result)
	}
	var got strings.Builder
	result, err = c.Stream(context.Background(), Request{}, func(s string) error { got.WriteString(s); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != "hi" || result.FinishReason != "stop" {
		t.Fatalf("stream = %+v text=%q", result, got.String())
	}
}

func TestNativeFactoryAndURLPolicy(t *testing.T) {
	if _, err := New(Config{Kind: KindAnthropic, Model: "fixture", BaseURL: "http://example.com"}); err == nil {
		t.Fatal("accepted non-loopback HTTP")
	}
	if _, err := New(Config{Kind: "unknown", Model: "fixture"}); err == nil {
		t.Fatal("accepted unknown provider")
	}
}

func TestNativeHTTPErrorAndCancellation(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"one\"}\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	c, err := NewOpenAIResponses(ResponsesConfig{BaseURL: srv.URL, Model: "fixture", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), Request{})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusTooManyRequests || httpErr.RetryAfter != "3" {
		t.Fatalf("error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = c.Stream(ctx, Request{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("request count = %d, want 2", calls)
	}
}

func TestResponsesRejectsInvalidImageAndReplaysOpaqueOutput(t *testing.T) {
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		requests = append(requests, body)
		_, _ = io.WriteString(w, `{"id":"r1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"look","arguments":"{\"x\":1}"}]}`)
	}))
	defer srv.Close()
	c, err := NewOpenAIResponses(ResponsesConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Images: []Image{{MediaType: "image/png"}}}}})
	if err == nil || len(requests) != 0 {
		t.Fatalf("invalid image err=%v requests=%d", err, len(requests))
	}
	first, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Message.Opaque) == 0 {
		t.Fatal("reasoning/output items were not preserved")
	}
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: RoleAssistant, Opaque: first.Message.Opaque}, {Role: RoleTool, ToolCallID: "call_1", Content: "ok"}}})
	if err != nil {
		t.Fatal(err)
	}
	input, ok := requests[1]["input"].([]any)
	if !ok || len(input) != 3 {
		t.Fatalf("replayed input=%#v", requests[1]["input"])
	}
	if item, ok := input[0].(map[string]any); !ok || item["type"] != "reasoning" {
		t.Fatalf("first replay item=%#v", input[0])
	}
}

func TestAnthropicThinkingOpaqueAndUnknownFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"m1","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"hidden","signature":"sig"},{"type":"text","text":"visible"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	c, err := NewAnthropic(AnthropicConfig{BaseURL: srv.URL, Model: "fixture", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message.Content != "visible" || len(result.Message.Opaque) == 0 || strings.Contains(result.Message.Content, "hidden") {
		t.Fatalf("thinking result=%+v", result.Message)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"stop_reason":"mystery","content":[{"type":"tool_use","id":"x","name":"x","input":{}}]}`)
	}))
	defer bad.Close()
	c, err = NewAnthropic(AnthropicConfig{BaseURL: bad.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("unknown Anthropic finish accepted")
	}
}

func TestGeminiUniqueIDsThoughtsAndUnknownFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"look","args":{"x":1}}},{"functionCall":{"name":"look","args":{"x":2}}},{"text":"hidden","thought":true},{"text":"shown"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()
	c, err := NewGemini(GeminiConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Complete(context.Background(), Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Message.ToolCalls) != 2 || result.Message.ToolCalls[0].ID == result.Message.ToolCalls[1].ID || result.Message.Content != "shown" {
		t.Fatalf("Gemini result=%+v", result.Message)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"x","args":{}}}]},"finishReason":"UNKNOWN"}]}`)
	}))
	defer bad.Close()
	c, err = NewGemini(GeminiConfig{BaseURL: bad.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("unknown Gemini finish accepted")
	}
}

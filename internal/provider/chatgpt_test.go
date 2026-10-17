package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatGPTWireContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("authorization header missing")
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["store"] != false || body["stream"] != true {
			t.Errorf("store/stream contract: %#v", body)
		}
		if _, ok := body["previous_response_id"]; ok {
			t.Errorf("previous_response_id must be omitted")
		}
		input := body["input"].([]any)
		first := input[0].(map[string]any)
		if first["role"] != "developer" {
			t.Errorf("role: %#v", first["role"])
		}
		tools := body["tools"].([]any)
		namespace := tools[0].(map[string]any)
		if namespace["type"] != "namespace" {
			t.Errorf("namespace: %#v", namespace)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[]}}\n\n"))
	}))
	defer server.Close()
	client, err := NewChatGPT(ChatGPTConfig{BaseURL: server.URL, Model: "model", TokenSource: func(context.Context) (string, error) { return "access", nil }})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Stream(context.Background(), Request{Messages: []Message{{Role: RoleSystem, Content: "rules"}}, Tools: []ToolDefinition{{Name: "run", Parameters: []byte(`{"type":"object"}`)}}, Opaque: []byte(`"old"`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hi" {
		t.Fatalf("content: %#v", resp.Message.Content)
	}
}

package cursorclient

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestMCPBridgeProtobufArgumentsAndErrors(t *testing.T) {
	value := func(kind string, data any) *Value { v, _ := New("Value"); v.Set(kind, data); return v }
	nested, _ := New("Struct")
	nested.Set("fields", map[string]any{"path": value("string_value", "main.go"), "start_line": value("number_value", float64(3))})
	list, _ := New("ListValue")
	list.Set("values", []*Value{value("struct_value", nested)})
	empty, _ := New("ListValue")
	parameters := map[string]any{"steps": value("list_value", list), "empty": value("list_value", empty), "enabled": value("bool_value", false), "nothing": value("null_value", int64(0))}
	req := execReq("mcp_args", "flow", map[string]any{"name": "bifrost_viewer_flow", "server_identifier": "bifrost", "args": parameters})
	raw, err := req.Encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode("ExecServerMessage", raw)
	if err != nil {
		t.Fatal(err)
	}
	var received map[string]any
	calls := 0
	env := &ToolEnv{Tools: []Tool{{Name: "viewer_flow", Schema: json.RawMessage(`{"type":"object"}`), Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
		calls++
		return "opened", json.Unmarshal(raw, &received)
	}}}}
	result := env.exec(context.Background(), decoded).Message("exec_client_message").Message("mcp_result").Message("success")
	if result == nil || result.Bool("is_error") || calls != 1 {
		t.Fatalf("bridge: %+v calls %d", result, calls)
	}
	want := map[string]any{"steps": []any{map[string]any{"path": "main.go", "start_line": float64(3)}}, "empty": []any{}, "enabled": false, "nothing": nil}
	if !reflect.DeepEqual(received, want) {
		t.Fatalf("args %+v want %+v", received, want)
	}
	env.Tools[0].Call = func(context.Context, json.RawMessage) (string, error) {
		return "", errors.New("path escapes workspace")
	}
	decoded.Set("exec_id", "bad")
	result = env.exec(context.Background(), decoded).Message("exec_client_message").Message("mcp_result").Message("success")
	if result == nil || !result.Bool("is_error") {
		t.Fatal("tool error lost")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	env.Tools[0].Call = func(context.Context, json.RawMessage) (string, error) {
		t.Fatal("called after cancellation")
		return "", nil
	}
	decoded.Set("exec_id", "cancelled")
	result = env.exec(ctx, decoded).Message("exec_client_message").Message("mcp_result").Message("success")
	if result == nil || !result.Bool("is_error") {
		t.Fatal("cancellation lost")
	}
}

func TestBridgeDiscoveryInRunAndContext(t *testing.T) {
	tools := []Tool{{Name: "viewer_flow", Description: "Show steps", Schema: json.RawMessage(`{"type":"object"}`)}}
	msg, err := buildRun(RunInput{Prompt: "Explain", Tools: tools}, "conversation", "run")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := msg.Encode()
	if err != nil {
		t.Fatal(err)
	}
	msg, err = Decode("AgentClientMessage", raw)
	if err != nil {
		t.Fatal(err)
	}
	defs := msg.Message("run_request").Message("mcp_tools").Messages("mcp_tools")
	if len(defs) != 1 || defs[0].String("tool_name") != "viewer_flow" || defs[0].String("input_schema_json") == "" {
		t.Fatal("missing advertised bridge")
	}
}

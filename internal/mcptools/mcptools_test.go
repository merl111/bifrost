package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/mcpclient"
	"bifrost/internal/provider"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testProtocol = "2026-07-28"

func TestMain(m *testing.M) {
	if os.Getenv("BIFROST_MCPTOOLS_FIXTURE") == "1" {
		server := mcp.NewServer(&mcp.Implementation{Name: "stdio-fixture", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
		server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "stdio-ok"}}}, nil
		})
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestOpenAdapterAndAgentToolCall(t *testing.T) {
	serverURL, closeServer := mcpFixture(t, false)
	defer closeServer()
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config := `{"servers":[{"name":"demo","url":"` + serverURL + `"}]}`
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	tools, cleanup, err := Open(context.Background(), configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(tools) != 1 || tools[0].Definition.Name != "mcp_demo_echo" {
		t.Fatalf("tools = %+v", tools)
	}
	encoded, err := tools[0].Call(context.Background(), json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	var result mcpclient.Result
	if err := json.Unmarshal([]byte(encoded), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || !strings.Contains(string(result.Content[0]), "hello") {
		t.Fatalf("MCP result = %+v", result)
	}

	root, state := t.TempDir(), t.TempDir()
	fake := &fakeProvider{}
	resultAgent, err := agent.Run(context.Background(), agent.Config{Provider: fake, Root: root, StateDir: state, ExtraTools: tools, MaxSteps: 3}, "echo hello")
	if err != nil {
		t.Fatal(err)
	}
	if resultAgent.Text != "done" || fake.calls != 2 || !strings.Contains(fake.toolResult, "hello") {
		t.Fatalf("agent result=%+v provider=%+v", resultAgent, fake)
	}
	cleanup()
	cleanup()
}

func TestInputRequiredIsSerializedAndExplicit(t *testing.T) {
	serverURL, closeServer := mcpFixture(t, true)
	defer closeServer()
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(configPath, []byte(`{"servers":[{"name":"demo","url":"`+serverURL+`"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	tools, cleanup, err := Open(context.Background(), configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
	var inputTool agent.Tool
	for _, tool := range tools {
		if tool.Definition.Name == "mcp_demo_needs_input" {
			inputTool = tool
		}
	}
	encoded, err := inputTool.Call(context.Background(), json.RawMessage(`{}`))
	if !errors.Is(err, mcpclient.ErrInputRequired) {
		t.Fatalf("error = %v", err)
	}
	var result mcpclient.Result
	if decodeErr := json.Unmarshal([]byte(encoded), &result); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if !result.NeedsInput || result.RequestState != "fixture-state" || len(result.InputRequests) == 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestStdioToolSurvivesDiscoveryContextCancellation(t *testing.T) {
	if err := os.Setenv("BIFROST_MCPTOOLS_FIXTURE", "1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("BIFROST_MCPTOOLS_FIXTURE") })
	configPath := filepath.Join(t.TempDir(), "mcp.json")
	config := map[string]any{"servers": []any{map[string]any{"name": "stdio", "command": []string{os.Args[0], "-test.run=TestStdioMCPFixture"}}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	tools, cleanup, err := Open(context.Background(), configPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", tools)
	}
	got, err := tools[0].Call(context.Background(), json.RawMessage(`{}`))
	if err != nil || !strings.Contains(got, "stdio-ok") {
		t.Fatalf("stdio result=%q err=%v", got, err)
	}
}

func TestConfigStrictLimitsAndNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	tests := []string{
		`{"servers":null}`,
		`{"servers":[{"name":"demo","url":"http://127.0.0.1:1","extra":true}]}`,
		`{"servers":[{"name":"demo","url":"http://127.0.0.1:1","command":["x"]}]}`,
		`{"servers":[{"name":"demo"}]}`,
		`{"servers":[{"name":"demo","url":"http://127.0.0.1:1"}]} {}`,
	}
	for _, content := range tests {
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Open(context.Background(), path, nil); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("config %s error = %v", content, err)
		}
	}
	var tooMany strings.Builder
	tooMany.WriteString(`{"servers":[`)
	for i := 0; i < 9; i++ {
		if i > 0 {
			tooMany.WriteByte(',')
		}
		fmt.Fprintf(&tooMany, `{"name":"s%d","url":"http://127.0.0.1:1"}`, i)
	}
	tooMany.WriteString(`]}`)
	if err := os.WriteFile(path, []byte(tooMany.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Open(context.Background(), path, nil); !errors.Is(err, ErrLimit) {
		t.Fatalf("server limit error = %v", err)
	}
	if _, err := toolName("demo", "a-b"); err != nil {
		t.Fatal(err)
	}
	a, _ := toolName("a-b", "run")
	b, _ := toolName("a_b", "run")
	if a != b {
		t.Fatalf("normalization unexpectedly differs: %q %q", a, b)
	}
	if _, err := toolName("demo", strings.Repeat("x", 60)); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("long tool name error = %v", err)
	}
}

type fakeProvider struct {
	calls      int
	toolResult string
}

func (p *fakeProvider) Complete(ctx context.Context, req provider.Request) (provider.Response, error) {
	p.calls++
	if p.calls == 1 {
		found := false
		for _, tool := range req.Tools {
			if tool.Name == "mcp_demo_echo" {
				found = true
			}
		}
		if !found {
			return provider.Response{}, fmt.Errorf("unexpected tools: %+v", req.Tools)
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "mcp_demo_echo", Arguments: json.RawMessage(`{"message":"hello"}`)}}}}, nil
	}
	for _, message := range req.Messages {
		if message.Role == provider.RoleTool {
			p.toolResult = message.Content
		}
	}
	return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
}

func mcpFixture(t *testing.T, includeInput bool) (string, func()) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{testProtocol}, PageSize: 1})
	server.AddTool(&mcp.Tool{Name: "echo", Description: "echo", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
	})
	if includeInput {
		server.AddTool(&mcp.Tool{Name: "needs_input", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{Message: "Continue?"}}, RequestState: "fixture-state"}, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: true})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler}
	go func() { _ = httpServer.Serve(listener) }()
	return "http://" + listener.Addr().String(), func() { _ = httpServer.Close() }
}

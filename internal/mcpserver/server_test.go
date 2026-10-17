package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRootedServerScopesToolsAndRevisionCheckedWrite(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "note.txt"), []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	server, cleanup, err := NewServer(Config{Root: root, StateDir: state, AllowWrite: true})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	defer serverSession.Close()
	tools, err := clientSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasTool(tools.Tools, "read_file") || !hasTool(tools.Tools, "write_file") || hasTool(tools.Tools, "exec") {
		t.Fatalf("tools=%v", toolNames(tools.Tools))
	}
	read, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "read_file", Arguments: map[string]any{"path": "note.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	var before struct{ Path, Revision, Content string }
	decodeTool(t, read, &before)
	if before.Content != "before" || before.Revision == "" {
		t.Fatalf("read=%+v", before)
	}
	updated, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "write_file", Arguments: map[string]any{"path": "note.txt", "content": "after", "revision": before.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.IsError {
		t.Fatalf("write error: %+v", updated)
	}
	data, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil || string(data) != "after" {
		t.Fatalf("disk=%q err=%v", data, err)
	}
	bad, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "write_file", Arguments: map[string]any{"path": "../escape", "content": "x"}})
	if err != nil {
		t.Fatal(err)
	}
	if !bad.IsError {
		t.Fatal("path escape was accepted")
	}
}

func TestRootedServerReadOnlyByDefault(t *testing.T) {
	server, cleanup, err := NewServer(Config{Root: t.TempDir(), StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasTool(tools.Tools, "write_file") || hasTool(tools.Tools, "exec") {
		t.Fatalf("privileged tools exposed: %v", toolNames(tools.Tools))
	}
}

func hasTool(tools []*mcp.Tool, name string) bool {
	for _, tool := range tools {
		if tool != nil && tool.Name == name {
			return true
		}
	}
	return false
}
func toolNames(tools []*mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		if tool != nil {
			out = append(out, tool.Name)
		}
	}
	return out
}
func decodeTool(t *testing.T, result *mcp.CallToolResult, out any) {
	t.Helper()
	if result == nil || result.IsError || len(result.Content) != 1 {
		t.Fatalf("result=%+v", result)
	}
	text, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content=%T", result.Content[0])
	}
	if err := json.Unmarshal([]byte(text.Text), out); err != nil {
		t.Fatal(err)
	}
}

// Package mcpserver exposes a deliberately small, rooted MCP server over
// stdio. Mutating and process tools are opt-in at construction time.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bifrost/internal/process"
	"bifrost/internal/workspace"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxToolOutput = 1 << 20

type Config struct {
	Root           string
	StateDir       string
	AllowWrite     bool
	AllowExec      bool
	MaxOutputBytes int
}

type serverState struct {
	workspace  *workspace.Workspace
	root       string
	allowWrite bool
	allowExec  bool
	maxOutput  int
	cleanup    func()
}

// NewServer builds a rooted server and returns an idempotent cleanup function.
// StateDir is used by workspace's revision journal. If omitted, a private
// temporary directory is created outside the workspace and removed on cleanup.
func NewServer(cfg Config) (*mcp.Server, func(), error) {
	root, err := filepath.Abs(strings.TrimSpace(cfg.Root))
	if err != nil || root == "." || strings.TrimSpace(cfg.Root) == "" {
		return nil, func() {}, errors.New("MCP server root is required")
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, func() {}, fmt.Errorf("MCP server root: %w", err)
	}
	if stat, err := os.Stat(rootReal); err != nil || !stat.IsDir() {
		if err != nil {
			return nil, func() {}, fmt.Errorf("MCP server root: %w", err)
		}
		return nil, func() {}, errors.New("MCP server root is not a directory")
	}
	stateDir := strings.TrimSpace(cfg.StateDir)
	tempState := false
	if stateDir == "" {
		stateDir, err = os.MkdirTemp("", "bifrost-mcp-state-")
		if err != nil {
			return nil, func() {}, fmt.Errorf("MCP server state: %w", err)
		}
		tempState = true
	}
	ws, err := workspace.Open(rootReal, stateDir)
	if err != nil {
		if tempState {
			_ = os.RemoveAll(stateDir)
		}
		return nil, func() {}, err
	}
	maxOutput := cfg.MaxOutputBytes
	if maxOutput <= 0 || maxOutput > maxToolOutput {
		maxOutput = maxToolOutput
	}
	state := &serverState{workspace: ws, root: rootReal, allowWrite: cfg.AllowWrite, allowExec: cfg.AllowExec, maxOutput: maxOutput}
	state.cleanup = func() {
		if state.workspace != nil {
			_ = state.workspace.Close()
			state.workspace = nil
		}
		if tempState {
			_ = os.RemoveAll(stateDir)
		}
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "bifrost-workspace", Version: "0.1.0"}, &mcp.ServerOptions{
		Instructions: "Rooted workspace tools. Writes and process execution are disabled unless explicitly enabled by the host.",
	})
	addTools(server, state)
	return server, state.cleanup, nil
}

// Serve runs the native stdio transport until it exits or ctx is canceled.
func Serve(ctx context.Context, cfg Config) error {
	if ctx == nil {
		ctx = context.Background()
	}
	server, cleanup, err := NewServer(cfg)
	if err != nil {
		return err
	}
	defer cleanup()
	return server.Run(ctx, &mcp.StdioTransport{})
}

type pathInput struct {
	Path string `json:"path"`
}
type searchInput struct {
	Query string `json:"query"`
}
type writeInput struct {
	Path     string  `json:"path"`
	Content  string  `json:"content"`
	Revision *string `json:"revision"`
}
type execInput struct {
	Argv []string `json:"argv"`
}

func addTools(server *mcp.Server, state *serverState) {
	server.AddTool(&mcp.Tool{Name: "list_files", Description: "List immediate children below the rooted workspace.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}, state.list)
	server.AddTool(&mcp.Tool{Name: "read_file", Description: "Read one UTF-8 text file below the rooted workspace.", InputSchema: map[string]any{"type": "object", "required": []string{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}, state.read)
	server.AddTool(&mcp.Tool{Name: "search_files", Description: "Search bounded UTF-8 text files below the rooted workspace.", InputSchema: map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string"}}}}, state.search)
	if state.allowWrite {
		server.AddTool(&mcp.Tool{Name: "write_file", Description: "Revision-checked write below the rooted workspace.", InputSchema: map[string]any{"type": "object", "required": []string{"path", "content"}, "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}, "revision": map[string]any{"type": "string"}}}}, state.write)
	}
	if state.allowExec {
		server.AddTool(&mcp.Tool{Name: "exec", Description: "Run an explicit argv in the rooted workspace; no shell interpolation.", InputSchema: map[string]any{"type": "object", "required": []string{"argv"}, "properties": map[string]any{"argv": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}}}, state.exec)
	}
}

func (s *serverState) list(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input pathInput
	if err := decodeArgs(req, &input); err != nil {
		return toolError(err), nil
	}
	entries, err := s.workspace.List(input.Path)
	if err != nil {
		return toolError(err), nil
	}
	return jsonTool(entries)
}

func (s *serverState) read(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input pathInput
	if err := decodeArgs(req, &input); err != nil {
		return toolError(err), nil
	}
	text, revision, err := s.workspace.Read(input.Path)
	if err != nil {
		return toolError(err), nil
	}
	return jsonTool(map[string]any{"path": input.Path, "revision": revision, "content": text})
}

func (s *serverState) search(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input searchInput
	if err := decodeArgs(req, &input); err != nil {
		return toolError(err), nil
	}
	result, err := s.workspace.Search(ctx, input.Query)
	if err != nil {
		return toolError(err), nil
	}
	return jsonTool(result)
}

func (s *serverState) write(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input writeInput
	if err := decodeArgs(req, &input); err != nil {
		return toolError(err), nil
	}
	change, err := s.workspace.Write(input.Path, input.Revision, input.Content)
	if err != nil {
		return toolError(err), nil
	}
	return jsonTool(change)
}

func (s *serverState) exec(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input execInput
	if err := decodeArgs(req, &input); err != nil {
		return toolError(err), nil
	}
	if len(input.Argv) == 0 || input.Argv[0] == "" {
		return toolError(errors.New("argv is required")), nil
	}
	for _, arg := range input.Argv {
		if strings.ContainsAny(arg, "\x00\r\n") {
			return toolError(errors.New("argv contains forbidden control characters")), nil
		}
	}
	result, err := process.Run(ctx, process.Spec{Argv: input.Argv, Dir: s.root, MaxOutputBytes: s.maxOutput})
	if err != nil && !result.Cancelled {
		return toolError(err), nil
	}
	return jsonTool(result)
}

func decodeArgs(req *mcp.CallToolRequest, out any) error {
	if req == nil || req.Params == nil {
		return errors.New("missing tool arguments")
	}
	if len(req.Params.Arguments) == 0 {
		return errors.New("tool arguments are required")
	}
	return json.Unmarshal(req.Params.Arguments, out)
}

func jsonTool(value any) (*mcp.CallToolResult, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(data) > maxToolOutput {
		return toolError(errors.New("tool output exceeds limit")), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: json.RawMessage(data)}, nil
}

func toolError(err error) *mcp.CallToolResult {
	if err == nil {
		err = errors.New("MCP tool failed")
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

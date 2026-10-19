// Package mcptools exposes configured MCP tools as agent-native tools.
// Configuration is explicit and is never discovered from a project.
package mcptools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bifrost/internal/agent"
	"bifrost/internal/mcpclient"
	"bifrost/internal/provider"
)

const (
	maxConfigBytes     = 1 << 20
	maxServers         = 8
	maxTools           = 100
	maxNameBytes       = 64
	maxDefinitionBytes = 64 << 10
	maxCatalogBytes    = 1 << 20
	discoveryLimit     = 20 * time.Second
	callLimit          = 2 * time.Minute
)

var (
	ErrInvalidConfig = errors.New("invalid MCP tools config")
	ErrLimit         = errors.New("MCP tools config limit exceeded")
)

type fileConfig struct {
	Servers []serverConfig `json:"servers"`
}

type serverConfig struct {
	Name    string                `json:"name"`
	URL     string                `json:"url,omitempty"`
	Command []string              `json:"command,omitempty"`
	Headers []mcpclient.HeaderRef `json:"headers,omitempty"`
}

// ServerConfig is the persisted, credential-free description of an MCP
// endpoint. Header values are referenced by environment variable name only.
type ServerConfig = serverConfig

// ConfigServers returns the explicit server entries from configPath.
func ConfigServers(configPath string) ([]ServerConfig, error) {
	cfg, err := readConfig(configPath)
	if err != nil {
		return nil, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return append([]ServerConfig(nil), cfg.Servers...), nil
}

// AddServer adds or replaces one named endpoint and writes a mode-0600 JSON
// file atomically. It never accepts or stores a header value.
func AddServer(configPath string, server ServerConfig) error {
	if err := validateServer(server); err != nil {
		return err
	}
	cfg, err := readConfigOrEmpty(configPath)
	if err != nil {
		return err
	}
	for i, existing := range cfg.Servers {
		if existing.Name == server.Name {
			cfg.Servers[i] = server
			return writeConfig(configPath, cfg)
		}
	}
	if len(cfg.Servers) >= maxServers {
		return fmt.Errorf("%w: at most %d servers", ErrLimit, maxServers)
	}
	cfg.Servers = append(cfg.Servers, server)
	return writeConfig(configPath, cfg)
}

func RemoveServer(configPath, name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%w: server name is required", ErrInvalidConfig)
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		return err
	}
	for i, server := range cfg.Servers {
		if server.Name == name {
			cfg.Servers = append(cfg.Servers[:i], cfg.Servers[i+1:]...)
			return writeConfig(configPath, cfg)
		}
	}
	return fmt.Errorf("MCP server %q is not configured", name)
}

// Open reads an explicit MCP server configuration, discovers its tools, and
// returns agent tools plus an idempotent cleanup function. Discovery is
// bounded to eight servers and one hundred tools in total.
func Open(ctx context.Context, configPath string, stderr io.Writer) ([]agent.Tool, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := readConfig(configPath)
	if err != nil {
		return nil, func() {}, err
	}
	if err := validateConfig(cfg); err != nil {
		return nil, func() {}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, func() {}, err
	}

	var sessions []*mcpclient.Session
	var tools []agent.Tool
	var catalogBytes int
	seenNames := make(map[string]string)
	cleanup := newCleanup(&sessions)
	fail := func(err error) ([]agent.Tool, func(), error) {
		cleanup()
		return nil, func() {}, err
	}
	for _, server := range cfg.Servers {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		serverCtx, cancel := context.WithTimeout(ctx, discoveryLimit)
		endpoint := mcpclient.Endpoint{ProtocolVersion: ""}
		if server.URL != "" {
			endpoint.URL = server.URL
		} else {
			endpoint.Command = append([]string(nil), server.Command...)
		}
		endpoint.Headers = append([]mcpclient.HeaderRef(nil), server.Headers...)
		session, err := mcpclient.Dial(serverCtx, endpoint, stderr)
		if err == nil {
			sessions = append(sessions, session)
		}
		if err == nil {
			var discovered []mcpclient.Tool
			discovered, err = session.List(serverCtx)
			if err == nil {
				for _, tool := range discovered {
					if len(tools) >= maxTools {
						err = fmt.Errorf("%w: at most %d tools", ErrLimit, maxTools)
						break
					}
					name, nameErr := toolName(server.Name, tool.Name)
					if nameErr != nil {
						err = nameErr
						break
					}
					if previous, exists := seenNames[name]; exists {
						err = fmt.Errorf("%w: tool %q from %q collides with %q", ErrInvalidConfig, name, server.Name, previous)
						break
					}
					seenNames[name] = server.Name + "/" + tool.Name
					definition := agent.Tool{Definition: agentDefinition(name, tool)}
					definitionBytes := definitionSize(definition.Definition)
					if definitionBytes > maxDefinitionBytes {
						err = fmt.Errorf("%w: tool definition %q exceeds %d bytes", ErrLimit, name, maxDefinitionBytes)
						break
					}
					catalogBytes += definitionBytes
					if catalogBytes > maxCatalogBytes {
						err = fmt.Errorf("%w: tool definitions exceed %d bytes", ErrLimit, maxCatalogBytes)
						break
					}
					definition.Call = makeToolCall(session, tool.Name)
					tools = append(tools, definition)
				}
			}
		}
		cancel()
		if err != nil {
			return fail(fmt.Errorf("discover MCP server %q: %w", server.Name, err))
		}
	}
	return tools, cleanup, nil
}

func readConfigOrEmpty(path string) (fileConfig, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return fileConfig{Servers: []serverConfig{}}, nil
	}
	return readConfig(path)
}

func writeConfig(path string, cfg fileConfig) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: config path is required", ErrInvalidConfig)
	}
	if len(cfg.Servers) > maxServers {
		return fmt.Errorf("%w: at most %d servers", ErrLimit, maxServers)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal MCP tools config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return fmt.Errorf("%w: config exceeds %d bytes", ErrLimit, maxConfigBytes)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mcp-config-*")
	if err != nil {
		return fmt.Errorf("create MCP config temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write MCP tools config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace MCP tools config: %w", err)
	}
	return nil
}

func readConfig(path string) (fileConfig, error) {
	if strings.TrimSpace(path) == "" {
		return fileConfig{}, fmt.Errorf("%w: config path is required", ErrInvalidConfig)
	}
	f, err := os.Open(path)
	if err != nil {
		return fileConfig{}, fmt.Errorf("open MCP tools config: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return fileConfig{}, fmt.Errorf("read MCP tools config: %w", err)
	}
	if len(data) > maxConfigBytes {
		return fileConfig{}, fmt.Errorf("%w: config exceeds %d bytes", ErrLimit, maxConfigBytes)
	}
	var cfg fileConfig
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidConfig, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return fileConfig{}, fmt.Errorf("%w: invalid JSON: %v", ErrInvalidConfig, err)
	}
	if _, ok := fields["servers"]; !ok {
		return fileConfig{}, fmt.Errorf("%w: servers is required", ErrInvalidConfig)
	}
	if bytes.Equal(bytes.TrimSpace(fields["servers"]), []byte("null")) {
		return fileConfig{}, fmt.Errorf("%w: servers must be an array", ErrInvalidConfig)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fileConfig{}, fmt.Errorf("%w: multiple JSON values", ErrInvalidConfig)
		}
		return fileConfig{}, fmt.Errorf("%w: trailing JSON: %v", ErrInvalidConfig, err)
	}
	return cfg, nil
}

func validateServer(server serverConfig) error {
	if err := mcpclient.ValidateEndpoint(mcpclient.Endpoint{URL: server.URL, Command: server.Command, Headers: server.Headers}); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}
	if strings.TrimSpace(server.Name) == "" {
		return fmt.Errorf("%w: server name is required", ErrInvalidConfig)
	}
	for _, arg := range server.Command {
		if arg == "" {
			return fmt.Errorf("%w: server %q has an empty command argument", ErrInvalidConfig, server.Name)
		}
	}
	return nil
}

func component(value string) string {
	var out strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

func toolName(server, tool string) (string, error) {
	serverPart, toolPart := component(server), component(tool)
	if serverPart == "" || toolPart == "" {
		return "", fmt.Errorf("%w: tool name %q is empty after normalization", ErrInvalidConfig, tool)
	}
	name := "mcp_" + serverPart + "_" + toolPart
	if len(name) > maxNameBytes {
		return "", fmt.Errorf("%w: tool name %q is empty or exceeds %d bytes", ErrInvalidConfig, tool, maxNameBytes)
	}
	return name, nil
}

func agentDefinition(name string, tool mcpclient.Tool) provider.ToolDefinition {
	parameters := tool.InputSchema
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{"type":"object"}`)
	}
	return provider.ToolDefinition{Name: name, Description: tool.Description, Parameters: parameters}
}

func definitionSize(definition provider.ToolDefinition) int {
	return len(definition.Name) + len(definition.Description) + len(definition.Parameters)
}

func makeToolCall(session *mcpclient.Session, originalName string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, args json.RawMessage) (string, error) {
		if ctx == nil {
			ctx = context.Background()
		}
		callCtx, cancel := context.WithTimeout(ctx, callLimit)
		defer cancel()
		result, err := session.Call(callCtx, originalName, args)
		serialized, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			return "", fmt.Errorf("serialize MCP result: %w", marshalErr)
		}
		if err != nil {
			return string(serialized), err
		}
		return string(serialized), nil
	}
}

type cleanupState struct {
	once     sync.Once
	sessions *[]*mcpclient.Session
}

func newCleanup(sessions *[]*mcpclient.Session) func() {
	state := &cleanupState{sessions: sessions}
	return func() {
		state.once.Do(func() {
			for i := len(*state.sessions) - 1; i >= 0; i-- {
				_ = (*state.sessions)[i].Close()
			}
		})
	}
}

func validateConfig(cfg fileConfig) error {
	if len(cfg.Servers) > maxServers {
		return fmt.Errorf("%w: at most %d servers", ErrLimit, maxServers)
	}
	seenAliases := make(map[string]string, len(cfg.Servers))
	for _, server := range cfg.Servers {
		if err := validateServer(server); err != nil {
			return err
		}
		alias := component(server.Name)
		if alias == "" {
			return fmt.Errorf("%w: server name %q is empty after normalization", ErrInvalidConfig, server.Name)
		}
		if previous, ok := seenAliases[alias]; ok {
			return fmt.Errorf("%w: server names %q and %q collide", ErrInvalidConfig, previous, server.Name)
		}
		seenAliases[alias] = server.Name
	}

	return nil
}

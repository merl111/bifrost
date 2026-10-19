package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/config"
	"bifrost/internal/ui"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCustomSkillInstallationScopesAndGit(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	source := filepath.Join(root, "custom skill")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: custom\ndescription: Custom skill fixture\n---\nInstructions\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := options{root: root, stateDir: state}
	run := workspaceExtensionCommand(opts)
	for _, scope := range []string{"project", "global"} {
		text, err := run(context.Background(), ui.ExtensionRequest{Kind: "skill-local", Source: "./custom skill", Scope: scope})
		if err != nil || !strings.Contains(text, "does not activate") {
			t.Fatalf("install %s: %s %v", scope, text, err)
		}
		manager, err := marketplaceManager(opts, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := manager.Load("custom"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := run(context.Background(), ui.ExtensionRequest{Kind: "skill-local", Source: source, Scope: "project"}); err == nil {
		t.Fatal("duplicate overwritten")
	}
	repo := t.TempDir()
	gitFixture(t, repo)
	if _, err := run(context.Background(), ui.ExtensionRequest{Kind: "skill-git", Source: repo, Ref: "main", Directory: "skills/fixture", Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	manager, _ := skillManager(opts)
	installed, err := manager.Load("fixture")
	if err != nil || !strings.Contains(installed.Source, "@") {
		t.Fatalf("Git provenance: %+v %v", installed, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := run(ctx, ui.ExtensionRequest{Kind: "skill-local", Source: source, Scope: "global"}); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestCustomMCPConfigPersistedScopesAndRestart(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	opts := options{root: root, stateDir: state}
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "fixture"}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
	defer httpServer.Close()
	path := filepath.Join(root, "custom servers.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"servers":[{"name":"custom","url":"` + httpServer.URL + `"}]}`)
	settings := config.DefaultSettings()
	settings.ActiveProvider = "fixture"
	settings.Providers["fixture"] = config.ProviderSettings{Kind: "chat-completions", BaseURL: "http://localhost:1", Model: "fixture"}
	configPath, _ := opts.configFile()
	if err := config.SaveSettings(configPath, settings, false); err != nil {
		t.Fatal(err)
	}
	run := workspaceExtensionCommand(opts)
	text, err := run(context.Background(), ui.ExtensionRequest{Kind: "mcp-config", Source: "./custom servers.json", Scope: "project"})
	if err != nil || !strings.Contains(text, "Restart Bifrost") {
		t.Fatalf("%s %v", text, err)
	}
	key, _ := mcpProjectKey(opts)
	saved, err := opts.settings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.ForProject(key).MCPConfig != path || saved.Defaults.MCPConfig != "" || saved.ActiveProvider != "fixture" {
		t.Fatalf("wrong scope/settings: %+v", saved)
	}
	setup, err := configureAgentSetup(context.Background(), opts, []string{"--agents", "0"}, io.Discard, false)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.cleanup()
	found := false
	for _, tool := range setup.cfg.ExtraTools {
		if tool.Definition.Name == "mcp_custom_echo" {
			found = true
		}
	}
	if !found {
		t.Fatal("root-only MCP defaults not connected at startup")
	}
	before, _ := os.ReadFile(configPath)
	for _, body := range []string{`{"servers":[{"name":"bad"}]}`, `{"servers":[{"name":"bad","url":"http://remote.example/mcp"}]}`, `{"servers":[{"name":"one","command":["never-run"]},{"name":"one","command":["never-run"]}]}`, `{"mcpServers":{}}`} {
		write(body)
		if _, err := run(context.Background(), ui.ExtensionRequest{Kind: "mcp-config", Source: path, Scope: "global"}); err == nil {
			t.Fatalf("invalid config accepted: %s", body)
		}
		after, _ := os.ReadFile(configPath)
		if string(before) != string(after) {
			t.Fatal("invalid config changed settings")
		}
	}
	write(`{"servers":[{"name":"local","command":["/never/executed"]}]}`)
	if _, err := run(context.Background(), ui.ExtensionRequest{Kind: "mcp-config", Source: path, Scope: "global"}); err != nil {
		t.Fatal(err)
	}
	// Named projects save by project name, rather than their currently selected repository.
	named := opts
	named.root = ""
	named.project = "multi-repo"
	if _, err := workspaceExtensionCommand(named)(context.Background(), ui.ExtensionRequest{Kind: "mcp-config", Source: path, Scope: "project"}); err != nil {
		t.Fatal(err)
	}
	saved, err = opts.settings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Defaults.MCPConfig != path || saved.Projects["multi-repo"].MCPConfig != path {
		t.Fatal("named/global config missing")
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "/never/executed") {
		t.Fatal("config should reference the file, not copy servers")
	}
}

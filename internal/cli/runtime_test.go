package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/provider"
)

type runtimeProvider func(context.Context, provider.Request) (provider.Response, error)

func (f runtimeProvider) Complete(ctx context.Context, r provider.Request) (provider.Response, error) {
	return f(ctx, r)
}

func TestProjectSkillsSharedAcrossRepositoryRootsAndActivated(t *testing.T) {
	rootA, rootB, state, source := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: fixture\ndescription: A test skill\n---\nUse the fixture instructions.\n"), 0600)
	os.WriteFile(filepath.Join(source, "reference.txt"), []byte("resource-content"), 0600)
	for _, args := range [][]string{
		{"--state-dir", state, "project", "add", "demo", "--alias", "a", "--root", rootA},
		{"--state-dir", state, "project", "add-repo", "demo", "--alias", "b", "--root", rootB},
		{"--state-dir", state, "--project", "demo", "--repository", "a", "skill", "install", source},
	} {
		if _, _, err := invoke(t, args...); err != nil {
			t.Fatal(err)
		}
	}
	opts := options{stateDir: state, project: "demo", repository: "b", environment: "local"}
	cfg, _, cleanup, err := configureAgent(context.Background(), opts, []string{"--base-url", "http://localhost:1", "--model", "fixture", "--skill", "fixture"}, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !strings.Contains(cfg.Instructions, "Use the fixture instructions") || cfg.Root != rootB {
		t.Fatalf("wrong project skill binding: %+v", cfg)
	}
	calls := 0
	cfg.Provider = runtimeProvider(func(ctx context.Context, r provider.Request) (provider.Response, error) {
		calls++
		if calls == 1 {
			if !strings.Contains(r.Messages[0].Content, "fixture instructions") {
				t.Fatal("skill instructions missing")
			}
			return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "resource", Name: "read_skill_resource", Arguments: json.RawMessage(`{"skill":"fixture","path":"reference.txt"}`)}}}}, nil
		}
		if r.Messages[len(r.Messages)-1].Content != "resource-content" {
			t.Fatalf("resource missing: %+v", r.Messages)
		}
		return provider.Response{Message: provider.Message{Content: "done"}}, nil
	})
	result, err := agent.Run(context.Background(), cfg, "check skill")
	if err != nil || result.Text != "done" {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestRuntimeCLIFlagsAndNoImplicitActivation(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	opts := options{root: root, stateDir: state}
	cfg, _, cleanup, err := configureAgent(context.Background(), opts, []string{"--base-url", "http://localhost:1", "--model", "fixture"}, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if cfg.Instructions != "" || cfg.WriteEnabled || cfg.ExecEnabled || len(cfg.ExtraTools) != 0 {
		t.Fatal("implicit capability activation")
	}
	_, command, err := extractGlobalFlags([]string{"--root", root, "tui", "--skill", "fixture", "--mcp-config", "servers.json", "--allow-write"})
	if err != nil || strings.Join(command, " ") != "tui --skill fixture --mcp-config servers.json --allow-write" {
		t.Fatalf("flags: %v %v", command, err)
	}
}

func TestRuntimeBrowserOptInAndMissingProvider(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	opts := options{root: root, stateDir: state}
	if _, _, _, err := configureAgent(context.Background(), opts, []string{"--browser"}, io.Discard, false); err == nil {
		t.Fatal("browser opt-in without provider was silently ignored")
	}
	// Saved config defaults (such as browser=true) without an active provider
	// must NOT block launching the TUI when no explicit CLI capability flag was passed.
	cfgSettings := config.DefaultSettings()
	cfgSettings.Defaults.Browser = true
	if err := config.SaveSettings(filepath.Join(state, "config.json"), cfgSettings, true); err != nil {
		t.Fatal(err)
	}
	if _, _, cleanupOffline, err := configureAgent(context.Background(), opts, nil, io.Discard, false); err != nil {
		t.Fatalf("offline TUI with saved defaults failed: %v", err)
	} else {
		cleanupOffline()
	}
	// Reset defaults before testing explicit --browser
	if err := config.SaveSettings(filepath.Join(state, "config.json"), config.DefaultSettings(), true); err != nil {
		t.Fatal(err)
	}
	cfg, _, cleanup, err := configureAgent(context.Background(), opts, []string{"--base-url", "http://localhost:1", "--model", "fixture", "--browser"}, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if len(cfg.ExtraTools) != 5 {
		t.Fatalf("browser tools = %d, want 5", len(cfg.ExtraTools))
	}
}

func TestConfigureAgentParsesSessionBindingOnce(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	opts := options{root: root, stateDir: state}
	setup, err := configureAgentSetup(context.Background(), opts, []string{
		"--max-steps", "2", "--skill", "", "--base-url", "http://localhost:1",
		"--model", "fixture", "--session", "ses_0123456789abcdef0123456789abcdef", "prompt",
	}, io.Discard, true)
	if err != nil {
		t.Fatal(err)
	}
	defer setup.cleanup()
	if setup.sessionID != "ses_0123456789abcdef0123456789abcdef" || setup.fingerprint != providerFingerprint("http://localhost:1", "fixture") {
		t.Fatalf("setup metadata = session %q fingerprint %q", setup.sessionID, setup.fingerprint)
	}
	if len(setup.rest) != 1 || setup.rest[0] != "prompt" {
		t.Fatalf("setup rest = %v", setup.rest)
	}
}

func TestSessionChatHistoryKeepsBoundedToolDetails(t *testing.T) {
	events := sessionChatHistory([]provider.Message{
		{Role: provider.RoleSystem, Content: "hidden system"},
		{Role: provider.RoleUser, Content: "question"},
		{Role: provider.RoleAssistant, Content: "answer", ToolCalls: []provider.ToolCall{{ID: "call", Name: "read_file", Arguments: json.RawMessage(`{"path":"note.txt"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "call", ToolName: "read_file", Content: "secret tool payload"},
	})
	if len(events) != 4 || events[0].Kind != "user" || events[0].Text != "question" || events[1].Kind != "tool" || events[1].ID != "call" || events[1].Detail == "" || events[2].Kind != "assistant" || events[3].Kind != "tool" || events[3].ID != "call" || events[3].Detail == "" {
		t.Fatalf("chat history = %#v", events)
	}
}

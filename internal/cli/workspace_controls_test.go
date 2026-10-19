package cli

import (
	"bifrost/internal/config"
	"bifrost/internal/execpolicy"
	"bifrost/internal/provider"
	"bifrost/internal/session"
	"bifrost/internal/ui"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func controlsWorkspace(t *testing.T) *DesktopWorkspace {
	t.Helper()
	root, state := t.TempDir(), t.TempDir()
	s := config.DefaultSettings()
	s.ActiveProvider = "test"
	s.Providers["test"] = config.ProviderSettings{Kind: "chat-completions", BaseURL: "http://localhost:1", Model: "fixture"}
	if err := config.SaveSettings(filepath.Join(state, "config.json"), s, false); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDesktopWorkspace(context.Background(), []string{"--root", root, "--state-dir", state, "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}
func TestCompactionNextTurnUsesSummaryAndPreservesSource(t *testing.T) {
	d := controlsWorkspace(t)
	ctx := context.Background()
	original := d.chat.current
	for i := 0; i < 8; i++ {
		original.Messages = append(original.Messages, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("earlier request ", 1000)}, provider.Message{Role: provider.RoleAssistant, Content: "decision", Opaque: json.RawMessage(`{"checkpoint":"must-not-reuse"}`)})
	}
	original.Status = session.StatusCompleted
	var err error
	original, err = d.chat.store.Save(original, original.Revision)
	if err != nil {
		t.Fatal(err)
	}
	d.chat.current = original
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "Preserve requirements and decision; next task remains open."}}, nil
	})
	before := EstimateSessionTokens(original.Messages, "", "")
	if err := d.Compact(ctx); err != nil {
		t.Fatal(err)
	}
	compacted := d.chat.current
	if compacted.ID == original.ID || EstimateSessionTokens(compacted.Messages, "", "") >= before {
		t.Fatal("compaction did not reduce context")
	}
	for _, m := range compacted.Messages {
		if len(m.Opaque) > 0 {
			t.Fatal("continuation survived compaction")
		}
	}
	saved, _ := d.chat.store.Load(original.ID)
	if len(saved.Messages) != 16 || saved.Revision != original.Revision {
		t.Fatal("source changed")
	}
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		if len(req.Messages) > 8 || !strings.Contains(req.Messages[1].Content, "Preserve requirements") {
			t.Fatalf("next request lacks compact history: %d messages", len(req.Messages))
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "continued"}}, nil
	})
	if err := d.Run(ctx, "continue", func(ui.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
}
func TestCanceledCompactionDoesNotFork(t *testing.T) {
	d := controlsWorkspace(t)
	s := d.chat.current
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "hello"}, {Role: provider.RoleAssistant, Content: "reply"}}
	s.Status = session.StatusCompleted
	s, _ = d.chat.store.Save(s, s.Revision)
	d.chat.current = s
	ctx, cancel := context.WithCancel(context.Background())
	d.chat.setup.cfg.Provider = runtimeProvider(func(context.Context, provider.Request) (provider.Response, error) {
		cancel()
		return provider.Response{}, context.Canceled
	})
	if err := d.Compact(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if d.chat.current.ID != s.ID {
		t.Fatal("cancel changed session")
	}
}
func TestInstalledSkillScopesAndNextTurnAutoload(t *testing.T) {
	d := controlsWorkspace(t)
	ctx := context.Background()
	for _, scope := range []string{"project", "global"} {
		source := t.TempDir()
		body := "---\nname: scoped-skill\ndescription: Scope fixture\n---\n" + scope + " body"
		if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		manager, err := marketplaceManager(d.chat.opts, scope)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = manager.Install(source); err != nil {
			t.Fatal(err)
		}
	}
	inventory, err := d.InstalledSkills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, item := range inventory.Items {
		if item.Skill.Name == "scoped-skill" {
			found++
			if item.Shadowed != (item.Scope == "global") {
				t.Fatal("wrong precedence")
			}
		}
	}
	if found != 2 {
		t.Fatal("hidden scope copy")
	}
	if err = d.SetSkillAutoload(ctx, "global", []string{"scoped-skill"}); err != nil {
		t.Fatal(err)
	}
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		if !strings.Contains(req.Messages[0].Content, "project body") {
			t.Fatal("new defaults not loaded")
		}
		counts := map[string]int{}
		for _, tool := range req.Tools {
			counts[tool.Name]++
		}
		if counts["read_skill_resource"] != 1 || counts["skills_activate"] != 1 {
			t.Fatal("invalid discovery tool catalog")
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "done"}}, nil
	})
	if err = d.Run(ctx, "use relevant skill", func(ui.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
}
func TestNativeAndCursorShellDenialPreventsExecution(t *testing.T) {
	d := controlsWorkspace(t)
	ctx := context.Background()
	if err := d.SetExecutionMode(ctx, "ask"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		calls++
		if calls == 1 {
			return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "exec1", Name: "exec", Arguments: json.RawMessage(`{"argv":["sh","-c","touch marker"]}`)}}}}, nil
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "denied"}}, nil
	})
	approvals := 0
	if err := d.Run(ctx, "execute", func(event ui.ChatEvent) {
		if event.Approval != nil {
			approvals++
			event.Approval.Reply <- false
		}
	}); err != nil {
		t.Fatal(err)
	}
	if approvals != 1 {
		t.Fatalf("approval count %d", approvals)
	}
	files := backendFiles{backend: d.Backend, authorize: execpolicy.Settings{Mode: "ask"}.Authorizer(func(r *execpolicy.Request) { r.Reply <- false })}
	if _, err := files.Exec(ctx, []string{"sh", "-c", "touch marker"}, "", 4096); err == nil {
		t.Fatal("cursor shell bypass")
	}
	if _, err := os.Stat(filepath.Join(d.Root, "marker")); !os.IsNotExist(err) {
		t.Fatal("denied command ran")
	}
}

func TestAutomaticCompactionRespectsSourceLockAndReducesNextRequest(t *testing.T) {
	d := controlsWorkspace(t)
	ctx := context.Background()
	original := d.chat.current
	for i := 0; i < 8; i++ {
		original.Messages = append(original.Messages, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("important requirement ", 3500)}, provider.Message{Role: provider.RoleAssistant, Content: "checked"})
	}
	original.Status = session.StatusCompleted
	var err error
	original, err = d.chat.store.Save(original, original.Revision)
	if err != nil {
		t.Fatal(err)
	}
	d.chat.current = original
	summaries := 0
	d.chat.setup.cfg.Provider = runtimeProvider(func(_ context.Context, req provider.Request) (provider.Response, error) {
		if len(req.Tools) == 0 {
			summaries++
			return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "Requirements retained. Checks complete; continue the task."}}, nil
		}
		if EstimateSessionTokens(req.Messages, "", "") >= EstimateSessionTokens(original.Messages, "", "") {
			t.Fatal("automatic compaction did not shrink next request")
		}
		return provider.Response{Message: provider.Message{Role: provider.RoleAssistant, Content: "continued"}}, nil
	})
	release, err := acquireSessionTurn(d.chat.opts.statePath(), original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = d.Run(ctx, "continue", func(ui.ChatEvent) {}); err == nil {
		t.Fatal("locked source accepted")
	}
	_ = release()
	if summaries != 0 {
		t.Fatal("summary ran before acquiring lock")
	}
	if err = d.Run(ctx, "continue", func(ui.ChatEvent) {}); err != nil {
		t.Fatal(err)
	}
	if summaries != 1 || d.chat.current.ID == original.ID {
		t.Fatal("automatic summarization missing")
	}
	saved, err := d.chat.store.Load(original.ID)
	if err != nil || saved.Revision != original.Revision {
		t.Fatal("auto compaction changed source")
	}
}

func TestRemovingSubagentProfileClearsItsDefaults(t *testing.T) {
	d := controlsWorkspace(t)
	path, _ := d.chat.opts.configFile()
	settings, revision, err := config.LoadSettingsRevision(path)
	if err != nil {
		t.Fatal(err)
	}
	settings.Providers["worker"] = settings.Providers["test"]
	settings.Subagents = config.SubagentSettings{Provider: "worker", Model: "small"}
	if err = config.SaveSettingsRevision(path, settings, revision); err != nil {
		t.Fatal(err)
	}
	if err = d.RemoveAccount(context.Background(), "worker"); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadSettings(path)
	if err != nil || loaded.Subagents.Provider != "" || loaded.Subagents.Model != "" {
		t.Fatalf("removed profile kept defaults: %+v %v", loaded.Subagents, err)
	}
}

func TestLocalSummaryRetainsLatestRequirements(t *testing.T) {
	d := controlsWorkspace(t)
	cfg := d.chat.setup.cfg
	cfg.Provider = nil
	messages := []provider.Message{{Role: provider.RoleUser, Content: "INITIAL REQUIREMENT"}}
	for i := 0; i < 60; i++ {
		messages = append(messages, provider.Message{Role: provider.RoleUser, Content: strings.Repeat("日", 100)})
	}
	messages = append(messages, provider.Message{Role: provider.RoleUser, Content: "LATEST OPEN TASK"})
	summary := BuildConversationSummary(context.Background(), cfg, messages)
	if !strings.Contains(summary, "INITIAL REQUIREMENT") || !strings.Contains(summary, "LATEST OPEN TASK") {
		t.Fatal("fallback lost initial/latest requirements")
	}
}

package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/agent"
	"bifrost/internal/skill"
)

func TestTaskAwareSkillSelection(t *testing.T) {
	opts := options{root: t.TempDir(), stateDir: t.TempDir(), environment: "local"}
	manager, err := skillManager(opts)
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: project-check\ndescription: Inspect fixture correctness\n---\nPRIVATE BODY SENT ONLY ON ACTIVATION\n"), 0600)
	os.WriteFile(filepath.Join(source, "reference.txt"), []byte("bounded reference"), 0600)
	installed, err := manager.Install(source)
	if err != nil {
		t.Fatal(err)
	}
	cfg := agent.Config{Root: opts.root}
	applyWorkspaceProjectContext(opts, &cfg)
	if strings.Contains(cfg.Instructions, "PRIVATE BODY") {
		t.Fatal("discovery activated skill body")
	}
	if !strings.Contains(cfg.Instructions, "project-check") || !strings.Contains(cfg.Instructions, "code-explore") {
		t.Fatal("metadata omitted")
	}
	call := func(name, args string) (string, error) {
		t.Helper()
		for _, tool := range cfg.ExtraTools {
			if tool.Definition.Name == name {
				return tool.Call(context.Background(), json.RawMessage(args))
			}
		}
		t.Fatalf("missing tool %s", name)
		return "", nil
	}
	if _, err := call("read_skill_resource", `{"skill":"project-check","path":"reference.txt"}`); err == nil {
		t.Fatal("resource accessible before activation")
	}
	found, err := call("skills_search", `{"query":"correctness"}`)
	if err != nil || !strings.Contains(found, "project-check") || strings.Contains(found, "PRIVATE BODY") {
		t.Fatalf("search: %s %v", found, err)
	}
	loaded, err := call("skills_activate", `{"name":"project-check"}`)
	if err != nil || !strings.Contains(loaded, "PRIVATE BODY") {
		t.Fatalf("activate: %s %v", loaded, err)
	}
	resource, err := call("read_skill_resource", `{"skill":"project-check","path":"reference.txt"}`)
	if err != nil || resource != "bounded reference" {
		t.Fatalf("resource: %q %v", resource, err)
	}
	if _, err := call("read_skill_resource", `{"skill":"project-check","path":"../outside"}`); err == nil {
		t.Fatal("resource traversal accepted")
	}
	if _, err := call("skills_activate", `{"name":"../../outside"}`); err == nil {
		t.Fatal("unknown skill accepted")
	}
	if _, err := call("skills_activate", `{"name":"code-explore"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call("skills_search", `{"offset":-1}`); err == nil {
		t.Fatal("invalid pagination accepted")
	}
	// Explicit --skill resources and selected resources share one unique tool.
	explicit := agent.Config{Root: opts.root}
	if err := activateSkills(opts, &explicit, "project-check"); err != nil {
		t.Fatal(err)
	}
	applyWorkspaceProjectContext(opts, &explicit)
	count := 0
	for _, tool := range explicit.ExtraTools {
		if tool.Definition.Name == "read_skill_resource" {
			count++
			if _, err := tool.Call(context.Background(), json.RawMessage(`{"skill":"project-check","path":"reference.txt"}`)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate resource tools: %d", count)
	}
	// Installation remains inert and does not alter permissions.
	if cfg.WriteEnabled || cfg.ExecEnabled {
		t.Fatal("skill enabled permissions")
	}
	if current, err := manager.Load(installed.Name); err != nil || current.Digest != installed.Digest {
		t.Fatal("selection mutated installed skill")
	}

	// Reinstalling a skill cannot silently replace an already selected revision.
	if err := manager.Remove(installed.Name); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "reference.txt"), []byte("new revision"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := call("read_skill_resource", `{"skill":"project-check","path":"reference.txt"}`); err == nil {
		t.Fatal("selected resource revision silently changed")
	}
	if _, err := call("skills_activate", `{"name":"project-check"}`); err == nil {
		t.Fatal("stale discovery silently changed")
	}
}

// Precedence is tested without relying on private manager storage paths.
func TestBundledSkillsAreAvailableAndOverridable(t *testing.T) {
	defaults := skill.Bundled()
	forged := defaults[0]
	forged.Digest = "changed"
	if skill.IsBundled(forged) {
		t.Fatal("source label impersonated builtin")
	}
	if len(defaults) != 4 {
		t.Fatalf("bundled: %d", len(defaults))
	}
	for _, s := range defaults {
		if s.Name == "" || s.Body == "" || s.Digest == "" {
			t.Fatalf("incomplete skill %+v", s)
		}
	}
	override := skill.Skill{Name: "code-explore", Body: "project-specific", Description: "override"}
	for _, s := range skill.WithBundled([]skill.Skill{override}) {
		if s.Name == override.Name && s.Body != override.Body {
			t.Fatal("builtin replaced project skill")
		}
	}
}

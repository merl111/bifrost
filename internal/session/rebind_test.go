package session

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bifrost/internal/provider"
)

func TestRebindKeepsConversationWithoutAccountOwnedContinuation(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Create(t.TempDir(), "first-account")
	if err != nil {
		t.Fatal(err)
	}
	current.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "fix the bug"},
		{Role: provider.RoleAssistant, Opaque: json.RawMessage(`{"secret_session":"vendor-session"}`), ToolCalls: []provider.ToolCall{{ID: "one", Name: "read_file", Namespace: "bifrost", Arguments: json.RawMessage(`{"path":"main.go"}`)}}},
		{Role: provider.RoleTool, ToolCallID: "one", ToolName: "read_file", Content: "file contents"},
		{Role: provider.RoleAssistant, Content: "ready to edit"},
	}
	current.Status = StatusCompleted
	current, err = s.Save(current, current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	switched, err := s.Rebind(current.ID, "second-account", current.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if switched.ID != current.ID || len(switched.Messages) != 4 || switched.ProviderFingerprint != "second-account" {
		t.Fatal("conversation changed")
	}
	for _, m := range switched.Messages {
		if len(m.Opaque) > 0 || len(m.ToolCalls) > 0 || m.Role == provider.RoleTool {
			t.Fatal("account-owned execution state carried")
		}
	}
	if !strings.Contains(switched.Messages[2].Content, "file contents") {
		t.Fatal("lost tool context")
	}
	if _, err := s.Rebind(current.ID, "third-account", current.Revision); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale rebind = %v", err)
	}
	loaded, err := s.Load(current.ID)
	if err != nil || loaded.ProviderFingerprint != "second-account" {
		t.Fatalf("persistence = %v", err)
	}
}

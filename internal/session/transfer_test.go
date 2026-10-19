package session

import (
	"bifrost/internal/provider"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTransferAndCompactPreserveSourceAndToolBatches(t *testing.T) {
	st, _ := New(t.TempDir())
	root := t.TempDir()
	s, _ := st.Create(root, "provider")
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "first"}, {Role: provider.RoleAssistant, Content: "first reply"}, {Role: provider.RoleUser, Content: "second"}, {Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "a", Name: "read", Arguments: json.RawMessage(`{}`)}}}, {Role: provider.RoleTool, ToolCallID: "a", Content: "contents"}, {Role: provider.RoleAssistant, Content: "done"}}
	s.Status = StatusCompleted
	s, err := st.Save(s, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	if err := st.Export(s.ID, &b); err != nil {
		t.Fatal(err)
	}
	imported, err := st.Import(bytes.NewReader(b.Bytes()), root)
	if err != nil {
		t.Fatal(err)
	}
	if imported.ID == s.ID || len(imported.Messages) != 6 {
		t.Fatal("import must fork identity")
	}
	if _, err := st.Import(bytes.NewReader(b.Bytes()), t.TempDir()); err == nil {
		t.Fatal("import root changed")
	}
	compacted, err := st.Compact(s.ID, "first turn summary", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.Messages) != 5 || compacted.Messages[1].Content != "second" || !strings.Contains(compacted.Messages[3].Content, "contents") || compacted.Messages[3].ToolCallID != "" {
		t.Fatal("split a turn")
	}
	original, _ := st.Load(s.ID)
	if len(original.Messages) != 6 || original.Revision != s.Revision {
		t.Fatal("compaction modified source")
	}
}

func TestCompactCountsUserImagePromptAsATurn(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := st.Create(root, "provider")
	if err != nil {
		t.Fatal(err)
	}
	s.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "old prompt"},
		{Role: provider.RoleAssistant, Content: "old reply"},
		{Role: provider.RoleUser, Content: "look at this", Images: []provider.Image{{MediaType: "image/png", Data: []byte("png")}}},
		{Role: provider.RoleAssistant, Content: "image reply"},
	}
	s.Status = StatusCompleted
	s, err = st.Save(s, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := st.Compact(s.ID, "old context", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.Messages) != 3 || compacted.Messages[1].Content != "look at this" || len(compacted.Messages[1].Images) != 1 {
		t.Fatalf("image prompt was split from its turn: %+v", compacted.Messages)
	}
}

func TestCompactKeepsToolImageObservationWithItsTurn(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	s, err := st.Create(root, "provider")
	if err != nil {
		t.Fatal(err)
	}
	s.Messages = []provider.Message{
		{Role: provider.RoleUser, Content: "old prompt"},
		{Role: provider.RoleAssistant, Content: "old reply"},
		{Role: provider.RoleUser, Content: "prompt"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "capture", Name: "capture", Arguments: json.RawMessage(`{}`)}}},
		{Role: provider.RoleTool, ToolCallID: "capture", ToolName: "capture", Content: "captured"},
		{Role: provider.RoleUser, Content: "Visual observations from the preceding tools.", Images: []provider.Image{{MediaType: "image/png", Data: []byte("png")}}},
		{Role: provider.RoleAssistant, Content: "done"},
	}
	s.Status = StatusCompleted
	s, err = st.Save(s, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := st.Compact(s.ID, "earlier", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted.Messages) != 6 || compacted.Messages[1].Content != "prompt" || compacted.Messages[4].Role != provider.RoleUser || compacted.Messages[5].Content != "done" {
		t.Fatalf("tool observation split from its turn: %+v", compacted.Messages)
	}
}

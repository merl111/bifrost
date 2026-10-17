package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bifrost/internal/provider"
)

func TestCreateRestartSaveAndListPreservesToolArguments(t *testing.T) {
	state := t.TempDir()
	root := t.TempDir()
	st, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Create(root, "https://example.invalid|model-a")
	if err != nil {
		t.Fatal(err)
	}
	sess.Messages = []provider.Message{
		{Role: provider.RoleSystem, Content: "system"},
		{Role: provider.RoleUser, Content: "run"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "read", Arguments: json.RawMessage(` { "path": "a" } `)}}},
		{Role: provider.RoleTool, ToolCallID: "call-1", Content: "contents"},
		{Role: provider.RoleAssistant, Content: "done"},
	}
	saved, err := st.Save(sess, sess.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision != 2 || !saved.UpdatedAt.After(saved.CreatedAt) {
		t.Fatalf("unexpected saved revision/timestamp: %+v", saved)
	}

	// A new Store simulates a process restart and verifies the actual snapshot.
	st, err = New(state)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	var args map[string]string
	if err := json.Unmarshal(got.Messages[2].ToolCalls[0].Arguments, &args); err != nil || args["path"] != "a" {
		t.Fatalf("tool arguments were not preserved: %q", got.Messages[2].ToolCalls[0].Arguments)
	}
	listed, err := st.List(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].ID != saved.ID {
		t.Fatalf("unexpected list: %+v", listed)
	}
}

func TestSnapshotRoundTripPreservesToolNamespace(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Create(t.TempDir(), "namespace")
	if err != nil {
		t.Fatal(err)
	}
	s.Messages = []provider.Message{{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "call", Name: "run", Namespace: "bifrost", Arguments: json.RawMessage(`{"type":"object"}`)}}}, {Role: provider.RoleTool, ToolCallID: "call", Content: "ok"}}
	saved, err := st.Save(s, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].ToolCalls[0].Namespace != "bifrost" {
		t.Fatalf("namespace lost: %#v", got.Messages[0].ToolCalls[0])
	}
}

func TestSaveRejectsStaleConcurrentRevision(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	st, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	original, err := st.Create(root, "base|model")
	if err != nil {
		t.Fatal(err)
	}
	left, err := st.Load(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	right, err := st.Load(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	left.Messages = []provider.Message{{Role: provider.RoleUser, Content: "left"}}
	right.Messages = []provider.Message{{Role: provider.RoleUser, Content: "right"}}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, candidate := range []Session{left, right} {
		candidate := candidate
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := st.Save(candidate, original.Revision)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflict++
		} else {
			t.Fatalf("unexpected concurrent save result: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("expected one save and one conflict, got success=%d conflict=%d", success, conflict)
	}
	current, err := st.Load(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != original.Revision+1 {
		t.Fatalf("unexpected current revision: %d", current.Revision)
	}
}

func TestCorruptAndUnsupportedSnapshotsFailClosed(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	st, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Create(root, "base|model")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "sessions", sess.ID.String()+".json")
	if err := os.WriteFile(path, []byte(`{"version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(sess.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected corrupt error, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(sess.ID); !errors.Is(err, ErrUnsupportedSchema) && !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"id":"`+sess.ID.String()+`","root":"`+root+`","provider_fingerprint":"base|model","messages":[],"created_at":"`+sess.CreatedAt.Format(time.RFC3339Nano)+`","updated_at":"`+sess.UpdatedAt.Format(time.RFC3339Nano)+`","revision":1,"status":"interrupted","extra":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(sess.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected unknown-field corruption, got %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"id":"`+sess.ID.String()+`","root":"`+root+`","provider_fingerprint":"base|model","created_at":"`+sess.CreatedAt.Format(time.RFC3339Nano)+`","updated_at":"`+sess.UpdatedAt.Format(time.RFC3339Nano)+`","revision":1,"status":"interrupted"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(sess.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("expected missing-field corruption, got %v", err)
	}
}

func TestBindingAndHistoryBounds(t *testing.T) {
	state, root, otherRoot := t.TempDir(), t.TempDir(), t.TempDir()
	st, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Create(root, "base|model")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateBinding(sess, otherRoot, "base|model"); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("expected root binding mismatch, got %v", err)
	}
	if err := ValidateBinding(sess, root, "other|model"); !errors.Is(err, ErrBindingMismatch) {
		t.Fatalf("expected provider binding mismatch, got %v", err)
	}
	sess.Messages = []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("x", MaxSnapshotBytes)}}
	if _, err := st.Save(sess, sess.Revision); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected size limit, got %v", err)
	}
	bad := sess
	bad.Messages = []provider.Message{{Role: provider.RoleTool, ToolCallID: "missing", Content: "x"}}
	if _, err := st.Save(bad, sess.Revision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("expected tool pairing validation, got %v", err)
	}
	tooMany := sess
	tooMany.Messages = make([]provider.Message, MaxMessages+1)
	for i := range tooMany.Messages {
		tooMany.Messages[i].Role = provider.RoleUser
	}
	if _, err := st.Save(tooMany, sess.Revision); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected message count limit, got %v", err)
	}
}

func TestRootCanonicalization(t *testing.T) {
	state, root := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	st, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := st.Create(link, "base|model")
	if err != nil {
		t.Fatal(err)
	}
	if sess.Root != root {
		t.Fatalf("root was not canonicalized: got %q want %q", sess.Root, root)
	}
}

func TestNativeOpaqueImagesAndFunctionNameSurviveRestart(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s, err := st.Create(t.TempDir(), "native")
	if err != nil {
		t.Fatal(err)
	}
	s.Messages = []provider.Message{{Role: provider.RoleUser, Content: "look", Images: []provider.Image{{MediaType: "image/png", Data: []byte("png")}}}, {Role: provider.RoleAssistant, Opaque: json.RawMessage(`{"reasoning":"opaque-token"}`), ToolCalls: []provider.ToolCall{{ID: "call", Name: "capture", Arguments: json.RawMessage(`{}`)}}}, {Role: provider.RoleTool, ToolCallID: "call", ToolName: "capture", Content: "ok"}}
	saved, err := st.Save(s, s.Revision)
	if err != nil {
		t.Fatal(err)
	}
	s.Messages[0].Images[0].Data[0] = 'X'
	got, err := st.Load(saved.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Messages[0].Images[0].Data) != "png" || got.Messages[2].ToolName != "capture" || !strings.Contains(string(got.Messages[1].Opaque), "opaque-token") {
		t.Fatal("lost native metadata")
	}
}

package agent

import (
	"bifrost/internal/provider"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type completionFunc func(context.Context, provider.Request) (provider.Response, error)

func (f completionFunc) Complete(ctx context.Context, r provider.Request) (provider.Response, error) {
	return f(ctx, r)
}

func TestCancellationAfterCompletionDoesNotWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	client := completionFunc(func(context.Context, provider.Request) (provider.Response, error) {
		cancel()
		return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "write", Name: toolWrite, Arguments: json.RawMessage(`{"path":"must-not-exist","expected_revision":null,"content":"bad"}`)}}}}, nil
	})
	_, err := Run(ctx, Config{Provider: client, Root: root, StateDir: t.TempDir(), WriteEnabled: true}, "test")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "must-not-exist")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mutation after cancellation")
	}
}

func TestNullIsNotAString(t *testing.T) {
	if _, err := requiredString(map[string]json.RawMessage{"new_text": json.RawMessage("null")}, "new_text"); err == nil {
		t.Fatal("null accepted as deletion text")
	}
	if s, err := requiredString(map[string]json.RawMessage{"new_text": json.RawMessage(`""`)}, "new_text"); err != nil || s != "" {
		t.Fatal("explicit empty deletion must work")
	}
}

func TestDuplicateToolCallsDoNotExecute(t *testing.T) {
	root := t.TempDir()
	c := provider.ToolCall{ID: "duplicate", Name: toolWrite, Arguments: json.RawMessage(`{"path":"bad","expected_revision":null,"content":"bad"}`)}
	client := completionFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{c, c}}}, nil
	})
	_, err := Run(context.Background(), Config{Provider: client, Root: root, StateDir: t.TempDir(), WriteEnabled: true}, "test")
	if err == nil {
		t.Fatal("duplicate accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("executed partial duplicate batch")
	}
}

func TestMalformedLaterCallPreventsEntireMutationBatch(t *testing.T) {
	root := t.TempDir()
	client := completionFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{
			{ID: "first", Name: toolWrite, Arguments: json.RawMessage(`{"path":"bad","expected_revision":null,"content":"bad"}`)},
			{ID: "second", Name: toolRead, Arguments: json.RawMessage(`null`)},
		}}}, nil
	})
	_, err := Run(context.Background(), Config{Provider: client, Root: root, StateDir: t.TempDir(), WriteEnabled: true}, "test")
	if err == nil {
		t.Fatal("malformed batch accepted")
	}
	if _, err := os.Stat(filepath.Join(root, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial malformed batch mutated workspace")
	}
}

func TestVisualToolResultsFollowCompleteBatchAndPreserveNames(t *testing.T) {
	count := 0
	client := completionFunc(func(_ context.Context, r provider.Request) (provider.Response, error) {
		count++
		if count == 1 {
			return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "a", Name: "capture", Arguments: json.RawMessage(`{}`)}, {ID: "b", Name: "capture", Arguments: json.RawMessage(`{}`)}}}}, nil
		}
		n := len(r.Messages)
		if n < 3 || r.Messages[n-1].Role != provider.RoleUser || len(r.Messages[n-1].Images) != 2 || r.Messages[n-2].ToolName != "capture" || r.Messages[n-3].ToolCallID != "a" {
			t.Fatalf("invalid image/tool ordering: %+v", r.Messages)
		}
		return provider.Response{Message: provider.Message{Content: "done"}}, nil
	})
	cfg := Config{Provider: client, Root: t.TempDir(), StateDir: t.TempDir(), ExtraTools: []Tool{{Definition: provider.ToolDefinition{Name: "capture", Parameters: json.RawMessage(`{"type":"object"}`)}, Call: func(context.Context, json.RawMessage) (string, error) { return "captured", nil }, Images: func() []provider.Image { return []provider.Image{{MediaType: "image/png", Data: []byte("image")}} }}}}
	var events []string
	cfg.RunHook = func(_ context.Context, event string, _ any) error { events = append(events, event); return nil }
	if _, err := Run(context.Background(), cfg, "look"); err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 || events[0] != "before-turn" || events[5] != "after-turn" {
		t.Fatal(events)
	}
}
func TestBeforeToolHookCanPreventMutation(t *testing.T) {
	root := t.TempDir()
	client := completionFunc(func(context.Context, provider.Request) (provider.Response, error) {
		return provider.Response{Message: provider.Message{ToolCalls: []provider.ToolCall{{ID: "w", Name: toolWrite, Arguments: json.RawMessage(`{"path":"x","expected_revision":null,"content":"bad"}`)}}}}, nil
	})
	cfg := Config{Provider: client, Root: root, StateDir: t.TempDir(), WriteEnabled: true, RunHook: func(_ context.Context, event string, _ any) error {
		if event == "before-tool" {
			return errors.New("blocked")
		}
		return nil
	}}
	if _, err := Run(context.Background(), cfg, "test"); err == nil {
		t.Fatal("hook ignored")
	}
	if _, err := os.Stat(filepath.Join(root, "x")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("mutation executed")
	}
}

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"bifrost/internal/provider"
)

func TestToolErrorsRespectResultLimit(t *testing.T) {
	for _, limit := range []int{2, 16, 64, maxToolErrorBytes} {
		got := toolErrorLimit(errors.New(strings.Repeat("x", 10000)), limit)
		if len(got) > limit {
			t.Fatalf("limit %d produced %d bytes", limit, len(got))
		}
		if !json.Valid([]byte(got)) {
			t.Fatalf("limit %d produced invalid JSON %q", limit, got)
		}
	}
}

func TestToolCatalogBoundsDefinitions(t *testing.T) {
	tooLarge := Tool{Definition: provider.ToolDefinition{Name: "large", Parameters: json.RawMessage(strings.Repeat("x", maxToolDefinitionBytes))}, Call: func(context.Context, json.RawMessage) (string, error) { return "", nil }}
	if _, err := toolCatalog(Config{ExtraTools: []Tool{tooLarge}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("oversized definition error = %v", err)
	}

	tools := make([]Tool, 0, 40)
	for i := 0; i < 40; i++ {
		name := "tool-" + string(rune('a'+i))
		tools = append(tools, Tool{Definition: provider.ToolDefinition{Name: name, Parameters: json.RawMessage(strings.Repeat("x", maxToolDefinitionBytes/2))}, Call: func(context.Context, json.RawMessage) (string, error) { return "", nil }})
	}
	if _, err := toolCatalog(Config{ExtraTools: tools}); !errors.Is(err, ErrBudget) {
		t.Fatalf("catalog budget error = %v", err)
	}
}

func TestMaxOutputBytesHasUpperBound(t *testing.T) {
	_, err := normalizeConfig(Config{Provider: boundsProvider{}, Root: ".", StateDir: ".", MaxOutputBytes: maxAgentOutputBytes + 1}, "prompt")
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("max output error = %v", err)
	}
}

type boundsProvider struct{}

func (boundsProvider) Complete(context.Context, provider.Request) (provider.Response, error) {
	return provider.Response{}, nil
}

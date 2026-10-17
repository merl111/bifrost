package mcptools

import (
	"strings"
	"testing"

	"bifrost/internal/provider"
)

func TestDefinitionBudgetBoundsMetadata(t *testing.T) {
	definition := provider.ToolDefinition{Name: "tool", Description: strings.Repeat("d", maxDefinitionBytes)}
	if got := definitionSize(definition); got <= maxDefinitionBytes {
		t.Fatalf("definition size = %d, want over %d", got, maxDefinitionBytes)
	}
	if maxCatalogBytes <= maxDefinitionBytes {
		t.Fatalf("catalog budget %d must exceed per-definition budget %d", maxCatalogBytes, maxDefinitionBytes)
	}
}

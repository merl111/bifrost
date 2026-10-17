package mcpclient

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestConvertToolBoundsUntrustedMetadata(t *testing.T) {
	tooLargeDescription := &mcp.Tool{Name: "large", Description: strings.Repeat("d", maxToolMetadata+1)}
	if _, err := convertTool(tooLargeDescription); err == nil {
		t.Fatal("oversized tool description was accepted")
	}
	tooLargeSchema := &mcp.Tool{Name: "large", InputSchema: map[string]any{"description": strings.Repeat("s", maxToolMetadata)}}
	if _, err := convertTool(tooLargeSchema); err == nil {
		t.Fatal("oversized tool schema was accepted")
	}
}

func TestConvertResultBoundsUntrustedContent(t *testing.T) {
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", maxToolResult)}}}
	if _, err := convertResult(result); err == nil {
		t.Fatal("oversized tool result was accepted")
	}
}

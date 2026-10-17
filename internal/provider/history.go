package provider

import (
	"fmt"
	"strings"
)

// PortableHistory carries the visible conversation and completed tool records
// across providers without replaying account-bound IDs or signed reasoning.
// Tool exchanges become transcript text, not executable tool calls.
func PortableHistory(messages []Message) []Message {
	result := make([]Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == RoleSystem {
			continue
		}
		next := Message{Role: m.Role, Content: m.Content, Images: append([]Image(nil), m.Images...)}
		if m.Role == RoleTool {
			next.Role = RoleAssistant
			next.Content = fmt.Sprintf("Historical tool result (%s, %s; already executed):\n%s", m.ToolName, m.ToolCallID, m.Content)
		}
		for _, call := range m.ToolCalls {
			next.Content += fmt.Sprintf("\nHistorical tool call (%s, %s; already executed): %s", call.Name, call.ID, call.Arguments)
		}
		if strings.TrimSpace(next.Content) != "" || len(next.Images) > 0 {
			result = append(result, next)
		}
	}
	return result
}

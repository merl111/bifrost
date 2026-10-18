package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/cursorclient"
)

func cursorBridgeTools(tools []agent.Tool) []cursorclient.Tool {
	result := make([]cursorclient.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, cursorclient.Tool{Name: tool.Definition.Name, Description: tool.Definition.Description, Schema: tool.Definition.Parameters, Call: tool.Call})
	}
	return result
}

// Package provider defines the small model-provider contract used by the
// headless agent. The contract is deliberately independent of any one SDK.
package provider

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID        string
	Name      string
	Namespace string
	Arguments json.RawMessage
}

// Image is an image supplied alongside a message. Data is sent as a
// data URL by native providers; URL is sent directly when set. A provider
// rejects inputs that specify neither or both forms.
type Image struct {
	MediaType string
	Data      []byte
	URL       string
}

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// ToolName is the provider function name for a tool result. Providers
	// that require it (for example Gemini) use it together with ToolCallID.
	ToolName string
	Images   []Image
	// Opaque contains provider-owned data needed to continue a
	// native response (for example Gemini thought signatures). Consumers must
	// preserve it without interpreting it.
	Opaque json.RawMessage
}

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type Request struct {
	Model    string
	Messages []Message
	Tools    []ToolDefinition
	// Opaque is provider-owned opaque continuation state.
	Opaque json.RawMessage
}

type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
	// ThinkingSummary is provider-supplied public reasoning summary text.
	// It deliberately excludes opaque continuation data and redacted thinking
	// blocks. Provider signatures authenticate text but are never included.
	ThinkingSummary string
	// Opaque is provider-owned opaque state for a later request.
	Opaque json.RawMessage
}

const maxThinkingSummaryBytes = 16 << 10

func boundedThinkingSummary(value string) string {
	value = strings.ToValidUTF8(value, "�")
	if len(value) <= maxThinkingSummaryBytes {
		return value
	}
	const suffix = "…"
	limit := maxThinkingSummaryBytes - len(suffix)
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + suffix
}

type Client interface {
	Complete(context.Context, Request) (Response, error)
}

// Streamer is an optional streaming capability. Implementations invoke
// onDelta for each text delta and return the same accumulated response shape
// as Complete. Tool calls are returned only after the stream is complete;
// partial tool arguments are never exposed to the callback.
type Streamer interface {
	Stream(context.Context, Request, func(string) error) (Response, error)
}

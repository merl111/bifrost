package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

type AnthropicConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
	Version          string
}

type Anthropic struct {
	endpoint string
	model    string
	apiKey   string
	version  string
	client   clientDoer
	maxBody  int
}

func NewAnthropic(cfg AnthropicConfig) (*Anthropic, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("provider: model is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.anthropic.com"
	}
	endpoint, err := nativeEndpoint(cfg.BaseURL, "/v1/messages")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("provider: invalid API key")
	}
	if cfg.Version == "" {
		cfg.Version = "2023-06-01"
	}
	if strings.ContainsAny(cfg.Version, "\r\n") {
		return nil, errors.New("provider: invalid Anthropic version")
	}
	return &Anthropic{endpoint: endpoint, model: cfg.Model, apiKey: cfg.APIKey, version: cfg.Version, client: nativeHTTPClient(cfg.Timeout), maxBody: nativeMaxBody(cfg.MaxResponseBytes)}, nil
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}
type anthropicOpaque struct {
	Blocks []json.RawMessage `json:"blocks,omitempty"`
}

type anthropicOpaqueBlock struct {
	Type      string
	Thinking  string
	Signature string
	Data      json.RawMessage
}

func (b anthropicOpaqueBlock) raw() json.RawMessage {
	block := map[string]any{"type": b.Type}
	if b.Thinking != "" {
		block["thinking"] = b.Thinking
	}
	if b.Signature != "" {
		block["signature"] = b.Signature
	}
	if len(b.Data) > 0 {
		block["data"] = json.RawMessage(b.Data)
	}
	raw, _ := json.Marshal(block)
	return raw
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func (a *Anthropic) request(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	model := req.Model
	if model == "" {
		model = a.model
	}
	w := anthropicRequest{Model: model, MaxTokens: 8192, Stream: stream}
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			w.System += m.Content
			continue
		}
		content := make([]any, 0, 1+len(m.Images)+len(m.ToolCalls))
		if m.Role == RoleTool {
			content = append(content, map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content})
		} else {
			if m.Role == RoleAssistant {
				var opaque anthropicOpaque
				if json.Unmarshal(m.Opaque, &opaque) == nil {
					for _, raw := range opaque.Blocks {
						var block any
						if json.Unmarshal(raw, &block) == nil {
							content = append(content, block)
						}
					}
				}
			}
			if m.Content != "" {
				content = append(content, map[string]any{"type": "text", "text": m.Content})
			}
			for _, image := range m.Images {
				if err := validateImage(image); err != nil {
					return nil, err
				}
				if len(image.Data) > 0 {
					media := image.MediaType
					if media == "" {
						media = "application/octet-stream"
					}
					content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": media, "data": base64.StdEncoding.EncodeToString(image.Data)}})
				} else if image.URL != "" {
					content = append(content, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": image.URL}})
				}
			}
			for _, c := range m.ToolCalls {
				content = append(content, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": json.RawMessage(c.Arguments)})
			}
		}
		role := "user"
		if m.Role == RoleAssistant {
			role = "assistant"
		}
		w.Messages = append(w.Messages, anthropicMessage{Role: role, Content: content})
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, anthropicTool{Name: t.Name, Description: t.Description, InputSchema: t.Parameters})
	}
	headers := map[string]string{"x-api-key": a.apiKey, "anthropic-version": a.version}
	return nativeRequest(ctx, a.client, a.endpoint, w, "", headers)
}

type anthropicResponse struct {
	ID         string             `json:"id"`
	StopReason string             `json:"stop_reason"`
	Content    []anthropicContent `json:"content"`
	Usage      anthropicUsage     `json:"usage"`
}
type anthropicContent struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	Thinking  string          `json:"thinking"`
	Signature string          `json:"signature"`
	Data      json.RawMessage `json:"data"`
}
type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func anthropicThinkingSummary(content []anthropicContent) string {
	var summary strings.Builder
	for _, block := range content {
		// Anthropic exposes the thinking text as a readable field. A signature
		// authenticates that field for continuation; it is never rendered here.
		// Redacted blocks carry opaque provider data and are intentionally hidden.
		if block.Type != "thinking" || strings.TrimSpace(block.Thinking) == "" {
			continue
		}
		if summary.Len() > 0 {
			summary.WriteString("\n")
		}
		summary.WriteString(block.Thinking)
	}
	return boundedThinkingSummary(summary.String())
}

func (a *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := a.request(ctx, req, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := readNativeBody(resp.Body, a.maxBody)
	if err != nil {
		return Response{}, err
	}
	var w anthropicResponse
	if err := json.Unmarshal(b, &w); err != nil {
		return Response{}, errors.New("provider: invalid JSON response")
	}
	return anthropicResult(w)
}

func anthropicResult(w anthropicResponse) (Response, error) {
	m := Message{Role: RoleAssistant}
	opaque := anthropicOpaque{}
	finish := w.StopReason
	if finish == "" {
		return Response{}, errors.New("provider: Anthropic response has no stop reason")
	}
	if finish == "max_tokens" || finish == "stop_sequence" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", finish)
	}
	if finish != "end_turn" && finish != "tool_use" {
		return Response{}, fmt.Errorf("provider: unsupported Anthropic stop reason (%s)", finish)
	}
	if finish == "end_turn" {
		finish = "stop"
	}
	for _, c := range w.Content {
		switch c.Type {
		case "text":
			m.Content += c.Text
		case "tool_use":
			args := c.Input
			if len(args) == 0 {
				args = json.RawMessage("{}")
			}
			if err := validateObject(args, "tool arguments"); err != nil {
				return Response{}, err
			}
			if c.ID == "" || c.Name == "" {
				return Response{}, errors.New("provider: invalid Anthropic tool use")
			}
			m.ToolCalls = append(m.ToolCalls, ToolCall{ID: c.ID, Name: c.Name, Arguments: copyRaw(args)})
			if w.StopReason == "tool_use" {
				finish = "tool_calls"
			}
		case "thinking", "redacted_thinking":
			block := map[string]any{"type": c.Type}
			if c.Thinking != "" {
				block["thinking"] = c.Thinking
			}
			if c.Signature != "" {
				block["signature"] = c.Signature
			}
			if len(c.Data) > 0 {
				block["data"] = json.RawMessage(c.Data)
			}
			raw, _ := json.Marshal(block)
			opaque.Blocks = append(opaque.Blocks, raw)
		}
	}
	if len(opaque.Blocks) > 0 {
		m.Opaque, _ = json.Marshal(opaque)
	}
	return Response{Message: m, FinishReason: finish, Usage: Usage{InputTokens: w.Usage.InputTokens, OutputTokens: w.Usage.OutputTokens, TotalTokens: w.Usage.InputTokens + w.Usage.OutputTokens}, ThinkingSummary: anthropicThinkingSummary(w.Content), Opaque: responseID(w.ID)}, nil
}

type anthropicToolAccum struct {
	id, name string
	args     strings.Builder
}
type anthropicStreamState struct {
	message      Message
	finish       string
	usage        Usage
	id           string
	blocks       map[int]*anthropicToolAccum
	opaqueBlocks map[int]*anthropicOpaqueBlock
}

func (a *Anthropic) Stream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	resp, err := a.request(ctx, req, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	s := anthropicStreamState{message: Message{Role: RoleAssistant}, blocks: make(map[int]*anthropicToolAccum), opaqueBlocks: make(map[int]*anthropicOpaqueBlock)}
	terminal := false
	err = nativeSSE(ctx, io.LimitReader(resp.Body, int64(a.maxBody)+1), a.maxBody, func(frame string) (bool, error) {
		var e struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
				StopReason  string `json:"stop_reason"`
				Thinking    string `json:"thinking"`
				Signature   string `json:"signature"`
			} `json:"delta"`
			ContentBlock struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				Thinking  string          `json:"thinking"`
				Signature string          `json:"signature"`
				Data      json.RawMessage `json:"data"`
			} `json:"content_block"`
			Message    anthropicResponse `json:"message"`
			StopReason string            `json:"stop_reason"`
			Usage      anthropicUsage    `json:"usage"`
		}
		if err := json.Unmarshal([]byte(frame), &e); err != nil {
			return false, errors.New("provider: invalid Anthropic SSE event")
		}
		switch e.Type {
		case "message_start":
			s.id = e.Message.ID
			s.usage = Usage{InputTokens: e.Message.Usage.InputTokens}
		case "content_block_start":
			if e.ContentBlock.Type == "tool_use" {
				s.blocks[e.Index] = &anthropicToolAccum{id: e.ContentBlock.ID, name: e.ContentBlock.Name}
			}
			if e.ContentBlock.Type == "thinking" || e.ContentBlock.Type == "redacted_thinking" {
				s.opaqueBlocks[e.Index] = &anthropicOpaqueBlock{Type: e.ContentBlock.Type, Thinking: e.ContentBlock.Thinking, Signature: e.ContentBlock.Signature, Data: copyRaw(e.ContentBlock.Data)}
			}
		case "content_block_delta":
			if e.Delta.Type == "text_delta" {
				s.message.Content += e.Delta.Text
				if onDelta != nil {
					if err := onDelta(e.Delta.Text); err != nil {
						return false, err
					}
				}
			} else if e.Delta.Type == "input_json_delta" {
				b := s.blocks[e.Index]
				if b == nil {
					b = &anthropicToolAccum{}
					s.blocks[e.Index] = b
				}
				b.args.WriteString(e.Delta.PartialJSON)
			} else if e.Delta.Type == "thinking_delta" || e.Delta.Type == "signature_delta" {
				block := s.opaqueBlocks[e.Index]
				if block == nil {
					block = &anthropicOpaqueBlock{Type: "thinking"}
					s.opaqueBlocks[e.Index] = block
				}
				block.Thinking += e.Delta.Thinking
				block.Signature += e.Delta.Signature
			}
		case "message_delta":
			s.finish = e.StopReason
			if s.finish == "" {
				s.finish = e.Delta.StopReason
			}
			s.usage.OutputTokens = e.Usage.OutputTokens
			s.usage.TotalTokens = s.usage.InputTokens + s.usage.OutputTokens
		case "message_stop":
			terminal = true
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return Response{}, err
	}
	if !terminal {
		return Response{}, errors.New("provider: streaming response ended before message_stop")
	}
	for _, b := range s.blocks {
		args := b.args.String()
		if args == "" {
			args = "{}"
		}
		if err := validateObject(json.RawMessage(args), "streamed tool arguments"); err != nil {
			return Response{}, err
		}
		s.message.ToolCalls = append(s.message.ToolCalls, ToolCall{ID: b.id, Name: b.name, Arguments: json.RawMessage(args)})
	}
	if s.finish == "" {
		return Response{}, errors.New("provider: Anthropic stream has no stop reason")
	}
	if s.finish != "end_turn" && s.finish != "tool_use" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", s.finish)
	}
	if len(s.message.ToolCalls) > 0 {
		if s.finish != "tool_use" {
			return Response{}, errors.New("provider: Anthropic tool calls have an invalid stop reason")
		}
		s.finish = "tool_calls"
	}
	if s.finish == "end_turn" || s.finish == "" {
		s.finish = "stop"
	}
	if len(s.opaqueBlocks) > 0 {
		indices := make([]int, 0, len(s.opaqueBlocks))
		for index := range s.opaqueBlocks {
			indices = append(indices, index)
		}
		sort.Ints(indices)
		opaque := anthropicOpaque{Blocks: make([]json.RawMessage, 0, len(indices))}
		thinking := make([]anthropicContent, 0, len(indices))
		for _, index := range indices {
			block := s.opaqueBlocks[index]
			opaque.Blocks = append(opaque.Blocks, block.raw())
			thinking = append(thinking, anthropicContent{Type: block.Type, Thinking: block.Thinking, Signature: block.Signature})
		}
		s.message.Opaque, _ = json.Marshal(opaque)
		return Response{Message: s.message, FinishReason: s.finish, Usage: s.usage, ThinkingSummary: anthropicThinkingSummary(thinking), Opaque: responseID(s.id)}, nil
	}
	return Response{Message: s.message, FinishReason: s.finish, Usage: s.usage, Opaque: responseID(s.id)}, nil
}

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type ResponsesConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
	// TokenSource supplies a current bearer token for each request. It is
	// useful for OAuth providers whose refresh token may rotate.
	TokenSource func(context.Context) (string, error)
	// Store controls the Responses store field when set.
	Store              *bool
	DeveloperMessages  bool
	NamespaceTools     bool
	NamespaceName      string
	NoPreviousResponse bool
}

type OpenAIResponses struct {
	endpoint           string
	model              string
	apiKey             string
	client             clientDoer
	maxBody            int
	tokenSource        func(context.Context) (string, error)
	store              *bool
	developerMessages  bool
	namespaceTools     bool
	namespaceName      string
	noPreviousResponse bool
}

type clientDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func NewOpenAIResponses(cfg ResponsesConfig) (*OpenAIResponses, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("provider: model is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	endpoint, err := nativeEndpoint(cfg.BaseURL, "/v1/responses")
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("provider: invalid API key")
	}
	return &OpenAIResponses{endpoint: endpoint, model: cfg.Model, apiKey: cfg.APIKey,
		client: nativeHTTPClient(cfg.Timeout), maxBody: nativeMaxBody(cfg.MaxResponseBytes),
		tokenSource: cfg.TokenSource, store: cfg.Store, developerMessages: cfg.DeveloperMessages,
		namespaceTools: cfg.NamespaceTools, namespaceName: cfg.NamespaceName, noPreviousResponse: cfg.NoPreviousResponse}, nil
}

type responsesRequest struct {
	Model            string `json:"model"`
	Input            []any  `json:"input"`
	Tools            []any  `json:"tools,omitempty"`
	Stream           bool   `json:"stream,omitempty"`
	PreviousResponse string `json:"previous_response_id,omitempty"`
	Store            *bool  `json:"store,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

func (o *OpenAIResponses) request(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	model := req.Model
	if model == "" {
		model = o.model
	}
	input, err := responsesInputMode(req.Messages, o.developerMessages)
	if err != nil {
		return nil, err
	}
	w := responsesRequest{Model: model, Input: input, Stream: stream}
	w.Store = o.store
	tools := make([]responsesTool, 0, len(req.Tools))
	for _, t := range req.Tools {
		tools = append(tools, responsesTool{Type: "function", Name: t.Name, Description: t.Description, Parameters: t.Parameters})
	}
	if o.namespaceTools && len(tools) > 0 {
		name := o.namespaceName
		if name == "" {
			name = "bifrost"
		}
		w.Tools = append(w.Tools, map[string]any{"type": "namespace", "name": name, "description": "Bifrost local tools", "tools": tools})
	} else {
		for _, t := range tools {
			w.Tools = append(w.Tools, t)
		}
	}
	if len(req.Opaque) > 0 && !o.noPreviousResponse {
		var previous string
		if err := json.Unmarshal(req.Opaque, &previous); err != nil || previous == "" {
			return nil, errors.New("provider: OpenAI Responses opaque continuation must be a response ID string")
		}
		w.PreviousResponse = previous
	}
	headers := map[string]string{}
	key := o.apiKey
	if o.tokenSource != nil {
		var err error
		key, err = o.tokenSource(ctx)
		if err != nil {
			return nil, err
		}
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("provider: invalid bearer token")
	}
	return nativeRequest(ctx, o.client, o.endpoint, w, key, headers)
}

type responsesOpaque struct {
	ResponseID string            `json:"response_id,omitempty"`
	Output     []json.RawMessage `json:"output,omitempty"`
}

func decodeResponsesOpaque(raw json.RawMessage) (responsesOpaque, bool) {
	var opaque responsesOpaque
	if len(raw) == 0 || json.Unmarshal(raw, &opaque) != nil || len(opaque.Output) == 0 {
		return responsesOpaque{}, false
	}
	return opaque, true
}

func responsesInput(messages []Message) ([]any, error) {
	return responsesInputMode(messages, false)
}

func responsesInputMode(messages []Message, developerMessages bool) ([]any, error) {
	input := make([]any, 0, len(messages))
	for _, m := range messages {
		if m.Role == RoleTool {
			input = append(input, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": m.Content})
			continue
		}
		if m.Role == RoleAssistant {
			if opaque, ok := decodeResponsesOpaque(m.Opaque); ok {
				for _, raw := range opaque.Output {
					var item any
					if err := json.Unmarshal(raw, &item); err != nil {
						return nil, errors.New("provider: invalid OpenAI Responses opaque output item")
					}
					input = append(input, item)
				}
				continue
			}
		}
		if len(m.ToolCalls) > 0 {
			for _, c := range m.ToolCalls {
				item := map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": string(c.Arguments)}
				if c.Namespace != "" {
					item["namespace"] = c.Namespace
				}
				input = append(input, item)
			}
			continue
		}
		contents := make([]any, 0, 1+len(m.Images))
		if m.Content != "" {
			contents = append(contents, map[string]any{"type": "input_text", "text": m.Content})
		}
		for _, image := range m.Images {
			if err := validateImage(image); err != nil {
				return nil, err
			}
			imageURL, err := imageDataURL(image)
			if err != nil {
				return nil, err
			}
			contents = append(contents, map[string]any{"type": "input_image", "image_url": imageURL})
		}
		if len(contents) == 1 && m.Content != "" && len(m.Images) == 0 {
			role := string(m.Role)
			if developerMessages && m.Role == RoleSystem {
				role = "developer"
			}
			input = append(input, map[string]any{"role": role, "content": m.Content})
		} else {
			role := string(m.Role)
			if developerMessages && m.Role == RoleSystem {
				role = "developer"
			}
			input = append(input, map[string]any{"role": role, "content": contents})
		}
	}
	return input, nil
}

type responsesWire struct {
	ID               string               `json:"id"`
	Status           string               `json:"status"`
	Output           []responsesOutput    `json:"output"`
	OutputRaw        []json.RawMessage    `json:"-"`
	Usage            responsesUsage       `json:"usage"`
	IncompleteDetail *responsesIncomplete `json:"incomplete_details"`
}

func (w *responsesWire) UnmarshalJSON(data []byte) error {
	type wire struct {
		ID               string               `json:"id"`
		Status           string               `json:"status"`
		Output           []json.RawMessage    `json:"output"`
		Usage            responsesUsage       `json:"usage"`
		IncompleteDetail *responsesIncomplete `json:"incomplete_details"`
	}
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	w.ID, w.Status, w.OutputRaw, w.Usage, w.IncompleteDetail = decoded.ID, decoded.Status, decoded.Output, decoded.Usage, decoded.IncompleteDetail
	w.Output = nil
	for _, raw := range decoded.Output {
		var item responsesOutput
		if err := json.Unmarshal(raw, &item); err != nil {
			return err
		}
		w.Output = append(w.Output, item)
	}
	return nil
}

type responsesOutput struct {
	Type      string             `json:"type"`
	Role      string             `json:"role"`
	ID        string             `json:"id"`
	CallID    string             `json:"call_id"`
	Name      string             `json:"name"`
	Namespace string             `json:"namespace"`
	Arguments string             `json:"arguments"`
	Content   []responsesContent `json:"content"`
	Summary   []responsesSummary `json:"summary"`
}

type responsesContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type responsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type responsesIncomplete struct {
	Reason string `json:"reason"`
}

func (o *OpenAIResponses) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := o.request(ctx, req, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := readNativeBody(resp.Body, o.maxBody)
	if err != nil {
		return Response{}, err
	}
	var wire responsesWire
	if err := json.Unmarshal(b, &wire); err != nil {
		return Response{}, errors.New("provider: invalid JSON response")
	}
	return responsesResult(wire)
}

func responsesResult(w responsesWire) (Response, error) {
	if w.Status != "completed" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", w.Status)
	}
	if wireIncomplete(w.Status, "", w.IncompleteDetail) {
		reason := "incomplete"
		if w.IncompleteDetail != nil {
			reason = w.IncompleteDetail.Reason
		}
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", reason)
	}
	m := Message{Role: RoleAssistant}
	finish := "stop"
	for _, item := range w.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				if c.Type == "output_text" || c.Type == "text" {
					m.Content += c.Text
				}
			}
		case "function_call", "mcp_call":
			call, err := responsesToolCall(item)
			if err != nil {
				return Response{}, err
			}
			m.ToolCalls = append(m.ToolCalls, call)
			finish = "tool_calls"
		}
	}
	if len(w.OutputRaw) > 0 {
		m.Opaque, _ = json.Marshal(responsesOpaque{ResponseID: w.ID, Output: w.OutputRaw})
	}
	return Response{Message: m, FinishReason: finish, Usage: Usage{InputTokens: w.Usage.InputTokens, OutputTokens: w.Usage.OutputTokens, TotalTokens: w.Usage.TotalTokens}, ThinkingSummary: responsesThinkingSummary(w.Output), Opaque: responseID(w.ID)}, nil
}

func responsesThinkingSummary(output []responsesOutput) string {
	var summary strings.Builder
	for _, item := range output {
		if item.Type != "reasoning" {
			continue
		}
		for _, part := range item.Summary {
			if part.Type != "summary_text" && part.Type != "text" {
				continue
			}
			if strings.TrimSpace(part.Text) == "" {
				continue
			}
			if summary.Len() > 0 {
				summary.WriteString("\n")
			}
			summary.WriteString(part.Text)
			if summary.Len() >= maxThinkingSummaryBytes {
				return boundedThinkingSummary(summary.String())
			}
		}
	}
	return boundedThinkingSummary(summary.String())
}

func wireIncomplete(status, finish string, detail *responsesIncomplete) bool {
	if status == "incomplete" || status == "failed" || status == "cancelled" {
		return true
	}
	if detail != nil && detail.Reason != "" {
		return true
	}
	return finish == "length" || finish == "content_filter"
}

func responseID(id string) json.RawMessage {
	if id == "" {
		return nil
	}
	b, _ := json.Marshal(id)
	return b
}

type responsesStreamState struct {
	message Message
	finish  string
	args    map[string]*responsesToolAccum
	order   []string
	alias   map[string]string
	usage   Usage
	id      string
	status  string
	output  []json.RawMessage
}

type responsesToolAccum struct {
	id, name, namespace string
	args                strings.Builder
}

func (o *OpenAIResponses) Stream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	resp, err := o.request(ctx, req, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	state := responsesStreamState{message: Message{Role: RoleAssistant}, args: make(map[string]*responsesToolAccum)}
	terminal := false
	err = nativeSSE(ctx, io.LimitReader(resp.Body, int64(o.maxBody)+1), o.maxBody, func(frame string) (bool, error) {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal([]byte(frame), &envelope); err != nil {
			return false, errors.New("provider: invalid Responses SSE event")
		}
		var event struct {
			Type        string         `json:"type"`
			Delta       string         `json:"delta"`
			Response    *responsesWire `json:"response"`
			CallID      string         `json:"call_id"`
			ItemID      string         `json:"item_id"`
			Name        string         `json:"name"`
			Namespace   string         `json:"namespace"`
			Arguments   string         `json:"arguments"`
			OutputIndex int            `json:"output_index"`
			Usage       responsesUsage `json:"usage"`
			Item        struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"item"`
		}
		if err := json.Unmarshal([]byte(frame), &event); err != nil {
			return false, errors.New("provider: invalid Responses SSE event")
		}
		switch event.Type {
		case "response.output_text.delta":
			state.message.Content += event.Delta
			if onDelta != nil {
				if err := onDelta(event.Delta); err != nil {
					return false, err
				}
			}
		case "response.output_item.added":
			if raw, ok := envelope["item"]; ok {
				state.output = append(state.output, copyRaw(raw))
			}
			if event.Item.Type == "function_call" || event.Item.Type == "mcp_call" {
				state.tool(event.Item.CallID, event.Item.ID, event.Item.Name, event.Item.Namespace)
			}
		case "response.function_call_arguments.delta", "response.mcp_call_arguments.delta":
			call := state.tool(event.CallID, event.ItemID, event.Name, event.Namespace)
			call.args.WriteString(event.Delta)
		case "response.function_call_arguments.done", "response.mcp_call_arguments.done":
			call := state.tool(event.CallID, event.ItemID, event.Name, event.Namespace)
			if event.Arguments != "" {
				call.args.Reset()
				call.args.WriteString(event.Arguments)
			}
		case "response.completed", "response.done":
			terminal = true
			if event.Response != nil {
				state.id = event.Response.ID
				state.status = event.Response.Status
				state.output = append([]json.RawMessage(nil), event.Response.OutputRaw...)
				state.usage = Usage{InputTokens: event.Response.Usage.InputTokens, OutputTokens: event.Response.Usage.OutputTokens, TotalTokens: event.Response.Usage.TotalTokens}
				if event.Response.IncompleteDetail != nil {
					state.finish = event.Response.IncompleteDetail.Reason
				}
				if state.finish == "" {
					state.finish = event.Response.Status
				}
			}
			if state.status != "completed" {
				return false, fmt.Errorf("provider: incomplete response (%s)", state.status)
			}
			state.finish = "stop"
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return Response{}, err
	}
	if !terminal {
		return Response{}, errors.New("provider: streaming response ended before terminal event")
	}
	calls, err := state.functionCalls()
	if err != nil {
		return Response{}, err
	}
	state.message.ToolCalls = calls
	if state.status != "completed" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", state.status)
	}
	if len(state.message.ToolCalls) > 0 {
		state.finish = "tool_calls"
	}
	if state.finish == "incomplete" || state.finish == "length" || state.finish == "content_filter" || state.finish == "failed" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", state.finish)
	}
	if len(state.output) > 0 {
		state.message.Opaque, _ = json.Marshal(responsesOpaque{ResponseID: state.id, Output: state.output})
	}
	output := make([]responsesOutput, 0, len(state.output))
	for _, raw := range state.output {
		var item responsesOutput
		if err := json.Unmarshal(raw, &item); err != nil {
			return Response{}, errors.New("provider: invalid Responses output item")
		}
		output = append(output, item)
	}
	return Response{Message: state.message, FinishReason: state.finish, Usage: state.usage, ThinkingSummary: responsesThinkingSummary(output), Opaque: responseID(state.id)}, nil
}

func (s *responsesStreamState) tool(callID, itemID, name, namespace string) *responsesToolAccum {
	if s.alias == nil {
		s.alias = map[string]string{}
	}
	key := ""
	for _, candidate := range []string{callID, itemID} {
		if candidate == "" {
			continue
		}
		if mapped, ok := s.alias[candidate]; ok {
			key = mapped
			break
		}
	}
	if key == "" {
		switch {
		case callID != "":
			key = callID
		case itemID != "":
			key = itemID
		default:
			key = fmt.Sprintf("tool-%d", len(s.order))
		}
		s.order = append(s.order, key)
		s.args[key] = &responsesToolAccum{}
	}
	for _, candidate := range []string{callID, itemID} {
		if candidate != "" {
			s.alias[candidate] = key
		}
	}
	call := s.args[key]
	if callID != "" {
		call.id = callID
	} else if call.id == "" {
		call.id = itemID
	}
	if name != "" {
		call.name = name
	}
	if namespace != "" {
		call.namespace = namespace
	}
	return call
}

func (s *responsesStreamState) functionCalls() ([]ToolCall, error) {
	if calls, ok, err := functionCallsFromOutput(s.output); err != nil || ok {
		return calls, err
	}
	calls := make([]ToolCall, 0, len(s.order))
	for _, key := range s.order {
		call := s.args[key]
		if call == nil || (call.id == "" && call.name == "" && call.args.Len() == 0) {
			continue
		}
		item := responsesOutput{Type: "function_call", ID: call.id, CallID: call.id, Name: call.name, Namespace: call.namespace, Arguments: call.args.String()}
		converted, err := responsesToolCall(item)
		if err != nil {
			return nil, err
		}
		calls = append(calls, converted)
	}
	return calls, nil
}

func functionCallsFromOutput(raw []json.RawMessage) ([]ToolCall, bool, error) {
	var calls []ToolCall
	found := false
	for _, itemRaw := range raw {
		var item responsesOutput
		if err := json.Unmarshal(itemRaw, &item); err != nil {
			return nil, false, errors.New("provider: invalid Responses output item")
		}
		if item.Type != "function_call" && item.Type != "mcp_call" {
			continue
		}
		found = true
		call, err := responsesToolCall(item)
		if err != nil {
			return nil, true, err
		}
		calls = append(calls, call)
	}
	return calls, found, nil
}

func responsesToolCall(item responsesOutput) (ToolCall, error) {
	args := json.RawMessage(item.Arguments)
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if err := validateObject(args, "tool arguments"); err != nil {
		return ToolCall{}, err
	}
	id := item.CallID
	if id == "" {
		id = item.ID
	}
	name := namespacedToolName(item.Name, item.Namespace)
	if id == "" || name == "" {
		return ToolCall{}, errors.New("provider: invalid Responses function call")
	}
	return ToolCall{ID: id, Name: name, Namespace: item.Namespace, Arguments: copyRaw(args)}, nil
}

func namespacedToolName(name, namespace string) string {
	name = strings.TrimSpace(name)
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return name
	}
	for _, sep := range []string{".", "/", ":"} {
		prefix := namespace + sep
		if strings.HasPrefix(name, prefix) {
			return strings.TrimPrefix(name, prefix)
		}
	}
	return name
}

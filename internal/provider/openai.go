package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	defaultTimeout     = 60 * time.Second
	defaultResponseMax = 8 << 20
)

type OpenAIConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
}

type OpenAI struct {
	endpoint string
	model    string
	apiKey   string
	client   *http.Client
	maxBody  int
}

func NewOpenAI(cfg OpenAIConfig) (*OpenAI, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("provider: model is required")
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("provider: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("provider: credentials, queries, and fragments are not allowed in base URL")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("provider: invalid API key")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, errors.New("provider: HTTPS is required except for loopback HTTP")
	}
	path := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(path, "/chat/completions") {
		if path == "" {
			path = "/v1"
		}
		path += "/chat/completions"
	}
	u.Path, u.RawQuery, u.Fragment = path, "", ""
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	maxBody := cfg.MaxResponseBytes
	if maxBody <= 0 {
		maxBody = defaultResponseMax
	}
	return &OpenAI{endpoint: u.String(), model: cfg.Model, apiKey: cfg.APIKey,
		client: &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, maxBody: maxBody}, nil
}

func isLoopbackHost(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() || strings.EqualFold(host, "localhost")
}

type wireRequest struct {
	Model    string               `json:"model"`
	Messages []wireRequestMessage `json:"messages"`
	Tools    []wireTool           `json:"tools,omitempty"`
	Stream   bool                 `json:"stream"`
}

type wireRequestMessage struct {
	Role       string         `json:"role"`
	Content    any            `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Index    int          `json:"index,omitempty"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireToolSpec `json:"function"`
}

type wireToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type wireUsage struct {
	Input  int `json:"prompt_tokens"`
	Output int `json:"completion_tokens"`
	Total  int `json:"total_tokens"`
}

func (u wireUsage) normalized() Usage {
	return Usage{InputTokens: u.Input, OutputTokens: u.Output, TotalTokens: u.Total}
}

type wireResponse struct {
	Usage   wireUsage    `json:"usage"`
	Choices []wireChoice `json:"choices"`
}

type wireChoice struct {
	Message wireMessage `json:"message"`
	Finish  string      `json:"finish_reason"`
}

type wireStreamResponse struct {
	Usage   *wireUsage         `json:"usage,omitempty"`
	Choices []wireStreamChoice `json:"choices"`
}

type wireStreamChoice struct {
	Index        int       `json:"index"`
	Delta        wireDelta `json:"delta"`
	FinishReason *string   `json:"finish_reason"`
}

type wireDelta struct {
	Role      string         `json:"role"`
	Content   *string        `json:"content"`
	ToolCalls []wireToolCall `json:"tool_calls"`
}

func (o *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := o.doRequest(ctx, req, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(o.maxBody)+1))
	if err != nil {
		return Response{}, fmt.Errorf("provider: read response: %w", err)
	}
	if len(data) > o.maxBody {
		return Response{}, errors.New("provider: response exceeds configured limit")
	}
	var wr wireResponse
	if err := json.Unmarshal(data, &wr); err != nil {
		return Response{}, errors.New("provider: invalid JSON response")
	}
	if len(wr.Choices) == 0 {
		return Response{}, errors.New("provider: response contains no choices")
	}
	result, err := responseFromWire(wr.Choices[0])
	result.Usage = wr.Usage.normalized()
	return result, err
}

func (o *OpenAI) doRequest(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	if o == nil || o.client == nil {
		return nil, errors.New("provider: nil client")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	model := req.Model
	if model == "" {
		model = o.model
	}
	w := wireRequest{Model: model, Stream: stream}
	for _, m := range req.Messages {
		wm := wireRequestMessage{Role: string(m.Role), ToolCallID: m.ToolCallID}
		if len(m.Images) == 0 {
			if m.Content != "" || m.Role != RoleAssistant {
				wm.Content = m.Content
			}
		} else {
			parts := make([]any, 0, 1+len(m.Images))
			if m.Content != "" {
				parts = append(parts, map[string]any{"type": "text", "text": m.Content})
			}
			for _, image := range m.Images {
				dataURL, err := imageDataURL(image)
				if err != nil {
					return nil, err
				}
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": dataURL}})
			}
			wm.Content = parts
		}
		for _, c := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{ID: c.ID, Type: "function", Function: wireFunction{Name: c.Name, Arguments: string(c.Arguments)}})
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, wireTool{Type: "function", Function: wireToolSpec{Name: t.Name, Description: t.Description, Parameters: t.Parameters}})
	}
	body, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("provider: encode request: %w", err)
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("provider: create request: %w", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if stream {
		hreq.Header.Set("Accept", "text/event-stream")
	}
	if o.apiKey != "" {
		hreq.Header.Set("Authorization", "Bearer "+o.apiKey)
	}
	resp, err := o.client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("provider: request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("provider: server returned HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

func responseFromWire(choice wireChoice) (Response, error) {
	if choice.Finish == "length" || choice.Finish == "content_filter" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", choice.Finish)
	}
	wm := choice.Message
	if wm.Role != "assistant" {
		return Response{}, errors.New("provider: response must be an assistant message")
	}
	if err := validateToolCalls(wm.ToolCalls); err != nil {
		return Response{}, err
	}
	m := Message{Role: Role(wm.Role), ToolCallID: wm.ToolCallID, Content: deref(wm.Content)}
	for _, c := range wm.ToolCalls {
		args := json.RawMessage(c.Function.Arguments)
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		m.ToolCalls = append(m.ToolCalls, ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: args})
	}
	return Response{Message: m, FinishReason: choice.Finish}, nil
}

func validateToolCalls(calls []wireToolCall) error {
	if len(calls) > 32 {
		return errors.New("provider: too many tool calls in one response")
	}
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		if call.ID == "" || seen[call.ID] || call.Type != "function" || call.Function.Name == "" {
			return errors.New("provider: invalid or duplicate tool call")
		}
		seen[call.ID] = true
	}
	return nil
}

// Stream parses a bounded Server-Sent Events response from an
// OpenAI-compatible chat-completions endpoint. The endpoint must send a
// terminal [DONE] event; partial tool calls are accumulated and returned only
// after the stream has completed.
func (o *OpenAI) Stream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	resp, err := o.doRequest(ctx, req, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	return o.parseSSE(ctx, io.LimitReader(resp.Body, int64(o.maxBody)+1), onDelta)
}

type streamToolCall struct {
	id        strings.Builder
	name      strings.Builder
	arguments strings.Builder
	typeName  string
}

type streamAccumulator struct {
	usage        Usage
	content      strings.Builder
	toolCalls    map[int]*streamToolCall
	role         string
	finish       string
	contentSize  int
	argumentSize int
}

func (o *OpenAI) parseSSE(ctx context.Context, body io.Reader, onDelta func(string) error) (Response, error) {
	frames := sseFrameReader{reader: bufio.NewReader(body), limit: o.maxBody}
	acc := streamAccumulator{toolCalls: make(map[int]*streamToolCall)}
	for {
		data, hasFrame, eof, err := frames.next(ctx)
		if err != nil {
			return Response{}, err
		}
		if hasFrame {
			result, complete, dispatchErr := dispatchSSEFrame(data, &acc, onDelta, o.maxBody)
			if dispatchErr != nil {
				return Response{}, dispatchErr
			}
			if complete {
				// [DONE] is terminal. Return without attempting another read;
				// this matters for long-lived or intentionally blocking bodies.
				return result, nil
			}
		}
		if eof {
			break
		}
	}
	return Response{}, errors.New("provider: streaming response ended before [DONE]")
}

type sseFrameReader struct {
	reader *bufio.Reader
	limit  int
	total  int
	data   []string
}

// next returns one complete SSE data payload. eof is true when the underlying
// body ended; a final unterminated data line is still returned as a frame.
func (r *sseFrameReader) next(ctx context.Context) (frame string, hasFrame bool, eof bool, err error) {
	for {
		line, readErr := r.reader.ReadString('\n')
		r.total += len(line)
		if r.total > r.limit {
			return "", false, false, errors.New("provider: streaming response exceeds configured limit")
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "" {
			if len(r.data) > 0 {
				frame, r.data = strings.Join(r.data, "\n"), nil
				return frame, true, false, nil
			}
		} else if !strings.HasPrefix(line, ":") {
			field, value, found := strings.Cut(line, ":")
			if found && strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			if field == "data" {
				r.data = append(r.data, value)
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return "", false, false, fmt.Errorf("provider: read SSE response: %w", readErr)
			}
			if len(r.data) > 0 {
				frame, r.data = strings.Join(r.data, "\n"), nil
				return frame, true, true, nil
			}
			return "", false, true, nil
		}
		if err := ctx.Err(); err != nil {
			return "", false, false, err
		}
	}
}

func dispatchSSEFrame(data string, acc *streamAccumulator, onDelta func(string) error, limit int) (Response, bool, error) {
	if strings.TrimSpace(data) == "[DONE]" {
		result, err := acc.response()
		return result, true, err
	}
	var event wireStreamResponse
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return Response{}, false, errors.New("provider: invalid SSE event")
	}
	if event.Usage != nil {
		acc.usage = event.Usage.normalized()
		if len(event.Choices) == 0 {
			return Response{}, false, nil
		}
	}
	if len(event.Choices) != 1 {
		return Response{}, false, errors.New("provider: streaming response must contain exactly one choice")
	}
	return Response{}, false, acc.add(event.Choices[0], onDelta, limit)
}

func (a *streamAccumulator) add(choice wireStreamChoice, onDelta func(string) error, limit int) error {
	if choice.Index != 0 {
		return errors.New("provider: streaming response contained multiple choices")
	}
	a.setFinish(choice.FinishReason)
	if err := a.setRole(choice.Delta.Role); err != nil {
		return err
	}
	if err := a.addContent(choice.Delta.Content, onDelta, limit); err != nil {
		return err
	}
	return a.addToolCalls(choice.Delta.ToolCalls, limit)
}

func (a *streamAccumulator) setFinish(reason *string) {
	if reason != nil {
		a.finish = *reason
	}
}

func (a *streamAccumulator) setRole(role string) error {
	if role == "" {
		return nil
	}
	if a.role != "" && a.role != role {
		return errors.New("provider: inconsistent streaming response role")
	}
	a.role = role
	return nil
}

func (a *streamAccumulator) addContent(content *string, onDelta func(string) error, limit int) error {
	if content == nil || *content == "" {
		return nil
	}
	text := *content
	a.contentSize += len(text)
	if a.contentSize > limit {
		return errors.New("provider: streamed content exceeds configured limit")
	}
	a.content.WriteString(text)
	if onDelta != nil {
		if err := onDelta(text); err != nil {
			return err
		}
	}
	return nil
}

func (a *streamAccumulator) addToolCalls(calls []wireToolCall, limit int) error {
	for _, call := range calls {
		if err := a.addToolCall(call, limit); err != nil {
			return err
		}
	}
	return nil
}

func (a *streamAccumulator) addToolCall(call wireToolCall, limit int) error {
	if call.Index < 0 || call.Index >= 32 {
		return errors.New("provider: too many tool calls in one response")
	}
	toolCall, ok := a.toolCalls[call.Index]
	if !ok {
		if len(a.toolCalls) >= 32 {
			return errors.New("provider: too many tool calls in one response")
		}
		toolCall = &streamToolCall{}
		a.toolCalls[call.Index] = toolCall
	}
	if call.Type != "" {
		if toolCall.typeName != "" && toolCall.typeName != call.Type {
			return errors.New("provider: inconsistent streaming tool call type")
		}
		toolCall.typeName = call.Type
	}
	toolCall.id.WriteString(call.ID)
	toolCall.name.WriteString(call.Function.Name)
	toolCall.arguments.WriteString(call.Function.Arguments)
	a.argumentSize += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments)
	if a.argumentSize > limit {
		return errors.New("provider: streamed tool calls exceed configured limit")
	}
	return nil
}

func (a *streamAccumulator) response() (Response, error) {
	if a.finish == "" {
		return Response{}, errors.New("provider: streaming response has no terminal finish reason")
	}
	if a.finish != "stop" && a.finish != "tool_calls" {
		if a.finish == "length" || a.finish == "content_filter" {
			return Response{}, fmt.Errorf("provider: incomplete response (%s)", a.finish)
		}
		return Response{}, fmt.Errorf("provider: nonterminal finish reason %q", a.finish)
	}
	indices := make([]int, 0, len(a.toolCalls))
	for index := range a.toolCalls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	wm := wireMessage{Role: a.role}
	if a.content.Len() > 0 {
		content := a.content.String()
		wm.Content = &content
	}
	for _, index := range indices {
		call := a.toolCalls[index]
		arguments := call.arguments.String()
		if arguments == "" {
			arguments = "{}"
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal([]byte(arguments), &object); err != nil || object == nil {
			return Response{}, errors.New("provider: streamed tool arguments must be a JSON object")
		}
		wm.ToolCalls = append(wm.ToolCalls, wireToolCall{ID: call.id.String(), Type: call.typeName, Function: wireFunction{Name: call.name.String(), Arguments: arguments}})
	}
	result, err := responseFromWire(wireChoice{Message: wm, Finish: a.finish})
	if err != nil {
		return Response{}, err
	}
	result.Usage = a.usage
	return result, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type GeminiConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
}

type Gemini struct {
	baseURL, model, apiKey string
	client                 clientDoer
	maxBody                int
}

func NewGemini(cfg GeminiConfig) (*Gemini, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("provider: model is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://generativelanguage.googleapis.com/v1beta"
	}
	u, err := url.Parse(strings.TrimSpace(cfg.BaseURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("provider: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("provider: credentials, queries, and fragments are not allowed in base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname())) {
		return nil, errors.New("provider: HTTPS is required except for loopback HTTP")
	}
	if strings.ContainsAny(cfg.APIKey, "\r\n") {
		return nil, errors.New("provider: invalid API key")
	}
	return &Gemini{baseURL: strings.TrimRight(u.String(), "/"), model: cfg.Model, apiKey: cfg.APIKey, client: nativeHTTPClient(cfg.Timeout), maxBody: nativeMaxBody(cfg.MaxResponseBytes)}, nil
}

type geminiRequest struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Tools             []geminiTools   `json:"tools,omitempty"`
}
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInline           `json:"inlineData,omitempty"`
	FileData         *geminiFile             `json:"fileData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
}
type geminiInline struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}
type geminiFile struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}
type geminiFunctionCall struct {
	Name string                     `json:"name"`
	Args map[string]json.RawMessage `json:"args"`
	ID   string                     `json:"id,omitempty"`
}
type geminiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
	ID       string         `json:"id,omitempty"`
}
type geminiTools struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}
type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parametersJsonSchema,omitempty"`
}

type geminiOpaque struct {
	ThoughtSignatures map[string]string `json:"thought_signatures,omitempty"`
}

func (g *Gemini) endpoint(stream bool) string {
	action := "generateContent"
	if stream {
		action = "streamGenerateContent?alt=sse"
	}
	return g.baseURL + "/models/" + url.PathEscape(g.model) + ":" + action
}

func (g *Gemini) request(ctx context.Context, req Request, stream bool) (*http.Response, error) {
	w := geminiRequest{}
	for _, m := range req.Messages {
		if m.Role == RoleSystem {
			c := geminiContent{Parts: []geminiPart{{Text: m.Content}}}
			if w.SystemInstruction == nil {
				w.SystemInstruction = &c
			} else {
				w.SystemInstruction.Parts = append(w.SystemInstruction.Parts, c.Parts...)
			}
			continue
		}
		role := "user"
		if m.Role == RoleAssistant {
			role = "model"
		}
		c := geminiContent{Role: role}
		if m.Role == RoleTool {
			resp := map[string]any{"output": m.Content}
			c.Role = "user"
			name := m.ToolName
			if name == "" {
				name = m.ToolCallID
			}
			c.Parts = append(c.Parts, geminiPart{FunctionResponse: &geminiFunctionResponse{Name: name, Response: resp, ID: m.ToolCallID}})
		} else {
			if m.Content != "" {
				c.Parts = append(c.Parts, geminiPart{Text: m.Content})
			}
			for _, im := range m.Images {
				if err := validateImage(im); err != nil {
					return nil, err
				}
				if len(im.Data) > 0 {
					mt := im.MediaType
					if mt == "" {
						mt = "application/octet-stream"
					}
					c.Parts = append(c.Parts, geminiPart{InlineData: &geminiInline{MIMEType: mt, Data: base64.StdEncoding.EncodeToString(im.Data)}})
				} else if im.URL != "" {
					c.Parts = append(c.Parts, geminiPart{FileData: &geminiFile{MIMEType: im.MediaType, FileURI: im.URL}})
				}
			}
			var opaque geminiOpaque
			_ = json.Unmarshal(m.Opaque, &opaque)
			for _, call := range m.ToolCalls {
				var args map[string]json.RawMessage
				_ = json.Unmarshal(call.Arguments, &args)
				p := geminiPart{FunctionCall: &geminiFunctionCall{Name: call.Name, Args: args, ID: call.ID}}
				if sig := opaque.ThoughtSignatures[call.ID]; sig != "" {
					p.ThoughtSignature = sig
				}
				c.Parts = append(c.Parts, p)
			}
		}
		w.Contents = append(w.Contents, c)
	}
	for _, t := range req.Tools {
		w.Tools = append(w.Tools, geminiTools{FunctionDeclarations: []geminiFunctionDecl{{Name: t.Name, Description: t.Description, Parameters: t.Parameters}}})
	}
	return nativeRequest(ctx, g.client, g.endpoint(stream), w, "", map[string]string{"x-goog-api-key": g.apiKey})
}

type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Usage      geminiUsage       `json:"usageMetadata"`
}
type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}
type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func (g *Gemini) Complete(ctx context.Context, req Request) (Response, error) {
	resp, err := g.request(ctx, req, false)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	b, err := readNativeBody(resp.Body, g.maxBody)
	if err != nil {
		return Response{}, err
	}
	var w geminiResponse
	if err := json.Unmarshal(b, &w); err != nil {
		return Response{}, errors.New("provider: invalid JSON response")
	}
	return geminiResult(w)
}

func geminiResult(w geminiResponse) (Response, error) {
	if len(w.Candidates) != 1 {
		return Response{}, errors.New("provider: Gemini response must contain exactly one candidate")
	}
	return geminiCandidateResult(w.Candidates[0], Usage{InputTokens: w.Usage.PromptTokenCount, OutputTokens: w.Usage.CandidatesTokenCount, TotalTokens: w.Usage.TotalTokenCount})
}
func geminiCandidateResult(c geminiCandidate, u Usage) (Response, error) {
	m := Message{Role: RoleAssistant}
	opaque := geminiOpaque{ThoughtSignatures: make(map[string]string)}
	seenIDs := make(map[string]bool)
	finish := strings.ToLower(c.FinishReason)
	if finish == "" {
		return Response{}, errors.New("provider: Gemini response has no finish reason")
	}
	if finish != "stop" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", finish)
	}
	for i, p := range c.Content.Parts {
		if p.Text != "" && !p.Thought {
			m.Content += p.Text
		}
		if p.FunctionCall != nil {
			args, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				return Response{}, err
			}
			if err := validateObject(args, "tool arguments"); err != nil {
				return Response{}, err
			}
			id := p.FunctionCall.ID
			if id == "" {
				id = fmt.Sprintf("gemini-call-%d", i)
			}
			if seenIDs[id] {
				return Response{}, errors.New("provider: duplicate Gemini tool call ID")
			}
			seenIDs[id] = true
			m.ToolCalls = append(m.ToolCalls, ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: args})
			if p.ThoughtSignature != "" {
				opaque.ThoughtSignatures[id] = p.ThoughtSignature
			}
			finish = "tool_calls"
		}
	}
	if len(opaque.ThoughtSignatures) > 0 {
		m.Opaque, _ = json.Marshal(opaque)
	}
	return Response{Message: m, FinishReason: finish, Usage: u, Opaque: copyRaw(m.Opaque)}, nil
}

func (g *Gemini) Stream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	resp, err := g.request(ctx, req, true)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	var last geminiResponse
	var text strings.Builder
	var calls []ToolCall
	callIndex := make(map[string]int)
	opaque := geminiOpaque{ThoughtSignatures: make(map[string]string)}
	finish := ""
	done := false
	err = nativeSSE(ctx, io.LimitReader(resp.Body, int64(g.maxBody)+1), g.maxBody, func(frame string) (bool, error) {
		var w geminiResponse
		if err := json.Unmarshal([]byte(frame), &w); err != nil {
			return false, errors.New("provider: invalid Gemini SSE event")
		}
		if len(w.Candidates) != 1 {
			return false, errors.New("provider: Gemini response must contain exactly one candidate")
		}
		last = w
		finish = w.Candidates[0].FinishReason
		for i, p := range w.Candidates[0].Content.Parts {
			if p.Text != "" && !p.Thought {
				text.WriteString(p.Text)
				if onDelta != nil {
					if err := onDelta(p.Text); err != nil {
						return false, err
					}
				}
			}
			if p.FunctionCall != nil {
				args, e := json.Marshal(p.FunctionCall.Args)
				if e != nil {
					return false, e
				}
				if e = validateObject(args, "tool arguments"); e != nil {
					return false, e
				}
				id := p.FunctionCall.ID
				key := id
				if id == "" {
					key = fmt.Sprintf("part-%d", i)
					id = fmt.Sprintf("gemini-call-%d", len(calls))
				}
				if index, ok := callIndex[key]; ok {
					// Gemini may repeat a function-call part in successive
					// stream chunks; retain one complete call per provider ID.
					calls[index].Arguments = args
				} else {
					callIndex[key] = len(calls)
					calls = append(calls, ToolCall{ID: id, Name: p.FunctionCall.Name, Arguments: args})
				}
				if p.ThoughtSignature != "" {
					opaque.ThoughtSignatures[id] = p.ThoughtSignature
				}
			}
		}
		if finish != "" {
			done = true
			return true, nil
		}
		return false, nil
	})
	if err != nil && !errors.Is(err, errNativeSSEEOF) {
		return Response{}, err
	}
	if !done && !errors.Is(err, errNativeSSEEOF) {
		return Response{}, errors.New("provider: Gemini stream ended without a final candidate")
	}
	m := Message{Role: RoleAssistant, Content: text.String(), ToolCalls: calls}
	if len(opaque.ThoughtSignatures) > 0 {
		m.Opaque, _ = json.Marshal(opaque)
	}
	if finish == "" {
		return Response{}, errors.New("provider: Gemini stream has no finish reason")
	}
	finish = strings.ToLower(finish)
	if finish != "stop" {
		return Response{}, fmt.Errorf("provider: incomplete response (%s)", finish)
	}
	if len(calls) > 0 {
		finish = "tool_calls"
	}
	return Response{Message: m, FinishReason: finish, Usage: Usage{InputTokens: last.Usage.PromptTokenCount, OutputTokens: last.Usage.CandidatesTokenCount, TotalTokens: last.Usage.TotalTokenCount}, Opaque: copyRaw(m.Opaque)}, nil
}

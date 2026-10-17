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

// ChatGPTConfig configures the official Sign in with ChatGPT Responses route.
// TokenSource is called for every request, allowing the account layer to
// refresh and rotate OAuth credentials before inference.
type ChatGPTConfig struct {
	BaseURL          string
	Model            string
	TokenSource      func(context.Context) (string, error)
	Timeout          time.Duration
	MaxResponseBytes int
}

type ChatGPT struct {
	responses      *OpenAIResponses
	modelsEndpoint string
	tokenSource    func(context.Context) (string, error)
	client         *http.Client
	maxBody        int
}

func NewChatGPT(cfg ChatGPTConfig) (*ChatGPT, error) {
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("provider: model is required")
	}
	if cfg.TokenSource == nil {
		return nil, errors.New("provider: ChatGPT token source is required")
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.openai.com"
	}
	store := false
	r, err := NewOpenAIResponses(ResponsesConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes, TokenSource: cfg.TokenSource, Store: &store, DeveloperMessages: true, NamespaceTools: true, NamespaceName: "bifrost", NoPreviousResponse: true})
	if err != nil {
		return nil, err
	}
	modelsEndpoint, err := modelsEndpointForBase(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	return &ChatGPT{responses: r, modelsEndpoint: modelsEndpoint, tokenSource: cfg.TokenSource, client: nativeHTTPClient(cfg.Timeout), maxBody: nativeMaxBody(cfg.MaxResponseBytes)}, nil
}

type ModelInfo struct {
	Slug             string `json:"slug"`
	ID               string `json:"id,omitempty"`
	DisplayName      string `json:"display_name"`
	Name             string `json:"name,omitempty"`
	Visibility       string `json:"visibility,omitempty"`
	ContextLength    int    `json:"context_length,omitempty"`
	ContextWindow    int    `json:"context_window,omitempty"`
	MaxContextWindow int    `json:"max_context_window,omitempty"`
	InputTokenLimit  int    `json:"input_token_limit,omitempty"`
}

func reportedContext(values ...int) int {
	max := 0
	for _, n := range values {
		if n > max {
			max = n
		}
	}
	return max
}

// Models returns the account-specific model catalog used by ChatGPT plan
// inference. Callers should present Slug to the model selector.
func (c *ChatGPT) Models(ctx context.Context) ([]ModelInfo, error) {
	if c == nil || c.client == nil || c.tokenSource == nil {
		return nil, errors.New("provider: nil ChatGPT client")
	}
	token, err := c.tokenSource(ctx)
	if err != nil {
		return nil, err
	}
	if strings.ContainsAny(token, "\r\n") || token == "" {
		return nil, errors.New("provider: invalid bearer token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsEndpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("provider: create models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provider: models request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider: models server returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxBody)+1))
	if err != nil || len(b) > c.maxBody {
		return nil, errors.New("provider: models response exceeds configured limit")
	}
	var wire struct {
		Models []ModelInfo `json:"models"`
		Data   []ModelInfo `json:"data"`
	}
	if json.Unmarshal(b, &wire) != nil {
		return nil, errors.New("provider: invalid models response")
	}
	models := wire.Models
	if len(models) == 0 {
		models = wire.Data
	}
	filtered := make([]ModelInfo, 0, len(models))
	for _, model := range models {
		if model.Slug == "" {
			model.Slug = strings.TrimSpace(model.ID)
		}
		if model.DisplayName == "" {
			model.DisplayName = strings.TrimSpace(model.Name)
		}
		if model.DisplayName == "" {
			model.DisplayName = model.Slug
		}
		model.ContextLength = reportedContext(model.ContextLength, model.ContextWindow, model.MaxContextWindow, model.InputTokenLimit)
		if model.Slug != "" && (model.Visibility == "" || model.Visibility == "list") {
			filtered = append(filtered, model)
		}
	}
	if len(filtered) > 0 {
		return filtered, nil
	}
	return nil, nil
}

func (c *ChatGPT) Complete(ctx context.Context, req Request) (Response, error) {
	if c == nil || c.responses == nil {
		return Response{}, errors.New("provider: nil ChatGPT client")
	}
	// ChatGPT plan usage requires streaming. Accumulate the stream for callers
	// that use the synchronous Client interface.
	return c.responses.Stream(ctx, req, nil)
}

func (c *ChatGPT) Stream(ctx context.Context, req Request, onDelta func(string) error) (Response, error) {
	if c == nil || c.responses == nil {
		return Response{}, errors.New("provider: nil ChatGPT client")
	}
	return c.responses.Stream(ctx, req, onDelta)
}

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// DiscoverCatalogModels queries the live provider API and returns only models
// returned by that API. It never returns hardcoded or fabricated model lists.
func DiscoverCatalogModels(ctx context.Context, cfg Config) ([]ModelInfo, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = KindChatCompletions
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	var live []ModelInfo
	var liveErr error

	switch kind {
	case KindChatGPT:
		if cfg.TokenSource == nil {
			return nil, nil
		}
		client, err := NewChatGPT(ChatGPTConfig{BaseURL: cfg.BaseURL, Model: "catalog", TokenSource: cfg.TokenSource, Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
		live, liveErr = client.Models(ctx)
	case KindAnthropic:
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, nil
		}
		live, liveErr = fetchAnthropicModels(ctx, cfg.BaseURL, cfg.APIKey)
	case KindGemini:
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, nil
		}
		live, liveErr = fetchGeminiModels(ctx, cfg.BaseURL, cfg.APIKey)
	case KindOpenRouter:
		live, liveErr = fetchOpenAIStyleModels(ctx, firstURL(cfg.BaseURL, "https://openrouter.ai/api/v1"), cfg.APIKey)
	case KindResponses:
		if strings.TrimSpace(cfg.APIKey) == "" {
			return nil, nil
		}
		live, liveErr = fetchOpenAIStyleModels(ctx, firstURL(cfg.BaseURL, "https://api.openai.com/v1"), cfg.APIKey)
	case KindChatCompletions:
		base := firstURL(cfg.BaseURL, "http://127.0.0.1:11434/v1")
		if strings.TrimSpace(cfg.APIKey) != "" || strings.Contains(base, "127.0.0.1") || strings.Contains(base, "localhost") || strings.TrimSpace(cfg.BaseURL) != "" {
			live, liveErr = fetchOpenAIStyleModels(ctx, base, cfg.APIKey)
		}
	default:
		return nil, nil
	}

	if liveErr != nil {
		return nil, liveErr
	}

	seen := make(map[string]bool, len(live))
	out := make([]ModelInfo, 0, len(live))
	for _, m := range live {
		slug := strings.TrimSpace(m.Slug)
		if slug == "" {
			slug = strings.TrimSpace(m.ID)
		}
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		m.Slug = slug
		if m.DisplayName == "" {
			m.DisplayName = strings.TrimSpace(m.Name)
		}
		if m.DisplayName == "" {
			m.DisplayName = slug
		}
		out = append(out, m)
	}
	return out, nil
}

// ModelContextWindowTokens returns the maximum input context window in tokens
// for a connected provider and model slug, or 0 when no model is connected.
func ModelContextWindowTokens(providerKind, model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	p := strings.ToLower(strings.TrimSpace(providerKind))
	if m == "" || m == "—" {
		return 0
	}

	switch {
	case strings.Contains(m, "gemini-2.5"), strings.Contains(m, "gemini-1.5"), strings.Contains(m, "gpt-4.1"), strings.Contains(m, "opus-4-6"), strings.Contains(m, "1m"):
		return 1_000_000
	case strings.Contains(m, "gpt-5"):
		return 400_000
	case strings.Contains(m, "claude"), p == "anthropic", p == "claude", p == "cursor":
		return 200_000
	case strings.Contains(m, "o3"), strings.Contains(m, "o4"), strings.Contains(m, "codex"):
		return 200_000
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "deepseek"):
		return 128_000
	case p == "gemini":
		return 1_000_000
	default:
		return 0
	}
}

func firstURL(a, fallback string) string {
	if strings.TrimSpace(a) != "" {
		return strings.TrimSpace(a)
	}
	return fallback
}

func modelsEndpointForBase(baseURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err == nil {
		p := strings.TrimRight(u.Path, "/")
		if strings.HasSuffix(p, "/v1") || strings.HasSuffix(p, "/v1/models") {
			return nativeEndpoint(baseURL, "/models")
		}
	}
	return nativeEndpoint(baseURL, "/v1/models")
}

func fetchOpenAIStyleModels(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	endpoint, err := modelsEndpointForBase(baseURL)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(apiKey))
	}
	client := nativeHTTPClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	type catalogItem struct {
		ID               string `json:"id"`
		Slug             string `json:"slug"`
		Name             string `json:"name"`
		DisplayName      string `json:"display_name"`
		ContextLength    int    `json:"context_length"`
		ContextWindow    int    `json:"context_window"`
		MaxContextWindow int    `json:"max_context_window"`
		InputTokenLimit  int    `json:"input_token_limit"`
		TopProvider      struct {
			ContextLength    int `json:"context_length"`
			ContextWindow    int `json:"context_window"`
			MaxContextWindow int `json:"max_context_window"`
			InputTokenLimit  int `json:"input_token_limit"`
		} `json:"top_provider"`
	}
	var wire struct {
		Data   []catalogItem `json:"data"`
		Models []catalogItem `json:"models"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	items := wire.Data
	if len(items) == 0 {
		items = wire.Models
	}
	out := make([]ModelInfo, 0, len(items))
	for _, item := range items {
		slug := strings.TrimSpace(item.ID)
		if slug == "" {
			slug = strings.TrimSpace(item.Slug)
		}
		if slug == "" {
			continue
		}
		name := strings.TrimSpace(item.DisplayName)
		if name == "" {
			name = strings.TrimSpace(item.Name)
		}
		if name == "" {
			name = slug
		}
		ctxLen := reportedContext(item.ContextLength, item.ContextWindow, item.MaxContextWindow, item.InputTokenLimit, item.TopProvider.ContextLength, item.TopProvider.ContextWindow, item.TopProvider.MaxContextWindow, item.TopProvider.InputTokenLimit)
		out = append(out, ModelInfo{
			Slug:          slug,
			DisplayName:   name,
			ContextLength: ctxLen,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func fetchAnthropicModels(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	endpoint, err := modelsEndpointForBase(firstURL(baseURL, "https://api.anthropic.com"))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-api-key", strings.TrimSpace(apiKey))
	req.Header.Set("anthropic-version", "2023-06-01")
	client := nativeHTTPClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("anthropic models returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var wire struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, item := range wire.Data {
		if strings.TrimSpace(item.ID) == "" {
			continue
		}
		out = append(out, ModelInfo{Slug: item.ID, DisplayName: firstURL(item.DisplayName, item.ID)})
	}
	return out, nil
}

func fetchGeminiModels(ctx context.Context, baseURL, apiKey string) ([]ModelInfo, error) {
	endpoint, err := nativeEndpoint(firstURL(baseURL, "https://generativelanguage.googleapis.com"), "/v1beta/models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-goog-api-key", strings.TrimSpace(apiKey))
	client := nativeHTTPClient(10 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("gemini models returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var wire struct {
		Models []struct {
			Name            string `json:"name"`
			DisplayName     string `json:"displayName"`
			InputTokenLimit int    `json:"inputTokenLimit"`
		} `json:"models"`
	}
	if err := json.Unmarshal(b, &wire); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, item := range wire.Models {
		slug := strings.TrimPrefix(strings.TrimSpace(item.Name), "models/")
		if slug == "" || !strings.Contains(slug, "gemini") {
			continue
		}
		out = append(out, ModelInfo{
			Slug:          slug,
			DisplayName:   firstURL(item.DisplayName, slug),
			ContextLength: item.InputTokenLimit,
		})
	}
	return out, nil
}

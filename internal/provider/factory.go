package provider

import (
	"context"
	"errors"
	"strings"
	"time"
)

type Kind string

const (
	KindChatCompletions Kind = "chat-completions"
	KindResponses       Kind = "responses"
	KindAnthropic       Kind = "anthropic"
	KindGemini          Kind = "gemini"
	KindChatGPT         Kind = "chatgpt"
	KindOpenRouter      Kind = "openrouter"
)

type Config struct {
	Kind             Kind
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
	TokenSource      func(context.Context) (string, error)
}

func New(cfg Config) (Client, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = KindChatCompletions
	}
	switch kind {
	case KindChatCompletions:
		if strings.TrimSpace(cfg.BaseURL) == "" {
			cfg.BaseURL = "https://api.openai.com/v1"
		}
		return NewOpenAI(OpenAIConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	case KindResponses:
		return NewOpenAIResponses(ResponsesConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	case KindChatGPT:
		return NewChatGPT(ChatGPTConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, TokenSource: cfg.TokenSource, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	case KindOpenRouter:
		return NewOpenRouter(OpenRouterConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	case KindAnthropic:
		return NewAnthropic(AnthropicConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	case KindGemini:
		return NewGemini(GeminiConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	default:
		return nil, errors.New("provider: unsupported provider kind " + string(kind))
	}
}

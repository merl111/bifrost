package provider

import (
	"net/http"
	"time"
)

// OpenRouterConfig uses OpenRouter's OpenAI-compatible chat-completions API.
type OpenRouterConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Timeout          time.Duration
	MaxResponseBytes int
	HTTPReferer      string
	XTitle           string
}

type OpenRouter struct{ *OpenAI }

func NewOpenRouter(cfg OpenRouterConfig) (*OpenRouter, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://openrouter.ai/api/v1"
	}
	o, err := NewOpenAI(OpenAIConfig{BaseURL: cfg.BaseURL, Model: cfg.Model, APIKey: cfg.APIKey, Timeout: cfg.Timeout, MaxResponseBytes: cfg.MaxResponseBytes})
	if err != nil {
		return nil, err
	}
	if cfg.HTTPReferer != "" || cfg.XTitle != "" {
		base := o.client.Transport
		o.client.Transport = roundTripHeaders{base: base, referer: cfg.HTTPReferer, title: cfg.XTitle}
	}
	return &OpenRouter{OpenAI: o}, nil
}

type roundTripHeaders struct {
	base           http.RoundTripper
	referer, title string
}

func (t roundTripHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.referer != "" {
		r = cloneRequestWithHeader(r, "HTTP-Referer", t.referer)
	}
	if t.title != "" {
		r = cloneRequestWithHeader(r, "X-Title", t.title)
	}
	if t.base == nil {
		return http.DefaultTransport.RoundTrip(r)
	}
	return t.base.RoundTrip(r)
}
func cloneRequestWithHeader(r *http.Request, name, value string) *http.Request {
	c := r.Clone(r.Context())
	c.Header.Set(name, value)
	return c
}

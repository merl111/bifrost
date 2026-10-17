// Package decision provides a small, bounded client for typed choice
// decisions. It supports TypeSafe's native System One endpoint and
// OpenRouter's Decisions API without adding another SDK dependency.
package decision

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"bifrost/internal/provider"
)

// Kind identifies a supported decision service.
type Kind string

const (
	KindTypeSafe   Kind = "typesafe"
	KindOpenRouter Kind = "openrouter"

	// These aliases make the public configuration readable at call sites while
	// retaining the Kind-prefixed names for callers that prefer them.
	TypeSafe   = KindTypeSafe
	OpenRouter = KindOpenRouter
)

const (
	typeSafeEndpoint   = "https://api.typesafe.ai/v1/systemone"
	openRouterEndpoint = "https://openrouter.ai/api/alpha/decisions"
	typeSafeModel      = "jev-latest"
	openRouterModel    = "~typesafe/jev-latest"
	defaultTimeout     = 60 * time.Second
	maxStateBytes      = 8 << 10
	maxCriteria        = 255
	maxCriterionBytes  = 4 << 10
	maxRequestBytes    = 64 << 10
	maxResponseBytes   = 1 << 20
	probabilityEpsilon = 1e-6
)

// Config configures a decision client. BaseURL is an endpoint override, not
// a provider root: when empty it uses the service's native decision endpoint.
// HTTP is accepted only for loopback fixtures; public traffic must use HTTPS.
type Config struct {
	Kind    Kind
	BaseURL string
	Model   string
	APIKey  string
	Timeout time.Duration
}

// Result is the validated answer to a choice question.
type Result struct {
	Choice     string
	Confidence float64
	Usage      provider.Usage
}

// Client performs typed choice decisions.
type Client struct {
	kind     Kind
	endpoint string
	model    string
	apiKey   string
	http     *http.Client
}

// New validates config and constructs a decision client. It performs no
// network request. API keys are retained only in memory and never included in
// returned errors.
func New(cfg Config) (*Client, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(string(cfg.Kind))))
	if kind != KindTypeSafe && kind != KindOpenRouter {
		return nil, fmt.Errorf("decision: unsupported kind %q", kind)
	}
	endpoint := strings.TrimSpace(cfg.BaseURL)
	if endpoint == "" {
		if kind == KindTypeSafe {
			endpoint = typeSafeEndpoint
		} else {
			endpoint = openRouterEndpoint
		}
	}
	endpoint, err := validateEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	key := strings.TrimSpace(cfg.APIKey)
	if key == "" {
		return nil, errors.New("decision: API key is required")
	}
	if strings.ContainsAny(key, "\r\n") {
		return nil, errors.New("decision: API key contains invalid control characters")
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		if kind == KindTypeSafe {
			model = typeSafeModel
		} else {
			model = openRouterModel
		}
	}
	if len(model) > 256 || strings.ContainsAny(model, "\r\n") {
		return nil, errors.New("decision: model is invalid")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &Client{
		kind:     kind,
		endpoint: endpoint,
		model:    model,
		apiKey:   key,
		http: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

// Choose asks the decision model to select one of criteria's keys. State is
// intentionally a string and bounded to keep routing calls cheap and safe;
// callers can JSON-encode structured state themselves.
func (c *Client) Choose(ctx context.Context, state string, criteria map[string]string) (Result, error) {
	if c == nil || c.http == nil {
		return Result{}, errors.New("decision: nil client")
	}
	if ctx == nil {
		return Result{}, errors.New("decision: nil context")
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !utf8.ValidString(state) || len([]byte(state)) > maxStateBytes {
		return Result{}, fmt.Errorf("decision: state exceeds %d-byte limit or is invalid UTF-8", maxStateBytes)
	}
	if len(criteria) == 0 || len(criteria) > maxCriteria {
		return Result{}, fmt.Errorf("decision: criteria must contain 1..%d options", maxCriteria)
	}
	cleanCriteria := make(map[string]string, len(criteria))
	for choice, description := range criteria {
		if choice == "" || !utf8.ValidString(choice) || len([]byte(choice)) > maxCriterionBytes {
			return Result{}, errors.New("decision: criterion key is invalid")
		}
		if !utf8.ValidString(description) || len([]byte(description)) > maxCriterionBytes {
			return Result{}, errors.New("decision: criterion description is invalid")
		}
		cleanCriteria[choice] = description
	}
	payload := requestWire{
		Model: c.model,
		State: state,
		Questions: map[string]questionWire{
			"route": {
				Type:         "choice",
				Instructions: "Choose the best route for this state.",
				Criteria:     cleanCriteria,
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, errors.New("decision: encode request")
	}
	if len(body) > maxRequestBytes {
		return Result{}, fmt.Errorf("decision: request exceeds %d-byte limit", maxRequestBytes)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, errors.New("decision: create request")
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, fmt.Errorf("decision: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("decision: server returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Result{}, errors.New("decision: read response")
	}
	if len(data) > maxResponseBytes {
		return Result{}, fmt.Errorf("decision: response exceeds %d-byte limit", maxResponseBytes)
	}
	var wire responseWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return Result{}, errors.New("decision: invalid JSON response")
	}
	return validateResponse(wire, cleanCriteria)
}

type requestWire struct {
	Model     string                  `json:"model"`
	State     string                  `json:"state"`
	Questions map[string]questionWire `json:"questions"`
}

type questionWire struct {
	Type         string            `json:"type"`
	Instructions string            `json:"instructions"`
	Criteria     map[string]string `json:"criteria"`
}

type responseWire struct {
	Answers map[string]answerWire `json:"answers"`
	Usage   *usageWire            `json:"usage"`
}

type answerWire struct {
	Type          string              `json:"type"`
	Choice        string              `json:"choice"`
	Probabilities map[string]*float64 `json:"probabilities"`
	Confidence    *float64            `json:"confidence"`
}

type usageWire struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func validateResponse(wire responseWire, criteria map[string]string) (Result, error) {
	answer, ok := wire.Answers["route"]
	if !ok {
		return Result{}, errors.New("decision: response omitted route answer")
	}
	if answer.Type != "choice" {
		return Result{}, errors.New("decision: route answer is not a choice")
	}
	if answer.Choice == "" {
		return Result{}, errors.New("decision: response omitted choice")
	}
	if _, ok := criteria[answer.Choice]; !ok {
		return Result{}, errors.New("decision: response selected an unknown choice")
	}
	if answer.Confidence == nil || !finiteProbability(*answer.Confidence) {
		return Result{}, errors.New("decision: confidence must be finite and between 0 and 1")
	}
	if len(answer.Probabilities) != len(criteria) {
		return Result{}, errors.New("decision: probability distribution does not match criteria")
	}
	sum := 0.0
	for choice := range criteria {
		probability, ok := answer.Probabilities[choice]
		if !ok || probability == nil || !finiteProbability(*probability) {
			return Result{}, errors.New("decision: probability distribution does not match criteria")
		}
		sum += *probability
	}
	for choice := range answer.Probabilities {
		if _, ok := criteria[choice]; !ok {
			return Result{}, errors.New("decision: probability distribution contains an unknown choice")
		}
	}
	if math.Abs(sum-1) > probabilityEpsilon {
		return Result{}, errors.New("decision: probabilities must sum to 1")
	}
	if wire.Usage == nil || wire.Usage.InputTokens < 0 || wire.Usage.OutputTokens < 0 {
		return Result{}, errors.New("decision: usage is invalid")
	}
	return Result{
		Choice:     answer.Choice,
		Confidence: *answer.Confidence,
		Usage: provider.Usage{
			InputTokens:  wire.Usage.InputTokens,
			OutputTokens: wire.Usage.OutputTokens,
			TotalTokens:  wire.Usage.InputTokens + wire.Usage.OutputTokens,
		},
	}, nil
}

func finiteProbability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func validateEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("decision: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("decision: base URL cannot contain credentials, query, or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return "", errors.New("decision: HTTPS is required except for loopback HTTP")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func isLoopback(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

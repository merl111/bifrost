package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/environment"
	"bifrost/internal/provider"
)

type decisionRequest struct {
	Questions map[string]struct {
		Criteria map[string]string `json:"criteria"`
	} `json:"questions"`
}

func decisionFixture(t *testing.T, choice string, confidence float64, bodyOverride string) (options, *httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if bodyOverride != "" {
			_, _ = io.WriteString(w, bodyOverride)
			return
		}
		var request decisionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		criteria := request.Questions["route"].Criteria
		probabilities := make(map[string]float64, len(criteria))
		for name := range criteria {
			probabilities[name] = (1 - confidence) / float64(max(1, len(criteria)-1))
		}
		probabilities[choice] = confidence
		_ = json.NewEncoder(w).Encode(map[string]any{
			"answers": map[string]any{"route": map[string]any{
				"type": "choice", "choice": choice, "confidence": confidence, "probabilities": probabilities,
			}},
			"usage": map[string]int{"input_tokens": 5, "output_tokens": 2},
		})
	}))

	root, state := t.TempDir(), t.TempDir()
	opts := options{configPath: filepath.Join(state, "config.json"), stateDir: state, root: root}
	settings := config.DefaultSettings()
	settings.Providers = map[string]config.ProviderSettings{
		"native-fast":     {Kind: "responses", Model: "fast"},
		"native-careful":  {Kind: "chat-completions", Model: "careful"},
		"external-review": {Kind: "claude", Model: "auto", Account: "review"},
	}
	settings.Decision = config.DecisionSettings{
		Enabled:       true,
		Kind:          "openrouter",
		BaseURL:       server.URL + "/decisions",
		Model:         "~typesafe/jev-latest",
		APIKeyEnv:     "BIFROST_DECISION_FIXTURE_KEY",
		MinConfidence: 0.8,
		Candidates: map[string]string{
			"native-fast":     "routine work",
			"native-careful":  "complex or risky work",
			"external-review": "vendor review work",
		},
	}
	if err := config.SaveSettings(opts.configPath, settings, true); err != nil {
		server.Close()
		t.Fatal(err)
	}
	t.Setenv("BIFROST_DECISION_FIXTURE_KEY", "fixture-only")
	return opts, server, &requests
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestRouteAgentSelectsAllowlistedNativeProfileAndPreservesPermissions(t *testing.T) {
	opts, server, requests := decisionFixture(t, "native-fast", .9, "")
	defer server.Close()
	cfg := agent.Config{
		Provider: runtimeProvider(func(context.Context, provider.Request) (provider.Response, error) {
			return provider.Response{}, errors.New("should not run")
		}),
		ProfileName:  "native-careful",
		ModelName:    "careful",
		Root:         opts.root,
		StateDir:     opts.stateDir,
		WriteEnabled: true,
		ExecEnabled:  true,
	}
	got, err := routeAgent(context.Background(), opts, cfg, nil, "ship the change")
	if err != nil {
		t.Fatal(err)
	}
	if got.ProfileName != "native-fast" || got.ModelName != "fast" || got.Provider == nil || got.RunExternal != nil {
		t.Fatalf("route = profile=%q model=%q provider=%T external=%v", got.ProfileName, got.ModelName, got.Provider, got.RunExternal != nil)
	}
	if !got.WriteEnabled || !got.ExecEnabled {
		t.Fatal("routing changed execution permissions")
	}
	if got.DecisionUsage.InputTokens != 5 || got.DecisionUsage.OutputTokens != 2 || got.DecisionUsage.TotalTokens != 7 {
		t.Fatalf("decision usage = %+v", got.DecisionUsage)
	}
	if !strings.Contains(got.DecisionNote, "native-fast") || !strings.Contains(got.DecisionNote, "7 decision tokens") {
		t.Fatalf("decision note = %q", got.DecisionNote)
	}
	if requests.Load() != 1 {
		t.Fatalf("decision requests = %d", requests.Load())
	}
}

func TestRouteAgentIncludesExternalCandidateForLocalTurn(t *testing.T) {
	opts, server, requests := decisionFixture(t, "native-careful", .9, "")
	defer server.Close()
	cfg := agent.Config{Root: opts.root, StateDir: opts.stateDir}
	if _, err := routeAgent(context.Background(), opts, cfg, nil, "review this change"); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatal("local routing did not call the fixture")
	}
}

// embeddedBackend supplies the identity routeAgent needs without making this
// test depend on a real SSH worker or any file/process capability.
type embeddedBackend struct {
	environment.Backend
	identity environment.Identity
}

func (b embeddedBackend) Identity() environment.Identity { return b.identity }

func TestRouteAgentExcludesExternalCandidateOutsideLocalBackend(t *testing.T) {
	opts, server, _ := decisionFixture(t, "external-review", .9, "")
	defer server.Close()
	cfg := agent.Config{
		Backend:     embeddedBackend{identity: environment.Identity{Kind: "ssh", ID: "fixture-worker"}},
		ProfileName: "native-careful",
		ModelName:   "careful",
	}
	got, err := routeAgent(context.Background(), opts, cfg, nil, "review this change")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.DecisionNote, "decision unavailable") {
		t.Fatalf("external candidate was sent to non-local turn: %q", got.DecisionNote)
	}
}

func TestRouteAgentFallsBackForLowConfidenceInvalidResponseAndMissingKey(t *testing.T) {
	t.Run("low confidence", func(t *testing.T) {
		opts, server, _ := decisionFixture(t, "native-fast", .7, "")
		defer server.Close()
		original := runtimeProvider(func(context.Context, provider.Request) (provider.Response, error) { return provider.Response{}, nil })
		cfg := agent.Config{Provider: original, ProfileName: "native-careful", ModelName: "careful"}
		got, err := routeAgent(context.Background(), opts, cfg, nil, "ambiguous request")
		if err != nil || got.Provider == nil || got.ProfileName != cfg.ProfileName {
			t.Fatalf("fallback = %+v, %v", got, err)
		}
		if got.DecisionUsage.TotalTokens != 7 || !strings.Contains(got.DecisionNote, "below 0.80") {
			t.Fatalf("fallback decision metadata = %+v", got)
		}
	})
	t.Run("invalid response", func(t *testing.T) {
		opts, server, _ := decisionFixture(t, "missing", .9, `{"answers":{"route":{"type":"choice","choice":"missing","confidence":0.9,"probabilities":{"missing":1}}},"usage":{"input_tokens":1,"output_tokens":1}}`)
		defer server.Close()
		cfg := agent.Config{ProfileName: "native-careful", ModelName: "careful"}
		got, err := routeAgent(context.Background(), opts, cfg, nil, "request")
		if err != nil || !strings.Contains(got.DecisionNote, "decision unavailable") {
			t.Fatalf("invalid response fallback = %+v, %v", got, err)
		}
		if strings.Contains(got.DecisionNote, "fixture-only") {
			t.Fatal("fallback leaked the decision API key")
		}
	})
	t.Run("missing key", func(t *testing.T) {
		opts, server, requests := decisionFixture(t, "native-fast", .9, "")
		defer server.Close()
		t.Setenv("BIFROST_DECISION_FIXTURE_KEY", "")
		cfg := agent.Config{ProfileName: "native-careful", ModelName: "careful"}
		got, err := routeAgent(context.Background(), opts, cfg, nil, "request")
		if err != nil || !strings.Contains(got.DecisionNote, "not configured") {
			t.Fatalf("missing key fallback = %+v, %v", got, err)
		}
		if requests.Load() != 0 {
			t.Fatal("missing key still made a network request")
		}
	})
}

func TestRouteAgentContextCancellationAndBoundedUnicodeState(t *testing.T) {
	opts, server, _ := decisionFixture(t, "native-fast", .9, "")
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := routeAgent(ctx, opts, agent.Config{ProfileName: "native-careful"}, nil, "request")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled route error = %v", err)
	}
	state := decisionState(strings.Repeat("界", 5000), []provider.Message{{Role: provider.RoleUser, Content: strings.Repeat("λ", 2000)}})
	if len([]byte(state)) > 8<<10 || !utf8.ValidString(state) {
		t.Fatalf("decision state is not bounded UTF-8: bytes=%d", len([]byte(state)))
	}
}

func TestAgentResultRetainsDecisionUsage(t *testing.T) {
	cfg := agent.Config{
		Root:     t.TempDir(),
		StateDir: t.TempDir(),
		Provider: runtimeProvider(func(context.Context, provider.Request) (provider.Response, error) {
			return provider.Response{Message: provider.Message{Content: "done"}, Usage: provider.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}}, nil
		}),
		DecisionUsage: provider.Usage{InputTokens: 5, OutputTokens: 2, TotalTokens: 7},
		MaxSteps:      1,
	}
	result, err := agent.Run(context.Background(), cfg, "request")
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 8 || result.Usage.OutputTokens != 6 || result.Usage.TotalTokens != 14 {
		t.Fatalf("result usage = %+v", result.Usage)
	}
}

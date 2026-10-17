package decision

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestChooseTypeSafeWireAndDefaults(t *testing.T) {
	criteria := map[string]string{"native": "Use the native provider", "external": "Use the external client"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer typesafe-secret" {
			t.Errorf("authorization = %q", got)
		}
		var body requestWire
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != typeSafeModel || body.State != "route this" || body.Questions["route"].Type != "choice" {
			t.Errorf("request = %#v", body)
		}
		if len(body.Questions["route"].Criteria) != len(criteria) {
			t.Errorf("criteria = %#v", body.Questions["route"].Criteria)
		}
		_, _ = io.WriteString(w, `{"answers":{"route":{"type":"choice","choice":"native","probabilities":{"native":0.75,"external":0.25},"confidence":0.8}},"usage":{"input_tokens":12,"output_tokens":4}}`)
	}))
	defer server.Close()

	client, err := New(Config{Kind: KindTypeSafe, BaseURL: server.URL + "/v1/systemone", APIKey: "typesafe-secret"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Choose(context.Background(), "route this", criteria)
	if err != nil {
		t.Fatal(err)
	}
	if got.Choice != "native" || got.Confidence != 0.8 || got.Usage.InputTokens != 12 || got.Usage.OutputTokens != 4 || got.Usage.TotalTokens != 16 {
		t.Fatalf("result = %#v", got)
	}
}

func TestChooseOpenRouterWireAndDefaultModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/alpha/decisions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		var body requestWire
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Model != openRouterModel {
			t.Errorf("model = %q", body.Model)
		}
		_, _ = io.WriteString(w, `{"answers":{"route":{"type":"choice","choice":"keep","probabilities":{"keep":1},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":0}}`)
	}))
	defer server.Close()
	client, err := New(Config{Kind: KindOpenRouter, BaseURL: server.URL + "/api/alpha/decisions", APIKey: "openrouter-secret"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := client.Choose(context.Background(), "state", map[string]string{"keep": "keep it"})
	if err != nil || got.Choice != "keep" {
		t.Fatalf("result = %#v, err = %v", got, err)
	}
}

func TestChooseRejectsInvalidDecision(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "unknown choice",
			body: `{"answers":{"route":{"type":"choice","choice":"other","probabilities":{"one":1},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "unknown choice",
		},
		{
			name: "missing probability",
			body: `{"answers":{"route":{"type":"choice","choice":"one","probabilities":{"one":0.5,"two":0.4},"confidence":0.5}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "sum to 1",
		},
		{
			name: "confidence out of range",
			body: `{"answers":{"route":{"type":"choice","choice":"one","probabilities":{"one":1},"confidence":1.1}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "confidence",
		},
		{
			name: "null probability",
			body: `{"answers":{"route":{"type":"choice","choice":"one","probabilities":{"one":null},"confidence":1}},"usage":{"input_tokens":1,"output_tokens":1}}`,
			want: "probability",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, tc.body) }))
			defer server.Close()
			client, err := New(Config{Kind: KindTypeSafe, BaseURL: server.URL + "/v1/systemone", APIKey: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Choose(context.Background(), "state", map[string]string{"one": "one", "two": "two"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestChooseErrorIsBoundedAndRedacted(t *testing.T) {
	secret := "very-secret-api-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"`+secret+`"}`)
	}))
	defer server.Close()
	client, err := New(Config{Kind: KindTypeSafe, BaseURL: server.URL + "/v1/systemone", APIKey: secret})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Choose(context.Background(), "state", map[string]string{"one": "one"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") || strings.Contains(err.Error(), secret) {
		t.Fatalf("err = %v", err)
	}
}

func TestChooseDoesNotFollowRedirects(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	client, err := New(Config{Kind: KindTypeSafe, BaseURL: redirect.URL, APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Choose(context.Background(), "state", map[string]string{"one": "one"})
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("err = %v", err)
	}
	if targetHits != 0 {
		t.Fatalf("redirect target received %d requests", targetHits)
	}
}

func TestChooseCancellation(t *testing.T) {
	client, err := New(Config{Kind: KindTypeSafe, BaseURL: "https://decision.example/v1/systemone", APIKey: "secret", Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	client.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.Choose(ctx, "state", map[string]string{"one": "one"})
		errCh <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not stop after context cancellation")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewRejectsUnsafeEndpointsAndKeys(t *testing.T) {
	for _, cfg := range []Config{
		{Kind: KindTypeSafe, BaseURL: "http://example.com", APIKey: "x"},
		{Kind: KindTypeSafe, BaseURL: "https://example.com?key=x", APIKey: "x"},
		{Kind: KindTypeSafe, BaseURL: "https://example.com", APIKey: "bad\nkey"},
	} {
		if _, err := New(cfg); err == nil {
			t.Fatalf("accepted unsafe config %#v", cfg)
		}
	}
}

func TestChooseBoundsState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("request should not be sent") }))
	defer server.Close()
	client, err := New(Config{Kind: KindTypeSafe, BaseURL: server.URL + "/v1/systemone", APIKey: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Choose(context.Background(), strings.Repeat("x", maxStateBytes+1), map[string]string{"one": "one"}); err == nil {
		t.Fatal("accepted oversized state")
	}
}

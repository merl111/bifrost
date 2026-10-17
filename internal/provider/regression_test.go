package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCompletionNeverFollowsRedirect(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Location", "/other")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	client, err := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("request replayed %d times", calls.Load())
	}
}

func TestIncompleteResponseIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"partial"},"finish_reason":"length"}]}`))
	}))
	defer srv.Close()
	client, err := NewOpenAI(OpenAIConfig{BaseURL: srv.URL, Model: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.Complete(context.Background(), Request{}); err == nil {
		t.Fatal("truncated completion reported success")
	}
}

func TestCredentialURLRejected(t *testing.T) {
	for _, u := range []string{"https://user:secret@example.org", "https://example.org?key=secret", "https://example.org#token"} {
		if _, err := NewOpenAI(OpenAIConfig{BaseURL: u, Model: "fixture"}); err == nil {
			t.Errorf("accepted %s", u)
		}
	}
}

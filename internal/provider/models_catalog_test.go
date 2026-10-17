package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIStyleCatalogKeepsAdvertisedContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"anthropic/claude-haiku-4.5","name":"Anthropic: Claude Haiku 4.5","context_length":200000,"top_provider":{"context_length":1000000}},
			{"id":"small-model","context_length":8192}
		]}`))
	}))
	defer srv.Close()

	models, err := fetchOpenAIStyleModels(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, model := range models {
		got[model.Slug] = model.ContextLength
	}
	if got["anthropic/claude-haiku-4.5"] != 1_000_000 {
		t.Fatalf("haiku context = %d, want the larger advertised window", got["anthropic/claude-haiku-4.5"])
	}
	if got["small-model"] != 8192 {
		t.Fatalf("small context = %d", got["small-model"])
	}
}

func TestCatalogReadsAlternateContextFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6-luna","context_window":128000}]}`))
	}))
	defer srv.Close()
	models, err := fetchOpenAIStyleModels(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ContextLength != 128000 {
		t.Fatalf("models = %+v", models)
	}
	if ModelContextWindowTokens("chatgpt", "gpt-6-luna") != 0 {
		t.Fatal("unknown ChatGPT model received a guessed window")
	}
}

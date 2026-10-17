package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHeaderRefsValidateWithoutReadingSecrets(t *testing.T) {
	if err := ValidateHeaderRefs([]HeaderRef{{Name: "Authorization", Env: "BIFROST_TEST_SECRET"}}); err != nil {
		t.Fatal(err)
	}
	for _, refs := range [][]HeaderRef{
		{{Name: "Authorization", Env: ""}},
		{{Name: "Bad\nHeader", Env: "TOKEN"}},
		{{Name: "Authorization", Env: "TOKEN"}, {Name: "authorization", Env: "TOKEN2"}},
	} {
		if err := ValidateHeaderRefs(refs); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("refs=%+v err=%v", refs, err)
		}
	}
}

func TestHeaderRoundTripperAddsSecretOnlyInRequest(t *testing.T) {
	var got *http.Request
	transport := &headerRoundTripper{base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		got = req
		return &http.Response{StatusCode: 200, Body: http.NoBody, Request: req}, nil
	}), origin: "https://example.test", headers: http.Header{"Authorization": {"Bearer secret"}}}
	u, _ := url.Parse("https://example.test/mcp")
	if _, err := transport.RoundTrip(&http.Request{Method: "POST", URL: u, Header: make(http.Header)}); err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Header.Get("Authorization") != "Bearer secret" {
		t.Fatalf("request headers = %#v", got)
	}
	if _, err := transport.RoundTrip(&http.Request{Method: "GET", URL: mustURL("https://other.test/mcp"), Header: make(http.Header)}); err == nil {
		t.Fatal("cross-origin request accepted")
	}
}

func TestSessionResourcesPromptsAndSubscription(t *testing.T) {
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "feature", Version: "1"}, nil)
	server.AddResource(&mcp.Resource{Name: "note", URI: "file:///note", MIMEType: "text/plain"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: "file:///note", MIMEType: "text/plain", Text: "hello"}}}, nil
	})
	server.AddPrompt(&mcp.Prompt{Name: "greet", Arguments: []*mcp.PromptArgument{{Name: "name", Required: true}}}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}}, nil
	})
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	defer serverSession.Close()
	s := &Session{client: client, session: clientSession, limits: Limits{MaxPages: 4, MaxTools: 10}}
	resources, err := s.ListResources(ctx)
	if err != nil || len(resources) != 1 {
		t.Fatalf("resources=%+v err=%v", resources, err)
	}
	contents, err := s.ReadResource(ctx, "file:///note")
	if err != nil || len(contents) != 1 || contents[0].Text != "hello" {
		t.Fatalf("contents=%+v err=%v", contents, err)
	}
	prompts, err := s.ListPrompts(ctx)
	if err != nil || len(prompts) != 1 || prompts[0].Name != "greet" {
		t.Fatalf("prompts=%+v err=%v", prompts, err)
	}
	got, err := s.GetPrompt(ctx, "greet", map[string]string{"name": "Ada"})
	if err != nil || len(got.Messages) != 1 {
		t.Fatalf("prompt=%+v err=%v", got, err)
	}
	if !json.Valid(got.Messages[0]) {
		t.Fatalf("prompt message is not JSON: %s", got.Messages[0])
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
func mustURL(raw string) *url.URL                                           { u, _ := url.Parse(raw); return u }

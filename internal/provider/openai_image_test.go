package provider

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIChatCompletionsEncodesImagesAsDataURLs(t *testing.T) {
	var body string
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		data, _ := io.ReadAll(req.Body)
		body = string(data)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)), Request: req}, nil
	})}
	op := &OpenAI{endpoint: "https://example.test/v1/chat/completions", model: "fixture", client: client, maxBody: 1 << 20}
	if _, err := op.Complete(context.Background(), Request{Messages: []Message{{Role: RoleUser, Content: "look", Images: []Image{{MediaType: "image/png", Data: []byte("png")}}}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"type":"image_url"`) || !strings.Contains(body, `data:image/png;base64,cG5n`) || !strings.Contains(body, `"type":"text"`) {
		t.Fatalf("request body = %s", body)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

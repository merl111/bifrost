package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	modernVersion = "2026-07-28"
	legacyVersion = "2025-11-25"
)

func TestMain(m *testing.M) {
	if os.Getenv("BIFROST_MCP_FIXTURE") == "1" {
		server := mcp.NewServer(&mcp.Implementation{Name: "stdio-fixture", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{legacyVersion}})
		server.AddTool(&mcp.Tool{Name: "echo", Description: "echo input", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}, StructuredContent: json.RawMessage(req.Params.Arguments)}, nil
		})
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestHTTPModernPaginationAndCall(t *testing.T) {
	serverURL, closeServer := fixtureHTTP(t, modernVersion)
	defer closeServer()
	session, err := Dial(context.Background(), Endpoint{URL: serverURL, ProtocolVersion: modernVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Inspect().ProtocolVersion; got != modernVersion {
		t.Fatalf("protocol = %q", got)
	}
	tools, err := session.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 4 || tools[0].Name != "echo" || tools[1].Name != "media" || tools[2].Name != "needs-input" || tools[3].Name != "slow" {
		t.Fatalf("tools = %+v", tools)
	}
	result, err := session.Call(context.Background(), "echo", json.RawMessage(`{"message":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || len(result.Content) != 1 || !strings.Contains(string(result.Content[0]), "hello") {
		t.Fatalf("result = %+v", result)
	}
	if string(result.StructuredContent) != `{"message":"hello"}` {
		t.Fatalf("structured = %s", result.StructuredContent)
	}
	result, err = session.Call(context.Background(), "needs-input", json.RawMessage(`{}`))
	if !errors.Is(err, ErrInputRequired) || !result.NeedsInput || result.RequestState != "fixture-state" {
		t.Fatalf("input-required result = %+v, %v", result, err)
	}
}

func TestHTTPLegacyNegotiation(t *testing.T) {
	serverURL, closeServer := fixtureHTTP(t, legacyVersion)
	defer closeServer()
	session, err := Dial(context.Background(), Endpoint{URL: serverURL, ProtocolVersion: legacyVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.Inspect().ProtocolVersion; got != legacyVersion {
		t.Fatalf("protocol = %q", got)
	}
	if _, err := session.List(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestStdioFixtureAndCancellation(t *testing.T) {
	if err := os.Setenv("BIFROST_MCP_FIXTURE", "1"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Unsetenv("BIFROST_MCP_FIXTURE") })
	session, err := Dial(context.Background(), Endpoint{Command: []string{os.Args[0], "-test.run=TestStdioFixture"}}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.Call(context.Background(), "echo", json.RawMessage(`{"x":1}`))
	var wire struct {
		Text string `json:"text"`
	}
	if len(result.Content) == 0 {
		t.Fatalf("stdio call has no content: %+v", result)
	}
	if decodeErr := json.Unmarshal(result.Content[0], &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || wire.Text != `{"x":1}` {
		t.Fatalf("stdio call = %+v, %v", result, err)
	}
}

func TestEndpointValidationAndCancellation(t *testing.T) {
	for _, endpoint := range []Endpoint{{}, {URL: "http://example.test"}, {URL: "https://example.test", Command: []string{"cmd"}}, {URL: "https://user@example.test"}, {URL: "https://example.test/path#fragment"}} {
		if _, err := Dial(context.Background(), endpoint, nil); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("endpoint %v error = %v", endpoint, err)
		}
	}
	serverURL, closeServer := fixtureHTTP(t, modernVersion)
	defer closeServer()
	session, err := Dial(context.Background(), Endpoint{URL: serverURL, ProtocolVersion: modernVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	if _, err := session.Call(ctx, "slow", json.RawMessage(`{}`)); err == nil {
		t.Fatal("cancelled call unexpectedly succeeded")
	}
}

func TestCallResultInputRequiredIsPreserved(t *testing.T) {
	var response mcp.CallToolResult
	if err := json.Unmarshal([]byte(`{"resultType":"input_required","inputRequests":{"confirm":{"method":"elicitation/create","params":{"message":"Continue?"}}},"requestState":"opaque-state"}`), &response); err != nil {
		t.Fatal(err)
	}
	result, err := convertResult(&response)
	if !errors.Is(err, ErrInputRequired) {
		t.Fatalf("error = %v", err)
	}
	if !result.NeedsInput || result.RequestState != "opaque-state" || len(result.InputRequests) == 0 {
		t.Fatalf("result = %+v", result)
	}
	var requests map[string]any
	if err := json.Unmarshal(result.InputRequests, &requests); err != nil || requests["confirm"] == nil {
		t.Fatalf("input requests = %s (%v)", result.InputRequests, err)
	}
}

func TestCallRejectsNonObjectArguments(t *testing.T) {
	serverURL, closeServer := fixtureHTTP(t, modernVersion)
	defer closeServer()
	session, err := Dial(context.Background(), Endpoint{URL: serverURL, ProtocolVersion: modernVersion}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for _, args := range []json.RawMessage{json.RawMessage("null"), json.RawMessage("[]"), json.RawMessage(`"text"`)} {
		if _, err := session.Call(context.Background(), "echo", args); err == nil {
			t.Fatalf("accepted arguments %s", args)
		}
	}
}

func TestRedirectPolicyRejectsHTTPSDowngrade(t *testing.T) {
	from, _ := url.Parse("https://example.test/start")
	to, _ := url.Parse("http://example.test/end")
	if err := rejectHTTPDowngrade(&http.Request{URL: to}, []*http.Request{{URL: from}}); err == nil {
		t.Fatal("accepted HTTPS downgrade")
	}
}

func TestHTTPFailureIsReturned(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fixture unavailable", http.StatusServiceUnavailable)
	})}
	go httpServer.Serve(listener)
	defer httpServer.Close()
	if _, err := Dial(context.Background(), Endpoint{URL: "http://" + listener.Addr().String(), ProtocolVersion: legacyVersion}, nil); err == nil {
		t.Fatal("unavailable HTTP server unexpectedly connected")
	}
}

func fixtureHTTP(t *testing.T, version string) (string, func()) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "http-fixture", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{version}, PageSize: 1})
	server.AddTool(&mcp.Tool{Name: "echo", Description: "echo input", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}, StructuredContent: json.RawMessage(req.Params.Arguments)}, nil
	})
	server.AddTool(&mcp.Tool{Name: "media", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("aGVsbG8="), MIMEType: "image/png"}}}, nil
	})
	if version == modernVersion {
		server.AddTool(&mcp.Tool{Name: "needs-input", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{InputRequests: mcp.InputRequestMap{"confirm": &mcp.ElicitParams{Message: "Continue?"}}, RequestState: "fixture-state"}, nil
		})
	}
	server.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: true, Stateless: version == modernVersion})
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: handler}
	go httpServer.Serve(listener)
	return "http://" + listener.Addr().String(), func() { _ = httpServer.Close() }
}

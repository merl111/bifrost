// Package mcpclient is the small, policy-neutral MCP connector used by the
// application layer. Authentication and trust decisions belong to callers.
package mcpclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	ErrInvalidEndpoint = errors.New("invalid MCP endpoint")
	ErrPaginationLimit = errors.New("MCP pagination limit exceeded")
	ErrInputRequired   = errors.New("MCP tool requires additional input")
)

const (
	DefaultMaxPages     = 100
	DefaultMaxTools     = 10000
	DefaultMaxEventSize = 8 << 20
	maxToolMetadata     = 64 << 10
	maxToolResult       = 1 << 20
)

// Endpoint selects either Streamable HTTP (URL) or a local stdio command.
// ProtocolVersion is optional; the SDK negotiates modern and legacy versions.
type Endpoint struct {
	URL             string
	Command         []string
	ProtocolVersion string
	// Headers refers to environment variables. Values are resolved only in
	// memory while dialing; they are never persisted by the config adapter.
	Headers []HeaderRef
}

// HeaderRef describes one HTTP header whose value is supplied by an
// environment variable. Header values are deliberately not part of Endpoint.
type HeaderRef struct {
	Name string `json:"name"`
	Env  string `json:"env"`
}

// DialOptions wires the callbacks that are safe for an application to own.
// Elicitation is explicit: a nil callback rejects an elicitation request.
type DialOptions struct {
	Elicitation         func(context.Context, *mcp.ElicitRequest) (*mcp.ElicitResult, error)
	ResourceUpdated     func(context.Context, string)
	ResourceListChanged func(context.Context)
	PromptListChanged   func(context.Context)
	ToolListChanged     func(context.Context)
}

type Session struct {
	client  *mcp.Client
	session *mcp.ClientSession
	inspect ServerInfo
	limits  Limits
}

type Limits struct {
	MaxPages int
	MaxTools int
}

type ServerInfo struct {
	ProtocolVersion string                  `json:"protocol_version"`
	Name            string                  `json:"name"`
	Title           string                  `json:"title,omitempty"`
	Version         string                  `json:"version"`
	Description     string                  `json:"description,omitempty"`
	WebsiteURL      string                  `json:"website_url,omitempty"`
	Instructions    string                  `json:"instructions,omitempty"`
	Capabilities    *mcp.ServerCapabilities `json:"capabilities,omitempty"`
}

type Tool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  json.RawMessage `json:"input_schema,omitempty"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
	Annotations  json.RawMessage `json:"annotations,omitempty"`
}

type Resource struct {
	URI, Name, Title, Description, MIMEType string
	Size                                    int64
}

type ResourceTemplate struct {
	URITemplate, Name, Title, Description, MIMEType string
}

type PromptArgument struct {
	Name, Title, Description string
	Required                 bool
}

type Prompt struct {
	Name, Title, Description string
	Arguments                []PromptArgument
}

type ResourceContent struct {
	URI, MIMEType, Text string
	Blob                []byte
}

type PromptResult struct {
	Description   string
	Messages      []json.RawMessage
	InputRequests json.RawMessage
	RequestState  string
}

// Result preserves every content block as wire JSON, including image, audio,
// resource-link, and embedded-resource blocks, so callers can serialize it
// without depending on SDK concrete content types.
type Result struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structured_content,omitempty"`
	IsError           bool              `json:"is_error,omitempty"`
	Error             string            `json:"error,omitempty"`
	NeedsInput        bool              `json:"needs_input,omitempty"`
	InputRequests     json.RawMessage   `json:"input_requests,omitempty"`
	RequestState      string            `json:"request_state,omitempty"`
}

func Dial(ctx context.Context, endpoint Endpoint, stderr io.Writer) (*Session, error) {
	return DialWithOptions(ctx, endpoint, stderr, DialOptions{})
}

// DialWithOptions connects using environment-referenced headers and optional
// protocol callbacks. OAuth is intentionally left to the SDK's OAuthHandler;
// callers that need it should use a separately constructed transport with an
// approved credential store rather than putting tokens in config.
func DialWithOptions(ctx context.Context, endpoint Endpoint, stderr io.Writer, dialOpts DialOptions) (*Session, error) {
	transport, err := makeTransport(endpoint, stderr)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "bifrost", Version: "0.1.0"}, &mcp.ClientOptions{
		MultiRoundTrip:     &mcp.MultiRoundTripOptions{Disabled: true},
		ElicitationHandler: dialOpts.Elicitation,
		ToolListChangedHandler: func(ctx context.Context, _ *mcp.ToolListChangedRequest) {
			if dialOpts.ToolListChanged != nil {
				dialOpts.ToolListChanged(ctx)
			}
		},
		PromptListChangedHandler: func(ctx context.Context, _ *mcp.PromptListChangedRequest) {
			if dialOpts.PromptListChanged != nil {
				dialOpts.PromptListChanged(ctx)
			}
		},
		ResourceListChangedHandler: func(ctx context.Context, _ *mcp.ResourceListChangedRequest) {
			if dialOpts.ResourceListChanged != nil {
				dialOpts.ResourceListChanged(ctx)
			}
		},
		ResourceUpdatedHandler: func(ctx context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			if dialOpts.ResourceUpdated != nil && req != nil && req.Params != nil {
				dialOpts.ResourceUpdated(ctx, req.Params.URI)
			}
		},
	})
	var opts *mcp.ClientSessionOptions
	if endpoint.ProtocolVersion != "" {
		opts = &mcp.ClientSessionOptions{ProtocolVersion: endpoint.ProtocolVersion}
	}
	cs, err := client.Connect(ctx, transport, opts)
	if err != nil {
		return nil, fmt.Errorf("connect MCP server: %w", err)
	}
	result := cs.InitializeResult()
	if result == nil {
		_ = cs.Close()
		return nil, errors.New("MCP server returned no initialization result")
	}
	info := ServerInfo{ProtocolVersion: result.ProtocolVersion, Instructions: result.Instructions, Capabilities: result.Capabilities}
	if result.ServerInfo != nil {
		info.Name, info.Title, info.Version, info.Description, info.WebsiteURL = result.ServerInfo.Name, result.ServerInfo.Title, result.ServerInfo.Version, result.ServerInfo.Description, result.ServerInfo.WebsiteURL
	}
	return &Session{client: client, session: cs, inspect: info, limits: Limits{MaxPages: DefaultMaxPages, MaxTools: DefaultMaxTools}}, nil
}

func (s *Session) Close() error {
	if s == nil || s.session == nil {
		return nil
	}
	return s.session.Close()
}

func (s *Session) Inspect() ServerInfo {
	if s == nil {
		return ServerInfo{}
	}
	return s.inspect
}

func (s *Session) List(ctx context.Context) ([]Tool, error) {
	if s == nil || s.session == nil {
		return nil, errors.New("MCP session is nil")
	}
	maxPages, maxTools := s.limits.MaxPages, s.limits.MaxTools
	if maxPages <= 0 {
		maxPages = DefaultMaxPages
	}
	if maxTools <= 0 {
		maxTools = DefaultMaxTools
	}
	var out []Tool
	cursor := ""
	seen := map[string]struct{}{}
	for page := 0; page < maxPages; page++ {
		params := (*mcp.ListToolsParams)(nil)
		if cursor != "" {
			params = &mcp.ListToolsParams{Cursor: cursor}
		}
		result, err := s.session.ListTools(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		for _, tool := range result.Tools {
			if len(out) >= maxTools {
				return nil, fmt.Errorf("%w: tools exceed %d", ErrPaginationLimit, maxTools)
			}
			converted, err := convertTool(tool)
			if err != nil {
				return nil, err
			}
			out = append(out, converted)
		}
		if result.NextCursor == "" {
			return out, nil
		}
		if _, ok := seen[result.NextCursor]; ok {
			return nil, fmt.Errorf("%w: cursor repeated", ErrPaginationLimit)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("%w: pages exceed %d", ErrPaginationLimit, maxPages)
}

func (s *Session) Call(ctx context.Context, name string, args json.RawMessage) (Result, error) {
	if s == nil || s.session == nil {
		return Result{}, errors.New("MCP session is nil")
	}
	if strings.TrimSpace(name) == "" {
		return Result{}, errors.New("MCP tool name is empty")
	}
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	if !json.Valid(args) {
		return Result{}, errors.New("MCP tool arguments are not valid JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(args, &object); err != nil || object == nil {
		return Result{}, errors.New("MCP tool arguments must be a JSON object")
	}
	response, err := s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return Result{Error: err.Error()}, fmt.Errorf("call MCP tool %q: %w", name, err)
	}
	return convertResult(response)
}

func (s *Session) ListResources(ctx context.Context) ([]Resource, error) {
	if s == nil || s.session == nil {
		return nil, errors.New("MCP session is nil")
	}
	var out []Resource
	seen := map[string]struct{}{}
	cursor := ""
	for page := 0; page < s.pageLimit(); page++ {
		var params *mcp.ListResourcesParams
		if cursor != "" {
			params = &mcp.ListResourcesParams{Cursor: cursor}
		}
		result, err := s.session.ListResources(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list MCP resources: %w", err)
		}
		if result == nil {
			return nil, errors.New("MCP server returned a null resources result")
		}
		for _, resource := range result.Resources {
			if resource == nil {
				return nil, errors.New("MCP server returned a null resource")
			}
			if len(resource.URI)+len(resource.Name)+len(resource.Title)+len(resource.Description)+len(resource.MIMEType) > maxToolMetadata {
				return nil, fmt.Errorf("MCP resource metadata exceeds %d bytes", maxToolMetadata)
			}
			if len(out) >= s.itemLimit() {
				return nil, fmt.Errorf("%w: resources exceed %d", ErrPaginationLimit, s.itemLimit())
			}
			out = append(out, Resource{URI: resource.URI, Name: resource.Name, Title: resource.Title, Description: resource.Description, MIMEType: resource.MIMEType, Size: resource.Size})
		}
		if result.NextCursor == "" {
			return out, nil
		}
		if _, ok := seen[result.NextCursor]; ok {
			return nil, fmt.Errorf("%w: cursor repeated", ErrPaginationLimit)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("%w: pages exceed %d", ErrPaginationLimit, s.pageLimit())
}

func (s *Session) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	if s == nil || s.session == nil {
		return nil, errors.New("MCP session is nil")
	}
	var out []ResourceTemplate
	seen := map[string]struct{}{}
	cursor := ""
	for page := 0; page < s.pageLimit(); page++ {
		var params *mcp.ListResourceTemplatesParams
		if cursor != "" {
			params = &mcp.ListResourceTemplatesParams{Cursor: cursor}
		}
		result, err := s.session.ListResourceTemplates(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list MCP resource templates: %w", err)
		}
		if result == nil {
			return nil, errors.New("MCP server returned a null resource templates result")
		}
		for _, item := range result.ResourceTemplates {
			if item == nil {
				return nil, errors.New("MCP server returned a null resource template")
			}
			if len(item.URITemplate)+len(item.Name)+len(item.Title)+len(item.Description)+len(item.MIMEType) > maxToolMetadata {
				return nil, fmt.Errorf("MCP resource template metadata exceeds %d bytes", maxToolMetadata)
			}
			if len(out) >= s.itemLimit() {
				return nil, fmt.Errorf("%w: resource templates exceed %d", ErrPaginationLimit, s.itemLimit())
			}
			out = append(out, ResourceTemplate{URITemplate: item.URITemplate, Name: item.Name, Title: item.Title, Description: item.Description, MIMEType: item.MIMEType})
		}
		if result.NextCursor == "" {
			return out, nil
		}
		if _, ok := seen[result.NextCursor]; ok {
			return nil, fmt.Errorf("%w: cursor repeated", ErrPaginationLimit)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("%w: pages exceed %d", ErrPaginationLimit, s.pageLimit())
}

func (s *Session) ListPrompts(ctx context.Context) ([]Prompt, error) {
	if s == nil || s.session == nil {
		return nil, errors.New("MCP session is nil")
	}
	var out []Prompt
	seen := map[string]struct{}{}
	cursor := ""
	for page := 0; page < s.pageLimit(); page++ {
		var params *mcp.ListPromptsParams
		if cursor != "" {
			params = &mcp.ListPromptsParams{Cursor: cursor}
		}
		result, err := s.session.ListPrompts(ctx, params)
		if err != nil {
			return nil, fmt.Errorf("list MCP prompts: %w", err)
		}
		if result == nil {
			return nil, errors.New("MCP server returned a null prompts result")
		}
		for _, item := range result.Prompts {
			if item == nil {
				return nil, errors.New("MCP server returned a null prompt")
			}
			if len(item.Name)+len(item.Title)+len(item.Description) > maxToolMetadata {
				return nil, fmt.Errorf("MCP prompt metadata exceeds %d bytes", maxToolMetadata)
			}
			if len(out) >= s.itemLimit() {
				return nil, fmt.Errorf("%w: prompts exceed %d", ErrPaginationLimit, s.itemLimit())
			}
			prompt := Prompt{Name: item.Name, Title: item.Title, Description: item.Description}
			for _, arg := range item.Arguments {
				if arg == nil {
					return nil, errors.New("MCP server returned a null prompt argument")
				}
				prompt.Arguments = append(prompt.Arguments, PromptArgument{Name: arg.Name, Title: arg.Title, Description: arg.Description, Required: arg.Required})
			}
			out = append(out, prompt)
		}
		if result.NextCursor == "" {
			return out, nil
		}
		if _, ok := seen[result.NextCursor]; ok {
			return nil, fmt.Errorf("%w: cursor repeated", ErrPaginationLimit)
		}
		seen[result.NextCursor] = struct{}{}
		cursor = result.NextCursor
	}
	return nil, fmt.Errorf("%w: pages exceed %d", ErrPaginationLimit, s.pageLimit())
}

func (s *Session) ReadResource(ctx context.Context, uri string) ([]ResourceContent, error) {
	if s == nil || s.session == nil {
		return nil, errors.New("MCP session is nil")
	}
	if strings.TrimSpace(uri) == "" || len(uri) > maxToolMetadata {
		return nil, errors.New("MCP resource URI is invalid")
	}
	result, err := s.session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("read MCP resource %q: %w", uri, err)
	}
	if result == nil {
		return nil, errors.New("MCP server returned a null resource result")
	}
	var out []ResourceContent
	total := 0
	for _, content := range result.Contents {
		if content == nil {
			return nil, errors.New("MCP server returned a null resource content")
		}
		blob := append([]byte(nil), content.Blob...)
		text := content.Text
		total += len(blob) + len(text) + len(content.URI) + len(content.MIMEType)
		if total > maxToolResult {
			return nil, fmt.Errorf("MCP resource content exceeds %d bytes", maxToolResult)
		}
		out = append(out, ResourceContent{URI: content.URI, MIMEType: content.MIMEType, Text: text, Blob: blob})
	}
	return out, nil
}

func (s *Session) GetPrompt(ctx context.Context, name string, args map[string]string) (PromptResult, error) {
	if s == nil || s.session == nil {
		return PromptResult{}, errors.New("MCP session is nil")
	}
	if strings.TrimSpace(name) == "" || len(name) > maxToolMetadata {
		return PromptResult{}, errors.New("MCP prompt name is invalid")
	}
	result, err := s.session.GetPrompt(ctx, &mcp.GetPromptParams{Name: name, Arguments: args})
	if err != nil {
		return PromptResult{}, fmt.Errorf("get MCP prompt %q: %w", name, err)
	}
	if result == nil {
		return PromptResult{}, errors.New("MCP server returned a null prompt result")
	}
	out := PromptResult{Description: result.Description, RequestState: result.RequestState}
	out.InputRequests, err = marshalOptional(result.InputRequests)
	if err != nil {
		return PromptResult{}, fmt.Errorf("marshal MCP prompt input requests: %w", err)
	}
	for _, message := range result.Messages {
		if message == nil {
			return PromptResult{}, errors.New("MCP server returned a null prompt message")
		}
		data, err := json.Marshal(message)
		if err != nil {
			return PromptResult{}, fmt.Errorf("marshal MCP prompt message: %w", err)
		}
		if len(data) > maxToolResult {
			return PromptResult{}, fmt.Errorf("MCP prompt message exceeds %d bytes", maxToolResult)
		}
		out.Messages = append(out.Messages, json.RawMessage(data))
	}
	data, _ := json.Marshal(out)
	if len(data) > maxToolResult {
		return PromptResult{}, fmt.Errorf("MCP prompt result exceeds %d bytes", maxToolResult)
	}
	return out, nil
}

func (s *Session) Subscribe(ctx context.Context, uri string) error {
	if s == nil || s.session == nil {
		return errors.New("MCP session is nil")
	}
	if strings.TrimSpace(uri) == "" {
		return errors.New("MCP resource URI is empty")
	}
	return s.session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri})
}

func (s *Session) Unsubscribe(ctx context.Context, uri string) error {
	if s == nil || s.session == nil {
		return errors.New("MCP session is nil")
	}
	if strings.TrimSpace(uri) == "" {
		return errors.New("MCP resource URI is empty")
	}
	return s.session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri})
}

func (s *Session) pageLimit() int {
	if s.limits.MaxPages > 0 {
		return s.limits.MaxPages
	}
	return DefaultMaxPages
}
func (s *Session) itemLimit() int {
	if s.limits.MaxTools > 0 {
		return s.limits.MaxTools
	}
	return DefaultMaxTools
}

func makeTransport(endpoint Endpoint, stderr io.Writer) (mcp.Transport, error) {
	if endpoint.URL != "" && len(endpoint.Command) > 0 {
		return nil, fmt.Errorf("%w: URL and Command are mutually exclusive", ErrInvalidEndpoint)
	}
	if endpoint.URL == "" && len(endpoint.Command) == 0 {
		return nil, fmt.Errorf("%w: URL or Command is required", ErrInvalidEndpoint)
	}
	if len(endpoint.Command) > 0 && len(endpoint.Headers) > 0 {
		return nil, fmt.Errorf("%w: HTTP headers require a URL endpoint", ErrInvalidEndpoint)
	}
	if endpoint.URL != "" {
		u, err := url.Parse(endpoint.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("%w: URL %q", ErrInvalidEndpoint, endpoint.URL)
		}
		if u.User != nil || u.Fragment != "" {
			return nil, fmt.Errorf("%w: URL userinfo and fragments are not allowed", ErrInvalidEndpoint)
		}
		if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
			return nil, fmt.Errorf("%w: MCP HTTP requires HTTPS except loopback HTTP", ErrInvalidEndpoint)
		}
		headers, err := resolveHeaders(endpoint.Headers)
		if err != nil {
			return nil, err
		}
		baseTransport := http.DefaultTransport
		client := &http.Client{
			Transport:     &headerRoundTripper{base: baseTransport, origin: originOf(u), headers: headers},
			CheckRedirect: rejectHTTPRedirect,
		}
		return &mcp.StreamableClientTransport{
			Endpoint:   endpoint.URL,
			HTTPClient: client,
			MaxRetries: -1, DisableStandaloneSSE: true, MaxEventSize: DefaultMaxEventSize,
		}, nil
	}
	if strings.TrimSpace(endpoint.Command[0]) == "" {
		return nil, fmt.Errorf("%w: command is empty", ErrInvalidEndpoint)
	}
	cmd := exec.Command(endpoint.Command[0], endpoint.Command[1:]...)
	if stderr != nil {
		cmd.Stderr = stderr
	}
	return &mcp.CommandTransport{Command: cmd}, nil
}

var envNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func resolveHeaders(refs []HeaderRef) (http.Header, error) {
	headers := make(http.Header, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		name, env := strings.TrimSpace(ref.Name), strings.TrimSpace(ref.Env)
		if err := validateHeaderRef(ref, seen); err != nil {
			return nil, err
		}
		canonical := http.CanonicalHeaderKey(name)
		value, ok := os.LookupEnv(env)
		if !ok || value == "" || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("%w: header environment %q is unset or invalid", ErrInvalidEndpoint, env)
		}
		headers.Set(canonical, value)
	}
	return headers, nil
}

// ValidateHeaderRefs checks the persisted shape without reading any secret
// environment values. Dial performs the additional presence/value check.
func ValidateHeaderRefs(refs []HeaderRef) error {
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if err := validateHeaderRef(ref, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateHeaderRef(ref HeaderRef, seen map[string]struct{}) error {
	name, env := strings.TrimSpace(ref.Name), strings.TrimSpace(ref.Env)
	if name == "" || strings.ContainsAny(name, "\r\n:") || http.CanonicalHeaderKey(name) == "" {
		return fmt.Errorf("%w: invalid HTTP header name", ErrInvalidEndpoint)
	}
	if !envNameRE.MatchString(env) {
		return fmt.Errorf("%w: invalid header environment name %q", ErrInvalidEndpoint, env)
	}
	canonical := http.CanonicalHeaderKey(name)
	if _, ok := seen[canonical]; ok {
		return fmt.Errorf("%w: duplicate HTTP header %q", ErrInvalidEndpoint, name)
	}
	seen[canonical] = struct{}{}
	return nil
}

type headerRoundTripper struct {
	base    http.RoundTripper
	origin  string
	headers http.Header
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil || originOf(req.URL) != t.origin {
		return nil, errors.New("MCP HTTP request left its configured origin")
	}
	copyReq := req.Clone(req.Context())
	copyReq.Header = req.Header.Clone()
	for name, values := range t.headers {
		for _, value := range values {
			copyReq.Header.Set(name, value)
		}
	}
	return t.base.RoundTrip(copyReq)
}

func originOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

func rejectHTTPRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if err := rejectHTTPDowngrade(req, via); err != nil {
		return err
	}
	if originOf(req.URL) != originOf(via[0].URL) {
		return errors.New("MCP HTTP redirect changed origin")
	}
	return nil
}

func rejectHTTPDowngrade(req *http.Request, via []*http.Request) error {
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("MCP HTTP redirect would downgrade HTTPS to HTTP")
	}
	return nil
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

func convertTool(tool *mcp.Tool) (Tool, error) {
	if tool == nil {
		return Tool{}, errors.New("MCP server returned a null tool")
	}
	if len(tool.Name) > maxToolMetadata || len(tool.Description) > maxToolMetadata {
		return Tool{}, fmt.Errorf("MCP tool metadata exceeds %d bytes", maxToolMetadata)
	}
	input, err := json.Marshal(tool.InputSchema)
	if err != nil {
		return Tool{}, fmt.Errorf("marshal tool %q input schema: %w", tool.Name, err)
	}
	if len(input) > maxToolMetadata {
		return Tool{}, fmt.Errorf("MCP tool %q input schema exceeds %d bytes", tool.Name, maxToolMetadata)
	}
	output, err := marshalOptional(tool.OutputSchema)
	if err != nil {
		return Tool{}, fmt.Errorf("marshal tool %q output schema: %w", tool.Name, err)
	}
	if len(output) > maxToolMetadata {
		return Tool{}, fmt.Errorf("MCP tool %q output schema exceeds %d bytes", tool.Name, maxToolMetadata)
	}
	annotations, err := marshalOptional(tool.Annotations)
	if err != nil {
		return Tool{}, fmt.Errorf("marshal tool %q annotations: %w", tool.Name, err)
	}
	if len(annotations) > maxToolMetadata {
		return Tool{}, fmt.Errorf("MCP tool %q annotations exceed %d bytes", tool.Name, maxToolMetadata)
	}
	return Tool{Name: tool.Name, Description: tool.Description, InputSchema: input, OutputSchema: output, Annotations: annotations}, nil
}

func convertResult(response *mcp.CallToolResult) (Result, error) {
	if response == nil {
		return Result{}, errors.New("MCP server returned a null tool result")
	}
	inputRequests, err := marshalOptional(response.InputRequests)
	if err != nil {
		return Result{}, fmt.Errorf("marshal MCP input requests: %w", err)
	}
	structured, err := marshalOptional(response.StructuredContent)
	if err != nil {
		return Result{}, fmt.Errorf("marshal MCP structured content: %w", err)
	}
	remaining := maxToolResult - len(inputRequests) - len(structured)
	if remaining < 0 {
		return Result{}, fmt.Errorf("MCP tool result exceeds %d bytes", maxToolResult)
	}
	content, err := marshalContent(response.Content, remaining)
	if err != nil {
		return Result{}, err
	}
	out := Result{IsError: response.IsError, NeedsInput: response.NeedsInput(), InputRequests: inputRequests, RequestState: response.RequestState, Content: content, StructuredContent: structured}
	if out.NeedsInput {
		out.Error = ErrInputRequired.Error()
	}
	if data, err := json.Marshal(out); err != nil {
		return Result{}, fmt.Errorf("marshal MCP result: %w", err)
	} else if len(data) > maxToolResult {
		return Result{}, fmt.Errorf("MCP tool result exceeds %d bytes", maxToolResult)
	}
	if out.NeedsInput {
		return out, ErrInputRequired
	}
	return out, nil
}

func marshalContent(contents []mcp.Content, budget int) ([]json.RawMessage, error) {
	var out []json.RawMessage
	for _, content := range contents {
		if content == nil {
			return nil, errors.New("MCP server returned null content")
		}
		data, err := content.MarshalJSON()
		if err != nil {
			return nil, fmt.Errorf("marshal MCP content: %w", err)
		}
		budget -= len(data)
		if budget < 0 {
			return nil, fmt.Errorf("MCP tool result exceeds %d bytes", maxToolResult)
		}
		out = append(out, json.RawMessage(data))
	}
	return out, nil
}

func marshalOptional(value any) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	return data, err
}

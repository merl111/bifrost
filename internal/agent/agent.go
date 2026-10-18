// Package agent contains the bounded, headless model/tool loop. It is
// streaming when the provider supports it; callers own session persistence.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"time"

	"bifrost/internal/environment"
	"bifrost/internal/provider"
	"bifrost/internal/workspace"
)

var (
	ErrInvalidConfig = errors.New("agent: invalid configuration")
	ErrBudget        = errors.New("agent: context or tool-call budget exceeded")
	ErrMaxSteps      = errors.New("agent: maximum steps exceeded")
)

type Event struct {
	Kind string `json:"kind,omitempty"`
	Text string `json:"text,omitempty"`
	// ID is stable across the started/finished events for one tool call.
	ID string `json:"id,omitempty"`
	// Detail is bounded provider/tool detail intended for an expandable UI.
	Detail   string          `json:"detail,omitempty"`
	Usage    *provider.Usage `json:"usage,omitempty"`
	Step     int             `json:"step,omitempty"`
	Provider string          `json:"provider,omitempty"`
	Profile  string          `json:"profile,omitempty"`
	Model    string          `json:"model,omitempty"`
}
type Tool struct {
	Definition provider.ToolDefinition
	Call       func(context.Context, json.RawMessage) (string, error)
	Images     func() []provider.Image
}

type Config struct {
	History         []provider.Message
	PortableHistory bool
	DecisionUsage   provider.Usage
	DecisionNote    string
	Instructions    string
	ExtraTools      []Tool
	// OnEvent is called synchronously. Callers should return promptly and
	// cooperate with cancellation; Run cannot interrupt a blocked callback.
	OnEvent func(Event)
	RunHook func(context.Context, string, any) error
	// RunExternal delegates a whole turn to a vendor's installed agent. It
	// receives this turn's root, permissions and history, including child roots.
	RunExternal func(context.Context, Config, string) (Result, error)
	// ExternalToolBridge allows an external protocol adapter to call ExtraTools.
	ExternalToolBridge bool

	Backend          environment.Backend
	Provider         provider.Client
	ModelName        string
	ProfileName      string
	Root             string
	StateDir         string
	WriteEnabled     bool
	ExecEnabled      bool
	DisableStreaming bool
	MaxSteps         int
	MaxOutputBytes   int
}

type Result struct {
	Messages  []provider.Message `json:"-"`
	Usage     provider.Usage
	Text      string
	Steps     int
	ToolCalls int
}

const (
	toolRead  = "read_file"
	toolList  = "list_dir"
	toolEdit  = "edit_file"
	toolWrite = "write_file"
	toolExec  = "exec"

	maxAgentOutputBytes    = 8 << 20
	maxToolErrorBytes      = 4 << 10
	maxToolDefinitionBytes = 64 << 10
	maxToolCatalogBytes    = 1 << 20
)

func Run(ctx context.Context, cfg Config, prompt string) (result Result, err error) {
	cfg, err = normalizeConfig(cfg, prompt)
	if err != nil {
		return Result{}, err
	}
	if cfg.PortableHistory {
		cfg.History = provider.PortableHistory(cfg.History)
	}
	if cfg.DecisionNote != "" && cfg.OnEvent != nil {
		cfg.OnEvent(Event{Kind: "decision", Text: cfg.DecisionNote, Usage: &cfg.DecisionUsage})
	}
	if cfg.RunHook != nil {
		if err = cfg.RunHook(ctx, "before-turn", map[string]any{"root": cfg.Root, "prompt": prompt}); err != nil {
			return result, err
		}
		defer func() {
			hookErr := cfg.RunHook(ctx, "after-turn", map[string]any{"root": cfg.Root, "steps": result.Steps, "tool_calls": result.ToolCalls})
			if err == nil {
				err = hookErr
			}
		}()
	}
	if cfg.RunExternal != nil {
		return cfg.RunExternal(ctx, cfg, prompt)
	}
	defs, err := toolCatalog(cfg)
	if err != nil {
		return Result{}, err
	}
	w := cfg.Backend
	if w == nil {
		local, openErr := environment.OpenLocal(environment.LocalConfig{Root: cfg.Root, StateDir: cfg.StateDir})
		if openErr != nil {
			return Result{}, fmt.Errorf("agent: open workspace: %w", openErr)
		}
		w = local
		defer w.Close()
	}
	msgs := []provider.Message{{Role: provider.RoleSystem, Content: systemPrompt(cfg.WriteEnabled, cfg.ExecEnabled) + "\n" + cfg.Instructions}}
	msgs = append(msgs, cfg.History...)
	msgs = append(msgs, provider.Message{Role: provider.RoleUser, Content: prompt})
	result = Result{Usage: cfg.DecisionUsage}
	contextBytes := 0
	for _, m := range msgs {
		contextBytes += messageSize(m)
	}
	for step := 1; step <= cfg.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if contextBytes > 16<<20 {
			return result, ErrBudget
		}
		resp, streamed, err := complete(ctx, cfg, provider.Request{Messages: msgs, Tools: defs})
		result.Steps = step
		if err != nil {
			return result, fmt.Errorf("agent: model completion: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := validateResponse(resp, result.ToolCalls); err != nil {
			return result, err
		}
		result.Usage.InputTokens += resp.Usage.InputTokens
		result.Usage.OutputTokens += resp.Usage.OutputTokens
		result.Usage.TotalTokens += resp.Usage.TotalTokens
		if cfg.OnEvent != nil {
			usage := result.Usage
			cfg.OnEvent(Event{Kind: "usage", Usage: &usage, Step: step})
			if summary := BoundedEventDetail(resp.ThinkingSummary); summary != "" {
				cfg.OnEvent(Event{Kind: "thinking", ID: fmt.Sprintf("thinking-%d", step), Text: "Thinking summary", Detail: summary, Step: step})
			}
		}
		assistant := resp.Message
		contextBytes += messageSize(assistant)
		if contextBytes > 16<<20 {
			return result, ErrBudget
		}
		assistant.Role = provider.RoleAssistant
		msgs = append(msgs, assistant)
		if len(assistant.ToolCalls) == 0 {
			if len(assistant.Content) > cfg.MaxOutputBytes {
				return result, fmt.Errorf("agent: final response exceeds %d bytes", cfg.MaxOutputBytes)
			}
			result.Text = assistant.Content
			result.Messages = msgs[1:]
			if !streamed {
				emit(cfg, "delta", assistant.Content)
			}
			return result, nil
		}
		batch, batchErr := (toolExecutor{cfg: cfg, backend: w}).executeBatch(ctx, assistant.ToolCalls, &result)
		if batchErr != nil {
			return result, batchErr
		}
		for _, message := range batch {
			contextBytes += messageSize(message)
		}
		if contextBytes > 16<<20 {
			return result, ErrBudget
		}
		msgs = append(msgs, batch...)
	}
	return result, ErrMaxSteps
}

func normalizeConfig(cfg Config, prompt string) (Config, error) {
	if (cfg.Provider == nil && cfg.RunExternal == nil) || strings.TrimSpace(cfg.Root) == "" || strings.TrimSpace(cfg.StateDir) == "" {
		return cfg, ErrInvalidConfig
	}
	if strings.TrimSpace(prompt) == "" {
		return cfg, fmt.Errorf("%w: prompt is required", ErrInvalidConfig)
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return cfg, fmt.Errorf("%w: root: %v", ErrInvalidConfig, err)
	}
	cfg.Root = root
	if cfg.MaxSteps > 100 {
		return cfg, fmt.Errorf("%w: max steps must be <= 100", ErrInvalidConfig)
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 1 << 20
	}
	if cfg.MaxOutputBytes < 2 || cfg.MaxOutputBytes > maxAgentOutputBytes {
		return cfg, fmt.Errorf("%w: max output bytes must be between 2 and %d", ErrInvalidConfig, maxAgentOutputBytes)
	}
	return cfg, nil
}

func toolCatalog(cfg Config) ([]provider.ToolDefinition, error) {
	defs := definitions(cfg.WriteEnabled, cfg.ExecEnabled)
	definitionBytes := 0
	for _, definition := range defs {
		if err := validateToolDefinition(definition); err != nil {
			return nil, err
		}
		definitionBytes += toolDefinitionBytes(definition)
	}
	if definitionBytes > maxToolCatalogBytes {
		return nil, fmt.Errorf("%w: tool definitions exceed %d bytes", ErrBudget, maxToolCatalogBytes)
	}
	if len(cfg.ExtraTools) > 100 {
		return nil, ErrBudget
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
	}
	for _, t := range cfg.ExtraTools {
		if t.Definition.Name == "" || names[t.Definition.Name] || t.Call == nil {
			return nil, fmt.Errorf("%w: duplicate or invalid extra tool", ErrInvalidConfig)
		}
		if err := validateToolDefinition(t.Definition); err != nil {
			return nil, err
		}
		names[t.Definition.Name] = true
		defs = append(defs, t.Definition)
		definitionBytes += toolDefinitionBytes(t.Definition)
		if definitionBytes > maxToolCatalogBytes {
			return nil, fmt.Errorf("%w: tool definitions exceed %d bytes", ErrBudget, maxToolCatalogBytes)
		}
	}
	return defs, nil
}

func toolDefinitionBytes(definition provider.ToolDefinition) int {
	return len(definition.Name) + len(definition.Description) + len(definition.Parameters)
}

func validateToolDefinition(definition provider.ToolDefinition) error {
	if definition.Name == "" || toolDefinitionBytes(definition) > maxToolDefinitionBytes {
		return fmt.Errorf("%w: tool definition %q exceeds %d bytes", ErrInvalidConfig, definition.Name, maxToolDefinitionBytes)
	}
	if len(definition.Parameters) > maxToolDefinitionBytes {
		return fmt.Errorf("%w: tool schema %q exceeds %d bytes", ErrInvalidConfig, definition.Name, maxToolDefinitionBytes)
	}
	return nil
}

func emit(cfg Config, kind, text string) {
	if cfg.OnEvent != nil {
		cfg.OnEvent(Event{Kind: kind, Text: text})
	}
}

func complete(ctx context.Context, cfg Config, req provider.Request) (provider.Response, bool, error) {
	const maxTransientRetries = 2
	for attempt := 0; ; attempt++ {
		if streamer, ok := cfg.Provider.(provider.Streamer); ok && cfg.OnEvent != nil && !cfg.DisableStreaming {
			streamedDelta := false
			resp, err := streamer.Stream(ctx, req, func(text string) error {
				if text != "" {
					streamedDelta = true
				}
				emit(cfg, "delta", text)
				return ctx.Err()
			})
			if err == nil || streamedDelta || attempt >= maxTransientRetries || !isTransientProviderError(ctx, err) {
				return resp, true, err
			}
		} else {
			resp, err := cfg.Provider.Complete(ctx, req)
			if err == nil || attempt >= maxTransientRetries || !isTransientProviderError(ctx, err) {
				return resp, false, err
			}
		}
		emit(cfg, "status", fmt.Sprintf("connection interrupted — reconnecting to provider (%d/%d)…", attempt+1, maxTransientRetries))
		backoff := time.Duration(attempt+1) * 250 * time.Millisecond
		select {
		case <-ctx.Done():
			return provider.Response{}, false, ctx.Err()
		case <-time.After(backoff):
		}
	}
}

func isTransientProviderError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"unexpected eof",
		"server closed idle connection",
		"tls handshake timeout",
		"status 502",
		"status 503",
		"status 504",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func messageSize(m provider.Message) int {
	n := len(m.Content) + len(m.ToolCallID) + len(m.Opaque)
	for _, image := range m.Images {
		n += len(image.Data)*4/3 + len(image.URL) + len(image.MediaType)
	}
	for _, t := range m.ToolCalls {
		n += len(t.Arguments) + len(t.Name) + len(t.ID)
	}
	return n
}

// Validate the complete batch before any tool executes. A malformed later call
// must not allow an earlier call in the same response to mutate the workspace.
func validateResponse(resp provider.Response, previousCalls int) error {
	if resp.FinishReason == "length" || resp.FinishReason == "content_filter" {
		return errors.New("agent: incomplete model response")
	}
	calls := resp.Message.ToolCalls
	if len(calls) > 32 || previousCalls+len(calls) > 64 {
		return ErrBudget
	}
	seen := make(map[string]bool)
	for _, call := range calls {
		if call.ID == "" || seen[call.ID] || call.Name == "" {
			return errors.New("agent: missing or duplicate tool call identity")
		}
		if _, err := object(call.Arguments); err != nil {
			return fmt.Errorf("agent: malformed tool arguments: %w", err)
		}
		seen[call.ID] = true
	}
	return nil
}

func systemPrompt(write, exec bool) string {
	permissions := "File writes are disabled. Command execution is disabled."
	if write && exec {
		permissions = "File writes and command execution are enabled by the caller."
	} else if write {
		permissions = "File writes are enabled by the caller. Command execution is disabled."
	} else if exec {
		permissions = "Direct file edit tools are disabled. Command execution is enabled and commands can change files with the user’s host permissions."
	}
	return "You are a local workspace assistant. Use the supplied tools when needed. " + permissions + " File mutations require an expected revision."
}

func definitions(write, exec bool) []provider.ToolDefinition {
	defs := []provider.ToolDefinition{
		{Name: "search", Description: "Bounded literal workspace search; returns file, line, and text.", Parameters: rawSchema(`{"type":"object","required":["query"],"properties":{"query":{"type":"string"}}}`)},
		{Name: "git_review", Description: "Read Git status and staged/unstaged diff without mutation.", Parameters: rawSchema(`{"type":"object","properties":{}}`)},
		{Name: toolRead, Description: "Read a UTF-8 text file and return its revision.", Parameters: rawSchema(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)},
		{Name: toolList, Description: "List immediate children of a workspace directory.", Parameters: rawSchema(`{"type":"object","required":["path"],"properties":{"path":{"type":"string"}}}`)},
	}
	if write {
		defs = append(defs, provider.ToolDefinition{Name: "apply_patch", Description: "Apply a recoverable multi-file JSON transaction. Each existing source requires expected_revision; create requires a missing target. Moves require a missing destination. Paths cannot repeat.", Parameters: rawSchema(`{"type":"object","required":["operations"],"properties":{"operations":{"type":"array","minItems":1,"maxItems":128,"items":{"type":"object","required":["kind","path"],"properties":{"kind":{"type":"string","enum":["create","replace","delete","move"]},"path":{"type":"string"},"to":{"type":"string"},"expected_revision":{"type":["string","null"]},"content":{"type":"string"}}}}}}`)},
			provider.ToolDefinition{Name: toolEdit, Description: "Replace exactly one occurrence in a text file using its current revision.", Parameters: rawSchema(`{"type":"object","required":["path","expected_revision","old_text","new_text"],"properties":{"path":{"type":"string"},"expected_revision":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}}}`)},
			provider.ToolDefinition{Name: toolWrite, Description: "Create or replace a text file. expected_revision must be a string or null.", Parameters: rawSchema(`{"type":"object","required":["path","expected_revision","content"],"properties":{"path":{"type":"string"},"expected_revision":{"type":["string","null"]},"content":{"type":"string"}}}`)},
		)
	}
	if exec {
		defs = append(defs, provider.ToolDefinition{Name: toolExec, Description: "Run an explicit argv command in the fixed workspace root.", Parameters: rawSchema(`{"type":"object","required":["argv"],"properties":{"argv":{"type":"array","items":{"type":"string"}}}}`)})
	}
	return defs
}

func rawSchema(s string) json.RawMessage { return json.RawMessage(s) }

func runTool(ctx context.Context, w environment.Backend, cfg Config, name string, raw json.RawMessage, limit int) string {
	args, err := object(raw)
	if err != nil {
		return toolErrorLimit(err, limit)
	}
	switch name {
	case "search", "git_review":
		return inspectWorkspace(ctx, w, cfg.Root, name, args, limit)
	case "apply_patch":
		if !cfg.WriteEnabled {
			return toolErrorLimit(errors.New("file writes are disabled"), limit)
		}
		patcher, ok := w.(environment.PatchController)
		if !ok {
			return toolErrorLimit(errors.New("environment does not support patch transactions"), limit)
		}
		var plan workspace.PatchPlan
		if err := json.Unmarshal(raw, &plan); err != nil {
			return toolErrorLimit(err, limit)
		}
		result, err := patcher.ApplyPatch(ctx, plan)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(result, limit)
	case toolRead:
		path, err := requiredString(args, "path")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		text, rev, err := w.Read(ctx, path)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(map[string]any{"path": path, "revision": rev, "content": text}, limit)
	case toolList:
		path, err := requiredString(args, "path")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		entries, err := w.List(ctx, path)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(map[string]any{"path": path, "entries": entries}, limit)
	case toolEdit:
		if !cfg.WriteEnabled {
			return toolErrorLimit(errors.New("file writes are disabled"), limit)
		}
		path, err := requiredString(args, "path")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		rev, err := requiredString(args, "expected_revision")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		oldText, err := requiredString(args, "old_text")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		newText, err := requiredString(args, "new_text")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		change, err := w.Edit(ctx, path, rev, oldText, newText)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(change, limit)
	case toolWrite:
		if !cfg.WriteEnabled {
			return toolErrorLimit(errors.New("file writes are disabled"), limit)
		}
		path, err := requiredString(args, "path")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		rawRev, ok := args["expected_revision"]
		if !ok {
			return toolErrorLimit(errors.New("expected_revision is required"), limit)
		}
		var rev *workspace.Revision
		if string(rawRev) != "null" {
			var s string
			if err := json.Unmarshal(rawRev, &s); err != nil {
				return toolErrorLimit(errors.New("expected_revision must be a string or null"), limit)
			}
			rev = &s
		}
		content, err := requiredString(args, "content")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		change, err := w.Write(ctx, path, rev, content)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(change, limit)
	case toolExec:
		if !cfg.ExecEnabled {
			return toolErrorLimit(errors.New("command execution is disabled"), limit)
		}
		var argv []string
		rawArgv, ok := args["argv"]
		if !ok || json.Unmarshal(rawArgv, &argv) != nil || len(argv) == 0 {
			return toolErrorLimit(errors.New("argv is required"), limit)
		}
		if _, ok := args["cwd"]; ok {
			return toolErrorLimit(errors.New("cwd is fixed to the workspace root"), limit)
		}
		commandCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		res, err := w.Exec(commandCtx, environment.ExecRequest{Argv: argv, MaxOutputBytes: limit})
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(map[string]any{"argv": res.Argv, "exit_code": res.ExitCode, "stdout": string(res.Stdout), "stderr": string(res.Stderr), "stdout_truncated": res.StdoutTruncated, "stderr_truncated": res.StderrTruncated, "cancelled": res.Cancelled}, limit)
	default:
		return toolErrorLimit(fmt.Errorf("unknown tool %q", name), limit)
	}
}

func inspectWorkspace(ctx context.Context, w environment.Backend, root, name string, args map[string]json.RawMessage, limit int) string {
	switch name {
	case "search":
		query, err := requiredString(args, "query")
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		result, err := w.Search(ctx, query)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(result, limit)
	case "git_review":
		review, err := w.Review(ctx)
		if err != nil {
			return toolErrorLimit(err, limit)
		}
		return boundedJSON(review, limit)

	default:
		return toolErrorLimit(errors.New("unknown inspection tool"), limit)
	}
}

func object(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var out map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil || out == nil {
		return nil, errors.New("tool arguments must be a JSON object")
	}
	return out, nil
}

func requiredString(args map[string]json.RawMessage, name string) (string, error) {
	raw, ok := args[name]
	if !ok {
		return "", fmt.Errorf("%s is required", name)
	}
	var s string
	if string(raw) == "null" || json.Unmarshal(raw, &s) != nil {
		return "", fmt.Errorf("%s must be a string", name)
	}
	return s, nil
}

func toolErrorLimit(err error, limit int) string {
	if limit < 2 {
		return "{}"
	}
	const prefix, suffix = `{"error":`, `}`
	maxQuoted := limit - len(prefix) - len(suffix)
	if maxQuoted < 2 {
		return "{}"
	}
	message := ""
	if err != nil {
		message = err.Error()
	}
	quoted := quote(message)
	if len(quoted) > maxQuoted {
		runes := []rune(message)
		low, high := 0, len(runes)
		for low < high {
			mid := low + (high-low+1)/2
			if len(quote(string(runes[:mid]))) <= maxQuoted {
				low = mid
			} else {
				high = mid - 1
			}
		}
		quoted = quote(string(runes[:low]))
	}
	return prefix + quoted + suffix
}

func quote(s string) string { b, _ := json.Marshal(s); return string(b) }

func boundedJSON(v any, limit int) string {
	b, err := json.Marshal(v)
	if err != nil {
		return toolErrorLimit(err, limit)
	}
	if len(b) <= limit {
		return string(b)
	}
	// Keep an explicit valid JSON error rather than cutting a JSON tool result.
	return toolErrorLimit(fmt.Errorf("tool result exceeds %d bytes", limit), limit)
}

func dispatchTool(ctx context.Context, w environment.Backend, cfg Config, call provider.ToolCall, limit int) string {
	for _, tool := range cfg.ExtraTools {
		if tool.Definition.Name != call.Name {
			continue
		}
		if _, err := object(call.Arguments); err != nil {
			return toolErrorLimit(err, limit)
		}
		result, err := tool.Call(ctx, call.Arguments)
		if err != nil {
			if len(result) > limit {
				return toolErrorLimit(err, limit)
			}
			var payload any = result
			if json.Valid([]byte(result)) {
				payload = json.RawMessage(result)
			}
			return boundedJSON(map[string]any{"error": err.Error(), "result": payload}, limit)
		}
		if len(result) > limit {
			return toolErrorLimit(errors.New("tool result exceeds output limit"), limit)
		}
		return result
	}
	return runTool(ctx, w, cfg, call.Name, call.Arguments, limit)
}

package agentbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

func runClaude(ctx context.Context, cfg Config, prompt, sessionID string, emit func(Event)) (Result, error) {
	events := newEventEmitter(emit, cfg.MaxOutputBytes)
	args := append([]string{}, cfg.Args...)
	args = append(args, "--output-format", "stream-json", "--verbose")
	args = append(args, "--setting-sources", "")
	mcpConfig, err := os.CreateTemp("", "bifrost-empty-mcp-*.json")
	if err != nil {
		return Result{}, fmt.Errorf("claude MCP isolation: %w", err)
	}
	mcpPath := mcpConfig.Name()
	defer os.Remove(mcpPath)
	if _, err := mcpConfig.WriteString(`{"mcpServers":{}}`); err != nil {
		_ = mcpConfig.Close()
		return Result{}, fmt.Errorf("claude MCP isolation: %w", err)
	}
	if err := mcpConfig.Close(); err != nil {
		return Result{}, fmt.Errorf("claude MCP isolation: %w", err)
	}
	args = append(args, "--strict-mcp-config", "--mcp-config", mcpPath)
	if cfg.Instructions != "" {
		args = append(args, "--append-system-prompt", cfg.Instructions)
	}
	if cfg.Model != "" {
		args = append(args, "--model", cfg.Model)
	}
	if cfg.MaxSteps > 0 {
		args = append(args, "--max-turns", strconv.Itoa(cfg.MaxSteps))
	}
	// Keep Claude's normal subscription permission flow. Disallowed tools are
	// explicit and narrow, and the dangerous bypass flag is never used.
	blocked := make([]string, 0, 4)
	allowed := []string{"Read", "Glob", "Grep", "LS"}
	if cfg.AllowWrite {
		allowed = append(allowed, "Write", "Edit", "NotebookEdit")
	}
	if cfg.AllowExec {
		allowed = append(allowed, "Bash")
	}
	args = append(args, "--tools", strings.Join(allowed, ","), "--allowedTools", strings.Join(allowed, ","))
	if !cfg.AllowWrite {
		blocked = append(blocked, "Write", "Edit", "NotebookEdit")
	}
	if !cfg.AllowExec {
		blocked = append(blocked, "Bash")
	}
	if len(blocked) > 0 {
		args = append(args, "--disallowedTools", strings.Join(blocked, ","))
	}
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	// `--` makes a prompt beginning with a dash an operand, never a CLI flag.
	args = append(args, "-p", "--", prompt)

	cmd := exec.CommandContext(ctx, cfg.Executable, args...)
	configureChild(cmd)
	cmd.WaitDelay = time.Second
	cmd.Dir, cmd.Env = cfg.Root, environment(cfg)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("agentbackend: start claude: %w", err)
	}
	waited := false
	waitCh := make(chan error, 1)
	stopWatch := make(chan struct{})
	stderrDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			terminateChild(cmd)
		case <-stopWatch:
		}
	}()
	defer func() {
		close(stopWatch)
		if !waited {
			terminateChild(cmd)
			<-waitCh
		}
		<-stderrDone
	}()
	go func() {
		defer close(stderrDone)
		drainStderr(stderr, cfg.MaxOutputBytes, events.emit)
	}()
	lineCh := make(chan []byte, 32)
	scanDone := make(chan error, 1)
	stopScan := make(chan struct{})
	var stopScanOnce sync.Once
	stopReader := func() {
		stopScanOnce.Do(func() {
			close(stopScan)
			_ = stdout.Close()
		})
	}
	go func() {
		defer close(lineCh)
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 4096), cfg.MaxOutputBytes)
		for s.Scan() {
			line := append([]byte(nil), s.Bytes()...)
			select {
			case lineCh <- line:
			case <-stopScan:
				scanDone <- nil
				return
			}
		}
		scanDone <- s.Err()
	}()
	// Start Wait only after the pipe readers are running. StdoutPipe and
	// StderrPipe are closed by Wait, so readers must already be draining when
	// the direct child exits.
	go func() { waitCh <- cmd.Wait() }()

	result := Result{SessionID: sessionID}
	var streamErr error
	terminalResult := false
	for line := range lineCh {
		if err := ctx.Err(); err != nil {
			streamErr = err
			stopReader()
			terminateChild(cmd)
			break
		}
		terminal, err := parseClaudeLine(line, &result, events.emit, cfg.MaxOutputBytes)
		if err != nil {
			streamErr = err
			stopReader()
			terminateChild(cmd)
			break
		}
		if terminal {
			result.terminal = true
			terminalResult = true
			// A terminal result is sufficient to finish the request. Kill the
			// process group before waiting so a CLI descendant holding a pipe
			// open cannot keep this call blocked forever.
			stopReader()
			terminateChild(cmd)
			break
		}
	}
	if scanErr := <-scanDone; scanErr != nil {
		if streamErr == nil && !terminalResult && !errors.Is(scanErr, os.ErrClosed) {
			streamErr = fmt.Errorf("claude stdout: %w", scanErr)
		}
		terminateChild(cmd)
	}
	waitErr := <-waitCh
	waited = true
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if streamErr != nil {
		return result, streamErr
	}
	if waitErr != nil && !terminalResult {
		return result, fmt.Errorf("claude: %w", waitErr)
	}
	if !result.terminal {
		return result, fmt.Errorf("claude: stream ended without a terminal result")
	}
	if result.SessionID == "" {
		return result, fmt.Errorf("claude: successful result did not contain session_id")
	}
	return result, nil
}

func parseClaudeLine(line []byte, result *Result, emit func(Event), max int) (bool, error) {
	var msg map[string]any
	if err := json.Unmarshal(line, &msg); err != nil {
		return false, fmt.Errorf("claude stream JSON: %w", err)
	}
	if id := stringValue(msg["session_id"]); id != "" {
		result.SessionID = id
	}
	typ := stringValue(msg["type"])
	switch typ {
	case "assistant":
		message, _ := msg["message"].(map[string]any)
		if err := parseClaudeContent(message["content"], result, emit, max); err != nil {
			return false, err
		}
		if usage, ok := message["usage"].(map[string]any); ok {
			result.Usage = usageValue(usage)
			emitEvent(emit, Event{Kind: "usage", Usage: &result.Usage})
		}
	case "content_block_delta":
		delta, _ := msg["delta"].(map[string]any)
		if text := stringValue(delta["text"]); text != "" {
			if len(result.Text)+len(text) > max {
				return false, fmt.Errorf("claude: output exceeds %d bytes", max)
			}
			result.Text += text
			emitEvent(emit, Event{Kind: "text", Text: text, Data: boundedJSON(msg, max)})
		}
	case "result":
		if text := stringValue(msg["result"]); text != "" {
			if len(text) > max {
				return false, fmt.Errorf("claude: output exceeds %d bytes", max)
			}
			result.Text = text
		}
		if usage, ok := msg["usage"].(map[string]any); ok {
			result.Usage = usageValue(usage)
			emitEvent(emit, Event{Kind: "usage", Usage: &result.Usage})
		}
		subtype := stringValue(msg["subtype"])
		result.StopReason = subtype
		if isError, _ := msg["is_error"].(bool); isError || strings.HasPrefix(subtype, "error") {
			return false, fmt.Errorf("claude: %s", stringValue(msg["result"]))
		}
		if subtype != "success" {
			return false, fmt.Errorf("claude: unsuccessful result subtype %q", subtype)
		}
		emitEvent(emit, Event{Kind: "status", Status: stringValue(msg["subtype"]), Data: boundedJSON(msg, max)})
	case "error":
		return false, fmt.Errorf("claude: %s", stringValue(msg["error"]))
	default:
		emitEvent(emit, Event{Kind: "status", Status: typ, Data: boundedJSON(msg, max)})
	}
	return typ == "result", nil
}

func parseClaudeContent(raw any, result *Result, emit func(Event), max int) error {
	items, _ := raw.([]any)
	for _, item := range items {
		part, _ := item.(map[string]any)
		switch stringValue(part["type"]) {
		case "text":
			if text := stringValue(part["text"]); text != "" {
				if len(result.Text)+len(text) > max {
					return fmt.Errorf("claude: output exceeds %d bytes", max)
				}
				result.Text += text
				emitEvent(emit, Event{Kind: "text", Text: text, Data: boundedJSON(part, max)})
			}
		case "tool_use":
			emitEvent(emit, Event{Kind: "tool", Tool: stringValue(part["name"]), Status: "started", Data: boundedJSON(part, max)})
		}
	}
	return nil
}

func usageValue(m map[string]any) Usage {
	u := Usage{InputTokens: intValue(m["input_tokens"]), OutputTokens: intValue(m["output_tokens"])}
	u.TotalTokens = intValue(m["total_tokens"])
	if u.TotalTokens == 0 {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u
}

func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func emitEvent(emit func(Event), event Event) {
	if emit != nil {
		emit(event)
	}
}

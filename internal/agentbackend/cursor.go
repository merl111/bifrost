package agentbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type cursorRPC struct {
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	lines      chan []byte
	readErr    chan error
	stop       chan struct{}
	stdoutDone chan struct{}
	stderrDone chan struct{}
	maxLine    int
	writeMu    sync.Mutex
	nextID     int64
	sessionID  string
	emit       func(Event)
	allowWrite bool
	allowExec  bool
	text       strings.Builder
	usage      Usage
	tooLarge   bool
}

func runCursor(ctx context.Context, cfg Config, prompt, sessionID string, emit func(Event)) (Result, error) {
	events := newEventEmitter(emit, cfg.MaxOutputBytes)
	argv := append([]string{cfg.Executable}, cfg.Args...)
	argv = append(argv, "acp")
	cmd := exec.Command(cfg.Executable, argv[1:]...)
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
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("agentbackend: start cursor: %w", err)
	}
	rpc := &cursorRPC{cmd: cmd, stdin: stdin, lines: make(chan []byte, 32), readErr: make(chan error, 1), stop: make(chan struct{}), stdoutDone: make(chan struct{}), stderrDone: make(chan struct{}), maxLine: cfg.MaxOutputBytes, emit: events.emit, allowWrite: cfg.AllowWrite, allowExec: cfg.AllowExec}
	go rpc.read(stdout)
	go rpc.readStderr(stderr)
	defer func() {
		close(rpc.stop)
		_ = stdin.Close()
		terminateChild(cmd)
		_ = cmd.Wait()
		<-rpc.stdoutDone
		<-rpc.stderrDone
	}()

	if _, err := rpc.request(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{"readTextFile": false, "writeTextFile": false}, "terminal": false},
		"clientInfo":         map[string]any{"name": "bifrost", "version": "1"},
	}); err != nil {
		return Result{}, fmt.Errorf("cursor initialize: %w", err)
	}
	if _, err := rpc.request(ctx, "authenticate", map[string]any{"methodId": "cursor_login"}); err != nil {
		return Result{}, fmt.Errorf("cursor authenticate: %w", err)
	}
	var sess map[string]any
	if sessionID == "" {
		params := map[string]any{"cwd": cfg.Root, "mcpServers": []any{}}
		var err error
		sess, err = rpc.request(ctx, "session/new", params)
		if err != nil {
			return Result{}, fmt.Errorf("cursor new session: %w", err)
		}
		sessionID = stringValue(sess["sessionId"])
	} else {
		var err error
		sess, err = rpc.request(ctx, "session/load", map[string]any{"sessionId": sessionID, "cwd": cfg.Root, "mcpServers": []any{}})
		if err != nil {
			return Result{}, fmt.Errorf("cursor load session %q: %w", sessionID, err)
		}
	}
	if sessionID == "" {
		return Result{}, errors.New("cursor session response did not contain sessionId")
	}
	rpc.sessionID = sessionID
	if cfg.Model != "" {
		if _, err := rpc.request(ctx, "session/set_model", map[string]any{"sessionId": sessionID, "modelId": cfg.Model}); err != nil {
			return Result{SessionID: sessionID}, fmt.Errorf("cursor set model: %w", err)
		}
	}
	if cfg.Instructions != "" {
		prompt = cfg.Instructions + "\n\n" + prompt
	}
	resp, err := rpc.request(ctx, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []any{map[string]any{"type": "text", "text": prompt}}})
	if err != nil {
		return Result{SessionID: sessionID}, fmt.Errorf("cursor prompt: %w", err)
	}
	result := Result{SessionID: sessionID, StopReason: stringValue(resp["stopReason"])}
	switch result.StopReason {
	case "cancelled":
		return result, context.Canceled
	case "refusal":
		return result, fmt.Errorf("cursor prompt refused")
	case "end_turn", "max_tokens", "max_turn_requests":
	default:
		return result, fmt.Errorf("cursor prompt failed with stop reason %q", result.StopReason)
	}
	if text := stringValue(resp["text"]); text != "" {
		result.Text = text
	}
	if result.Text == "" {
		result.Text = rpc.text.String()
	}
	if result.Text == "" {
		result.Text = stringValue(resp["result"])
	}
	result.Usage = rpc.usage
	if usage, ok := resp["usage"].(map[string]any); ok {
		result.Usage = usageValue(usage)
	}
	if rpc.tooLarge || len(result.Text) > cfg.MaxOutputBytes {
		return result, fmt.Errorf("cursor: output exceeds %d bytes", cfg.MaxOutputBytes)
	}
	return result, nil
}

func (r *cursorRPC) read(src io.Reader) {
	defer close(r.stdoutDone)
	s := bufio.NewScanner(src)
	s.Buffer(make([]byte, 4096), r.maxLine)
	defer close(r.lines)
	defer func() {
		if err := s.Err(); err != nil {
			r.readErr <- fmt.Errorf("cursor stdout: %w", err)
		} else {
			r.readErr <- io.EOF
		}
	}()
	for s.Scan() {
		line := append([]byte(nil), s.Bytes()...)
		select {
		case r.lines <- line:
		case <-r.stop:
			return
		}
	}
}

func (r *cursorRPC) readStderr(src io.Reader) {
	defer close(r.stderrDone)
	drainStderr(src, r.maxLine, r.emitEvent)
}

func (r *cursorRPC) request(ctx context.Context, method string, params any) (map[string]any, error) {
	id, err := r.send(method, params)
	if err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			if r.sessionID != "" {
				_ = r.sendNotification("session/cancel", map[string]any{"sessionId": r.sessionID})
			}
			return nil, ctx.Err()
		case line, ok := <-r.lines:
			if !ok {
				select {
				case err := <-r.readErr:
					return nil, err
				default:
					return nil, io.EOF
				}
			}
			var msg map[string]any
			if err := json.Unmarshal(line, &msg); err != nil {
				r.emitEvent(Event{Kind: "status", Status: "malformed-json"})
				continue
			}
			if methodName := stringValue(msg["method"]); methodName != "" {
				if methodName == "session/update" {
					r.handleUpdate(msg)
					if r.tooLarge {
						return nil, fmt.Errorf("cursor: output exceeds %d bytes", r.maxLine)
					}
				} else if methodName == "session/request_permission" {
					if err := r.handlePermission(msg); err != nil {
						return nil, fmt.Errorf("cursor permission response: %w", err)
					}
				} else {
					// Extension methods that require a response are denied. This
					// avoids blocking on an unsupported interactive surface.
					if msg["id"] != nil {
						if err := r.respond(msg["id"], map[string]any{"error": map[string]any{"code": -32601, "message": "unsupported client method"}}); err != nil {
							return nil, fmt.Errorf("cursor unsupported request response: %w", err)
						}
					}
				}
				continue
			}
			if sameID(msg["id"], id) {
				if e, ok := msg["error"].(map[string]any); ok {
					return nil, fmt.Errorf("rpc error %s", stringValue(e["message"]))
				}
				if result, ok := msg["result"].(map[string]any); ok {
					return result, nil
				}
				return map[string]any{}, nil
			}
		}
	}
}

func (r *cursorRPC) send(method string, params any) (int64, error) {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.nextID++
	id := r.nextID
	err := r.writeLocked(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	return id, err
}

func (r *cursorRPC) sendNotification(method string, params any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	return r.writeLocked(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (r *cursorRPC) respond(id any, result any) error {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	msg := map[string]any{"jsonrpc": "2.0", "id": id}
	if m, ok := result.(map[string]any); ok && m["error"] != nil {
		msg["error"] = m["error"]
	} else {
		msg["result"] = result
	}
	return r.writeLocked(msg)
}

func (r *cursorRPC) writeLocked(msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = r.stdin.Write(b)
	return err
}

func (r *cursorRPC) handleUpdate(msg map[string]any) {
	params, _ := msg["params"].(map[string]any)
	update, _ := params["update"].(map[string]any)
	kind := stringValue(update["sessionUpdate"])
	text := ""
	if content, ok := update["content"].(map[string]any); ok {
		text = stringValue(content["text"])
	}
	if text != "" && kind == "agent_message_chunk" {
		if r.text.Len()+len(text) > r.maxLine {
			r.tooLarge = true
		} else {
			r.text.WriteString(text)
		}
		r.emitEvent(Event{Kind: "text", Text: text, Data: boundedJSON(update, r.maxLine)})
	} else if kind == "tool_call" || kind == "tool_call_update" {
		r.emitEvent(Event{Kind: "tool", Tool: stringValue(update["kind"]), Status: stringValue(update["status"]), Data: boundedJSON(update, r.maxLine)})
	} else {
		r.emitEvent(Event{Kind: "status", Status: kind, Data: boundedJSON(update, r.maxLine)})
	}
	if usage, ok := update["usage"].(map[string]any); ok {
		r.usage = usageValue(usage)
		u := r.usage
		r.emitEvent(Event{Kind: "usage", Usage: &u})
	}
}

func (r *cursorRPC) handlePermission(msg map[string]any) error {
	params, _ := msg["params"].(map[string]any)
	toolCall, _ := params["toolCall"].(map[string]any)
	kind := strings.ToLower(stringValue(toolCall["kind"]))
	allowed := false
	switch {
	case kind == "read" || kind == "search" || kind == "fetch" || kind == "think":
		allowed = true
	case kind == "edit" || kind == "delete" || kind == "move":
		allowed = r.allowWrite
	case kind == "execute":
		allowed = r.allowExec
	}
	r.emitEvent(Event{Kind: "permission", Tool: kind, Status: map[bool]string{true: "allowed", false: "denied"}[allowed], Data: boundedJSON(params, r.maxLine)})
	desiredKind := "reject_once"
	if allowed {
		desiredKind = "allow_once"
	}
	optionID := ""
	rejectID := ""
	hasOptions := false
	if options, ok := params["options"].([]any); ok {
		hasOptions = true
		for _, raw := range options {
			option, _ := raw.(map[string]any)
			kind := stringValue(option["kind"])
			if kind == "reject_once" && rejectID == "" {
				rejectID = stringValue(option["optionId"])
			}
			if kind == desiredKind && optionID == "" {
				optionID = stringValue(option["optionId"])
			}
		}
	}
	if optionID == "" {
		// Never invent an option ID. If an allowed request has no one-shot
		// allow option, fall back to a supplied one-shot rejection. If the
		// vendor supplied no usable option, cancel the request below.
		optionID = rejectID
	}
	if optionID == "" {
		if !hasOptions {
			return r.respond(msg["id"], map[string]any{"outcome": map[string]any{"outcome": "cancelled"}})
		}
		return fmt.Errorf("permission request has no usable one-shot option")
	}
	return r.respond(msg["id"], map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": optionID}})
}

func (r *cursorRPC) emitEvent(event Event) {
	if r.emit != nil {
		r.emit(event)
	}
}

func sameID(a any, b int64) bool {
	switch v := a.(type) {
	case float64:
		return int64(v) == b
	case json.Number:
		n, _ := v.Int64()
		return n == b
	case int64:
		return v == b
	}
	return false
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func boundedJSON(v any, max int) json.RawMessage {
	b, _ := json.Marshal(v)
	if len(b) > max {
		b = b[:max]
	}
	return json.RawMessage(b)
}

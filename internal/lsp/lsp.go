// Package lsp provides a bounded, explicit-command Language Server Protocol
// client. It never discovers or executes language servers from repository
// configuration.
package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultMaxMessageBytes = 8 << 20
	DefaultTimeout         = 10 * time.Second
)

var ErrClosed = errors.New("lsp client is closed")

type Config struct {
	Command         []string
	WorkspaceRoot   string
	RootURI         string
	MaxMessageBytes int
	RequestTimeout  time.Duration
}

type Diagnostic struct {
	Range    Range           `json:"range"`
	Severity int             `json:"severity,omitempty"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}
type Diagnostics struct {
	URI         string       `json:"uri"`
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type Client struct {
	cmd         *exec.Cmd
	in          io.WriteCloser
	out         io.ReadCloser
	reader      *bufio.Reader
	max         int
	timeout     time.Duration
	writeMu     sync.Mutex
	pendingMu   sync.Mutex
	pending     map[int64]chan rpcResult
	nextID      atomic.Int64
	diagnostics chan Diagnostics
	docMu       sync.Mutex
	docVersions map[string]int
	closed      chan struct{}
	closeOnce   sync.Once
	closeErr    error
}
type rpcResult struct {
	result json.RawMessage
	err    error
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Start launches the explicitly supplied server and completes LSP initialize.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	if len(cfg.Command) == 0 || cfg.Command[0] == "" {
		return nil, errors.New("lsp command is required")
	}
	for _, arg := range cfg.Command {
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, errors.New("lsp command contains NUL")
		}
	}
	if cfg.MaxMessageBytes <= 0 {
		cfg.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultTimeout
	}
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	configure(cmd)
	cmd.Dir = cfg.WorkspaceRoot
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		in.Close()
		out.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		in.Close()
		out.Close()
		stderr.Close()
		return nil, err
	}
	go func() { _, _ = io.Copy(io.Discard, stderr); _ = stderr.Close() }()
	c := &Client{cmd: cmd, in: in, out: out, reader: bufio.NewReaderSize(out, min(cfg.MaxMessageBytes, 64<<10)), max: cfg.MaxMessageBytes, timeout: cfg.RequestTimeout, pending: map[int64]chan rpcResult{}, diagnostics: make(chan Diagnostics, 64), docVersions: map[string]int{}, closed: make(chan struct{})}
	go c.readLoop()
	rootURI := cfg.RootURI
	if rootURI == "" && cfg.WorkspaceRoot != "" {
		rootURI = fileURI(cfg.WorkspaceRoot)
	}
	params := map[string]any{"processId": os.Getpid(), "rootUri": rootURI, "capabilities": map[string]any{"textDocument": map[string]any{"hover": map[string]any{}, "publishDiagnostics": map[string]any{}}}}
	var initResult json.RawMessage
	initCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
	err = c.request(initCtx, "initialize", params, &initResult)
	cancel()
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("lsp initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

func (c *Client) Diagnostics() <-chan Diagnostics { return c.diagnostics }
func (c *Client) DidOpen(ctx context.Context, path, language, text string, version int) error {
	if version <= 0 {
		return errors.New("lsp document version must be positive")
	}
	if err := c.checkVersion(path, version); err != nil {
		return err
	}
	if err := c.notifyContext(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{"uri": fileURI(path), "languageId": language, "version": version, "text": text}}); err != nil {
		return err
	}
	c.setVersion(path, version)
	return nil
}
func (c *Client) DidChange(ctx context.Context, path, text string, version int) error {
	if version <= 0 {
		return errors.New("lsp document version must be positive")
	}
	if err := c.checkVersion(path, version); err != nil {
		return err
	}
	params := map[string]any{
		"textDocument":   map[string]any{"uri": fileURI(path), "version": version},
		"contentChanges": []any{map[string]any{"text": text}},
	}
	if err := c.notifyContext(ctx, "textDocument/didChange", params); err != nil {
		return err
	}
	c.setVersion(path, version)
	return nil
}

func (c *Client) checkVersion(path string, version int) error {
	c.docMu.Lock()
	defer c.docMu.Unlock()
	if old, ok := c.docVersions[path]; ok && version <= old {
		return fmt.Errorf("lsp document version %d is not newer than %d", version, old)
	}
	return nil
}
func (c *Client) setVersion(path string, version int) {
	c.docMu.Lock()
	c.docVersions[path] = version
	c.docMu.Unlock()
}
func (c *Client) Hover(ctx context.Context, path string, line, character int) (json.RawMessage, error) {
	if line < 0 || character < 0 {
		return nil, errors.New("lsp position is negative")
	}
	var out json.RawMessage
	err := c.request(ctx, "textDocument/hover", map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": Position{line, character}}, &out)
	return out, err
}
func (c *Client) Definition(ctx context.Context, path string, line, character int) (json.RawMessage, error) {
	if line < 0 || character < 0 {
		return nil, errors.New("lsp position is negative")
	}
	var out json.RawMessage
	err := c.request(ctx, "textDocument/definition", map[string]any{"textDocument": map[string]string{"uri": fileURI(path)}, "position": Position{line, character}}, &out)
	return out, err
}
func (c *Client) Shutdown(ctx context.Context) error {
	var out json.RawMessage
	err := c.request(ctx, "shutdown", nil, &out)
	if err == nil {
		err = c.notify("exit", nil)
	}
	return err
}
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.in.Close()
		_ = c.out.Close()
		kill(c.cmd)
		c.closeErr = c.cmd.Wait()
		c.failPending(ErrClosed)
	})
	return c.closeErr
}

func (c *Client) request(ctx context.Context, method string, params any, out *json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcResult, 1)
	c.pendingMu.Lock()
	c.pending[id] = ch
	c.pendingMu.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.removePending(id)
		return err
	}
	timer := time.NewTimer(c.timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if out != nil {
			*out = r.result
		}
		return nil
	case <-ctx.Done():
		c.removePending(id)
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return ctx.Err()
	case <-timer.C:
		c.removePending(id)
		_ = c.notify("$/cancelRequest", map[string]any{"id": id})
		return context.DeadlineExceeded
	}
}
func (c *Client) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
func (c *Client) notifyContext(ctx context.Context, method string, params any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return c.notify(method, params)
}
func (c *Client) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > c.max {
		return errors.New("lsp message exceeds size limit")
	}
	frame := append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(b))), b...)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	for len(frame) > 0 {
		n, err := c.in.Write(frame)
		if err != nil {
			return err
		}
		frame = frame[n:]
	}
	return nil
}
func (c *Client) readLoop() {
	for {
		body, err := readFrame(c.reader, c.max)
		if err != nil {
			c.failPending(err)
			return
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			c.failPending(err)
			return
		}
		if msg.Method == "textDocument/publishDiagnostics" {
			var d Diagnostics
			if json.Unmarshal(msg.Params, &d) == nil {
				select {
				case c.diagnostics <- d:
				default:
				}
			}
			continue
		}
		if msg.ID == nil {
			continue
		}
		c.pendingMu.Lock()
		ch := c.pending[*msg.ID]
		delete(c.pending, *msg.ID)
		c.pendingMu.Unlock()
		if ch == nil {
			continue
		}
		if msg.Error != nil {
			ch <- rpcResult{err: fmt.Errorf("lsp %d: %s", msg.Error.Code, msg.Error.Message)}
		} else {
			ch <- rpcResult{result: append(json.RawMessage(nil), msg.Result...)}
		}
	}
}
func (c *Client) removePending(id int64) {
	c.pendingMu.Lock()
	delete(c.pending, id)
	c.pendingMu.Unlock()
}
func (c *Client) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- rpcResult{err: err}
	}
}
func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		key, val, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			var e error
			length, e = strconv.Atoi(strings.TrimSpace(val))
			if e != nil {
				return nil, e
			}
		}
		if len(line) > 8192 {
			return nil, errors.New("lsp headers exceed limit")
		}
	}
	if length < 0 || length > max {
		return nil, errors.New("lsp content length exceeds limit")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}
func fileURI(path string) string {
	abs, _ := filepath.Abs(path)
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	return u.String()
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

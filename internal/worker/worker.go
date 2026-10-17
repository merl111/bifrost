// Package worker implements the bounded stdio protocol used by a Bifrost
// worker.  A worker owns one canonical workspace and never accepts a root
// change after the handshake.
package worker

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bifrost/internal/browser"
	"bifrost/internal/desktop"
	"bifrost/internal/gitview"
	"bifrost/internal/process"
	"bifrost/internal/workspace"
)

const (
	ProtocolMajor           = 1
	ProtocolMinor           = 0
	DefaultMaxMessageBytes  = 1 << 20
	DefaultOperationTimeout = 30 * time.Second
	MaxRemoteImageBytes     = 512 << 10
)

var (
	ErrProtocol     = errors.New("worker protocol error")
	ErrRootMismatch = errors.New("worker root identity mismatch")
)

type Config struct {
	Root             string
	StateDir         string
	BuildVersion     string
	MaxMessageBytes  int
	OperationTimeout time.Duration
	MaxConcurrent    int
	BrowserEnabled   bool
	DesktopEnabled   bool
	BrowserConfig    browser.Config
	DesktopConfig    desktop.Config
}

type Capabilities struct {
	FilesRead   bool `json:"files_read"`
	FilesMutate bool `json:"files_mutate"`
	Search      bool `json:"search"`
	Exec        bool `json:"exec"`
	GitReview   bool `json:"git_review"`
	Browser     bool `json:"browser"`
	Desktop     bool `json:"desktop"`
}

type Hello struct {
	Type          string       `json:"type"`
	ProtocolMajor int          `json:"protocol_major"`
	ProtocolMinor int          `json:"protocol_minor"`
	Build         string       `json:"build,omitempty"`
	BootID        string       `json:"boot_id"`
	Platform      string       `json:"platform"`
	Architecture  string       `json:"architecture"`
	Root          string       `json:"root"`
	Capabilities  Capabilities `json:"capabilities"`
}

type ClientOptions struct {
	ExpectedRoot     string
	MaxMessageBytes  int
	HandshakeTimeout time.Duration
}

type Request struct {
	Type              string          `json:"type"`
	ID                string          `json:"id"`
	OperationID       string          `json:"operation_id,omitempty"`
	Method            string          `json:"method,omitempty"`
	DeadlineUnixMilli int64           `json:"deadline_unix_milli,omitempty"`
	Payload           json.RawMessage `json:"payload,omitempty"`
}

type Response struct {
	Type        string          `json:"type"`
	ID          string          `json:"id,omitempty"`
	OperationID string          `json:"operation_id,omitempty"`
	Status      string          `json:"status"`
	Result      json.RawMessage `json:"result,omitempty"`
	Error       string          `json:"error,omitempty"`
}

type BrowserResult struct {
	Snapshot browser.Snapshot `json:"snapshot"`
	PNG      []byte           `json:"png,omitempty"`
}

type DesktopResult struct {
	Observation desktop.Observation `json:"observation"`
	PNG         []byte              `json:"png,omitempty"`
}

type DesktopAction struct {
	ObservationID string   `json:"observation_id"`
	Display       string   `json:"display"`
	X             int      `json:"x"`
	Y             int      `json:"y"`
	Button        int      `json:"button"`
	Keys          []string `json:"keys"`
	Text          string   `json:"text"`
}

type helloFrame struct {
	Type          string `json:"type"`
	ProtocolMajor int    `json:"protocol_major"`
	ProtocolMinor int    `json:"protocol_minor"`
	ClientID      string `json:"client_id,omitempty"`
}

type Client struct {
	conn      io.ReadWriteCloser
	reader    *bufio.Reader
	max       int
	hello     Hello
	writeMu   sync.Mutex
	opMu      sync.Mutex
	closed    chan struct{}
	closeOnce sync.Once
}

// Connect performs the two-way version handshake. The returned client owns
// conn and closes it when Close is called or a protocol timeout is reached.
func Connect(ctx context.Context, conn io.ReadWriteCloser, opts ClientOptions) (*Client, error) {
	if conn == nil {
		return nil, errors.New("worker: nil connection")
	}
	max := opts.MaxMessageBytes
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	c := &Client{conn: conn, reader: bufio.NewReaderSize(conn, min(max, 64<<10)), max: max, closed: make(chan struct{})}
	readCtx := ctx
	if opts.HandshakeTimeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, opts.HandshakeTimeout)
		defer cancel()
	}
	var hf helloFrame
	if err := c.readJSON(readCtx, &hf); err != nil {
		c.Close()
		return nil, fmt.Errorf("worker hello: %w", err)
	}
	if hf.Type != "hello" || hf.ProtocolMajor != ProtocolMajor || hf.ProtocolMinor > ProtocolMinor {
		c.Close()
		return nil, fmt.Errorf("%w: unsupported worker version %d.%d", ErrProtocol, hf.ProtocolMajor, hf.ProtocolMinor)
	}
	if err := c.writeJSON(helloFrame{Type: "hello", ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, ClientID: randomID()}); err != nil {
		c.Close()
		return nil, err
	}
	var ok Hello
	if err := c.readJSON(readCtx, &ok); err != nil {
		c.Close()
		return nil, fmt.Errorf("worker handshake: %w", err)
	}
	if ok.Type != "hello.ok" || ok.ProtocolMajor != ProtocolMajor {
		c.Close()
		return nil, fmt.Errorf("%w: invalid handshake response", ErrProtocol)
	}
	if opts.ExpectedRoot != "" {
		expected, _ := canonicalRoot(opts.ExpectedRoot)
		if ok.Root != expected {
			c.Close()
			return nil, ErrRootMismatch
		}
	}
	c.hello = ok
	return c, nil
}

// Hello returns the immutable capability and identity advertisement.
func (c *Client) Hello() Hello { return c.hello }

func (c *Client) Close() error {
	c.closeOnce.Do(func() { close(c.closed); _ = c.conn.Close() })
	return nil
}

// Do sends one bounded operation. operationID should be stable across a
// retry, but the worker intentionally does not claim durable exactly-once
// semantics for mutations.
func (c *Client) Do(ctx context.Context, operationID, method string, payload any, out any) error {
	c.opMu.Lock()
	defer c.opMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if operationID == "" {
		operationID = randomID()
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	r := Request{Type: "request", ID: randomID(), OperationID: operationID, Method: method, Payload: b}
	if deadline, ok := ctx.Deadline(); ok {
		r.DeadlineUnixMilli = deadline.UnixMilli()
	}
	if err := c.writeJSON(r); err != nil {
		return err
	}
	requestID := r.ID
	respCh := make(chan struct {
		resp Response
		err  error
	}, 1)
	go func() {
		var resp Response
		err := c.readJSON(context.Background(), &resp)
		respCh <- struct {
			resp Response
			err  error
		}{resp, err}
	}()
	select {
	case x := <-respCh:
		if x.err == nil && x.resp.ID != requestID {
			c.Close()
			return fmt.Errorf("%w: response id mismatch", ErrProtocol)
		}
		return c.finishResponse(x.resp, x.err, out, ctx)
	case <-ctx.Done():
		_ = c.writeJSON(Request{Type: "cancel", ID: randomID(), OperationID: operationID})
		select {
		case x := <-respCh:
			if x.err == nil && x.resp.ID != requestID {
				c.Close()
				return fmt.Errorf("%w: response id mismatch", ErrProtocol)
			}
			_ = c.finishResponse(x.resp, x.err, nil, ctx)
			return contextError(ctx)
		case <-time.After(2 * time.Second):
			c.Close()
			return contextError(ctx)
		}
	}
}

func (c *Client) finishResponse(resp Response, err error, out any, ctx context.Context) error {
	if err != nil {
		c.Close()
		return err
	}
	if resp.Status != "ok" {
		if resp.Status == "cancelled" {
			if ctx.Err() != nil {
				return contextError(ctx)
			}
			if strings.Contains(resp.Error, context.DeadlineExceeded.Error()) {
				return context.DeadlineExceeded
			}
			return context.Canceled
		}
		if resp.Error == "" {
			return fmt.Errorf("worker operation failed")
		}
		return errors.New(resp.Error)
	}
	if out != nil && len(resp.Result) != 0 {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("worker result: %w", err)
		}
	}
	return nil
}

func contextError(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
		return context.DeadlineExceeded
	}
	return ctx.Err()
}

func (c *Client) writeJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > c.max {
		return fmt.Errorf("%w: frame exceeds %d bytes", ErrProtocol, c.max)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	b = append(b, '\n')
	for len(b) > 0 {
		n, err := c.conn.Write(b)
		if err != nil {
			return err
		}
		b = b[n:]
	}
	return nil
}

func (c *Client) readJSON(ctx context.Context, out any) error {
	ch := make(chan error, 1)
	go func() {
		line, err := readFrame(c.reader, c.max)
		if err == nil {
			err = json.Unmarshal(line, out)
		}
		ch <- err
	}()
	select {
	case err := <-ch:
		return err
	case <-ctx.Done():
		c.Close()
		return ctx.Err()
	}
}

func (c *Client) List(ctx context.Context, path string) ([]workspace.Entry, error) {
	var out []workspace.Entry
	return out, c.Do(ctx, "", "file.list", map[string]string{"path": path}, &out)
}
func (c *Client) Read(ctx context.Context, path string) (string, workspace.Revision, error) {
	var out struct {
		Content  string             `json:"content"`
		Revision workspace.Revision `json:"revision"`
	}
	err := c.Do(ctx, "", "file.read", map[string]string{"path": path}, &out)
	return out.Content, out.Revision, err
}
func (c *Client) Search(ctx context.Context, query string) (workspace.SearchResult, error) {
	var out workspace.SearchResult
	return out, c.Do(ctx, "", "file.search", map[string]string{"query": query}, &out)
}
func (c *Client) Write(ctx context.Context, path string, expected *workspace.Revision, content string) (workspace.Change, error) {
	var out workspace.Change
	p := struct {
		Path     string              `json:"path"`
		Expected *workspace.Revision `json:"expected_revision,omitempty"`
		Content  string              `json:"content"`
	}{path, expected, content}
	return out, c.Do(ctx, "", "file.write", p, &out)
}
func (c *Client) ApplyPatch(ctx context.Context, plan workspace.PatchPlan) (workspace.PatchResult, error) {
	var out workspace.PatchResult
	return out, c.Do(ctx, "", "files.patch", plan, &out)
}
func (c *Client) UndoPatch(ctx context.Context, id workspace.ChangeID) (workspace.PatchResult, error) {
	var out workspace.PatchResult
	return out, c.Do(ctx, "", "files.undo_patch", map[string]string{"id": string(id)}, &out)
}
func (c *Client) Edit(ctx context.Context, path string, expected workspace.Revision, oldText, newText string) (workspace.Change, error) {
	var out workspace.Change
	p := map[string]string{"path": path, "expected_revision": expected, "old": oldText, "new": newText}
	return out, c.Do(ctx, "", "file.edit", p, &out)
}
func (c *Client) Undo(ctx context.Context, id workspace.ChangeID) error {
	return c.Do(ctx, "", "file.undo", map[string]string{"change_id": string(id)}, nil)
}
func (c *Client) Exec(ctx context.Context, argv []string, cwd string, maxOutput int) (process.Result, error) {
	var out process.Result
	p := struct {
		Argv           []string `json:"argv"`
		CWD            string   `json:"cwd,omitempty"`
		MaxOutputBytes int      `json:"max_output_bytes,omitempty"`
	}{argv, cwd, maxOutput}
	return out, c.Do(ctx, "", "process.exec", p, &out)
}
func (c *Client) Review(ctx context.Context) (gitview.Review, error) {
	var out gitview.Review
	return out, c.Do(ctx, "", "git.review", nil, &out)
}

func (c *Client) BrowserNavigate(ctx context.Context, rawURL string) (browser.Snapshot, error) {
	var out browser.Snapshot
	err := c.Do(ctx, "", "browser.navigate", map[string]string{"url": rawURL}, &out)
	return out, err
}
func (c *Client) BrowserSnapshot(ctx context.Context) (browser.Snapshot, error) {
	var out browser.Snapshot
	return out, c.Do(ctx, "", "browser.snapshot", nil, &out)
}
func (c *Client) BrowserClick(ctx context.Context, ref string) (browser.Snapshot, error) {
	var out browser.Snapshot
	err := c.Do(ctx, "", "browser.click", map[string]string{"ref": ref}, &out)
	return out, err
}
func (c *Client) BrowserType(ctx context.Context, ref, text string) (browser.Snapshot, error) {
	var out browser.Snapshot
	err := c.Do(ctx, "", "browser.type", map[string]string{"ref": ref, "text": text}, &out)
	return out, err
}
func (c *Client) BrowserScreenshot(ctx context.Context) ([]byte, error) {
	var out BrowserResult
	if err := c.Do(ctx, "", "browser.screenshot", nil, &out); err != nil {
		return nil, err
	}
	return out.PNG, nil
}
func (c *Client) DesktopCapabilities(ctx context.Context) (desktop.Capability, error) {
	var out desktop.Capability
	return out, c.Do(ctx, "", "desktop.capabilities", nil, &out)
}
func (c *Client) DesktopCapture(ctx context.Context) (DesktopResult, error) {
	var out DesktopResult
	err := c.Do(ctx, "", "desktop.capture", nil, &out)
	return out, err
}
func (c *Client) DesktopMove(ctx context.Context, action DesktopAction) error {
	return c.Do(ctx, "", "desktop.move", action, nil)
}
func (c *Client) DesktopClick(ctx context.Context, action DesktopAction) error {
	return c.Do(ctx, "", "desktop.click", action, nil)
}
func (c *Client) DesktopKeyPress(ctx context.Context, action DesktopAction) error {
	return c.Do(ctx, "", "desktop.keypress", action, nil)
}
func (c *Client) DesktopType(ctx context.Context, action DesktopAction) error {
	return c.Do(ctx, "", "desktop.type", action, nil)
}

type server struct {
	cfg            Config
	root           string
	ws             *workspace.Workspace
	reader         *bufio.Reader
	writer         io.Writer
	max            int
	writeMu        sync.Mutex
	mu             sync.Mutex
	active         map[string]context.CancelFunc
	closed         chan struct{}
	wg             sync.WaitGroup
	sem            chan struct{}
	browser        *browser.Session
	desktopManager *desktop.Manager
	desktop        *desktop.Session
}

// Serve runs a worker until the peer closes the stdio stream or ctx ends.
func Serve(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	root, err := canonicalRoot(cfg.Root)
	if err != nil {
		return err
	}
	if cfg.StateDir == "" {
		return errors.New("worker: state directory is required")
	}
	max := cfg.MaxMessageBytes
	if max <= 0 {
		max = DefaultMaxMessageBytes
	}
	timeout := cfg.OperationTimeout
	if timeout <= 0 {
		timeout = DefaultOperationTimeout
	}
	concurrent := cfg.MaxConcurrent
	if concurrent <= 0 {
		concurrent = 4
	}
	ws, err := workspace.Open(root, cfg.StateDir)
	if err != nil {
		return err
	}
	defer ws.Close()
	s := &server{cfg: cfg, root: root, ws: ws, reader: bufio.NewReaderSize(r, min(max, 64<<10)), writer: w, max: max, active: map[string]context.CancelFunc{}, closed: make(chan struct{}), sem: make(chan struct{}, concurrent)}
	if cfg.BrowserEnabled {
		s.browser, err = browser.Launch(ctx, cfg.BrowserConfig)
		if err != nil {
			return fmt.Errorf("worker browser: %w", err)
		}
		defer s.browser.Close()
	}
	if cfg.DesktopEnabled {
		s.desktopManager, err = desktop.New(cfg.DesktopConfig)
		if err != nil {
			return fmt.Errorf("worker desktop: %w", err)
		}
		s.desktop, err = s.desktopManager.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("worker desktop lease: %w", err)
		}
		defer s.desktop.Close()
	}
	readDone := make(chan struct{})
	if closer, ok := r.(io.Closer); ok {
		go func() {
			select {
			case <-ctx.Done():
				_ = closer.Close()
			case <-readDone:
			}
		}()
	}
	defer close(readDone)
	bootID := randomID()
	caps := Capabilities{FilesRead: true, FilesMutate: true, Search: true, Exec: true, GitReview: true, Browser: cfg.BrowserEnabled, Desktop: cfg.DesktopEnabled}
	if err := s.write(Hello{Type: "hello", ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, Build: cfg.BuildVersion, BootID: bootID, Platform: runtime.GOOS, Architecture: runtime.GOARCH, Root: root, Capabilities: caps}); err != nil {
		return err
	}
	var hf helloFrame
	if err := s.read(ctx, &hf); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if hf.Type != "hello" || hf.ProtocolMajor != ProtocolMajor || hf.ProtocolMinor > ProtocolMinor {
		return fmt.Errorf("%w: unsupported client version", ErrProtocol)
	}
	if err := s.write(Hello{Type: "hello.ok", ProtocolMajor: ProtocolMajor, ProtocolMinor: ProtocolMinor, Build: cfg.BuildVersion, BootID: bootID, Platform: runtime.GOOS, Architecture: runtime.GOARCH, Root: root, Capabilities: caps}); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			s.wg.Wait()
			return ctx.Err()
		default:
		}
		var req Request
		if err := s.read(ctx, &req); err != nil {
			s.cancelAll()
			s.wg.Wait()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if req.Type == "cancel" {
			s.cancel(req.OperationID)
			continue
		}
		if req.Type != "request" || req.ID == "" || req.Method == "" {
			_ = s.respond(Response{Type: "response", ID: req.ID, Status: "error", Error: "invalid request"})
			continue
		}
		s.mu.Lock()
		_, duplicate := s.active[req.ID]
		if req.OperationID != "" {
			if _, duplicate = s.active[req.OperationID]; duplicate {
				// Stable operation IDs are the mutation identity and cannot overlap.
			}
		}
		if duplicate {
			s.mu.Unlock()
			_ = s.respond(Response{Type: "response", ID: req.ID, OperationID: req.OperationID, Status: "error", Error: "duplicate request id"})
			continue
		}
		opctx, cancel := context.WithTimeout(ctx, timeout)
		if req.DeadlineUnixMilli > 0 {
			d := time.UnixMilli(req.DeadlineUnixMilli)
			if deadline, ok := opctx.Deadline(); !ok || d.Before(deadline) {
				cancel()
				opctx, cancel = context.WithDeadline(ctx, d)
			}
		}
		s.active[req.ID] = cancel
		if req.OperationID != "" {
			s.active[req.OperationID] = cancel
		}
		s.mu.Unlock()
		select {
		case s.sem <- struct{}{}:
		default:
			s.mu.Lock()
			if cancel := s.active[req.ID]; cancel != nil {
				cancel()
			}
			delete(s.active, req.ID)
			if req.OperationID != "" {
				delete(s.active, req.OperationID)
			}
			s.mu.Unlock()
			_ = s.respond(Response{Type: "response", ID: req.ID, OperationID: req.OperationID, Status: "error", Error: "worker concurrency limit reached"})
			continue
		}
		s.wg.Add(1)
		go s.handle(opctx, req)
	}
}

func (s *server) handle(ctx context.Context, req Request) {
	defer s.wg.Done()
	defer func() { <-s.sem }()
	defer func() {
		s.mu.Lock()
		if cancel := s.active[req.ID]; cancel != nil {
			cancel()
		}
		delete(s.active, req.ID)
		if req.OperationID != "" {
			delete(s.active, req.OperationID)
		}
		s.mu.Unlock()
	}()
	var result any
	err := s.dispatch(ctx, req.Method, req.Payload, &result)
	resp := Response{Type: "response", ID: req.ID, OperationID: req.OperationID}
	if err != nil {
		resp.Status = "error"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			resp.Status = "cancelled"
		}
		resp.Error = err.Error()
	} else {
		resp.Status = "ok"
		resp.Result, err = json.Marshal(result)
		if err != nil {
			resp.Status = "error"
			resp.Error = err.Error()
		}
	}
	_ = s.respond(resp)
}

func (s *server) dispatch(ctx context.Context, method string, payload json.RawMessage, out *any) error {
	decode := func(v any) error {
		if len(payload) == 0 {
			return nil
		}
		return json.Unmarshal(payload, v)
	}
	switch method {
	case "file.list":
		var p struct {
			Path string `json:"path"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.List(p.Path)
		*out = v
		return e
	case "file.read":
		var p struct {
			Path string `json:"path"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		content, rev, e := s.ws.Read(p.Path)
		*out = struct {
			Content  string             `json:"content"`
			Revision workspace.Revision `json:"revision"`
		}{content, rev}
		return e
	case "file.search":
		var p struct {
			Query string `json:"query"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.Search(ctx, p.Query)
		*out = v
		return e
	case "file.write":
		var p struct {
			Path     string              `json:"path"`
			Expected *workspace.Revision `json:"expected_revision"`
			Content  string              `json:"content"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.Write(p.Path, p.Expected, p.Content)
		*out = v
		return e
	case "files.patch":
		var p workspace.PatchPlan
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.ApplyPatch(p)
		*out = v
		return e
	case "files.undo_patch":
		var p struct {
			ID workspace.ChangeID `json:"id"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.UndoPatch(p.ID)
		*out = v
		return e
	case "file.edit":
		var p struct {
			Path     string             `json:"path"`
			Expected workspace.Revision `json:"expected_revision"`
			Old      string             `json:"old"`
			New      string             `json:"new"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		v, e := s.ws.Edit(p.Path, p.Expected, p.Old, p.New)
		*out = v
		return e
	case "file.undo":
		var p struct {
			ChangeID workspace.ChangeID `json:"change_id"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		return s.ws.Undo(p.ChangeID)
	case "process.exec":
		var p struct {
			Argv           []string `json:"argv"`
			CWD            string   `json:"cwd"`
			MaxOutputBytes int      `json:"max_output_bytes"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		dir, e := s.cwd(p.CWD)
		if e != nil {
			return e
		}
		v, e := process.Run(ctx, process.Spec{Argv: p.Argv, Dir: dir, MaxOutputBytes: p.MaxOutputBytes})
		*out = v
		return e
	case "git.review":
		v, e := gitview.Load(ctx, s.root)
		*out = v
		return e
	case "browser.navigate":
		if s.browser == nil {
			return errors.New("worker browser capability is disabled")
		}
		var p struct {
			URL string `json:"url"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		if err := validateBrowserURL(p.URL); err != nil {
			return err
		}
		if err := s.browser.Navigate(p.URL); err != nil {
			return err
		}
		v, e := s.browser.Snapshot()
		*out = v
		return e
	case "browser.snapshot":
		if s.browser == nil {
			return errors.New("worker browser capability is disabled")
		}
		v, e := s.browser.Snapshot()
		*out = v
		return e
	case "browser.click":
		if s.browser == nil {
			return errors.New("worker browser capability is disabled")
		}
		var p struct {
			Ref string `json:"ref"`
		}
		if err := decode(&p); err != nil || strings.TrimSpace(p.Ref) == "" {
			if err != nil {
				return err
			}
			return errors.New("browser ref is required")
		}
		if err := s.browser.Click(p.Ref); err != nil {
			return err
		}
		v, e := s.browser.Snapshot()
		*out = v
		return e
	case "browser.type":
		if s.browser == nil {
			return errors.New("worker browser capability is disabled")
		}
		var p struct {
			Ref  string `json:"ref"`
			Text string `json:"text"`
		}
		if err := decode(&p); err != nil {
			return err
		}
		if strings.TrimSpace(p.Ref) == "" || len(p.Text) > 64<<10 || strings.IndexByte(p.Text, 0) >= 0 || !utf8.ValidString(p.Text) {
			return errors.New("invalid browser type arguments")
		}
		if err := s.browser.Type(p.Ref, p.Text); err != nil {
			return err
		}
		v, e := s.browser.Snapshot()
		*out = v
		return e
	case "browser.screenshot":
		if s.browser == nil {
			return errors.New("worker browser capability is disabled")
		}
		png, e := s.browser.Screenshot()
		if e != nil {
			return e
		}
		png, e = boundImage(png)
		*out = BrowserResult{PNG: png}
		return e
	case "desktop.capabilities":
		if s.desktopManager == nil {
			return errors.New("worker desktop capability is disabled")
		}
		*out = s.desktopManager.Capabilities()
		return nil
	case "desktop.capture":
		if s.desktop == nil {
			return errors.New("worker desktop capability is disabled")
		}
		observation, e := s.desktop.Capture(ctx)
		if e != nil {
			return e
		}
		png, e := readBoundedImage(observation.PNGPath)
		// The path is an implementation detail of the guest adapter. Remove
		// each captured artifact after it has crossed the worker boundary so a
		// long-lived worker cannot accumulate screenshots indefinitely.
		removeErr := os.Remove(observation.PNGPath)
		if e != nil {
			return e
		}
		png, e = boundImage(png)
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove desktop capture artifact: %w", removeErr)
		}
		*out = DesktopResult{Observation: observation, PNG: png}
		return e
	case "desktop.move", "desktop.click", "desktop.keypress", "desktop.type":
		if s.desktop == nil {
			return errors.New("worker desktop capability is disabled")
		}
		var p DesktopAction
		if err := decode(&p); err != nil {
			return err
		}
		switch method {
		case "desktop.move":
			return s.desktop.Move(ctx, p.ObservationID, p.Display, p.X, p.Y)
		case "desktop.click":
			return s.desktop.Click(ctx, p.ObservationID, p.Display, p.X, p.Y, p.Button)
		case "desktop.keypress":
			return s.desktop.KeyPress(ctx, p.ObservationID, p.Display, p.Keys...)
		case "desktop.type":
			return s.desktop.Type(ctx, p.ObservationID, p.Display, p.Text)
		}
		return nil
	default:
		return fmt.Errorf("unknown worker method %q", method)
	}
}

func validateBrowserURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return errors.New("browser URL must use HTTP(S), a hostname, and no embedded credentials")
	}
	return nil
}

func boundImage(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > MaxRemoteImageBytes {
		return nil, fmt.Errorf("image exceeds %d bytes", MaxRemoteImageBytes)
	}
	return data, nil
}

func readBoundedImage(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("desktop capture artifact is not a regular file")
	}
	if info.Size() <= 0 || info.Size() > MaxRemoteImageBytes {
		return nil, fmt.Errorf("desktop capture artifact exceeds %d bytes", MaxRemoteImageBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxRemoteImageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > MaxRemoteImageBytes {
		return nil, fmt.Errorf("desktop capture artifact exceeds %d bytes", MaxRemoteImageBytes)
	}
	return data, nil
}

func (s *server) cwd(name string) (string, error) {
	if name == "" || name == "." {
		return s.root, nil
	}
	if filepath.IsAbs(name) {
		return "", errors.New("worker: absolute cwd is not allowed")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("worker: cwd escapes root")
	}
	candidate := filepath.Join(s.root, clean)
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("worker: cwd escapes root")
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("worker: cwd is not a directory")
	}
	return real, nil
}
func (s *server) cancel(id string) {
	s.mu.Lock()
	if f := s.active[id]; f != nil {
		f()
	}
	s.mu.Unlock()
}
func (s *server) cancelAll() {
	s.mu.Lock()
	for _, f := range s.active {
		f()
	}
	s.mu.Unlock()
}
func (s *server) respond(resp Response) error { return s.write(resp) }
func (s *server) write(v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b)+1 > s.max {
		return fmt.Errorf("%w: response exceeds limit", ErrProtocol)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	b = append(b, '\n')
	for len(b) > 0 {
		n, e := s.writer.Write(b)
		if e != nil {
			return e
		}
		b = b[n:]
	}
	return nil
}
func (s *server) read(ctx context.Context, out any) error {
	line, e := readFrame(s.reader, s.max)
	if e == nil {
		e = json.Unmarshal(line, out)
	}
	return e
}

func readFrame(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > max {
			return nil, fmt.Errorf("%w: frame exceeds %d bytes", ErrProtocol, max)
		}
		if err == nil {
			return line[:len(line)-1], nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
}
func canonicalRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("worker: root is required")
	}
	a, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	r, e := filepath.EvalSymlinks(a)
	if e != nil {
		return "", e
	}
	return filepath.Clean(r), nil
}
func randomID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		h := sha256.Sum256([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
		b = h[:16]
	}
	return hex.EncodeToString(b)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

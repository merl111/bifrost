// Package daemon provides the local, single-writer control plane. It speaks
// bounded HTTP/JSON and SSE over a permissioned Unix socket; it does not know
// how an agent or provider is implemented.
package daemon

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"bifrost/internal/atomicfile"
	"bifrost/internal/filelock"
)

const (
	ProtocolMajor          = 1
	ProtocolMinor          = 0
	DefaultMaxMessageBytes = 1 << 20
	DefaultMaxEvents       = 10000
	DefaultMaxOutputBytes  = 1 << 20
	DefaultMaxConcurrent   = 4
)

var (
	ErrAlreadyRunning = errors.New("daemon is already running")
	ErrCursorExpired  = errors.New("daemon event cursor expired")
	ErrUnauthorized   = errors.New("daemon authentication failed")
	ErrLimit          = errors.New("daemon request exceeds limit")
)

type Config struct {
	StateDir        string
	SocketPath      string
	Token           string
	MaxMessageBytes int
	MaxEvents       int
	MaxOutputBytes  int
	MaxConcurrent   int
}

type AgentRequest struct {
	Root       string   `json:"root,omitempty"`
	StateDir   string   `json:"state_dir,omitempty"`
	Project    string   `json:"project,omitempty"`
	Repository string   `json:"repository,omitempty"`
	Profile    string   `json:"profile,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Model      string   `json:"model,omitempty"`
	Args       []string `json:"args,omitempty"`
	Prompt     string   `json:"prompt,omitempty"`
	SessionID  string   `json:"session_id,omitempty"`
}

// Event is the durable, bounded stream item supplied to handlers and clients.
// Handlers should set Type and Payload; Cursor, ID, JobID, and Time are owned
// by the daemon.
type Event struct {
	Cursor  uint64          `json:"cursor"`
	ID      string          `json:"event_id"`
	JobID   string          `json:"job_id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Time    time.Time       `json:"time"`
}

type Handler func(context.Context, AgentRequest, func(Event) error) (json.RawMessage, error)

type Job struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	CreatedAt  time.Time       `json:"created_at"`
	StartedAt  time.Time       `json:"started_at,omitempty"`
	FinishedAt time.Time       `json:"finished_at,omitempty"`
	LastCursor uint64          `json:"last_cursor"`
	Result     json.RawMessage `json:"result,omitempty"`
	Error      string          `json:"error,omitempty"`
}

type eventRecord struct {
	Event
	Schema int `json:"schema"`
}
type stateFile struct {
	Schema int   `json:"schema"`
	Jobs   []Job `json:"jobs"`
}

type Server struct {
	cfg        Config
	handler    Handler
	lock       *filelock.Lock
	listener   net.Listener
	http       *http.Server
	token      string
	bootID     string
	statePath  string
	eventsPath string
	mu         sync.Mutex
	jobs       map[string]*jobState
	events     []Event
	nextCursor uint64
	subs       map[string]map[chan Event]struct{}
	closed     chan struct{}
	closeOnce  sync.Once
	slots      chan struct{}
	wg         sync.WaitGroup
}

type jobState struct {
	Job
	request AgentRequest
	cancel  context.CancelFunc
}

// Start acquires the singleton lock, publishes a private Unix socket, and
// returns immediately. The injected handler runs asynchronously per job.
func Start(ctx context.Context, cfg Config, handler Handler) (*Server, error) {
	if handler == nil {
		return nil, errors.New("daemon handler is required")
	}
	cfg = normalizeConfig(cfg)
	if cfg.StateDir == "" {
		return nil, errors.New("daemon state directory is required")
	}
	if err := os.MkdirAll(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.StateDir, 0700); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(filepath.Join(cfg.StateDir, "daemon.lock"))
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, handler: handler, lock: lock, token: cfg.Token, bootID: randomToken(), statePath: filepath.Join(cfg.StateDir, "jobs.json"), eventsPath: filepath.Join(cfg.StateDir, "events.jsonl"), jobs: map[string]*jobState{}, subs: map[string]map[chan Event]struct{}{}, closed: make(chan struct{}), slots: make(chan struct{}, cfg.MaxConcurrent)}
	if s.token == "" {
		s.token = randomToken()
	}
	if err := s.persistToken(); err != nil {
		s.Close()
		return nil, err
	}
	if err := s.load(); err != nil {
		s.Close()
		return nil, err
	}
	ln, err := listenSocket(cfg.SocketPath)
	if err != nil {
		s.Close()
		return nil, err
	}
	s.listener = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/hello", s.handleHello)
	mux.HandleFunc("/v1/jobs", s.handleJobs)
	mux.HandleFunc("/v1/jobs/", s.handleJob)
	mux.HandleFunc("/v1/shutdown", s.handleShutdown)
	s.http = &http.Server{Handler: s.authMiddleware(mux), ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		}
	}()
	if ctx != nil {
		go func() {
			select {
			case <-ctx.Done():
				_ = s.Shutdown()
			case <-s.closed:
			}
		}()
	}
	return s, nil
}

func normalizeConfig(c Config) Config {
	if c.MaxMessageBytes <= 0 {
		c.MaxMessageBytes = DefaultMaxMessageBytes
	}
	if c.MaxEvents <= 0 {
		c.MaxEvents = DefaultMaxEvents
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.SocketPath == "" && c.StateDir != "" {
		c.SocketPath = filepath.Join(c.StateDir, "daemon.sock")
	}
	return c
}

func (s *Server) persistToken() error {
	return atomicfile.Write(filepath.Join(s.cfg.StateDir, "daemon.token"), []byte(s.token+"\n"))
}
func (s *Server) load() error {
	if b, err := os.ReadFile(s.statePath); err == nil {
		var sf stateFile
		if err := json.Unmarshal(b, &sf); err != nil {
			return fmt.Errorf("daemon jobs state: %w", err)
		}
		for _, j := range sf.Jobs {
			copy := j
			if copy.Status == "running" {
				copy.Status = "interrupted"
				copy.Error = "daemon restarted; execution was interrupted"
			}
			s.jobs[j.ID] = &jobState{Job: copy}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	b, err := os.ReadFile(s.eventsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) > 64<<20 {
		return ErrLimit
	}
	lines := bytes.Split(b, []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		var rec eventRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			if i == len(lines)-1 {
				return nil
			}
			return fmt.Errorf("daemon event journal: %w", err)
		}
		s.events = append(s.events, rec.Event)
		if rec.Cursor > s.nextCursor {
			s.nextCursor = rec.Cursor
		}
	}
	if len(s.events) > s.cfg.MaxEvents {
		s.events = s.events[len(s.events)-s.cfg.MaxEvents:]
	}
	return nil
}

// Shutdown stops accepting requests, cancels active handlers, and releases
// the singleton lock. It never replays interrupted work.
func (s *Server) Shutdown() error {
	var out error
	s.closeOnce.Do(func() {
		close(s.closed)
		s.mu.Lock()
		for _, j := range s.jobs {
			if j.cancel != nil {
				j.cancel()
			}
		}
		s.mu.Unlock()
		if s.http != nil {
			out = s.http.Shutdown(context.Background())
		}
		s.wg.Wait()
		if s.listener != nil {
			_ = os.Remove(s.cfg.SocketPath)
		}
		if s.lock != nil {
			if e := s.lock.Close(); out == nil {
				out = e
			}
		}
	})
	return out
}
func (s *Server) Close() error { return s.Shutdown() }

// Done is closed after Shutdown has stopped the listener and waited for all
// active handlers. It lets foreground callers wait on the daemon lifecycle
// rather than only on their own context.
func (s *Server) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.closed
}

func (s *Server) submit(ctx context.Context, req AgentRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id := "job-" + randomToken()
	now := time.Now().UTC()
	js := &jobState{Job: Job{ID: id, Status: "queued", CreatedAt: now}, request: req}
	s.mu.Lock()
	s.jobs[id] = js
	err := s.persistLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	s.emit(id, Event{Type: "job.queued"})
	s.wg.Add(1)
	go s.runJob(id)
	return id, nil
}
func (s *Server) runJob(id string) {
	defer s.wg.Done()
	jobctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		cancel()
		return
	}
	j.cancel = cancel
	s.mu.Unlock()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-jobctx.Done():
		s.finishQueuedJob(id, context.Canceled)
		return
	case <-s.closed:
		cancel()
		s.finishQueuedJob(id, context.Canceled)
		return
	}
	s.mu.Lock()
	j = s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		cancel()
		return
	}
	j.Status = "running"
	j.StartedAt = time.Now().UTC()
	_ = s.persistLocked()
	req := j.request
	s.mu.Unlock()
	s.emit(id, Event{Type: "job.started"})
	result, err := s.handler(jobctx, req, func(ev Event) error { ev.JobID = id; return s.emit(id, ev) })
	cancel()
	s.mu.Lock()
	if err != nil {
		if errors.Is(err, context.Canceled) {
			j.Status = "cancelled"
		} else {
			j.Status = "failed"
		}
		j.Error = err.Error()
	} else {
		j.Status = "completed"
		if len(result) > s.cfg.MaxOutputBytes {
			j.Status = "failed"
			j.Error = "handler result exceeds output limit"
		} else {
			j.Result = append(json.RawMessage(nil), result...)
		}
	}
	j.FinishedAt = time.Now().UTC()
	j.cancel = nil
	status := j.Status
	_ = s.persistLocked()
	s.mu.Unlock()
	if status != "completed" {
		s.emit(id, Event{Type: "job." + status, Payload: json.RawMessage(`{"error":"handler failed"}`)})
	} else {
		s.emit(id, Event{Type: "job.completed"})
	}
}

func (s *Server) finishQueuedJob(id string, runErr error) {
	s.mu.Lock()
	j := s.jobs[id]
	if j == nil || j.Status != "queued" {
		s.mu.Unlock()
		return
	}
	j.Status = "cancelled"
	if runErr != nil {
		j.Error = runErr.Error()
	}
	j.FinishedAt = time.Now().UTC()
	j.cancel = nil
	_ = s.persistLocked()
	s.mu.Unlock()
	_ = s.emit(id, Event{Type: "job.cancelled", Payload: json.RawMessage(`{"error":"job cancelled"}`)})
}
func (s *Server) emit(jobID string, ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.Type == "" {
		return errors.New("daemon event type is empty")
	}
	if len(ev.Payload) > s.cfg.MaxOutputBytes {
		return ErrLimit
	}
	s.nextCursor++
	ev.Cursor = s.nextCursor
	ev.ID = "event-" + randomToken()
	ev.JobID = jobID
	ev.Time = time.Now().UTC()
	trimmed := len(s.events) >= s.cfg.MaxEvents
	s.events = append(s.events, ev)
	if len(s.events) > s.cfg.MaxEvents {
		s.events = s.events[len(s.events)-s.cfg.MaxEvents:]
	}
	if j := s.jobs[jobID]; j != nil {
		j.LastCursor = ev.Cursor
	}
	if trimmed {
		var all bytes.Buffer
		for _, kept := range s.events {
			line, err := json.Marshal(eventRecord{Event: kept, Schema: 1})
			if err != nil {
				return err
			}
			all.Write(line)
			all.WriteByte('\n')
		}
		if err := atomicfile.Write(s.eventsPath, all.Bytes()); err != nil {
			return err
		}
	} else {
		rec := eventRecord{Event: ev, Schema: 1}
		line, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		line = append(line, '\n')
		f, err := os.OpenFile(s.eventsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		if _, err = f.Write(line); err == nil {
			err = f.Sync()
		}
		_ = f.Close()
		if err != nil {
			return err
		}
	}
	if err := s.persistLocked(); err != nil {
		return err
	}
	if subs := s.subs[jobID]; subs != nil {
		for ch := range subs {
			select {
			case ch <- ev:
			default:
			}
		}
	}
	return nil
}
func (s *Server) persistLocked() error {
	list := make([]Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, j.Job)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	b, _ := json.Marshal(stateFile{Schema: 1, Jobs: list})
	b = append(b, '\n')
	return atomicfile.Write(s.statePath, b)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+s.token {
			http.Error(w, ErrUnauthorized.Error(), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) handleHello(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{"protocol_major": ProtocolMajor, "protocol_minor": ProtocolMinor, "boot_id": s.bootID, "capabilities": []string{"jobs", "events", "cancel"}})
}
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req AgentRequest
		if err := decodeBody(r, s.cfg.MaxMessageBytes, &req); err != nil {
			writeError(w, err)
			return
		}
		id, err := s.submit(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]string{"id": id})
	case http.MethodGet:
		s.mu.Lock()
		list := make([]Job, 0, len(s.jobs))
		for _, j := range s.jobs {
			list = append(list, j.Job)
		}
		s.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
		s.writeJSON(w, http.StatusOK, list)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "events" {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleEvents(w, r, id)
		return
	}
	if len(parts) == 3 && parts[1] == "events" && parts[2] == "stream" {
		s.handleStream(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.mu.Lock()
		j := s.jobs[id]
		if j != nil && j.cancel != nil {
			j.cancel()
		}
		s.mu.Unlock()
		if j == nil {
			http.NotFound(w, r)
			return
		}
		s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelling"})
		return
	}
	if len(parts) != 1 {
		http.NotFound(w, r)
		return
	}
	s.mu.Lock()
	j := s.jobs[id]
	var out Job
	if j != nil {
		out = j.Job
	}
	s.mu.Unlock()
	if j == nil {
		http.NotFound(w, r)
		return
	}
	s.writeJSON(w, http.StatusOK, out)
}
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, id string) {
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	ev, err := s.eventsFor(id, after)
	if err != nil {
		writeError(w, err)
		return
	}
	s.writeJSON(w, http.StatusOK, ev)
}
func (s *Server) eventsFor(id string, after uint64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) > 0 && after > 0 && after < s.events[0].Cursor-1 {
		return nil, ErrCursorExpired
	}
	out := make([]Event, 0)
	for _, ev := range s.events {
		if ev.JobID == id && ev.Cursor > after {
			out = append(out, ev)
		}
	}
	return out, nil
}
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request, id string) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeError(w, errors.New("SSE unsupported"))
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	s.mu.Lock()
	backlog, err := s.eventsForLocked(id, after)
	var ch chan Event
	if err == nil {
		s.subs[id] = ensureSubs(s.subs[id])
		ch = make(chan Event, 32)
		s.subs[id][ch] = struct{}{}
	}
	terminal := false
	for _, ev := range backlog {
		if ev.Type == "job.completed" || ev.Type == "job.failed" || ev.Type == "job.cancelled" || ev.Type == "job.interrupted" {
			terminal = true
			break
		}
	}
	if j := s.jobs[id]; j != nil && j.Status == "interrupted" {
		terminal = true
	}
	s.mu.Unlock()
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	for _, ev := range backlog {
		if !writeSSE(w, ev) {
			return
		}
		fl.Flush()
	}
	if terminal {
		s.removeSub(id, ch)
		return
	}
	for {
		select {
		case <-r.Context().Done():
			s.removeSub(id, ch)
			return
		case ev := <-ch:
			if !writeSSE(w, ev) {
				s.removeSub(id, ch)
				return
			}
			fl.Flush()
			if strings.HasPrefix(ev.Type, "job.") && (ev.Type == "job.completed" || ev.Type == "job.failed" || ev.Type == "job.cancelled") {
				s.removeSub(id, ch)
				return
			}
		}
	}
}
func (s *Server) eventsForLocked(id string, after uint64) ([]Event, error) {
	if len(s.events) > 0 && after > 0 && after < s.events[0].Cursor-1 {
		return nil, ErrCursorExpired
	}
	out := []Event{}
	for _, ev := range s.events {
		if ev.JobID == id && ev.Cursor > after {
			out = append(out, ev)
		}
	}
	return out, nil
}
func ensureSubs(m map[chan Event]struct{}) map[chan Event]struct{} {
	if m == nil {
		return map[chan Event]struct{}{}
	}
	return m
}
func (s *Server) removeSub(id string, ch chan Event) {
	s.mu.Lock()
	if m := s.subs[id]; m != nil {
		delete(m, ch)
	}
	s.mu.Unlock()
}
func (s *Server) handleShutdown(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusAccepted, map[string]string{"status": "stopping"})
	go s.Shutdown()
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "daemon response encoding failed", http.StatusInternalServerError)
		return
	}
	if len(b) > s.cfg.MaxMessageBytes {
		http.Error(w, ErrLimit.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

type Client struct {
	cfg   Config
	http  *http.Client
	token string
}

func (c *Client) Close() error {
	if c == nil || c.http == nil {
		return nil
	}
	if tr, ok := c.http.Transport.(interface{ CloseIdleConnections() }); ok {
		tr.CloseIdleConnections()
	}
	return nil
}

func Dial(ctx context.Context, cfg Config) (*Client, error) {
	cfg = normalizeConfig(cfg)
	token := cfg.Token
	if token == "" {
		b, err := os.ReadFile(filepath.Join(cfg.StateDir, "daemon.token"))
		if err != nil {
			return nil, err
		}
		token = strings.TrimSpace(string(b))
	}
	if token == "" {
		return nil, ErrUnauthorized
	}
	c := &Client{cfg: cfg, token: token}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.SocketPath)
	}}
	c.http = &http.Client{Transport: tr}
	var hello map[string]any
	if err := c.do(ctx, http.MethodGet, "/v1/hello", nil, &hello); err != nil {
		return nil, err
	}
	return c, nil
}
func (c *Client) Submit(ctx context.Context, req AgentRequest) (string, error) {
	var out struct {
		ID string `json:"id"`
	}
	err := c.do(ctx, http.MethodPost, "/v1/jobs", req, &out)
	return out.ID, err
}
func (c *Client) Status(ctx context.Context, id string) (Job, error) {
	var out Job
	err := c.do(ctx, http.MethodGet, "/v1/jobs/"+urlPath(id), nil, &out)
	return out, err
}
func (c *Client) List(ctx context.Context) ([]Job, error) {
	var out []Job
	err := c.do(ctx, http.MethodGet, "/v1/jobs", nil, &out)
	return out, err
}
func (c *Client) Events(ctx context.Context, id string, after uint64) ([]Event, error) {
	var out []Event
	err := c.do(ctx, http.MethodGet, "/v1/jobs/"+urlPath(id)+"/events?after="+strconv.FormatUint(after, 10), nil, &out)
	return out, err
}
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/v1/jobs/"+urlPath(id)+"/cancel", nil, nil)
}
func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/shutdown", nil, nil)
}
func (c *Client) Attach(ctx context.Context, id string, after uint64, fn func(Event) error) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://daemon/v1/jobs/"+urlPath(id)+"/events/stream?after="+strconv.FormatUint(after, 10), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return readHTTPError(resp)
	}
	scan := bufio.NewScanner(io.LimitReader(resp.Body, int64(c.cfg.MaxMessageBytes)*2))
	scan.Buffer(make([]byte, 1024), c.cfg.MaxMessageBytes)
	for scan.Scan() {
		line := scan.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			return err
		}
		if err := fn(ev); err != nil {
			return err
		}
	}
	return scan.Err()
}
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if len(b) > c.cfg.MaxMessageBytes {
			return ErrLimit
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readHTTPError(resp)
	}
	if out == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.cfg.MaxMessageBytes)+1))
	if err != nil {
		return err
	}
	if len(b) > c.cfg.MaxMessageBytes {
		return ErrLimit
	}
	return json.Unmarshal(b, out)
}

func decodeBody(r *http.Request, max int, out any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, int64(max)+1))
	if err != nil {
		return err
	}
	if len(b) > max {
		return ErrLimit
	}
	if len(b) == 0 {
		return errors.New("request body is required")
	}
	return json.Unmarshal(b, out)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, _ := json.Marshal(v)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}
func writeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, ErrUnauthorized) {
		status = http.StatusUnauthorized
	}
	if errors.Is(err, ErrCursorExpired) {
		status = http.StatusGone
	}
	if errors.Is(err, ErrLimit) {
		status = http.StatusRequestEntityTooLarge
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
func readHTTPError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var out struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(b, &out) == nil && out.Error != "" {
		return errors.New(out.Error)
	}
	return fmt.Errorf("daemon HTTP status %d", resp.StatusCode)
}
func writeSSE(w io.Writer, ev Event) bool {
	b, _ := json.Marshal(ev)
	_, err := fmt.Fprintf(w, "id: %d\ndata: %s\n\n", ev.Cursor, b)
	return err == nil
}
func urlPath(s string) string {
	return strings.NewReplacer("%", "%25", "/", "%2F", "?", "%3F", "#", "%23").Replace(s)
}
func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		b = []byte(fmt.Sprintf("%d", time.Now().UnixNano()))
	}
	return hex.EncodeToString(b)
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func listenSocket(path string) (net.Listener, error) {
	if path == "" {
		return nil, errors.New("daemon socket path is required")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("daemon socket path exists and is not a socket")
		}
		owned, err := daemonSocketOwned(info)
		if err != nil {
			return nil, err
		}
		if !owned {
			return nil, errors.New("daemon socket is not owned by current user")
		}
		conn, err := net.DialTimeout("unix", path, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil, ErrAlreadyRunning
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale daemon socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0600); err != nil {
		ln.Close()
		os.Remove(path)
		return nil, err
	}
	return ln, nil
}

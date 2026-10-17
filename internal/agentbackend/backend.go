// Package agentbackend adapts vendor coding-agent CLIs to the Bifrost session
// runner. The adapters intentionally use the vendors' supported local CLI
// interfaces; they do not exchange API tokens for subscription credentials.
package agentbackend

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Kind string

const (
	KindCursorACP Kind = "cursor-acp"
	KindClaudeCLI Kind = "claude-cli"
	KindCursor    Kind = "cursor"
	KindClaude    Kind = "claude"
	// Short aliases are useful when decoding user configuration.
	Cursor Kind = KindCursorACP
	Claude Kind = KindClaudeCLI
)

var (
	ErrInvalidConfig          = errors.New("agentbackend: invalid configuration")
	ErrUnsupported            = errors.New("agentbackend: unsupported operation")
	ErrCursorAccountIsolation = errors.New("agentbackend: Cursor per-account isolation is unsupported on macOS")
)

// Config controls one external CLI invocation. AccountDir is passed through
// the vendor-supported per-account configuration variable and is never used
// as HOME. LoginCommand and StatusCommand are argv arrays, not shell strings.
type Config struct {
	Kind         Kind
	Executable   string
	Args         []string
	AccountDir   string
	Root         string
	Model        string
	Instructions string
	AllowWrite   bool
	AllowExec    bool
	MaxSteps     int
	// MaxOutputBytes bounds each JSON line and accumulated final text.
	MaxOutputBytes int

	LoginCommand  []string
	StatusCommand []string
	LogoutCommand []string
}

type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

// Event is deliberately provider-neutral. Data carries a bounded copy of a
// provider event for callers that need richer tool/status presentation.
type Event struct {
	Kind   string
	Text   string
	Tool   string
	Status string
	Usage  *Usage
	Data   json.RawMessage
}

type Result struct {
	SessionID  string
	Text       string
	Usage      Usage
	StopReason string
	terminal   bool
}

type Backend interface {
	Run(context.Context, string, string, func(Event)) (Result, error)
}

type Runner struct{ cfg Config }

// eventEmitter serializes callbacks because provider stdout and stderr are
// consumed concurrently. The session/event store callback is then safe to
// implement without its own provider-specific locking.
type eventEmitter struct {
	mu    sync.Mutex
	fn    func(Event)
	bytes int
	limit int
}

func newEventEmitter(fn func(Event), limit int) *eventEmitter {
	return &eventEmitter{fn: fn, limit: limit}
}

func (e *eventEmitter) emit(event Event) {
	if e == nil || e.fn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.bytes >= e.limit {
		return
	}
	cost := len(event.Text) + len(event.Data)
	if cost == 0 {
		cost = 1
	}
	if cost > e.limit-e.bytes {
		return
	}
	e.bytes += cost
	e.fn(event)
}

func redactDiagnostic(s string) string {
	for _, prefix := range []string{"ANTHROPIC_API_KEY=", "ANTHROPIC_AUTH_TOKEN=", "CURSOR_API_KEY=", "CURSOR_AUTH_TOKEN=", "Bearer ", "sk-ant-"} {
		searchFrom := 0
		for {
			rel := strings.Index(s[searchFrom:], prefix)
			if rel < 0 {
				break
			}
			i := searchFrom + rel
			start := i + len(prefix)
			end := start
			for end < len(s) && !strings.ContainsRune(" \t\r\n,;)]}", rune(s[end])) {
				end++
			}
			s = s[:start] + "[redacted]" + s[end:]
			searchFrom = start + len("[redacted]")
		}
	}
	return s
}

// drainStderr never retains more than one scanner line and continues draining
// after the event budget is exhausted, so a verbose child cannot deadlock on a
// full stderr pipe. Diagnostics are emitted only while the byte budget holds.
func drainStderr(src io.Reader, max int, emit func(Event)) {
	s := bufio.NewScanner(src)
	s.Buffer(make([]byte, 1024), max)
	used := 0
	for s.Scan() {
		line := s.Text()
		if used+len(line) <= max {
			emit(Event{Kind: "status", Status: "stderr", Text: redactDiagnostic(line)})
			used += len(line)
		}
	}
	if s.Err() != nil {
		_, _ = io.Copy(io.Discard, src)
	}
}

// New validates and returns a reusable backend. Run itself remains safe to
// call concurrently because each invocation owns its child process.
func New(cfg Config) (*Runner, error) {
	cfg, err := normalize(cfg)
	if err != nil {
		return nil, err
	}
	return &Runner{cfg: cfg}, nil
}

func Run(ctx context.Context, cfg Config, prompt, sessionID string, emit func(Event)) (Result, error) {
	r, err := New(cfg)
	if err != nil {
		return Result{}, err
	}
	return r.Run(ctx, prompt, sessionID, emit)
}

func (r *Runner) Run(ctx context.Context, prompt, sessionID string, emit func(Event)) (Result, error) {
	if r == nil {
		return Result{}, ErrInvalidConfig
	}
	if strings.TrimSpace(prompt) == "" {
		return Result{}, fmt.Errorf("%w: prompt is required", ErrInvalidConfig)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	switch r.cfg.Kind {
	case KindCursorACP, KindCursor:
		return runCursor(ctx, r.cfg, prompt, sessionID, emit)
	case KindClaudeCLI, KindClaude:
		return runClaude(ctx, r.cfg, prompt, sessionID, emit)
	default:
		return Result{}, fmt.Errorf("%w: %q", ErrUnsupported, r.cfg.Kind)
	}
}

// Login runs an explicitly configured vendor login command. It is provided
// for subscription login flows; the adapter never parses or stores tokens.
func (r *Runner) Login(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	if r == nil || len(r.cfg.LoginCommand) == 0 {
		return fmt.Errorf("%w: login command is not configured", ErrUnsupported)
	}
	return runAuthCommand(ctx, r.cfg, r.cfg.LoginCommand, stdin, stdout, stderr)
}

func (r *Runner) Status(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	if r == nil || len(r.cfg.StatusCommand) == 0 {
		return fmt.Errorf("%w: status command is not configured", ErrUnsupported)
	}
	return runAuthCommand(ctx, r.cfg, r.cfg.StatusCommand, stdin, stdout, stderr)
}

func (r *Runner) Logout(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer) error {
	if r == nil || len(r.cfg.LogoutCommand) == 0 {
		return fmt.Errorf("%w: logout command is not configured", ErrUnsupported)
	}
	return runAuthCommand(ctx, r.cfg, r.cfg.LogoutCommand, stdin, stdout, stderr)
}

func Login(ctx context.Context, cfg Config, stdin io.Reader, stdout, stderr io.Writer) error {
	r, err := New(cfg)
	if err != nil {
		return err
	}
	return r.Login(ctx, stdin, stdout, stderr)
}

func Status(ctx context.Context, cfg Config, stdin io.Reader, stdout, stderr io.Writer) error {
	r, err := New(cfg)
	if err != nil {
		return err
	}
	return r.Status(ctx, stdin, stdout, stderr)
}

func Logout(ctx context.Context, cfg Config, stdin io.Reader, stdout, stderr io.Writer) error {
	r, err := New(cfg)
	if err != nil {
		return err
	}
	return r.Logout(ctx, stdin, stdout, stderr)
}

// Models runs the vendor CLI's model listing command in the isolated account
// environment and returns parsed model slugs and display names.
func Models(ctx context.Context, cfg Config) ([]ModelEntry, error) {
	cfg, err := normalize(cfg)
	if err != nil {
		return nil, err
	}
	var argv []string
	switch cfg.Kind {
	case KindCursorACP, KindCursor:
		argv = []string{cfg.Executable, "models"}
	default:
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	configureChild(cmd)
	cmd.Dir, cmd.Env = cfg.Root, environment(cfg)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("agentbackend: %s models: %w", argv[0], err)
	}
	return parseCursorModelsOutput(string(out)), nil
}

type ModelEntry struct {
	Slug        string
	DisplayName string
}

func parseCursorModelsOutput(raw string) []ModelEntry {
	var out []ModelEntry
	seen := make(map[string]bool)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.EqualFold(line, "Available models") || strings.HasPrefix(line, "Tip:") || strings.HasPrefix(line, "No models") {
			continue
		}
		slug, display, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		slug = strings.TrimSpace(slug)
		display = strings.TrimSpace(display)
		if slug == "" || strings.ContainsAny(slug, " \t") || seen[slug] {
			continue
		}
		seen[slug] = true
		out = append(out, ModelEntry{
			Slug:        slug,
			DisplayName: display,
		})
	}
	return out
}

func normalize(cfg Config) (Config, error) {
	switch cfg.Kind {
	case KindCursorACP, KindClaudeCLI, KindCursor, KindClaude:
	default:
		return cfg, fmt.Errorf("%w: kind must be cursor-acp or claude-cli", ErrInvalidConfig)
	}
	if strings.TrimSpace(cfg.Root) == "" {
		return cfg, fmt.Errorf("%w: root is required", ErrInvalidConfig)
	}
	if runtime.GOOS == "darwin" && (cfg.Kind == KindCursorACP || cfg.Kind == KindCursor) && strings.TrimSpace(cfg.AccountDir) != "" {
		return cfg, ErrCursorAccountIsolation
	}
	for _, arg := range cfg.Args {
		if unsafeCLIArg(arg) {
			return cfg, fmt.Errorf("%w: unsafe CLI argument %q", ErrInvalidConfig, arg)
		}
	}
	root, err := filepath.Abs(cfg.Root)
	if err != nil {
		return cfg, fmt.Errorf("%w: root: %v", ErrInvalidConfig, err)
	}
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return cfg, fmt.Errorf("%w: root: %v", ErrInvalidConfig, err)
	}
	cfg.Root = root
	if cfg.AccountDir != "" {
		cfg.AccountDir, err = filepath.Abs(cfg.AccountDir)
		if err != nil {
			return cfg, fmt.Errorf("%w: account directory: %v", ErrInvalidConfig, err)
		}
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = 8
	}
	if cfg.MaxSteps > 100 {
		return cfg, fmt.Errorf("%w: max steps must be <= 100", ErrInvalidConfig)
	}
	if cfg.MaxOutputBytes <= 0 {
		cfg.MaxOutputBytes = 8 << 20
	}
	if cfg.MaxOutputBytes < 2 || cfg.MaxOutputBytes > 64<<20 {
		return cfg, fmt.Errorf("%w: max output bytes out of range", ErrInvalidConfig)
	}
	if cfg.Executable == "" {
		if cfg.Kind == KindCursorACP || cfg.Kind == KindCursor {
			cfg.Executable = "cursor-agent"
		} else {
			cfg.Executable = "claude"
		}
	}
	return cfg, nil
}

func unsafeCLIArg(arg string) bool {
	lower := strings.ToLower(arg)
	if lower == "--dangerously-skip-permissions" || lower == "--force" || lower == "--bare" || lower == "--api-key" || lower == "--auth-token" {
		return true
	}
	for _, prefix := range []string{"--dangerously-skip-permissions=", "--permission-mode=bypass", "--permission-mode=unsafe", "--api-key=", "--auth-token="} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

func environment(cfg Config) []string {
	// Keep the process environment useful (PATH, locale, proxy) while removing
	// credential variables that could silently select a different account.
	sensitive := map[string]bool{
		"ANTHROPIC_API_KEY": true, "ANTHROPIC_AUTH_TOKEN": true,
		"ANTHROPIC_OAUTH_TOKEN": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
		"CLAUDE_CODE_OAUTH_REFRESH_TOKEN": true, "CLAUDE_CODE_OAUTH_SCOPES": true,
		"ANTHROPIC_PROFILE": true, "PROVIDER_MANAGED_BY_HOST": true,
		"CLAUDE_CODE_USE_ANTHROPIC_AWS": true, "ANTHROPIC_MODEL": true,
		"CURSOR_API_KEY": true, "CURSOR_AUTH_TOKEN": true,
		"CURSOR_CONFIG_DIR": true, "CURSOR_DATA_DIR": true, "CLAUDE_CONFIG_DIR": true,
		"XDG_CONFIG_HOME":    true,
		"ANTHROPIC_BASE_URL": true, "ANTHROPIC_BEDROCK_BASE_URL": true,
		"ANTHROPIC_VERTEX_PROJECT_ID": true, "ANTHROPIC_VERTEX_REGION": true,
		"CLAUDE_CODE_USE_BEDROCK": true, "CLAUDE_CODE_USE_VERTEX": true,
		"CLAUDE_CODE_USE_FOUNDRY": true, "AWS_ACCESS_KEY_ID": true,
		"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true,
		"AWS_PROFILE": true, "AWS_REGION": true, "AWS_DEFAULT_REGION": true,
		"AWS_ENDPOINT_URL": true, "ANTHROPIC_CUSTOM_HEADERS": true,
		"GOOGLE_APPLICATION_CREDENTIALS": true,
		"CLOUDSDK_AUTH_ACCESS_TOKEN":     true,
	}
	env := make([]string, 0, len(os.Environ())+2)
	for _, value := range os.Environ() {
		name, _, ok := strings.Cut(value, "=")
		if ok && !sensitive[name] {
			env = append(env, value)
		}
	}
	if cfg.AccountDir != "" {
		if cfg.Kind == KindCursorACP || cfg.Kind == KindCursor {
			// Cursor uses CURSOR_CONFIG_DIR for cli-config.json, while its
			// Linux credential manager resolves auth.json from
			// XDG_CONFIG_HOME/cursor. Set both in the child so a host login
			// cannot be selected accidentally. The caller's HOME is unchanged.
			env = append(env, "CURSOR_CONFIG_DIR="+cfg.AccountDir)
			env = append(env, "CURSOR_DATA_DIR="+cfg.AccountDir)
			env = append(env, "XDG_CONFIG_HOME="+cfg.AccountDir)
		} else {
			env = append(env, "CLAUDE_CONFIG_DIR="+cfg.AccountDir)
		}
	}
	return env
}

func runAuthCommand(ctx context.Context, cfg Config, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(argv) == 0 || argv[0] == "" {
		return fmt.Errorf("%w: empty command", ErrInvalidConfig)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	configureChild(cmd)
	cmd.Dir, cmd.Env = cfg.Root, environment(cfg)
	cmd.WaitDelay = time.Second
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("agentbackend: %s: %w", argv[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		terminateChild(cmd)
		<-done
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("agentbackend: %s: %w", argv[0], err)
		}
	}
	return nil
}

package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"bifrost/internal/agent"
	"bifrost/internal/domain"
	"bifrost/internal/filelock"
	"bifrost/internal/provider"
	"bifrost/internal/session"
)

// sessionStore opens the user-selected durable conversation directory. It is
// intentionally separate from the workspace transaction directory.
func sessionStore(opts options) (*session.Store, error) {
	return session.New(opts.statePath())
}

func providerFingerprint(base, model string) string {
	// The digest is a stable binding identifier and cannot disclose a URL or
	// model string in a snapshot. API keys are deliberately not inputs here.
	sum := sha256.Sum256([]byte(strings.TrimSpace(base) + "\x00" + strings.TrimSpace(model)))
	return "provider-sha256:" + hex.EncodeToString(sum[:])
}

func loadOrCreateSession(st *session.Store, requested, root, fingerprint string) (session.Session, error) {
	if requested != "" {
		id, err := domain.ParseID(requested)
		if err != nil {
			return session.Session{}, fmt.Errorf("session ID: %w", err)
		}
		loaded, err := st.Load(id)
		if err != nil {
			return session.Session{}, err
		}
		if err := st.ValidateBinding(loaded, root, fingerprint); err != nil {
			return session.Session{}, err
		}
		return loaded, nil
	}
	return resumeLatestSession(st, root, fingerprint)
}

// resumeLatestSession reopens the newest conversation for this workspace and
// account. A later empty session does not hide one that already has messages.
func resumeLatestSession(st *session.Store, root, fingerprint string) (session.Session, error) {
	items, err := st.List(root)
	if err != nil {
		return session.Session{}, err
	}
	var empty session.Session
	haveEmpty := false
	for _, item := range items {
		if item.ProviderFingerprint != fingerprint {
			continue
		}
		if len(item.Messages) > 0 {
			return item, nil
		}
		if !haveEmpty {
			empty = item
			haveEmpty = true
		}
	}
	if haveEmpty {
		return empty, nil
	}
	return st.Create(root, fingerprint)
}

func addResumeNotice(cfg agent.Config, current session.Session, stderr io.Writer) agent.Config {
	updated, added := addResumeNoticeToConfig(cfg, current)
	if !added {
		return cfg
	}
	if stderr != nil {
		_, _ = fmt.Fprintf(stderr, "resuming session %s with status %s; prior tool actions may have completed\n", current.ID, current.Status)
	}
	return updated
}

func resumeNotice(current session.Session) string {
	if current.Status == session.StatusCompleted || current.Revision <= 1 {
		return ""
	}
	return sessionResumeNotice
}

const sessionResumeNotice = "This session was interrupted or ended with an error. The saved history ends at the last complete turn; a prior tool action may have happened before interruption. Do not replay tools automatically; inspect the current workspace and ask the user when state is uncertain."

func addResumeNoticeToConfig(cfg agent.Config, current session.Session) (agent.Config, bool) {
	notice := resumeNotice(current)
	if notice == "" || strings.Contains(cfg.Instructions, sessionResumeNotice) {
		return cfg, false
	}
	cfg.Instructions += "\n" + notice + "\n"
	return cfg, true
}

func acquireSessionTurn(stateDir string, id domain.ID) (func() error, error) {
	lock, err := filelock.Acquire(filepath.Join(stateDir, "sessions", id.String()+".turn.lock"))
	if err != nil {
		if errors.Is(err, filelock.ErrLocked) {
			return nil, fmt.Errorf("session %s is already running", id)
		}
		return nil, fmt.Errorf("lock session %s: %w", id, err)
	}
	return lock.Close, nil
}
func saveSessionStatus(st *session.Store, current session.Session, status session.Status) (session.Session, error) {
	if current.Status == status {
		return current, nil
	}
	current.Status = status
	return st.Save(current, current.Revision)
}

func markSessionInterrupted(st *session.Store, current session.Session) (session.Session, error) {
	current.Status = session.StatusInterrupted
	return st.Save(current, current.Revision)
}

var ErrSessionStale = errors.New("session changed before turn")

// EstimateSessionTokens computes a fast token estimate for the session history and instructions.
func EstimateSessionTokens(messages []provider.Message, instructions string, prompt string) int {
	totalBytes := len(instructions) + len(prompt)
	for _, m := range messages {
		totalBytes += len(m.Content) + len(m.Role) + 12
		for _, tc := range m.ToolCalls {
			totalBytes += len(tc.Name) + len(tc.Arguments) + 8
		}
		totalBytes += len(m.Images) * 1200
	}
	return totalBytes / 4
}

// BuildConversationSummary produces a structured summary of earlier conversation turns,
// using the configured provider when available or a deterministic structured summary as fallback.
func BuildConversationSummary(ctx context.Context, cfg agent.Config, messages []provider.Message) string {
	var transcript strings.Builder
	maxBytes := 48 << 10
	for _, m := range messages {
		if transcript.Len() >= maxBytes {
			break
		}
		role := string(m.Role)
		content := strings.TrimSpace(m.Content)
		if content == "" && len(m.ToolCalls) > 0 {
			var names []string
			for _, tc := range m.ToolCalls {
				names = append(names, tc.Name)
			}
			content = "[called tools: " + strings.Join(names, ", ") + "]"
		}
		if len(content) > 1200 {
			content = content[:1200] + "…"
		}
		if content != "" {
			transcript.WriteString(fmt.Sprintf("%s: %s\n", role, content))
		}
	}

	if cfg.Provider != nil && transcript.Len() > 0 {
		sumReq := provider.Request{
			Model: cfg.ModelName,
			Messages: []provider.Message{
				{
					Role:    provider.RoleSystem,
					Content: "Summarize the earlier software engineering conversation concisely. Preserve key user requirements, architectural decisions, files modified, and open tasks.",
				},
				{
					Role:    provider.RoleUser,
					Content: transcript.String(),
				},
			},
		}
		if resp, err := cfg.Provider.Complete(ctx, sumReq); err == nil && strings.TrimSpace(resp.Message.Content) != "" {
			return strings.TrimSpace(resp.Message.Content)
		}
	}

	// Deterministic structured fallback summary
	var bullets []string
	for _, m := range messages {
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		firstLine := strings.Split(text, "\n")[0]
		if len(firstLine) > 160 {
			firstLine = firstLine[:159] + "…"
		}
		if m.Role == provider.RoleUser {
			bullets = append(bullets, "- User: "+firstLine)
		} else if m.Role == provider.RoleAssistant {
			bullets = append(bullets, "- Assistant: "+firstLine)
		}
		if len(bullets) >= 24 {
			break
		}
	}
	if len(bullets) == 0 {
		return "Earlier conversation turns were compacted to free context window space."
	}
	return "Summary of compacted earlier turns:\n" + strings.Join(bullets, "\n")
}

func maybeAutoCompactSession(ctx context.Context, st *session.Store, current session.Session, cfg agent.Config, prompt string) session.Session {
	if len(current.Messages) < 6 {
		return current
	}
	maxTokens := provider.ModelContextWindowTokens("", cfg.ModelName)
	usedTokens := EstimateSessionTokens(current.Messages, cfg.Instructions, prompt)

	// Trigger auto-compaction when context exceeds 80% of model window or 75% of agent byte budget.
	byteCount := len(cfg.Instructions) + len(prompt)
	for _, m := range current.Messages {
		byteCount += len(m.Content)
	}
	needsCompact := (maxTokens > 0 && usedTokens >= (maxTokens*80)/100) || byteCount >= (400<<10)
	if !needsCompact {
		return current
	}

	if cfg.OnEvent != nil {
		cfg.OnEvent(agent.Event{
			Kind: "status",
			Text: fmt.Sprintf("context saturation high (%d/%d tokens) — auto-compacting and summarizing earlier turns…", usedTokens, maxTokens),
		})
	}
	if current.Status != session.StatusCompleted {
		if saved, err := saveSessionStatus(st, current, session.StatusCompleted); err == nil {
			current = saved
		}
	}
	summary := BuildConversationSummary(ctx, cfg, current.Messages)
	compacted, err := st.Compact(current.ID, summary, 2)
	if err != nil {
		return current
	}
	if cfg.OnEvent != nil {
		newTokens := EstimateSessionTokens(compacted.Messages, cfg.Instructions, prompt)
		cfg.OnEvent(agent.Event{
			Kind: "status",
			Text: fmt.Sprintf("auto-compacted conversation into %s (%d → %d tokens)", compacted.ID, usedTokens, newTokens),
		})
	}
	return compacted
}

func runSessionTurn(ctx context.Context, stateDir string, st *session.Store, current session.Session, cfg agent.Config, prompt string) (session.Session, agent.Result, error) {
	current = maybeAutoCompactSession(ctx, st, current, cfg, prompt)
	unlock, err := acquireSessionTurn(stateDir, current.ID)
	if err != nil {
		return current, agent.Result{}, err
	}
	defer unlock()
	fresh, err := st.Load(current.ID)
	if err != nil {
		return current, agent.Result{}, err
	}
	if fresh.Revision != current.Revision {
		return current, agent.Result{}, fmt.Errorf("%w: got revision %d, want %d", ErrSessionStale, fresh.Revision, current.Revision)
	}
	current = fresh
	if updatedCfg, uncertain := addResumeNoticeToConfig(cfg, current); uncertain {
		cfg = updatedCfg
		if cfg.OnEvent != nil {
			cfg.OnEvent(agent.Event{Kind: "status", Text: sessionResumeNotice})
		}
	}
	// Persist the interrupted marker before invoking the provider or any tool.
	// A hard process crash therefore cannot leave a completed marker attached to
	// a turn whose side effects are unknown.
	marked, markErr := markSessionInterrupted(st, current)
	if markErr != nil {
		return current, agent.Result{}, markErr
	}
	current = marked
	cfg.History = current.Messages
	result, runErr := agent.Run(ctx, cfg, prompt)
	if runErr != nil {
		status := session.StatusError
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			status = session.StatusInterrupted
		}
		updated, saveErr := saveSessionStatus(st, current, status)
		if saveErr != nil {
			return current, result, errors.Join(runErr, saveErr)
		}
		return updated, result, runErr
	}
	current.Messages = result.Messages
	current.Status = session.StatusCompleted
	updated, saveErr := st.Save(current, current.Revision)
	if saveErr != nil {
		return current, result, saveErr
	}
	return updated, result, nil
}

func runSession(opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		return errors.New("usage: session list | show ID")
	}
	st, err := sessionStore(opts)
	if err != nil {
		return err
	}
	switch args[0] {
	case "export":
		if len(args) != 2 {
			return errors.New("session export ID (writes versioned JSON to stdout)")
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			return err
		}
		return st.Export(id, stdout)
	case "import":
		if len(args) != 2 {
			return errors.New("session import FILE")
		}
		root, err := resolveRoot(opts)
		if err != nil {
			return err
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		imported, err := st.Import(f, root)
		if err != nil {
			return err
		}
		return writeJSON(stdout, imported)
	case "compact":
		if len(args) < 3 || len(args) > 4 {
			return errors.New("session compact ID SUMMARY_FILE [KEEP_TURNS]")
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			return err
		}
		f, err := os.Open(args[2])
		if err != nil {
			return err
		}
		defer f.Close()
		summary, err := io.ReadAll(io.LimitReader(f, (256<<10)+1))
		if err != nil {
			return err
		}
		keep := 2
		if len(args) == 4 {
			keep, err = strconv.Atoi(args[3])
			if err != nil {
				return err
			}
		}
		compacted, err := st.Compact(id, string(summary), keep)
		if err != nil {
			return err
		}
		return writeJSON(stdout, compacted)
	case "list":
		if len(args) != 1 {
			return errors.New("session list takes no positional arguments")
		}
		root, err := resolveRoot(opts)
		if err != nil {
			return err
		}
		items, err := st.List(root)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, items)
		}
		for _, item := range items {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\trev %d\t%s\n", item.ID, item.Status, item.Revision, item.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"))
		}
		return nil
	case "show":
		if len(args) != 2 {
			return errors.New("usage: session show ID")
		}
		id, err := domain.ParseID(args[1])
		if err != nil {
			return err
		}
		item, err := st.Load(id)
		if err != nil {
			return err
		}
		return writeJSON(stdout, item)
	default:
		return fmt.Errorf("unknown session command %q", args[0])
	}
}

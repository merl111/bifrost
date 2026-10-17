// Package gitops provides a small, explicit argv wrapper around local Git.
// It never invokes a shell and never performs reset, force, push, or cleanup
// outside the selected repository root.
package gitops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"bifrost/internal/process"
)

var (
	ErrInvalidPath   = errors.New("gitops: invalid repository path")
	ErrDirtyWorktree = errors.New("gitops: worktree is dirty")
)

type Git struct {
	Root           string
	GitPath        string
	MaxOutputBytes int
}

func Open(root string) (*Git, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("gitops: resolve root: %w", err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("gitops: resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return nil, fmt.Errorf("gitops: root: %w", err)
	}
	return &Git{Root: abs, GitPath: "git", MaxOutputBytes: 2 << 20}, nil
}

type CommandResult struct {
	Stdout    []byte
	Stderr    []byte
	ExitCode  int
	Truncated bool
}

func (g *Git) run(ctx context.Context, args ...string) (CommandResult, error) {
	if g == nil || g.Root == "" {
		return CommandResult{ExitCode: -1}, errors.New("gitops: nil repository")
	}
	bin := g.GitPath
	if bin == "" {
		bin = "git"
	}
	r, err := process.Run(ctx, process.Spec{Argv: append([]string{bin, "-C", g.Root}, args...), Dir: g.Root, MaxOutputBytes: g.MaxOutputBytes})
	result := CommandResult{Stdout: r.Stdout, Stderr: r.Stderr, ExitCode: r.ExitCode, Truncated: r.StdoutTruncated || r.StderrTruncated}
	if err != nil {
		return result, err
	}
	if r.ExitCode != 0 {
		return result, fmt.Errorf("git exited with status %d", r.ExitCode)
	}
	return result, nil
}

type StatusEntry struct {
	XY           string
	Path         string
	OriginalPath string
}

type StatusResult struct {
	Entries []StatusEntry
	Raw     []byte
	Command CommandResult
}

func (g *Git) Status(ctx context.Context) (StatusResult, error) {
	r, err := g.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return StatusResult{Command: r}, commandError("status", r, err)
	}
	entries, parseErr := parseStatus(r.Stdout)
	if parseErr != nil {
		return StatusResult{Raw: r.Stdout, Command: r}, parseErr
	}
	return StatusResult{Entries: entries, Raw: r.Stdout, Command: r}, nil
}

type DiffOptions struct {
	Cached bool
	Stat   bool
	Paths  []string
}
type DiffResult struct {
	Text    string
	Command CommandResult
}

func (g *Git) Diff(ctx context.Context, opts DiffOptions) (DiffResult, error) {
	paths, err := g.paths(opts.Paths)
	if err != nil {
		return DiffResult{}, err
	}
	args := []string{"diff"}
	if opts.Cached {
		args = append(args, "--cached")
	}
	if opts.Stat {
		args = append(args, "--stat")
	}
	args = append(args, "--")
	args = append(args, paths...)
	r, err := g.run(ctx, args...)
	if err != nil {
		return DiffResult{Text: string(r.Stdout), Command: r}, commandError("diff", r, err)
	}
	return DiffResult{Text: string(r.Stdout), Command: r}, nil
}

func (g *Git) Stage(ctx context.Context, paths []string) (CommandResult, error) {
	return g.pathMutation(ctx, "stage", "add", paths)
}
func (g *Git) Unstage(ctx context.Context, paths []string) (CommandResult, error) {
	return g.pathMutation(ctx, "unstage", "restore", "--staged", paths)
}

func (g *Git) pathMutation(ctx context.Context, label string, gitArgs ...any) (CommandResult, error) {
	if len(gitArgs) == 0 {
		return CommandResult{ExitCode: -1}, errors.New("gitops: mutation command is empty")
	}
	verb, ok := gitArgs[0].(string)
	if !ok {
		return CommandResult{ExitCode: -1}, errors.New("gitops: invalid mutation command")
	}
	args := []string{verb}
	for _, v := range gitArgs[1:] {
		if s, ok := v.(string); ok {
			args = append(args, s)
		}
	}
	pathValue := gitArgs[len(gitArgs)-1]
	paths, ok := pathValue.([]string)
	if !ok || len(paths) == 0 {
		return CommandResult{ExitCode: -1}, fmt.Errorf("gitops: %s requires explicit paths", label)
	}
	valid, err := g.paths(paths)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	if verb == "restore" {
		args = append(args, "--")
	} else {
		args = append(args, "--")
	}
	args = append(args, valid...)
	r, runErr := g.run(ctx, args...)
	if runErr != nil {
		return r, commandError(label, r, runErr)
	}
	return r, nil
}

type CommitOptions struct {
	Message string
	Signoff bool
}

func (g *Git) Commit(ctx context.Context, opts CommitOptions) (CommandResult, error) {
	if strings.TrimSpace(opts.Message) == "" {
		return CommandResult{ExitCode: -1}, errors.New("gitops: commit message is required")
	}
	args := []string{"commit", "-m", opts.Message}
	if opts.Signoff {
		args = append(args, "--signoff")
	}
	r, err := g.run(ctx, args...)
	if err != nil {
		return r, commandError("commit", r, err)
	}
	return r, nil
}

type Worktree struct {
	Path     string
	Head     string
	Branch   string
	Detached bool
	Bare     bool
}

func (g *Git) WorktreeList(ctx context.Context) ([]Worktree, error) {
	r, err := g.run(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, commandError("worktree list", r, err)
	}
	return parseWorktrees(string(r.Stdout))
}

func (g *Git) WorktreeCreate(ctx context.Context, path, ref string) (CommandResult, error) {
	target, err := g.subpath(path)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	if err := validateRef(ref); err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	if _, err := os.Stat(target); err == nil {
		return CommandResult{ExitCode: -1}, fmt.Errorf("gitops: worktree path already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return CommandResult{ExitCode: -1}, err
	}
	r, runErr := g.run(ctx, "worktree", "add", target, ref)
	if runErr != nil {
		return r, commandError("worktree create", r, runErr)
	}
	return r, nil
}

func (g *Git) WorktreeRemove(ctx context.Context, path string) (CommandResult, error) {
	target, err := g.subpath(path)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	status, err := Open(target)
	if err != nil {
		return CommandResult{ExitCode: -1}, fmt.Errorf("gitops: inspect worktree: %w", err)
	}
	st, err := status.Status(ctx)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	if len(st.Entries) > 0 {
		return CommandResult{ExitCode: -1}, fmt.Errorf("%w: %s", ErrDirtyWorktree, target)
	}
	r, runErr := g.run(ctx, "worktree", "remove", "--", target)
	if runErr != nil {
		return r, commandError("worktree remove", r, runErr)
	}
	return r, nil
}

func (g *Git) paths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, errors.New("gitops: explicit paths are required")
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		clean, err := g.relativePath(p)
		if err != nil {
			return nil, err
		}
		out[i] = clean
	}
	return out, nil
}
func (g *Git) relativePath(path string) (string, error) {
	if path == "" || strings.HasPrefix(path, "-") || strings.ContainsRune(path, '\x00') {
		return "", ErrInvalidPath
	}
	if filepath.IsAbs(path) {
		return "", ErrInvalidPath
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return clean, nil
}
func (g *Git) subpath(path string) (string, error) {
	if path == "" || strings.ContainsRune(path, '\x00') || strings.HasPrefix(path, "-") {
		return "", ErrInvalidPath
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)
	if resolved, resolveErr := filepath.EvalSymlinks(clean); resolveErr == nil {
		clean = resolved
	} else if !errors.Is(resolveErr, os.ErrNotExist) {
		return "", resolveErr
	} else if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(clean)); parentErr == nil {
		clean = filepath.Join(parent, filepath.Base(clean))
	}
	rel, err := filepath.Rel(g.Root, clean)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return clean, nil
}
func validateRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\x00\r\n\t") || strings.Contains(ref, "..") || strings.HasPrefix(ref, "/") {
		return errors.New("gitops: invalid ref")
	}
	for _, r := range ref {
		if unicode.IsSpace(r) {
			return errors.New("gitops: invalid ref")
		}
	}
	return nil
}
func commandError(label string, r CommandResult, err error) error {
	if len(r.Stderr) > 0 {
		return fmt.Errorf("gitops: %s: %w: %s", label, err, strings.TrimSpace(string(r.Stderr)))
	}
	return fmt.Errorf("gitops: %s: %w", label, err)
}

func parseStatus(raw []byte) ([]StatusEntry, error) {
	parts := strings.Split(string(raw), "\x00")
	out := make([]StatusEntry, 0)
	for i := 0; i < len(parts)-1; i++ {
		v := parts[i]
		if v == "" {
			continue
		}
		if len(v) < 4 {
			return nil, errors.New("gitops: malformed status output")
		}
		xy := v[:2]
		path := v[3:]
		entry := StatusEntry{XY: xy, Path: path}
		if xy[0] == 'R' || xy[1] == 'R' {
			if i+1 >= len(parts) {
				return nil, errors.New("gitops: malformed rename status")
			}
			entry.OriginalPath = parts[i+1]
			entry.Path = path
			i++
		}
		out = append(out, entry)
	}
	return out, nil
}

func parseWorktrees(raw string) ([]Worktree, error) {
	var out []Worktree
	var cur *Worktree
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			if cur != nil {
				out = append(out, *cur)
				cur = nil
			}
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			key, val = line, ""
		}
		if key == "worktree" {
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Worktree{Path: val}
			continue
		}
		if cur == nil {
			continue
		}
		switch key {
		case "HEAD":
			cur.Head = val
		case "branch":
			cur.Branch = val
		case "detached":
			cur.Detached = true
		case "bare":
			cur.Bare = true
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out, nil
}

// Package environment gives the agent and UI one transport-neutral view of a
// project. Local and worker-backed implementations expose the same bounded
// file, process, and review operations.
package environment

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bifrost/internal/browser"
	"bifrost/internal/desktop"
	"bifrost/internal/gitview"
	"bifrost/internal/process"
	"bifrost/internal/worker"
	"bifrost/internal/workspace"
)

type Identity struct {
	ID           string              `json:"id"`
	Kind         string              `json:"kind"` // local or ssh
	Root         string              `json:"root"`
	StateDir     string              `json:"state_dir,omitempty"`
	BootID       string              `json:"boot_id,omitempty"`
	Platform     string              `json:"platform,omitempty"`
	Architecture string              `json:"architecture,omitempty"`
	Capabilities worker.Capabilities `json:"capabilities"`
}

type ExecRequest struct {
	Argv           []string
	CWD            string
	MaxOutputBytes int
}

type Backend interface {
	Identity() Identity
	List(context.Context, string) ([]workspace.Entry, error)
	Read(context.Context, string) (string, workspace.Revision, error)
	Search(context.Context, string) (workspace.SearchResult, error)
	Write(context.Context, string, *workspace.Revision, string) (workspace.Change, error)
	Edit(context.Context, string, workspace.Revision, string, string) (workspace.Change, error)
	Undo(context.Context, workspace.ChangeID) error
	Exec(context.Context, ExecRequest) (process.Result, error)
	Review(context.Context) (gitview.Review, error)
	Close() error
}

type BrowserController interface {
	BrowserNavigate(context.Context, string) (browser.Snapshot, error)
	BrowserSnapshot(context.Context) (browser.Snapshot, error)
	BrowserClick(context.Context, string) (browser.Snapshot, error)
	BrowserType(context.Context, string, string) (browser.Snapshot, error)
	BrowserScreenshot(context.Context) ([]byte, error)
}

type DesktopController interface {
	DesktopCapabilities(context.Context) (desktop.Capability, error)
	DesktopCapture(context.Context) (worker.DesktopResult, error)
	DesktopMove(context.Context, worker.DesktopAction) error
	DesktopClick(context.Context, worker.DesktopAction) error
	DesktopKeyPress(context.Context, worker.DesktopAction) error
	DesktopType(context.Context, worker.DesktopAction) error
}

// PatchController is optional so existing Backend implementations remain
// source-compatible while native and worker-backed environments can expose
// one revision-checked multi-file transaction boundary.
type PatchController interface {
	ApplyPatch(context.Context, workspace.PatchPlan) (workspace.PatchResult, error)
	UndoPatch(context.Context, workspace.ChangeID) (workspace.PatchResult, error)
}

type LocalConfig struct {
	ID       string
	Root     string
	StateDir string
}
type Local struct {
	identity Identity
	ws       *workspace.Workspace
	root     string
	mu       sync.Mutex
	closed   bool
}

func OpenLocal(cfg LocalConfig) (*Local, error) {
	ws, err := workspace.Open(cfg.Root, cfg.StateDir)
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		ws.Close()
		return nil, err
	}
	root, _ = filepath.Abs(root)
	if cfg.ID == "" {
		cfg.ID = root
	}
	return &Local{identity: Identity{ID: cfg.ID, Kind: "local", Root: root, StateDir: cfg.StateDir, Platform: "local", Capabilities: worker.Capabilities{FilesRead: true, FilesMutate: true, Search: true, Exec: true, GitReview: true}}, ws: ws, root: root}, nil
}
func (l *Local) Identity() Identity { return l.identity }
func (l *Local) List(ctx context.Context, p string) ([]workspace.Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return l.ws.List(p)
}
func (l *Local) Read(ctx context.Context, p string) (string, workspace.Revision, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	return l.ws.Read(p)
}
func (l *Local) Search(ctx context.Context, q string) (workspace.SearchResult, error) {
	return l.ws.Search(ctx, q)
}
func (l *Local) Write(ctx context.Context, p string, e *workspace.Revision, c string) (workspace.Change, error) {
	if err := ctx.Err(); err != nil {
		return workspace.Change{}, err
	}
	return l.ws.Write(p, e, c)
}
func (l *Local) ApplyPatch(ctx context.Context, plan workspace.PatchPlan) (workspace.PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return workspace.PatchResult{}, err
	}
	return l.ws.ApplyPatch(plan)
}
func (l *Local) UndoPatch(ctx context.Context, id workspace.ChangeID) (workspace.PatchResult, error) {
	if err := ctx.Err(); err != nil {
		return workspace.PatchResult{}, err
	}
	return l.ws.UndoPatch(id)
}
func (l *Local) Edit(ctx context.Context, p string, e workspace.Revision, o, n string) (workspace.Change, error) {
	if err := ctx.Err(); err != nil {
		return workspace.Change{}, err
	}
	return l.ws.Edit(p, e, o, n)
}
func (l *Local) Undo(ctx context.Context, id workspace.ChangeID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.ws.Undo(id)
}

// Delete removes a rooted file and returns its previous text. The change can
// be restored with Undo while the path stays empty.
func (l *Local) Delete(ctx context.Context, p string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	_, prev, err := l.ws.Delete(p)
	return prev, err
}
func (l *Local) Exec(ctx context.Context, r ExecRequest) (process.Result, error) {
	dir, err := localCWD(l.root, r.CWD)
	if err != nil {
		return process.Result{}, err
	}
	return process.Run(ctx, process.Spec{Argv: r.Argv, Dir: dir, MaxOutputBytes: r.MaxOutputBytes})
}
func (l *Local) Review(ctx context.Context) (gitview.Review, error) { return gitview.Load(ctx, l.root) }
func (l *Local) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.ws.Close()
}

type Remote struct {
	client   *worker.Client
	identity Identity
}

func NewRemote(c *worker.Client) *Remote {
	h := c.Hello()
	return &Remote{client: c, identity: Identity{ID: h.Root + "#" + h.BootID, Kind: "ssh", Root: h.Root, BootID: h.BootID, Platform: h.Platform, Architecture: h.Architecture, Capabilities: h.Capabilities}}
}
func (r *Remote) Identity() Identity { return r.identity }
func (r *Remote) List(c context.Context, p string) ([]workspace.Entry, error) {
	return r.client.List(c, p)
}
func (r *Remote) Read(c context.Context, p string) (string, workspace.Revision, error) {
	return r.client.Read(c, p)
}
func (r *Remote) Search(c context.Context, q string) (workspace.SearchResult, error) {
	return r.client.Search(c, q)
}
func (r *Remote) Write(c context.Context, p string, e *workspace.Revision, v string) (workspace.Change, error) {
	return r.client.Write(c, p, e, v)
}
func (r *Remote) ApplyPatch(c context.Context, plan workspace.PatchPlan) (workspace.PatchResult, error) {
	return r.client.ApplyPatch(c, plan)
}
func (r *Remote) UndoPatch(c context.Context, id workspace.ChangeID) (workspace.PatchResult, error) {
	return r.client.UndoPatch(c, id)
}
func (r *Remote) Edit(c context.Context, p string, e workspace.Revision, o, n string) (workspace.Change, error) {
	return r.client.Edit(c, p, e, o, n)
}
func (r *Remote) Undo(c context.Context, id workspace.ChangeID) error { return r.client.Undo(c, id) }
func (r *Remote) Exec(c context.Context, v ExecRequest) (process.Result, error) {
	return r.client.Exec(c, v.Argv, v.CWD, v.MaxOutputBytes)
}
func (r *Remote) Review(c context.Context) (gitview.Review, error) { return r.client.Review(c) }
func (r *Remote) Close() error                                     { return r.client.Close() }
func (r *Remote) BrowserNavigate(c context.Context, u string) (browser.Snapshot, error) {
	return r.client.BrowserNavigate(c, u)
}
func (r *Remote) BrowserSnapshot(c context.Context) (browser.Snapshot, error) {
	return r.client.BrowserSnapshot(c)
}
func (r *Remote) BrowserClick(c context.Context, ref string) (browser.Snapshot, error) {
	return r.client.BrowserClick(c, ref)
}
func (r *Remote) BrowserType(c context.Context, ref, text string) (browser.Snapshot, error) {
	return r.client.BrowserType(c, ref, text)
}
func (r *Remote) BrowserScreenshot(c context.Context) ([]byte, error) {
	return r.client.BrowserScreenshot(c)
}
func (r *Remote) DesktopCapabilities(c context.Context) (desktop.Capability, error) {
	return r.client.DesktopCapabilities(c)
}
func (r *Remote) DesktopCapture(c context.Context) (worker.DesktopResult, error) {
	return r.client.DesktopCapture(c)
}
func (r *Remote) DesktopMove(c context.Context, a worker.DesktopAction) error {
	return r.client.DesktopMove(c, a)
}
func (r *Remote) DesktopClick(c context.Context, a worker.DesktopAction) error {
	return r.client.DesktopClick(c, a)
}
func (r *Remote) DesktopKeyPress(c context.Context, a worker.DesktopAction) error {
	return r.client.DesktopKeyPress(c, a)
}
func (r *Remote) DesktopType(c context.Context, a worker.DesktopAction) error {
	return r.client.DesktopType(c, a)
}

func localCWD(root, name string) (string, error) {
	if name == "" || name == "." {
		return root, nil
	}
	if filepath.IsAbs(name) {
		return "", errors.New("environment: absolute cwd is not allowed")
	}
	clean := filepath.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("environment: cwd escapes root")
	}
	candidate := filepath.Join(root, clean)
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("environment: cwd escapes root")
	}
	return real, nil
}

type SSHConfig struct {
	Target           string
	Port             int
	IdentityFile     string
	RemoteExecutable string
	Root             string
	StateDir         string
	// BrowserEnabled and DesktopEnabled are explicit guest opt-ins. They are
	// forwarded to the worker command and never cause host-side fallbacks.
	BrowserEnabled   bool
	DesktopEnabled   bool
	MaxMessageBytes  int
	HandshakeTimeout time.Duration
}

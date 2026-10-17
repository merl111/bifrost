package environment

import (
	"context"
	"path/filepath"

	"bifrost/internal/gitview"
	"bifrost/internal/process"
	"bifrost/internal/worker"
	"bifrost/internal/workspace"
)

// TransientLocal exposes a local environment without holding the workspace
// lock for the lifetime of a UI. Each operation opens and closes a regular
// Local backend, so a daemon job can own the lock only while it performs an
// operation. Close is intentionally a no-op.
type TransientLocal struct {
	identity Identity
	cfg      LocalConfig
}

var _ Backend = (*TransientLocal)(nil)

func OpenTransientLocal(cfg LocalConfig) (*TransientLocal, error) {
	root, err := filepath.EvalSymlinks(cfg.Root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if cfg.ID == "" {
		cfg.ID = root
	}
	return &TransientLocal{cfg: cfg, identity: Identity{
		ID: cfg.ID, Kind: "local", Root: root, StateDir: cfg.StateDir, Platform: "local",
		Capabilities: worker.Capabilities{FilesRead: true, FilesMutate: true, Search: true, Exec: true, GitReview: true},
	}}, nil
}

func (t *TransientLocal) Identity() Identity { return t.identity }

func (t *TransientLocal) with(ctx context.Context, fn func(*Local) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l, err := OpenLocal(t.cfg)
	if err != nil {
		return err
	}
	err = fn(l)
	if closeErr := l.Close(); err == nil {
		err = closeErr
	}
	return err
}

func (t *TransientLocal) List(ctx context.Context, path string) (out []workspace.Entry, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.List(ctx, path); return err })
	return
}

func (t *TransientLocal) Read(ctx context.Context, path string) (text string, rev workspace.Revision, err error) {
	err = t.with(ctx, func(l *Local) error { text, rev, err = l.Read(ctx, path); return err })
	return
}

func (t *TransientLocal) Search(ctx context.Context, query string) (out workspace.SearchResult, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.Search(ctx, query); return err })
	return
}

func (t *TransientLocal) Write(ctx context.Context, path string, expected *workspace.Revision, content string) (out workspace.Change, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.Write(ctx, path, expected, content); return err })
	return
}

func (t *TransientLocal) Edit(ctx context.Context, path string, expected workspace.Revision, old, new string) (out workspace.Change, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.Edit(ctx, path, expected, old, new); return err })
	return
}

func (t *TransientLocal) Undo(ctx context.Context, id workspace.ChangeID) error {
	return t.with(ctx, func(l *Local) error { return l.Undo(ctx, id) })
}

func (t *TransientLocal) Exec(ctx context.Context, req ExecRequest) (out process.Result, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.Exec(ctx, req); return err })
	return
}

func (t *TransientLocal) Review(ctx context.Context) (out gitview.Review, err error) {
	err = t.with(ctx, func(l *Local) error { out, err = l.Review(ctx); return err })
	return
}

func (t *TransientLocal) Close() error { return nil }

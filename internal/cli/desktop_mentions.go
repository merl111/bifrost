package cli

import (
	"bifrost/internal/ui"
	"context"
)

type desktopFilesystem struct {
	ctx context.Context
	ws  *DesktopWorkspace
}

func (f desktopFilesystem) Close() error { return nil }
func (f desktopFilesystem) Read(_ context.Context, path string) (string, error) {
	text, _, err := f.ws.Backend.Read(f.ctx, path)
	return text, err
}
func (f desktopFilesystem) List(_ context.Context, path string) ([]ui.FileEntry, error) {
	entries, err := f.ws.Backend.List(f.ctx, path)
	var result []ui.FileEntry
	for _, e := range entries {
		result = append(result, ui.FileEntry{Path: e.Path, Dir: e.IsDir})
	}
	return result, err
}
func (d *DesktopWorkspace) MentionContext(ctx context.Context, terminal *ui.NativeTerminal) *ui.MentionContext {
	var status ui.ProjectStatus
	if d.Projects.Scan != nil {
		status, _ = d.Projects.Scan(ctx)
	}
	review := func() (string, error) { r, err := d.Backend.Review(ctx); return r.Text(), err }
	return ui.NewMentionContext(d.Root, desktopFilesystem{ctx, d}, status, d.Backend.Identity().Kind != "local", review, terminal)
}

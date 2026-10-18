package cli

import (
	"context"
	"errors"
	"slices"

	"bifrost/internal/ui"
)

// RunConversation owns a turn's history and cancellation independently of the
// workspace's selected conversation. Empty IDs create fresh sessions. Existing
// session and external-account locks still prevent unsafe concurrent reuse.
// The returned snapshot includes saved partial history even when a turn fails.
func (d *DesktopWorkspace) RunConversation(ctx context.Context, id, prompt string, emit func(ui.ChatEvent)) (DesktopSnapshot, error) {
	d.conversations.RLock()
	defer d.conversations.RUnlock()
	if err := d.chat.ctx.Err(); err != nil {
		return DesktopSnapshot{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(d.chat.ctx, cancel)
	defer stop()
	d.chat.mu.Lock()
	c := &workspaceChat{
		ctx: d.chat.ctx, opts: d.chat.opts, args: slices.Clone(d.chat.args),
		setup: d.chat.setup, monitor: d.chat.monitor, stderr: d.chat.stderr,
		store: d.chat.store,
	}
	// Rebinding portable history checks the workspace root, not the selected tab.
	c.current.Root = d.chat.setup.cfg.Root
	c.setup.cfg.ExtraTools = slices.Clone(c.setup.cfg.ExtraTools)
	d.chat.mu.Unlock()
	conversation := &DesktopWorkspace{Root: d.Root, chat: c}
	var err error
	if id == "" {
		err = conversation.NewSession(ctx)
	} else {
		err = conversation.Resume(ctx, id)
	}
	if err != nil {
		return DesktopSnapshot{}, err
	}
	// Publish identity before streaming so reopening a saved conversation can
	// focus its running tab instead of starting a duplicate view.
	emit(ui.ChatEvent{Kind: "session", ID: string(c.current.ID)})
	err = c.run(ctx, prompt, emit)
	snapshot, snapshotErr := conversation.Snapshot(d.chat.ctx)
	return snapshot, errors.Join(err, snapshotErr)
}

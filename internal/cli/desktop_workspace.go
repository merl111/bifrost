package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"bifrost/internal/config"
	"bifrost/internal/environment"
	"bifrost/internal/execpolicy"
	"bifrost/internal/ui"
)

// DesktopWorkspace exposes the existing workspace services to the optional
// desktop executable. It has no native GUI dependency. Call Close after workers
// have stopped; callbacks and snapshots may block and belong off the UI thread.
type DesktopWorkspace struct {
	Root          string
	Backend       environment.Backend
	chat          *workspaceChat
	cleanup       func()
	once          sync.Once
	conversations sync.RWMutex
	Settings      ui.SettingsCallbacks
	Marketplace   ui.MarketplaceCallbacks
	Extensions    ui.ExtensionCommand
	Tasks         ui.TaskMonitorCallbacks
	Projects      ui.ProjectCallbacks
	Appearance    config.UISettings
	StateDir      string
	TerminalArgv  []string
	TerminalDir   string
	SaveLayout    func(ui.LayoutSettings) error
}

type DesktopSnapshot struct {
	Execution                 execpolicy.Settings
	History                   []ui.ChatEvent
	Accounts                  []ui.AccountChoice
	Sessions                  []ui.SessionSummary
	Connection                ui.ConnectionInfo
	WriteEnabled, ExecEnabled bool
}

func OpenDesktopWorkspace(ctx context.Context, args []string, stderr io.Writer) (*DesktopWorkspace, error) {
	if stderr == nil {
		stderr = io.Discard
	}
	global, runtimeArgs, err := extractGlobalFlags(args)
	if err != nil {
		return nil, err
	}
	opts, _, err := parseGlobal(global, stderr)
	if err != nil {
		return nil, err
	}
	opts, err = applyProjectDefault(opts)
	if err != nil {
		return nil, err
	}
	setup, err := configureAgentSetup(ctx, opts, runtimeArgs, stderr, false)
	if err != nil {
		return nil, err
	}
	cleanup := setup.cleanup
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	if len(setup.rest) != 0 {
		return nil, errors.New("desktop takes workspace and runtime flags, not a prompt")
	}
	backend := setup.cfg.Backend
	if backend == nil {
		var local environment.Backend
		var openErr error
		localConfig := environment.LocalConfig{Root: setup.cfg.Root, StateDir: setup.cfg.StateDir}
		if setup.daemonMode {
			local, openErr = environment.OpenTransientLocal(localConfig)
		} else {
			local, openErr = environment.OpenLocal(localConfig)
		}
		if openErr != nil {
			return nil, openErr
		}
		backend = local
		cleanup = composeCleanup(func() { _ = local.Close() }, cleanup)
	}
	setup.cfg.Backend = backend
	monitor := &taskMonitorState{}
	chat, err := newWorkspaceChat(ctx, opts, runtimeArgs, setup, monitor, stderr)
	if err != nil {
		return nil, err
	}
	settings, err := opts.settings()
	if err != nil {
		chat.close()
		return nil, err
	}
	settingsCallbacks, err := settingsUICallbacks(opts)
	if err != nil {
		chat.close()
		return nil, err
	}
	market, err := workspaceMarketplaceCallbacks(opts, settings)
	if err != nil {
		chat.close()
		return nil, err
	}
	var terminalArgv []string
	terminalDir := setup.cfg.Root
	if backend.Identity().Kind == "ssh" {
		terminalArgv, err = remoteTerminalArgv(settings.SSH[setup.sshName])
		if err != nil {
			chat.close()
			return nil, err
		}
		terminalDir = ""
	}
	ok = true
	return &DesktopWorkspace{Root: setup.cfg.Root, Backend: backend, chat: chat, cleanup: cleanup,
		Settings: settingsCallbacks, Marketplace: market, Extensions: workspaceExtensionCommand(opts), Tasks: monitor.callbacks(opts),
		Projects: workspaceProjectCallbacks(opts, setup.cfg.Root), Appearance: settings.UI,
		SaveLayout: workspaceLayoutCallback(opts), StateDir: setup.cfg.StateDir, TerminalArgv: terminalArgv, TerminalDir: terminalDir}, nil
}

func (d *DesktopWorkspace) Close() {
	d.once.Do(func() {
		d.chat.cancel()
		d.conversations.Lock()
		defer d.conversations.Unlock()
		d.chat.close()
		d.cleanup()
	})
}

func (d *DesktopWorkspace) Run(ctx context.Context, prompt string, emit func(ui.ChatEvent)) error {
	return d.chat.run(ctx, prompt, emit)
}

func (d *DesktopWorkspace) Snapshot(ctx context.Context) (DesktopSnapshot, error) {
	sessions, err := d.chat.listSessions(ctx)
	if err != nil {
		return DesktopSnapshot{}, err
	}
	d.chat.mu.Lock()
	defer d.chat.mu.Unlock()
	settings, err := d.chat.opts.settings()
	if err != nil {
		return DesktopSnapshot{}, err
	}
	cfg := d.chat.setup.cfg
	return DesktopSnapshot{
		Execution: settings.Execution, History: d.chat.history(), Accounts: d.chat.buildAccountChoices(settings), Sessions: sessions,
		Connection:   d.chat.currentConnectionInfo(settings.Providers[cfg.ProfileName].Kind),
		WriteEnabled: cfg.WriteEnabled, ExecEnabled: cfg.ExecEnabled,
	}, nil
}

func (d *DesktopWorkspace) NewSession(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.chat.mu.Lock()
	defer d.chat.mu.Unlock()
	if d.chat.setup.fingerprint == "" {
		return errors.New("connect an account before creating a conversation")
	}
	// An explicit New action must not use loadOrCreateSession: an empty ID in
	// that startup helper intentionally resumes the newest existing history.
	created, err := d.chat.store.Create(d.Root, d.chat.setup.fingerprint)
	if err == nil {
		d.chat.current = created
	}
	return err
}

func (d *DesktopWorkspace) Resume(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, err := d.chat.resumeSession(ctx, id)
	return err
}

func (d *DesktopWorkspace) SwitchAccount(ctx context.Context, id string) error {
	d.conversations.Lock()
	defer d.conversations.Unlock()
	_, err := d.chat.switchAccount(ctx, id)
	return err
}

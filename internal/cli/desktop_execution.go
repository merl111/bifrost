package cli

import (
	"bifrost/internal/config"
	"context"
	"errors"
)

func (d *DesktopWorkspace) SetExecutionMode(ctx context.Context, mode string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode != "disabled" && mode != "allow" && mode != "ask" && mode != "rules" {
		return errors.New("unknown execution mode")
	}
	d.conversations.Lock()
	defer d.conversations.Unlock()
	d.chat.mu.Lock()
	defer d.chat.mu.Unlock()
	cfg := &d.chat.setup.cfg
	if mode != "disabled" && mode != "allow" && cfg.RunExternal != nil && !cfg.ExternalToolBridge {
		return errors.New("this vendor CLI owns its tools; select a native provider or Cursor Native for Bifrost approvals")
	}
	if d.chat.setup.daemonMode {
		return errors.New("change command policy in the daemon settings and reconnect")
	}
	path, err := d.chat.opts.configFile()
	if err != nil {
		return err
	}
	settings, rev, err := config.LoadSettingsRevision(path)
	if err != nil {
		return err
	}
	settings.Defaults.AllowExec = mode != "disabled"
	if mode != "disabled" {
		settings.Execution.Mode = mode
	}
	if err = config.SaveSettingsRevision(path, settings, rev); err != nil {
		return err
	}
	cfg.ExecEnabled = settings.Defaults.AllowExec
	return nil
}

package cli

import (
	"bifrost/internal/ui"
	"context"
)

func (d *DesktopWorkspace) DiscoverModels(ctx context.Context, request ui.AccountAddRequest, login bool) ([]ui.AccountModelOption, string, error) {
	return d.chat.discoverAccountModels(ctx, request, login)
}

func (d *DesktopWorkspace) AddAccount(ctx context.Context, request ui.AccountAddRequest) error {
	d.conversations.Lock()
	defer d.conversations.Unlock()
	_, _, _, err := d.chat.addAccount(ctx, request)
	return err
}

func (d *DesktopWorkspace) RemoveAccount(ctx context.Context, id string) error {
	d.conversations.Lock()
	defer d.conversations.Unlock()
	_, err := d.chat.removeAccount(ctx, id)
	return err
}

func (d *DesktopWorkspace) SwitchModel(ctx context.Context, account, model string) error {
	d.conversations.Lock()
	defer d.conversations.Unlock()
	_, _, _, err := d.chat.switchAccountModel(ctx, account, model)
	return err
}

func (d *DesktopWorkspace) Compact(ctx context.Context) error {
	_, _, _, _, err := d.chat.compactCurrentSession(ctx)
	return err
}

func (d *DesktopWorkspace) RepositoryArguments(project, alias, root string, linked bool) ([]string, error) {
	path, err := d.chat.opts.configFile()
	if err != nil {
		return nil, err
	}
	args := []string{"--config", path, "--state-dir", d.chat.opts.statePath()}
	if linked {
		args = append(args, "--project", project, "--repository", alias)
	} else {
		args = append(args, "--root", root)
	}
	return args, nil
}

package cli

import (
	"bifrost/internal/config"
	"bifrost/internal/environment"
	"bifrost/internal/ui"
	"context"
	"strings"
)

func remoteUIOptions(backend environment.Backend, ssh config.SSHSettings) ([]ui.Option, error) {
	argv, err := remoteTerminalArgv(ssh)
	if err != nil {
		return nil, err
	}
	return []ui.Option{ui.WithFilesystem(func(ctx context.Context, path string) ([]ui.FileEntry, error) {
		entries, err := backend.List(ctx, path)
		if err != nil {
			return nil, err
		}
		out := make([]ui.FileEntry, 0, len(entries))
		for _, entry := range entries {
			out = append(out, ui.FileEntry{Path: entry.Path, Dir: entry.IsDir})
		}
		return out, nil
	}, func(ctx context.Context, path string) (string, error) {
		s, _, err := backend.Read(ctx, path)
		return s, err
	}), ui.WithRemoteTerminal(argv, "")}, nil
}

func remoteTerminalArgv(ssh config.SSHSettings) ([]string, error) {
	argv, err := environment.BuildSSHArgv(environment.SSHConfig{Target: ssh.Target, Port: ssh.Port, IdentityFile: ssh.IdentityFile, RemoteExecutable: ssh.Executable, Root: ssh.Root, StateDir: ssh.StateDir})
	if err != nil {
		return nil, err
	}
	// The interactive shell and worker use distinct SSH channels.
	argv = append(argv[:1], append([]string{"-tt"}, argv[1:]...)...)
	argv[len(argv)-1] = "cd -- " + remoteQuote(ssh.Root) + " && exec \"${SHELL:-/bin/sh}\" -l"
	return argv, nil
}
func remoteQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

package cli

import (
	"bifrost/internal/environment"
	"bifrost/internal/worker"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

func runWorker(ctx context.Context, opts options, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var stdio, browserEnabled, desktopEnabled bool
	fs.BoolVar(&stdio, "stdio", false, "stdio transport")
	fs.BoolVar(&browserEnabled, "browser", false, "allow guest browser")
	fs.BoolVar(&desktopEnabled, "desktop", false, "allow guest desktop")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !stdio || fs.NArg() != 0 {
		return errors.New("worker requires --stdio --root ABS --state-dir ABS")
	}
	if opts.root == "" || opts.stateDir == "" {
		return errors.New("worker requires explicit root and state directory")
	}
	return worker.Serve(ctx, os.Stdin, out, worker.Config{Root: opts.root, StateDir: opts.stateDir, BuildVersion: Version, BrowserEnabled: browserEnabled, DesktopEnabled: desktopEnabled})
}
func connectRemote(ctx context.Context, opts options, name string) (*environment.Remote, error) {
	cfg, err := opts.settings()
	if err != nil {
		return nil, err
	}
	ssh, ok := cfg.SSH[name]
	if !ok {
		return nil, fmt.Errorf("unknown SSH environment %q; configure ssh.%s", name, name)
	}
	return environment.DialSSH(ctx, environment.SSHConfig{Target: ssh.Target, Port: ssh.Port, IdentityFile: ssh.IdentityFile, RemoteExecutable: ssh.Executable, Root: ssh.Root, StateDir: ssh.StateDir})
}
func runRemote(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: remote NAME inspect | read PATH | list PATH | review | exec -- ARGV...")
	}
	remote, err := connectRemote(ctx, opts, args[0])
	if err != nil {
		return err
	}
	defer remote.Close()
	switch args[1] {
	case "inspect":
		if len(args) != 2 {
			return errors.New("inspect takes no args")
		}
		return writeJSON(out, remote.Identity())
	case "read":
		if len(args) != 3 {
			return errors.New("read requires PATH")
		}
		text, rev, err := remote.Read(ctx, args[2])
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"content": text, "revision": rev})
	case "list":
		p := "."
		if len(args) == 3 {
			p = args[2]
		} else if len(args) != 2 {
			return errors.New("list takes one path")
		}
		entries, err := remote.List(ctx, p)
		if err != nil {
			return err
		}
		return writeJSON(out, entries)
	case "review":
		r, err := remote.Review(ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, r)
	case "exec":
		argv := args[2:]
		if len(argv) > 0 && argv[0] == "--" {
			argv = argv[1:]
		}
		r, err := remote.Exec(ctx, environment.ExecRequest{Argv: argv})
		if err != nil {
			return err
		}
		if err := writeJSON(out, r); err != nil {
			return err
		}
		if r.ExitCode != 0 {
			return &ExitError{Code: r.ExitCode}
		}
		return nil
	default:
		return fmt.Errorf("unknown remote command %q", args[1])
	}
}

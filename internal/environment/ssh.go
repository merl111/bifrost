package environment

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"bifrost/internal/worker"
)

// DialSSH starts exactly one explicit ssh client. Host-key checking,
// password prompting, and the user's SSH configuration remain enabled; this
// function deliberately does not add insecure -o overrides.
func DialSSH(ctx context.Context, cfg SSHConfig) (*Remote, error) {
	argv, err := BuildSSHArgv(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("ssh", argv[1:]...)
	configureSSHCommand(cmd)
	cmd.Env = sshEnvironment()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		stdin.Close()
		stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		stderr.Close()
		return nil, err
	}
	go func() { _, _ = io.Copy(io.Discard, stderr); _ = stderr.Close() }()
	conn := &commandConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr, done: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-conn.done:
		}
	}()
	c, err := worker.Connect(ctx, conn, worker.ClientOptions{ExpectedRoot: cfg.Root, MaxMessageBytes: cfg.MaxMessageBytes, HandshakeTimeout: cfg.HandshakeTimeout})
	if err != nil {
		conn.Close()
		return nil, err
	}
	return NewRemote(c), nil
}

// BuildSSHArgv returns argv suitable for exec.Command. The remote command is
// one shell-quoted argument because OpenSSH assembles command arguments for a
// remote shell. Values are never interpolated unquoted.
func BuildSSHArgv(cfg SSHConfig) ([]string, error) {
	if strings.TrimSpace(cfg.Target) == "" || strings.ContainsAny(cfg.Target, " \t\r\n") || strings.HasPrefix(cfg.Target, "-") {
		return nil, errors.New("environment: invalid ssh target")
	}
	if cfg.Port < 0 || cfg.Port > 65535 {
		return nil, errors.New("environment: invalid ssh port")
	}
	if cfg.RemoteExecutable == "" {
		cfg.RemoteExecutable = "bifrost"
	}
	if strings.ContainsRune(cfg.RemoteExecutable, 0) || strings.ContainsRune(cfg.Root, 0) || strings.ContainsRune(cfg.StateDir, 0) || strings.ContainsRune(cfg.IdentityFile, 0) {
		return nil, errors.New("environment: NUL in ssh configuration")
	}
	if cfg.Root == "" || cfg.StateDir == "" {
		return nil, errors.New("environment: remote root and state directory are required")
	}
	argv := []string{"ssh"}
	if cfg.Port > 0 {
		argv = append(argv, "-p", strconv.Itoa(cfg.Port))
	}
	if cfg.IdentityFile != "" {
		if !filepath.IsAbs(cfg.IdentityFile) {
			return nil, errors.New("environment: identity file must be absolute")
		}
		argv = append(argv, "-i", cfg.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	argv = append(argv, "-o", "BatchMode=yes")
	remote := []string{cfg.RemoteExecutable, "worker", "--stdio", "--root", cfg.Root, "--state-dir", cfg.StateDir}
	if cfg.BrowserEnabled {
		remote = append(remote, "--browser")
	}
	if cfg.DesktopEnabled {
		remote = append(remote, "--desktop")
	}
	parts := make([]string, len(remote))
	for i, v := range remote {
		parts[i] = shellQuote(v)
	}
	return append(argv, cfg.Target, strings.Join(parts, " ")), nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// sshEnvironment preserves normal SSH operation while preventing model/API
// credentials in the parent process environment from crossing the transport.
func sshEnvironment() []string {
	out := make([]string, 0)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		u := strings.ToUpper(key)
		if strings.Contains(u, "API_KEY") || strings.Contains(u, "_TOKEN") || strings.Contains(u, "_SECRET") || strings.Contains(u, "_PASSWORD") || strings.HasPrefix(u, "BIFROST_MODEL_") {
			continue
		}
		out = append(out, entry)
	}
	return out
}

type commandConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser
	once   sync.Once
	done   chan struct{}
}

func (c *commandConn) Read(p []byte) (int, error)  { return c.stdout.Read(p) }
func (c *commandConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }
func (c *commandConn) Close() error {
	var err error
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		_ = c.stderr.Close()
		killSSHCommand(c.cmd)
		err = c.cmd.Wait()
		close(c.done)
	})
	return err
}

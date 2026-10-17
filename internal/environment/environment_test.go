package environment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildSSHArgvQuotesRemoteValues(t *testing.T) {
	argv, err := BuildSSHArgv(SSHConfig{Target: "user@example", Port: 2222, IdentityFile: "/tmp/key", RemoteExecutable: "/opt/my bifrost", Root: "/srv/o'repo", StateDir: "/var/lib/bifrost"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, bad := range []string{"StrictHostKeyChecking=no", "UserKnownHostsFile=/dev/null"} {
		if strings.Contains(joined, bad) {
			t.Fatalf("insecure ssh option %q in %q", bad, joined)
		}
	}
	if !strings.Contains(joined, "BatchMode=yes") {
		t.Fatalf("ssh must not prompt for passwords: %q", joined)
	}
	if argv[1] != "-p" || argv[2] != "2222" || argv[3] != "-i" || argv[4] != "/tmp/key" || argv[9] != "user@example" {
		t.Fatalf("unexpected argv %#v", argv)
	}
	if !strings.Contains(argv[10], "'\"'\"'") {
		t.Fatalf("remote command is not shell quoted: %q", argv[10])
	}
}

func TestBuildSSHArgvForwardsGuestControlOptIns(t *testing.T) {
	argv, err := BuildSSHArgv(SSHConfig{
		Target: "user@example", Root: "/srv/repo", StateDir: "/var/lib/bifrost",
		BrowserEnabled: true, DesktopEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := argv[len(argv)-1], "'bifrost' 'worker' '--stdio' '--root' '/srv/repo' '--state-dir' '/var/lib/bifrost' '--browser' '--desktop'"; got != want {
		t.Fatalf("remote command = %q, want %q", got, want)
	}
}

func TestSSHEnvironmentDropsModelSecrets(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "secret")
	t.Setenv("BIFROST_MODEL_TOKEN", "secret")
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	joined := strings.Join(sshEnvironment(), "\n")
	if strings.Contains(joined, "OPENAI_API_KEY=") || strings.Contains(joined, "BIFROST_MODEL_TOKEN=") {
		t.Fatal("model credential leaked into ssh environment")
	}
	if !strings.Contains(joined, "SSH_AUTH_SOCK=/tmp/agent.sock") {
		t.Fatal("ssh agent environment was dropped")
	}
}

func TestLocalUsesWorkspaceAndRejectsEscapingCWD(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	l, err := OpenLocal(LocalConfig{Root: root, StateDir: state, ID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got, _, err := l.Read(context.Background(), "x.txt")
	if err != nil || got != "x" {
		t.Fatalf("read %q err=%v", got, err)
	}
	if _, err := l.Exec(context.Background(), ExecRequest{Argv: []string{"pwd"}, CWD: "../"}); err == nil {
		t.Fatal("expected cwd escape rejection")
	}
}

func TestDialSSHContextStopsUnresponsiveClient(t *testing.T) {
	bin := t.TempDir()
	ssh := filepath.Join(bin, "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nsleep 30\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := DialSSH(ctx, SSHConfig{Target: "host", Root: t.TempDir(), StateDir: t.TempDir()})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

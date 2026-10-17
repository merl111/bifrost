package process

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func shell(t *testing.T, script string, max int) Result {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture uses sh")
	}
	r, err := Run(context.Background(), Spec{Argv: []string{"sh", "-c", script}, MaxOutputBytes: max})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunCapturesExitAndStreams(t *testing.T) {
	r := shell(t, "printf out; printf err >&2; exit 7", 1024)
	if r.ExitCode != 7 || string(r.Stdout) != "out" || string(r.Stderr) != "err" {
		t.Fatalf("result = %#v", r)
	}
}

func TestRunCapsOutputWithoutBlockingChild(t *testing.T) {
	r := shell(t, "yes x | head -c 200000", 1024)
	if len(r.Stdout) != 1024 || !r.StdoutTruncated {
		t.Fatalf("output cap = %d, truncated=%v", len(r.Stdout), r.StdoutTruncated)
	}
}

func TestRunUsesExplicitWorkingDirectoryAndEnvironment(t *testing.T) {
	dir := t.TempDir()
	r, err := Run(context.Background(), Spec{Argv: []string{"sh", "-c", "printf '%s/%s' \"$PWD\" \"$BIFROST_TEST\""}, Dir: dir, Env: []string{"BIFROST_TEST=ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(r.Stdout); got != dir+"/ok" && got != filepath.Clean(dir)+"/ok" {
		t.Fatalf("cwd/env output = %q", got)
	}
}

func TestRunCancellationKillsProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process group fixture uses sh")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := Run(ctx, Spec{Argv: []string{"sh", "-c", "trap '' TERM; sleep 30 & wait"}})
	if !errors.Is(err, context.DeadlineExceeded) || !r.Cancelled {
		t.Fatalf("cancel result = %#v, %v", r, err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("process group cancellation took %s", elapsed)
	}
}

func TestRunRejectsEmptyArgv(t *testing.T) {
	_, err := Run(context.Background(), Spec{})
	if err == nil || !strings.Contains(err.Error(), "argv") {
		t.Fatalf("empty argv error = %v", err)
	}
}

func TestRunMissingExecutable(t *testing.T) {
	r, err := Run(context.Background(), Spec{Argv: []string{"/definitely/missing/bifrost-command"}})
	if err == nil || r.ExitCode != -1 || !os.IsNotExist(err) {
		t.Fatalf("missing command = %#v, %v", r, err)
	}
}

func TestQuickExitDrainsAllOutput(t *testing.T) {
	// Larger than a pipe buffer: Wait must not close StdoutPipe before readers finish.
	for i := 0; i < 20; i++ {
		r := shell(t, "head -c 131072 /dev/zero", 262144)
		if len(r.Stdout) != 131072 || r.StdoutTruncated {
			t.Fatalf("iteration %d: lost output: %d", i, len(r.Stdout))
		}
	}
}

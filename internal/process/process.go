// Package process runs bounded, non-interactive commands. It intentionally
// accepts argv rather than a shell string; PTY and interactive lifecycle
// control belong to a separate layer.
package process

import (
	"context"
	"errors"
	"os/exec"
	"time"
)

const defaultOutputLimit = 1 << 20

// Spec describes one explicit argv command. Env, when non-nil, is passed as
// the complete environment in exec.Cmd's standard KEY=value form.
type Spec struct {
	Argv           []string
	Dir            string
	Env            []string
	MaxOutputBytes int
}

// Result is available even when Run returns a context cancellation error.
// Non-zero command exit status is represented in ExitCode and is not itself a
// Go error.
type Result struct {
	Argv            []string
	ExitCode        int
	Stdout          []byte
	Stderr          []byte
	StdoutTruncated bool
	StderrTruncated bool
	Cancelled       bool
	Duration        time.Duration
}

func Run(ctx context.Context, spec Spec) (Result, error) {
	if len(spec.Argv) == 0 || spec.Argv[0] == "" {
		return Result{ExitCode: -1}, errors.New("process: argv is required")
	}
	if err := ctx.Err(); err != nil {
		return Result{Argv: append([]string(nil), spec.Argv...), ExitCode: -1, Cancelled: true}, err
	}
	limit := spec.MaxOutputBytes
	if limit <= 0 {
		limit = defaultOutputLimit
	}
	cmd := exec.Command(spec.Argv[0], spec.Argv[1:]...)
	cmd.Dir = spec.Dir
	if spec.Env != nil {
		cmd.Env = append([]string(nil), spec.Env...)
	}
	configureProcess(cmd)
	var out, errOut cappedBuffer
	out.limit, errOut.limit = limit, limit
	cmd.Stdout, cmd.Stderr = &out, &errOut
	// Wait drains the copy goroutines before returning. A child that leaves
	// inherited pipes open cannot keep the caller waiting indefinitely.
	cmd.WaitDelay = time.Second
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return Result{Argv: append([]string(nil), spec.Argv...), ExitCode: -1}, err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	cancelled := false
	var waitErr error
	select {
	case waitErr = <-waitDone:
	case <-ctx.Done():
		cancelled = true
		_ = terminateProcess(cmd, false)
		timer := time.NewTimer(500 * time.Millisecond)
		select {
		case waitErr = <-waitDone:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			_ = terminateProcess(cmd, true)
			waitErr = <-waitDone
		}
	}
	if cancelled {
		// The group leader may exit on TERM while a descendant ignores it.
		// Finish cleanup even when Wait returned before the grace timer.
		_ = terminateProcess(cmd, true)
	}
	result := Result{
		Argv:            append([]string(nil), spec.Argv...),
		ExitCode:        exitCode(cmd, waitErr),
		Stdout:          out.Bytes(),
		Stderr:          errOut.Bytes(),
		StdoutTruncated: out.truncated,
		StderrTruncated: errOut.truncated,
		Cancelled:       cancelled,
		Duration:        time.Since(started),
	}
	if cancelled {
		return result, ctx.Err()
	}
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		return result, waitErr
	}
	return result, nil
}

type cappedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - len(b.buf)
	if remaining > 0 {
		if len(p) <= remaining {
			b.buf = append(b.buf, p...)
		} else {
			b.buf = append(b.buf, p[:remaining]...)
			b.truncated = true
		}
	} else if len(p) > 0 {
		b.truncated = true
	}
	// Always consume the complete pipe input so a prolific child cannot block.
	return len(p), nil
}

func (b *cappedBuffer) Bytes() []byte { return append([]byte(nil), b.buf...) }

func exitCode(cmd *exec.Cmd, waitErr error) int {
	if cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	var ee *exec.ExitError
	if errors.As(waitErr, &ee) && ee.ProcessState != nil {
		return ee.ProcessState.ExitCode()
	}
	return -1
}

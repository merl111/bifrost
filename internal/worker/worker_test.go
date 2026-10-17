package worker_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bifrost/internal/desktop"
	"bifrost/internal/process"
	"bifrost/internal/worker"
	"bifrost/internal/workspace"
)

func TestWorkerHelper(t *testing.T) {
	if os.Getenv("BIFROST_WORKER_HELPER") != "1" {
		return
	}
	root := os.Getenv("BIFROST_WORKER_ROOT")
	state := os.Getenv("BIFROST_WORKER_STATE")
	if err := worker.Serve(context.Background(), os.Stdin, os.Stdout, worker.Config{Root: root, StateDir: state, BuildVersion: "test"}); err != nil && !errors.Is(err, io.EOF) {
		os.Exit(3)
	}
	os.Exit(0)
}

type procConn struct {
	in  io.WriteCloser
	out io.ReadCloser
	cmd *exec.Cmd
}

func (c *procConn) Read(p []byte) (int, error)  { return c.out.Read(p) }
func (c *procConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *procConn) Close() error {
	_ = c.in.Close()
	_ = c.out.Close()
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	return c.cmd.Wait()
}

func startWorker(t *testing.T, root, state string) (*worker.Client, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestWorkerHelper")
	cmd.Env = append(os.Environ(), "BIFROST_WORKER_HELPER=1", "BIFROST_WORKER_ROOT="+root, "BIFROST_WORKER_STATE="+state)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	conn := &procConn{in: in, out: out, cmd: cmd}
	c, err := worker.Connect(context.Background(), conn, worker.ClientOptions{ExpectedRoot: root, HandshakeTimeout: 2 * time.Second})
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return c, cmd
}

func TestSubprocessOperationsAndCancellation(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	c, cmd := startWorker(t, root, state)
	defer func() { c.Close(); _ = cmd.Wait() }()
	content, rev, err := c.Read(context.Background(), "a.txt")
	if err != nil || content != "hello\n" || rev == "" {
		t.Fatalf("read content=%q rev=%q err=%v", content, rev, err)
	}
	change, err := c.Edit(context.Background(), "a.txt", rev, "hello", "world")
	if err != nil {
		t.Fatal(err)
	}
	if change.After == "" {
		t.Fatal("missing change revision")
	}
	if _, err := c.Edit(context.Background(), "a.txt", rev, "world", "again"); err == nil || !strings.Contains(err.Error(), "revision conflict") {
		t.Fatalf("stale edit err=%v", err)
	}
	r, err := c.Exec(context.Background(), []string{"sh", "-c", "printf output"}, "", 0)
	if err != nil || string(r.Stdout) != "output" {
		t.Fatalf("exec result=%q err=%v", r.Stdout, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = c.Exec(ctx, []string{"sh", "-c", "sleep 30"}, "", 0)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel err=%v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("cancellation took too long")
	}
}

func TestSubprocessPatchRPC(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("before"), 0644); err != nil {
		t.Fatal(err)
	}
	c, cmd := startWorker(t, root, state)
	defer func() { _ = c.Close(); _ = cmd.Wait() }()
	_, rev, err := c.Read(context.Background(), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.ApplyPatch(context.Background(), workspace.PatchPlan{Operations: []workspace.PatchOperation{
		{Kind: "replace", Path: "a.txt", Expected: &rev, Content: "after"},
		{Kind: "create", Path: "new.txt", Content: "created"},
	}})
	if err != nil || result.Status != "committed" {
		t.Fatalf("patch result=%+v err=%v", result, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(content) != "after" {
		t.Fatalf("patched a.txt=%q err=%v", content, err)
	}
	undone, err := c.UndoPatch(context.Background(), result.ID)
	if err != nil || undone.Status != "reverted" {
		t.Fatalf("undo patch result=%+v err=%v", undone, err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "a.txt")); err != nil || string(content) != "before" {
		t.Fatalf("undo a.txt=%q err=%v", content, err)
	}
}

func TestRootMismatchAndProtocolLimit(t *testing.T) {
	root := t.TempDir()
	state := t.TempDir()
	_, cmd := startWorker(t, root, state)
	// A second client cannot claim a different canonical root during the
	// authenticated final hello. Close the first transport by killing helper.
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	cmd = exec.Command(os.Args[0], "-test.run=TestWorkerHelper")
	cmd.Env = append(os.Environ(), "BIFROST_WORKER_HELPER=1", "BIFROST_WORKER_ROOT="+root, "BIFROST_WORKER_STATE="+state)
	in, _ := cmd.StdinPipe()
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	conn := &procConn{in: in, out: out, cmd: cmd}
	_, err := worker.Connect(context.Background(), conn, worker.ClientOptions{ExpectedRoot: t.TempDir(), HandshakeTimeout: 2 * time.Second})
	if !errors.Is(err, worker.ErrRootMismatch) {
		t.Fatalf("root mismatch err=%v", err)
	}
}

func TestServeCancellationClosesStdioReader(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	r, w := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Serve(ctx, r, io.Discard, worker.Config{Root: root, StateDir: state}) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("serve err=%v", err)
		}
	case <-time.After(time.Second):
		_ = w.Close()
		t.Fatal("Serve remained blocked after context cancellation")
	}
	_ = w.Close()
}

type pipeConn struct {
	reader *io.PipeReader
	writer *io.PipeWriter
}

func (c *pipeConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *pipeConn) Write(p []byte) (int, error) { return c.writer.Write(p) }
func (c *pipeConn) Close() error {
	_ = c.reader.Close()
	return c.writer.Close()
}

func TestGuestDesktopCapabilityHandshakeAndRPC(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- worker.Serve(ctx, clientToServerReader, serverToClientWriter, worker.Config{
			Root: root, StateDir: state, DesktopEnabled: true,
			DesktopConfig: desktop.Config{Backend: desktop.BackendUnknown},
		})
	}()
	conn := &pipeConn{reader: serverToClientReader, writer: clientToServerWriter}
	c, err := worker.Connect(ctx, conn, worker.ClientOptions{ExpectedRoot: root, HandshakeTimeout: time.Second})
	if err != nil {
		conn.Close()
		t.Fatalf("connect: %v", err)
	}
	if !c.Hello().Capabilities.Desktop {
		c.Close()
		t.Fatal("worker did not advertise explicitly enabled desktop capability")
	}
	capability, err := c.DesktopCapabilities(ctx)
	if err != nil {
		c.Close()
		t.Fatalf("desktop capabilities RPC: %v", err)
	}
	if capability.Backend != desktop.BackendUnknown || capability.Capture {
		c.Close()
		t.Fatalf("unexpected capability report: %+v", capability)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("worker did not close after client transport closed")
	}
}

func TestGuestDesktopCaptureReturnsBoundedImageAndCleansArtifact(t *testing.T) {
	root, state := t.TempDir(), t.TempDir()
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var capturePath string
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- worker.Serve(ctx, clientToServerReader, serverToClientWriter, worker.Config{
			Root: root, StateDir: state, DesktopEnabled: true,
			DesktopConfig: desktop.Config{
				Backend: desktop.BackendX11, Display: ":99",
				Tools: desktop.ToolPaths{Import: "fixture-import", Xdotool: "fixture-xdotool"},
				Run: func(_ context.Context, spec process.Spec) (process.Result, error) {
					if len(spec.Argv) > 0 && spec.Argv[0] == "fixture-import" && len(spec.Argv) == 4 {
						capturePath = spec.Argv[3]
						if err := os.WriteFile(capturePath, []byte("png"), 0o600); err != nil {
							return process.Result{}, err
						}
					}
					if len(spec.Argv) > 0 && spec.Argv[0] == "fixture-xdotool" {
						return process.Result{Stdout: []byte("640 480\n")}, nil
					}
					return process.Result{}, nil
				},
			},
		})
	}()
	conn := &pipeConn{reader: serverToClientReader, writer: clientToServerWriter}
	c, err := worker.Connect(ctx, conn, worker.ClientOptions{ExpectedRoot: root, HandshakeTimeout: time.Second})
	if err != nil {
		conn.Close()
		t.Fatalf("connect: %v", err)
	}
	result, err := c.DesktopCapture(ctx)
	if err != nil {
		c.Close()
		t.Fatalf("desktop capture RPC: %v", err)
	}
	if string(result.PNG) != "png" || result.Observation.Width != 640 || result.Observation.Height != 480 {
		t.Fatalf("capture result = %+v", result)
	}
	if capturePath == "" {
		t.Fatal("fixture did not receive capture path")
	}
	if _, err := os.Stat(capturePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("capture artifact still exists, stat err=%v", err)
	}
	_ = c.Close()
	select {
	case <-serveDone:
	case <-time.After(time.Second):
		t.Fatal("worker did not close after capture client transport closed")
	}
}

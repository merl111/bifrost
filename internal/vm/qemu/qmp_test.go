package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDialStatusEventsInputAndScreenshot(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go fakeQMP(t, listener, serverDone)
	client, err := Dial(context.Background(), Config{SocketPath: socket, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.QueryStatus(context.Background())
	if err != nil || status.State != "running" || !status.Running {
		t.Fatalf("status = %+v, %v", status, err)
	}
	events := client.DrainEvents()
	if len(events) != 1 || events[0].Name != "RESUME" {
		t.Fatalf("events = %+v", events)
	}
	if err := client.SendKey(context.Background(), "ret"); err != nil {
		t.Fatal(err)
	}
	if err := client.SendPointer(context.Background(), 10, 20, "left", true); err != nil {
		t.Fatal(err)
	}
	if err := client.SendPointer(context.Background(), 32767, 32767, "", false); err != nil {
		t.Fatal("maximum absolute pointer coordinate rejected:", err)
	}
	if err := client.SendPointer(context.Background(), 32768, 0, "", false); err == nil {
		t.Fatal("out-of-range absolute pointer coordinate accepted")
	}
	output := filepath.Join(t.TempDir(), "screen.png")
	if err := client.Screenshot(context.Background(), output); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || info.Size() == 0 {
		t.Fatalf("screenshot = %v, %v", info, err)
	}
	if err := client.Screenshot(context.Background(), output); err == nil {
		t.Fatal("existing screenshot destination accepted")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-serverDone:
		if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("fake QMP server did not stop")
	}
}

func TestLifecycleAndSnapshotCommandsAreFixedAndValidated(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go fakeQMP(t, listener, serverDone)
	client, err := Dial(context.Background(), Config{SocketPath: socket, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, action := range []LifecycleAction{Pause, Resume, Reset, Powerdown} {
		if err := client.Lifecycle(context.Background(), action); err != nil {
			t.Fatalf("lifecycle %s: %v", action, err)
		}
	}
	spec := SnapshotSpec{Tag: "before-edit", JobID: "job-1", VMState: "disk0", Devices: []string{"disk0"}}
	if err := client.SaveSnapshot(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := client.LoadSnapshot(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteSnapshot(context.Background(), spec.Tag, "job-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListSnapshots(context.Background()); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("snapshot list error = %v", err)
	}
	if err := client.SaveSnapshot(context.Background(), SnapshotSpec{Tag: "bad\nname", JobID: "j", VMState: "disk0"}); err == nil {
		t.Fatal("invalid snapshot name accepted")
	}
}

func TestScreenshotParentMustBeDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := screenshotPath(filepath.Join(parent, "capture.png")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("parent error = %v", err)
	}
}

func TestDialCancellationAndDisconnect(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte(`{"QMP":{"version":{},"capabilities":[]}}` + "\r\n"))
		reader := bufio.NewReader(conn)
		_, _ = reader.ReadBytes('\n')
		_, _ = conn.Write([]byte(`{"return":{},"id":1}` + "\r\n"))
		_, _ = reader.ReadBytes('\n')
		select {}
	}()
	client, err := Dial(context.Background(), Config{SocketPath: socket, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = client.QueryStatus(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestCloseInterruptsBlockedOperation(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	querySeen := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		if _, err := conn.Write([]byte(`{"QMP":{"version":{},"capabilities":[]}}` + "\r\n")); err != nil {
			serverDone <- err
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			serverDone <- err
			return
		}
		if _, err := conn.Write([]byte(`{"return":{},"id":1}` + "\r\n")); err != nil {
			serverDone <- err
			return
		}
		if _, err := reader.ReadBytes('\n'); err != nil {
			serverDone <- err
			return
		}
		close(querySeen)
		_, err = reader.ReadBytes('\n')
		serverDone <- err
	}()
	client, err := Dial(context.Background(), Config{SocketPath: socket, Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := client.QueryStatus(context.Background())
		result <- err
	}()
	select {
	case <-querySeen:
	case <-time.After(time.Second):
		t.Fatal("QMP query was not received")
	}
	started := time.Now()
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Close blocked for %s", elapsed)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("blocked operation unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("blocked operation did not unblock after Close")
	}
	select {
	case <-serverDone:
	case <-time.After(time.Second):
		t.Fatal("QMP server did not observe disconnect")
	}
}

func TestDisposableQEMUQueryStatus(t *testing.T) {
	if os.Getenv("BIFROST_QEMU_INTEGRATION") == "" {
		t.Skip("set BIFROST_QEMU_INTEGRATION=1 to run disposable QEMU integration")
	}
	qemu, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		t.Skip("qemu-system-x86_64 unavailable")
	}
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	cmd := exec.Command(qemu, "-machine", "none", "-nodefaults", "-display", "none", "-S", "-no-reboot", "-qmp", "unix:"+socket+",server=on,wait=off")
	if err := cmd.Start(); err != nil {
		t.Skipf("disposable QEMU unavailable: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("QEMU did not publish QMP socket")
		}
		time.Sleep(20 * time.Millisecond)
	}
	client, err := Dial(context.Background(), Config{SocketPath: socket, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	status, err := client.QueryStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.State == "" {
		t.Fatalf("unexpected disposable QEMU status: %+v", status)
	}
}

func fakeQMP(t *testing.T, listener net.Listener, done chan<- error) {
	t.Helper()
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"QMP":{"version":{"qemu":{"major":11}},"capabilities":["oob"]}}` + "\r\n")); err != nil {
		done <- err
		return
	}
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			done <- err
			return
		}
		var request struct {
			Execute   string          `json:"execute"`
			ID        json.RawMessage `json:"id"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(line))), &request); err != nil {
			done <- err
			return
		}
		switch request.Execute {
		case "qmp_capabilities":
			if _, err := conn.Write([]byte(`{"return":{},"id":` + string(request.ID) + "}\r\n")); err != nil {
				done <- err
				return
			}
		case "query-status":
			_, _ = conn.Write([]byte(`{"event":"RESUME","data":{"reason":"test"}}` + "\r\n"))
			_, err = conn.Write([]byte(`{"return":{"status":"running","running":true,"singlestep":false},"id":` + string(request.ID) + "}\r\n"))
		case "input-send-event":
			if len(request.Arguments) == 0 || !strings.Contains(string(request.Arguments), "events") {
				done <- errors.New("input events missing")
				return
			}
			_, err = conn.Write([]byte(`{"return":{},"id":` + string(request.ID) + "}\r\n"))
		case "screendump":
			var args struct {
				Filename string `json:"filename"`
			}
			if err := json.Unmarshal(request.Arguments, &args); err != nil {
				done <- err
				return
			}
			if err := os.WriteFile(args.Filename, []byte("png"), 0o600); err != nil {
				done <- err
				return
			}
			_, err = conn.Write([]byte(`{"return":{},"id":` + string(request.ID) + "}\r\n"))
		case "stop", "cont", "system_reset", "system_powerdown", "snapshot-save", "snapshot-load", "snapshot-delete":
			_, err = conn.Write([]byte(`{"return":{},"id":` + string(request.ID) + "}\r\n"))
		default:
			done <- errors.New("unexpected QMP command " + request.Execute)
			return
		}
		if err != nil {
			done <- err
			return
		}
	}
}

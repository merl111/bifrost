package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRunVMQEMUStatusUsesQMPAndJSON(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "qmp.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("Unix sockets unavailable: %v", err)
	}
	defer listener.Close()
	serverErr := make(chan error, 1)
	go serveCLIQMP(listener, serverErr)
	var output bytes.Buffer
	if err := runVM(context.Background(), options{}, []string{"qemu", "status", socket}, &output); err != nil {
		t.Fatal(err)
	}
	var status struct {
		State   string `json:"status"`
		Running bool   `json:"running"`
	}
	if err := json.Unmarshal(output.Bytes(), &status); err != nil {
		t.Fatalf("status JSON %q: %v", output.String(), err)
	}
	if status.State != "running" || !status.Running {
		t.Fatalf("status = %+v", status)
	}
	if err := <-serverErr; err != nil && !strings.Contains(err.Error(), "closed") && !strings.Contains(err.Error(), "EOF") {
		t.Fatal(err)
	}
}

func TestRunVMValidatesQEMUArgumentsBeforeConnecting(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.sock")
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "wrong arity", args: []string{"qemu", "status", missing, "extra"}},
		{name: "relative screenshot", args: []string{"qemu", "screenshot", missing, "screen.png"}},
		{name: "bad pointer x", args: []string{"qemu", "pointer", missing, "x", "2", "left", "true"}},
		{name: "pointer above QEMU range", args: []string{"qemu", "pointer", missing, "32768", "2", "left", "true"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := runVM(context.Background(), options{}, test.args, &output); err == nil {
				t.Fatal("invalid VM command unexpectedly succeeded")
			}
			if strings.Contains(output.String(), "captured") {
				t.Fatal("invalid VM command emitted success output")
			}
		})
	}
}

func TestRunVMVirtualBoxFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX executable")
	}
	bin := t.TempDir()
	vbox := filepath.Join(bin, "VBoxManage")
	script := `#!/bin/sh
case "$1" in
list) printf '"demo" {11111111-1111-1111-1111-111111111111}\n' ;;
showvminfo) printf 'name="demo"\nUUID="{11111111-1111-1111-1111-111111111111}"\nVMState="running"\nCfgFile="/tmp/demo.vbox"\n' ;;
controlvm)
  case "$3" in
  screenshotpng) printf 'png' > "$4" ;;
  keyboardputstring|keyboardputscancode) ;;
  esac
  ;;
startvm) ;;
snapshot)
  case "$3" in
  list) printf 'SnapshotName="base"\nSnapshotUUID="snap-1"\n' ;;
  take|restore|delete) ;;
  esac
  ;;
*) exit 2 ;;
esac
`
	if err := os.WriteFile(vbox, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	var output bytes.Buffer
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "list"}, &output); err != nil {
		t.Fatal(err)
	}
	var machines []struct {
		Name string `json:"name"`
		UUID string `json:"uuid"`
	}
	if err := json.Unmarshal(output.Bytes(), &machines); err != nil || len(machines) != 1 || machines[0].Name != "demo" {
		t.Fatalf("list = %q, %v", output.String(), err)
	}

	output.Reset()
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "show", "demo"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"state": "running"`) {
		t.Fatalf("show = %q", output.String())
	}

	screenshot := filepath.Join(t.TempDir(), "screen.png")
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "screenshot", "demo", screenshot}, &output); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(screenshot); err != nil || string(content) != "png" {
		t.Fatalf("screenshot = %q, %v", content, err)
	}
	output.Reset()
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "text", "demo", "hello", "world"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"text": "hello world"`) {
		t.Fatalf("text = %q", output.String())
	}
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "scancodes", "demo", "1d", "2e"}, &output); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "pause", "resume", "reset", "acpi-shutdown", "poweroff"} {
		if err := runVM(context.Background(), options{}, []string{"virtualbox", action, "demo"}, &output); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	output.Reset()
	if err := runVM(context.Background(), options{}, []string{"virtualbox", "snapshot", "list", "demo"}, &output); err != nil || !strings.Contains(output.String(), "base") {
		t.Fatalf("snapshot list = %q err=%v", output.String(), err)
	}
	for _, args := range [][]string{{"take", "demo", "before"}, {"restore", "demo", "before"}, {"delete", "demo", "before"}} {
		if err := runVM(context.Background(), options{}, append([]string{"virtualbox", "snapshot"}, args...), &output); err != nil {
			t.Fatalf("snapshot %v: %v", args, err)
		}
	}
}

func serveCLIQMP(listener net.Listener, done chan<- error) {
	conn, err := listener.Accept()
	if err != nil {
		done <- err
		return
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, `{"QMP":{"version":{},"capabilities":[]}}`+"\r\n"); err != nil {
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
			Execute string          `json:"execute"`
			ID      json.RawMessage `json:"id"`
		}
		if err := json.Unmarshal(bytes.TrimSpace(line), &request); err != nil {
			done <- err
			return
		}
		if request.Execute == "qmp_capabilities" {
			_, err = fmt.Fprintf(conn, `{"return":{},"id":%s}`+"\r\n", request.ID)
		} else if request.Execute == "query-status" {
			_, err = fmt.Fprintf(conn, `{"return":{"status":"running","running":true,"singlestep":false},"id":%s}`+"\r\n", request.ID)
		} else {
			done <- fmt.Errorf("unexpected QMP command %q", request.Execute)
			return
		}
		if err != nil {
			done <- err
			return
		}
	}
}

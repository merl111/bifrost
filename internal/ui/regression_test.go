package ui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestCloseDuringBackpressuredInput(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux PTY lifecycle regression")
	}
	root := t.TempDir()
	shell := filepath.Join(root, "blocked-shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nstty -echo -icanon\nprintf READY\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	p, err := startPTY(root, 80, 20, shell)
	if err != nil {
		t.Fatal(err)
	}
	ready := make(chan struct{})
	go func() {
		for {
			msg := p.read(context.Background())
			if strings.Contains(string(msg.data), "READY") {
				close(ready)
				return
			}
			if msg.err != nil {
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("no ready output")
	}
	pasted := make(chan struct{})
	go func() { _ = p.paste(strings.Repeat("x", 1<<20)); close(pasted) }()
	time.Sleep(30 * time.Millisecond)
	closed := make(chan struct{})
	go func() { _ = p.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		buf := make([]byte, 65536)
		n := runtime.Stack(buf, true)
		t.Log(string(buf[:n]))
		p.cmd.Process.Kill()
		t.Fatal("close blocked behind paste")
	}
	select {
	case <-pasted:
	case <-time.After(time.Second):
		t.Fatal("paste did not stop")
	}
}

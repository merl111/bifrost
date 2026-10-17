package lsp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bifrost/internal/lsp"
)

func TestLSPHelper(t *testing.T) {
	if os.Getenv("BIFROST_LSP_HELPER") != "1" {
		return
	}
	r := bufio.NewReader(os.Stdin)
	for {
		body, err := readFixture(r)
		if err != nil {
			return
		}
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(body, &msg) != nil {
			continue
		}
		switch msg.Method {
		case "initialize":
			writeFixture(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]any{"capabilities": map[string]any{}}})
		case "textDocument/didOpen":
			writeFixture(map[string]any{"jsonrpc": "2.0", "method": "textDocument/publishDiagnostics", "params": map[string]any{"uri": "file:///tmp/note.txt", "diagnostics": []any{map[string]any{"message": "fixture warning", "range": map[string]any{"start": map[string]int{"line": 0, "character": 0}, "end": map[string]int{"line": 0, "character": 1}}}}}})
		case "textDocument/hover":
			writeFixture(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": map[string]string{"contents": "fixture hover"}})
		case "textDocument/definition":
			writeFixture(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": []any{}})
		case "shutdown":
			writeFixture(map[string]any{"jsonrpc": "2.0", "id": *msg.ID, "result": nil})
		case "exit":
			return
		}
	}
}
func writeFixture(v any) {
	b, _ := json.Marshal(v)
	fmt.Printf("Content-Length: %d\r\n\r\n%s", len(b), b)
}
func readFixture(r *bufio.Reader) ([]byte, error) {
	n := -1
	for {
		line, e := r.ReadString('\n')
		if e != nil {
			return nil, e
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			fmt.Sscanf(line, "Content-Length: %d", &n)
		}
	}
	if n < 0 {
		return nil, errors.New("missing length")
	}
	b := make([]byte, n)
	_, e := io.ReadFull(r, b)
	return b, e
}

type lspProc struct {
	in  io.WriteCloser
	out io.ReadCloser
	cmd *exec.Cmd
}

func (p *lspProc) Read(b []byte) (int, error)  { return p.out.Read(b) }
func (p *lspProc) Write(b []byte) (int, error) { return p.in.Write(b) }
func (p *lspProc) Close() error {
	_ = p.in.Close()
	_ = p.out.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	return p.cmd.Wait()
}

func TestClientInitializeDocumentsDiagnosticsAndRequests(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BIFROST_LSP_HELPER", "1")
	c, err := lsp.Start(context.Background(), lsp.Config{Command: []string{os.Args[0], "-test.run=TestLSPHelper"}, WorkspaceRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.DidOpen(context.Background(), filepath.Join(root, "note.txt"), "text", "hello", 1); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-c.Diagnostics():
		if len(d.Diagnostics) != 1 || d.Diagnostics[0].Message != "fixture warning" {
			t.Fatalf("diagnostics=%+v", d)
		}
	case <-time.After(time.Second):
		t.Fatal("diagnostics timeout")
	}
	h, err := c.Hover(context.Background(), filepath.Join(root, "note.txt"), 0, 0)
	if err != nil || !strings.Contains(string(h), "fixture hover") {
		t.Fatalf("hover=%s err=%v", h, err)
	}
	if _, err := c.Definition(context.Background(), "note.txt", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := c.DidChange(context.Background(), "note.txt", "changed", 2); err != nil {
		t.Fatal(err)
	}
	if err := c.DidChange(context.Background(), "note.txt", "stale", 2); err == nil {
		t.Fatal("stale document version accepted")
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

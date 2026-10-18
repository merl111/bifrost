package cursorclient

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"bifrost/internal/workspace"
)

func TestRootedRegexAndGlobSearch(t *testing.T) {
	root := t.TempDir()
	for name, body := range map[string]string{"main.go": "package main\nfunc Desktop() {}\nfunc desktopOther() {}\n", "nested/server.go": "package nested\nfunc desktopStart() {}\n", "binary.bin": "\x00binary", "notes.txt": "func desktopNoise()\n", ".git/secret.go": "func desktopHidden()\n"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.Open(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	env := &ToolEnv{Root: root, Files: wsFiles{ws, root}}
	args, _ := New("GrepArgs")
	args.Set("pattern", `^func desktop\w*\(`)
	args.Set("case_insensitive", true)
	args.Set("glob", "**/*.go")
	result, err := env.search(context.Background(), args)
	if err != nil || len(result.Matches) != 3 {
		t.Fatalf("regex/glob: %+v %v", result, err)
	}
	args.Set("path", "nested")
	result, err = env.search(context.Background(), args)
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Path != "nested/server.go" {
		t.Fatalf("scope: %+v %v", result, err)
	}
	args.Set("path", "main.go")
	result, err = env.search(context.Background(), args)
	if err != nil || len(result.Matches) != 2 {
		t.Fatalf("file scope: %+v %v", result, err)
	}
	args.Set("head_limit", int64(1))
	result, err = env.search(context.Background(), args)
	if err != nil || len(result.Matches) != 1 || !result.Truncated {
		t.Fatalf("limit: %+v %v", result, err)
	}
	args.Set("path", ".")
	args.Set("glob", "")
	args.Set("head_limit", int64(100))
	result, err = env.search(context.Background(), args)
	if err != nil || len(result.Matches) != 4 {
		t.Fatalf("binary skip: %+v %v", result, err)
	}
	args.Set("path", "../outside")
	if _, err = env.search(context.Background(), args); err == nil {
		t.Fatal("escaped root")
	}
	args.Set("path", ".")
	args.Set("pattern", "[")
	if _, err = env.search(context.Background(), args); err == nil {
		t.Fatal("accepted invalid regex")
	}
	find, _ := New("PiFindExecArgs")
	find.Set("pattern", "**/*.go")
	found, err := env.findText(context.Background(), find)
	if err != nil || found != "main.go\nnested/server.go\n" {
		t.Fatalf("glob: %q %v", found, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := env.findText(ctx, find); err == nil {
		t.Fatal("swallowed cancellation")
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		glob, path string
		want       bool
	}{{"**/*.go", "main.go", true}, {"**/*.go", "a/b/main.go", true}, {"*.go", "a/main.go", true}, {"a/**/test?.go", "a/test1.go", true}, {"a/**/test?.go", "a/b/test1.go", true}, {"a/*.go", "a/b/main.go", false}} {
		got, err := globMatch(tc.glob, tc.path)
		if err != nil || got != tc.want {
			t.Errorf("%s %s: %v %v", tc.glob, tc.path, got, err)
		}
	}
}

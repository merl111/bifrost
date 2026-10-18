package main

import (
	"io/fs"
	"reflect"
	"testing"

	"bifrost/internal/workspace"
)

func TestFileKinds(t *testing.T) {
	for name, want := range map[string]string{
		"main.go": "Go", "go.mod": "Go", "go.sum": "Go", ".env": "Environment", ".env.example": "Environment",
		"Dockerfile": "Docker", "Dockerfile.gateway": "Docker", "docker-compose.yml": "Docker", ".gitignore": "Git",
		"AGENTS.md": "Documentation", "README.MD": "Documentation", "LICENSE": "License", "Makefile": "Build script",
		"railpack.gateway.json": "JSON", "config.yaml": "Configuration", "logo.svg": "Image", "source.zip": "Archive",
		"app.tsx": "TypeScript", "main.py": "Python", "Cargo.lock": "Lockfile", "unknown.blob": "File",
	} {
		if got := fileKind(workspace.Entry{Name: name}); got != want {
			t.Errorf("%s: got %s, want %s", name, got, want)
		}
	}
	if got := fileKind(workspace.Entry{Name: "code.go", IsDir: true}); got != "Folder" {
		t.Fatal(got)
	}
	if got := fileKind(workspace.Entry{Name: "link.go", Mode: fs.ModeSymlink}); got != "Symbolic link" {
		t.Fatal(got)
	}
}

func TestExplorerSortPreservesPathsAndInput(t *testing.T) {
	in := []workspace.Entry{{Name: "A.go", Path: "src/A.go"}, {Name: "zoo", Path: "src/zoo", IsDir: true}, {Name: ".env", Path: "src/.env"}, {Name: "Cmd", Path: "src/Cmd", IsDir: true}}
	before := append([]workspace.Entry(nil), in...)
	out := sortedFiles(in)
	paths := []string{}
	for _, e := range out {
		paths = append(paths, e.Path)
	}
	if !reflect.DeepEqual(paths, []string{"src/Cmd", "src/zoo", "src/.env", "src/A.go"}) || !reflect.DeepEqual(in, before) {
		t.Fatalf("sort lost identity or mutated input: %v", out)
	}
}

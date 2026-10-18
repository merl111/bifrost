package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bifrost/internal/cli"
	cui "cui.local/cui"
)

func TestNativeFileExplorer(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := filepath.Join(t.TempDir(), "bifrost")
	for _, dir := range []string{"cmd", "docs", "internal", "scripts", ".github"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{".env.example", ".gitignore", "AGENTS.md", "Dockerfile", "Dockerfile.gateway", "LICENSE", "Makefile", "README.md", "docker-compose.yml", "go.mod", "go.sum", "railpack.json", "main.go", "app.tsx", "logo.svg", "internal/auth.go"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("// Explorer fixture\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := cli.OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", t.TempDir(), "--base-url", "http://127.0.0.1:1", "--model", "fixture", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	app, err := cui.New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	g := newDesktop(ctx, app, ws, 1200, 1180)
	g.window.Show()
	g.showFiles()
	g.loadDirectory(".")
	phase := 0
	deadline := time.Now().Add(15 * time.Second)
	var failed error
	capture := func(name string) {
		if dir := os.Getenv("BIFROST_CAPTURE_DIR"); dir != "" {
			w, h, _ := g.window.Size()
			if out, err := exec.Command("import", "-window", "root", "-crop", fmt.Sprintf("%dx%d+0+0", w, h), filepath.Join(dir, name+".png")).CombinedOutput(); err != nil {
				failed = fmt.Errorf("capture: %v %s", err, out)
			}
		}
	}
	app.Every(250, func() {
		if time.Now().After(deadline) {
			failed = fmt.Errorf("explorer timed out phase %d", phase)
		}
		if failed != nil {
			cancel()
			app.Quit()
			return
		}
		switch phase {
		case 0:
			if len(g.visibleFiles) != 20 {
				return
			}
			if !g.visibleFiles[0].IsDir {
				failed = fmt.Errorf("folders not sorted first")
				return
			}
			for i, row := range g.explorer.rows {
				icon := row.GetIcon()
				if icon == nil {
					failed = fmt.Errorf("missing icon %d", i)
					return
				}
				icon.Close()
			}
			phase++
		case 1:
			capture("desktop-file-explorer")
			g.sideSearch.SetText("internal")
			g.filterNavigation()
			g.explorer.rows[0].Activate()
			phase++
		case 2:
			if g.cwd != "internal" {
				return
			}
			if g.sideSearch.Text() != "" || len(g.visibleFiles) != 1 || !strings.Contains(g.directory.Text(), "internal") {
				failed = fmt.Errorf("navigation retained filter or wrong path")
				return
			}
			g.explorer.rows[0].Activate()
			phase++
		case 3:
			if g.previewLoading {
				return
			}
			if g.previewPath != "internal/auth.go" {
				failed = fmt.Errorf("opened wrong file %q", g.previewPath)
				return
			}
			g.explorer.parent.Activate()
			phase++
		case 4:
			if g.cwd != "." {
				return
			}
			g.sideSearch.SetText("dockerfile.gateway")
			g.filterNavigation()
			if len(g.visibleFiles) != 1 || g.visibleFiles[0].Path != "Dockerfile.gateway" {
				failed = fmt.Errorf("filter lost file identity")
				return
			}
			pool := len(g.explorer.rows)
			g.filterNavigation()
			if len(g.explorer.rows) != pool {
				failed = fmt.Errorf("row pool grew on refresh")
				return
			}
			g.sideSearch.SetText("")
			g.filterNavigation()
			g.window.SetSize(800, 720)
			phase++
		case 5:
			g.showFiles()
			phase++
		case 6:
			capture("desktop-file-explorer-narrow")
			appearance := ws.Appearance
			appearance.Theme = "light"
			g.applyAppearance(appearance)
			phase++
		case 7:
			capture("desktop-file-explorer-light")
			cancel()
			app.Quit()
		}
	})
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	cancel()
	g.workers.Wait()
	if failed != nil {
		t.Fatal(failed)
	}
}

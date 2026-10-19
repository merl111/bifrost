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

	"bifrost/internal/cli"
	"bifrost/internal/domain"
	cui "cui.local/cui"
)

func TestNativeProjectPicker(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	app, err := cui.New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Theme(cui.Dark)
	root, state := t.TempDir(), t.TempDir()
	args := []string{"--state-dir", state, "--model", "fixture", "--base-url", "http://127.0.0.1:1", "--agents", "0"}
	var selected []string
	p := newProjectPicker(app, args, func(args []string) { selected = args })
	p.apply(cli.DesktopLaunchCatalog{Directory: root, Environment: "local", DefaultProject: "Bifrost", Projects: []domain.Project{
		{ID: "project_bifrost", Name: "Bifrost", Description: "One workspace. Every way you build.", Repositories: []domain.Repository{{Alias: "core", Roots: map[string]string{"local": root}}, {Alias: "native-ui", Roots: map[string]string{"local": "/workspace/cui"}}}},
		{ID: "project_capsl", Name: "Capsl", Description: "Isolated development environments", Repositories: []domain.Repository{{Alias: "runner", Roots: map[string]string{"local": "/workspace/capsl"}}}},
		{ID: "project_docs", Name: "Research", Description: "Architecture notes and experiments"},
	}}, nil)
	p.window.Show()
	var failed error
	phase := 0
	resizeAttempts := 0
	app.Every(300, func() {
		switch phase {
		case 0:
			if dir := os.Getenv("BIFROST_CAPTURE_DIR"); dir != "" {
				out, err := exec.Command("import", "-window", "root", "-crop", "900x640+0+0", filepath.Join(dir, "desktop-project-picker.png")).CombinedOutput()
				if err != nil {
					failed = fmt.Errorf("capture: %v %s", err, out)
				}
			}
			p.repositories.SetSelected(1)
			p.selectRepository()
			if p.path.Text() != "/workspace/cui" || !p.open.Activate() || !strings.Contains(strings.Join(selected, " "), "--repository native-ui") {
				failed = fmt.Errorf("repository selection failed: %q %q", p.path.Text(), selected)
			}
			p.setBusy(false)
			p.search.SetText("Research")
			p.filter()
			if len(p.visible) != 1 || p.selected.Name != "Research" || !strings.Contains(p.path.Text(), "no linked repositories") {
				failed = fmt.Errorf("filter or empty project failed")
			}
			p.search.SetText("no matches")
			p.filter()
			if len(p.visible) != 0 || p.selected != nil {
				failed = fmt.Errorf("empty search retained stale project")
			}
			p.openFolder(root)
			ws, err := cli.OpenDesktopWorkspace(context.Background(), selected, io.Discard)
			if err != nil {
				failed = err
			} else {
				if ws.Root != root {
					failed = fmt.Errorf("wrong root: %s", ws.Root)
				}
				ws.Close()
			}
			p.setBusy(false)
			p.search.SetText("")
			p.filter()
			p.window.SetSize(640, 560)
		case 1:
			width, height, _ := p.window.Size()
			if width > 640 || height > 560 {
				// Captures block the event loop; allow the X11 configure event to arrive.
				resizeAttempts++
				if resizeAttempts < 10 {
					return
				}
				failed = fmt.Errorf("chooser overflows narrow window: %dx%d", width, height)
			}
			if dir := os.Getenv("BIFROST_CAPTURE_DIR"); dir != "" {
				out, err := exec.Command("import", "-window", "root", "-crop", fmt.Sprintf("%dx%d+0+0", width, height), filepath.Join(dir, "desktop-project-picker-narrow.png")).CombinedOutput()
				if err != nil {
					failed = fmt.Errorf("capture: %v %s", err, out)
				}
			}
			app.Quit()
		}
		phase++
		if failed != nil {
			app.Quit()
		}
	})
	if err := app.Run(); err != nil {
		t.Fatal(err)
	}
	if failed != nil {
		t.Fatal(failed)
	}
	if phase != 2 {
		t.Fatalf("incomplete native chooser test: %d", phase)
	}
}

package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bifrost/internal/cli"
	"bifrost/internal/config"
	cui "cui.local/cui"
)

func TestNativeIndependentProviderAndModel(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var requests atomic.Int32
	var block atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if block.Load() {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"model-one"},{"id":"model-two"}]}`)
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, state := t.TempDir(), t.TempDir()
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "alpha"
	cfg.Providers = map[string]config.ProviderSettings{"alpha": {Kind: "chat-completions", BaseURL: server.URL, Model: "model-one"}, "beta": {Kind: "chat-completions", BaseURL: server.URL, Model: "beta-default"}}
	if err := config.SaveSettings(filepath.Join(state, "config.json"), cfg, false); err != nil {
		t.Fatal(err)
	}
	ws, err := cli.OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", state, "--profile", "alpha", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	app, err := cui.New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	g := newDesktop(ctx, app, ws, 1200, 900)
	snapshot, err := ws.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.applySnapshot(snapshot)
	g.window.Show()
	g.setPrompt("Keep this unfinished prompt")
	phase := 0
	deadline := time.Now().Add(15 * time.Second)
	var failed error
	capture := func(name string) {
		if dir := os.Getenv("BIFROST_CAPTURE_DIR"); dir != "" {
			w, h, _ := g.window.Size()
			if output, err := exec.Command("import", "-window", "root", "-crop", fmt.Sprintf("%dx%d+0+0", w, h), filepath.Join(dir, name+".png")).CombinedOutput(); err != nil {
				failed = fmt.Errorf("capture: %v %s", err, output)
			}
		}
	}
	choose := func(query string) {
		g.palette.PickerSetQuery(query)
		if results, ok := g.palette.PickerPart(cui.PickerResults); ok {
			results.SetSelected(0)
		}
		if !g.palette.PickerAccept() {
			failed = fmt.Errorf("could not choose %s", query)
		}
	}
	app.Every(250, func() {
		if time.Now().After(deadline) {
			failed = fmt.Errorf("switch test timed out phase %d", phase)
		}
		if failed != nil {
			cancel()
			app.Quit()
			return
		}
		switch phase {
		case 0:
			if !strings.Contains(g.profiles.Text(), "alpha") || strings.Contains(g.profiles.Text(), "model-one") {
				failed = fmt.Errorf("provider still combines model")
				return
			}
			g.modelName.Activate()
			phase++
		case 1:
			if g.quick.models["alpha"] == nil {
				return
			}
			g.palette.PickerSetQuery("model-two")
			if g.palette.PickerMatchCount() != 1 {
				failed = fmt.Errorf("model search failed")
				return
			}
			capture("desktop-model-picker")
			choose("model-two")
			phase++
		case 2:
			if g.busy {
				return
			}
			if !strings.Contains(g.modelName.Text(), "model-two") || !strings.Contains(g.profiles.Text(), "alpha") || g.composer.Text() != "Keep this unfinished prompt" {
				failed = fmt.Errorf("model change lost provider or draft")
				return
			}
			g.profiles.Activate()
			phase++
		case 3:
			capture("desktop-provider-picker")
			choose("beta")
			phase++
		case 4:
			if g.busy {
				return
			}
			if !strings.Contains(g.profiles.Text(), "beta") || !strings.Contains(g.modelName.Text(), "beta-default") {
				failed = fmt.Errorf("provider did not restore its model")
				return
			}
			g.profiles.Activate()
			choose("alpha")
			phase++
		case 5:
			if g.busy {
				return
			}
			if !strings.Contains(g.modelName.Text(), "model-two") {
				failed = fmt.Errorf("provider forgot selected model")
				return
			}
			g.modelName.Activate()
			if requests.Load() != 1 {
				failed = fmt.Errorf("cached model picker refetched")
				return
			}
			g.closePalette()
			block.Store(true)
			delete(g.quick.models, "alpha")
			g.modelName.Activate()
			phase++
		case 6:
			if requests.Load() < 2 {
				return
			}
			g.closePalette()
			g.profiles.Activate()
			phase++
		case 7:
			if g.quick.kind != "provider" || g.palette.PickerMatchCount() != 3 {
				failed = fmt.Errorf("stale discovery replaced provider picker")
				return
			}
			g.closePalette()
			g.activeChat().cancel = func() {}
			g.refreshBusy()
			g.openQuickSelection("model")
			if g.paletteOpen {
				failed = fmt.Errorf("switch allowed during a running turn")
				return
			}
			g.activeChat().cancel = nil
			g.refreshBusy()
			g.window.SetSize(800, 720)
			phase++
		case 8:
			capture("desktop-provider-model-controls")
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

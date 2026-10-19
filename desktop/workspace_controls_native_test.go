package main

import (
	"bifrost/internal/cli"
	"bifrost/internal/config"
	"context"
	cui "cui.local/cui"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestNativeWorkspaceControls(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, state := t.TempDir(), t.TempDir()
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "fixture"
	cfg.Providers["fixture"] = config.ProviderSettings{Kind: "chat-completions", BaseURL: "http://localhost:1", Model: "gpt-4.1"}
	if err := config.SaveSettings(filepath.Join(state, "config.json"), cfg, false); err != nil {
		t.Fatal(err)
	}
	ws, err := cli.OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", state, "--agents", "0"}, io.Discard)
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
	snapshot.Connection.ContextTokens = 40000
	snapshot.Connection.ContextMaxTokens = 200000
	g.applySnapshot(snapshot)
	g.window.Show()
	phase := 0
	deadline := time.Now().Add(15 * time.Second)
	var failed error
	capture := func(name string) {
		if dir := os.Getenv("BIFROST_CAPTURE_DIR"); dir != "" {
			w, h, _ := g.window.Size()
			output, err := exec.Command("import", "-window", "root", "-crop", fmt.Sprintf("%dx%d+0+0", w, h), filepath.Join(dir, name+".png")).CombinedOutput()
			if err != nil {
				failed = fmt.Errorf("capture %v %s", err, output)
			}
		}
	}
	app.Every(300, func() {
		if time.Now().After(deadline) {
			failed = fmt.Errorf("timeout phase %d", phase)
		}
		if failed != nil {
			cancel()
			app.Quit()
			return
		}
		switch phase {
		case 0:
			if g.contextMeter.Text() != "Context ~20%" {
				failed = fmt.Errorf("context meter %s", g.contextMeter.Text())
				return
			}
			g.addChat("Review auth flow")
			g.addChat("Write tests")
			g.chats.closes[1].Activate()
			if len(g.chats.items) != 2 {
				failed = fmt.Errorf("tab close failed")
				return
			}
			g.showHistory()
			phase++
		case 1:
			if len(g.visibleSessions) == 0 {
				return
			}
			capture("desktop-context-tabs-history")
			g.openWorkbench("Installed skills")
			phase++
		case 2:
			if g.workbench.skills.busy {
				return
			}
			if len(g.workbench.skills.visible) < 4 {
				failed = fmt.Errorf("missing bundled skills")
				return
			}
			g.workbench.skills.scope.SetSelected(1)
			g.showInstalledSkill()
			g.workbench.skills.toggle.Activate()
			phase++
		case 3:
			if g.workbench.skills.busy {
				return
			}
			if len(g.workbench.skills.inventory.GlobalDefaults) != 1 {
				failed = fmt.Errorf("auto-load not saved")
				return
			}
			phase++
		case 4:
			capture("desktop-installed-skills")
			g.closeWorkbench()
			g.execution.mode.Activate()
			g.palette.PickerSetQuery("Ask every time")
			results, _ := g.palette.PickerPart(cui.PickerResults)
			results.SetSelected(0)
			g.palette.PickerAccept()
			phase++
		case 5:
			if g.busy {
				return
			}
			saved, err := config.LoadSettings(filepath.Join(state, "config.json"))
			if err != nil || saved.Execution.Mode != "ask" || !saved.Defaults.AllowExec {
				failed = fmt.Errorf("execution mode not saved: %v", err)
				return
			}
			g.window.SetSize(800, 720)
			phase++
		case 6:
			if g.execution.mode.Text() != "Commands: ask" {
				failed = fmt.Errorf("execution selection reset")
				return
			}
			capture("desktop-workspace-controls-narrow")
			g.window.SetSize(1200, 900)
			g.openWorkbench("Sub-agents")
			phase++
		case 7:
			if g.workbench.subagents.busy {
				return
			}
			g.workbench.subagents.provider.SetSelected(1)
			g.workbench.subagents.model.SetText("small-worker")
			g.workbench.subagents.save.Activate()
			phase++
		case 8:
			if g.workbench.subagents.busy {
				return
			}
			saved, err := config.LoadSettings(filepath.Join(state, "config.json"))
			if err != nil || saved.Subagents.Provider != "fixture" || saved.Subagents.Model != "small-worker" {
				failed = fmt.Errorf("subagent settings not saved: %v", err)
				return
			}
			phase++
		case 9:
			capture("desktop-subagent-defaults")
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

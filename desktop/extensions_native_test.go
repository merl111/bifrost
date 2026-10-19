package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bifrost/internal/cli"
	"bifrost/internal/config"
	cui "cui.local/cui"
)

func TestNativeCustomExtensions(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root, state := t.TempDir(), t.TempDir()
	folder := filepath.Join(root, "custom skill")
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "SKILL.md"), []byte("---\nname: native-custom\ndescription: Installed through the native composer\n---\nFixture instructions."), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "mcp servers.json")
	if err := os.WriteFile(path, []byte(`{"servers":[{"name":"fixture","command":["/not/launched/during/configuration"]}]}`), 0600); err != nil {
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
	g := newDesktop(ctx, app, ws, 1100, 800)
	snapshot, err := ws.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.applySnapshot(snapshot)
	g.window.Show()
	phase := 0
	var failed error
	deadline := time.Now().Add(15 * time.Second)
	app.Every(150, func() {
		if time.Now().After(deadline) {
			failed = fmt.Errorf("extension native timeout phase %d", phase)
		}
		if failed != nil {
			cancel()
			app.Quit()
			return
		}
		switch phase {
		case 0:
			g.connected = false
			g.replacePrompt(`/skill install "./custom skill" --scope global`)
			g.updateComposer()
			g.send.Activate()
			phase++
		case 1:
			if g.workbench.market.installing {
				return
			}
			inventory, err := ws.InstalledSkills(ctx)
			if err != nil {
				failed = err
				return
			}
			found := false
			for _, item := range inventory.Items {
				if item.Skill.Name == "native-custom" && item.Scope == "global" {
					found = true
				}
			}
			if !found {
				failed = fmt.Errorf("composer did not install custom skill: %s", g.status.Text())
				return
			}
			if g.composer.Text() != "" {
				failed = fmt.Errorf("successful command draft retained")
				return
			}
			g.replacePrompt(`/mcp config "./mcp servers.json"`)
			g.updateComposer()
			g.send.Activate()
			phase++
		case 2:
			if g.workbench.market.installing {
				return
			}
			cfg, err := config.LoadSettings(filepath.Join(state, "config.json"))
			if err != nil {
				failed = err
				return
			}
			found := false
			for _, project := range cfg.Projects {
				if project.MCPConfig == path {
					found = true
				}
			}
			if !found {
				failed = fmt.Errorf("MCP project config not saved")
				return
			}
			g.replacePrompt(`/skill install "./unclosed`)
			g.updateComposer()
			g.send.Activate()
			phase++
		case 3:
			if g.composer.Text() != `/skill install "./unclosed` || !strings.Contains(g.status.Text(), "unclosed quote") {
				failed = fmt.Errorf("invalid command did not retain draft and error: %s", g.status.Text())
				return
			}
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

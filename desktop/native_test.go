package main

import (
	"context"
	"encoding/json"
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
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

// Opt-in integration: actual GTK/CUI controls, loopback inference fixture,
// independent workspace/state and a disposable PTY. No real accounts or paid models.
func TestNativeWorkspace(t *testing.T) {
	if os.Getenv("BIFROST_NATIVE_TEST") == "" {
		t.Skip("set BIFROST_NATIVE_TEST=1 under Xvfb")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var requests atomic.Int32
	stopFixture := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if requests.Add(1) > 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"I’m checking the code for this conversation. You can switch tabs while I work.\"}}]}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-stopFixture:
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		reply, _ := json.Marshal("## One runtime, two ways to work\n\nThe desktop is connected to the same Bifrost runtime.\n\n- **Shared sessions.** Resume this conversation in the terminal.\n- **Code in context.** Open source alongside the conversation.\n\n```go\nworkspace.Run(ctx, prompt, emit)\n```")
		fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n", reply)
	}))
	defer server.Close()
	defer close(stopFixture)
	root, state := filepath.Join(t.TempDir(), "bifrost"), t.TempDir()
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "workspace.go"), []byte("package workspace\n\n// One runtime. Two ways to work.\nfunc Open() {\n    // Native desktop + terminal workspace\n}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"repo-a", "repo-b"} {
		path := filepath.Join(root, name)
		if out, err := exec.Command("git", "init", "-q", path).CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "second.go"), []byte("package second\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultSettings()
	cfg.ActiveProvider = "alpha"
	cfg.Providers = map[string]config.ProviderSettings{
		"alpha": {Kind: "chat-completions", BaseURL: server.URL, Model: "Local test fixture"},
		"beta":  {Kind: "chat-completions", BaseURL: server.URL, Model: "Local test fixture"},
	}
	if err := config.SaveSettings(filepath.Join(state, "config.json"), cfg, false); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws, err := cli.OpenDesktopWorkspace(ctx, []string{"--root", root, "--state-dir", state, "--base-url", server.URL, "--model", "Local test fixture", "--agents", "0"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	// Exercise readline without sourcing the user's shell configuration or
	// writing their history. The production adapter still uses the user's shell.
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("native PTY fixture requires bash:", err)
	}
	ws.TerminalArgv = []string{bash, "--noprofile", "--norc", "-i"}
	t.Setenv("HISTFILE", filepath.Join(state, "shell_history"))
	app, err := cui.New()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.Theme(cui.Dark)
	g := newDesktop(ctx, app, ws, 1440, 920)
	s, err := ws.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	g.applySnapshot(s)
	g.loadDirectory(".")
	g.window.Show()
	phase := 0
	deadline := time.Now().Add(75 * time.Second)
	var failed error
	capture := func(name string) {
		if directory := os.Getenv("BIFROST_CAPTURE_DIR"); directory != "" {
			width, height, _ := g.window.Size()
			if output, err := exec.Command("import", "-window", "root", "-crop", fmt.Sprintf("%dx%d+0+0", width, height), filepath.Join(directory, name+".png")).CombinedOutput(); err != nil {
				failed = fmt.Errorf("capture: %w: %s", err, output)
			}
		}
	}
	parity := &nativeParity{root: root, state: state, capture: capture, requests: &requests}
	app.Every(450, func() {
		defer func() {
			if failed != nil {
				g.cancel()
				app.Quit()
			}
		}()
		if time.Now().After(deadline) {
			failed = fmt.Errorf("native test timed out in phase %d, parity %d; terminal: %q", phase, parity.step, g.terminal.output.Text())
			app.Quit()
			return
		}
		switch phase {
		case 0:
			capture("desktop-welcome")
			g.openPalette()
			phase = 10
		case 10:
			capture("desktop-commands")
			g.palette.PickerSetQuery("workspace.go")
			if g.palette.PickerMatchCount() != 1 || !g.palette.PickerAccept() || g.paletteOpen {
				failed = fmt.Errorf("command palette did not filter, select and dismiss")
				app.Quit()
				return
			}
			g.setPrompt("How does this workspace connect to Bifrost?")
			if !g.send.Activate() {
				failed = fmt.Errorf("send button did not activate")
				app.Quit()
				return
			}
			phase = 1
		case 1:
			if g.busy {
				return
			}
			if len(g.transcript.messages) != 2 || !strings.Contains(g.transcript.messages[1].Text, "same Bifrost runtime") {
				failed = fmt.Errorf("missing response: %+v", g.transcript.messages)
				app.Quit()
				return
			}
			g.loadSource("stale request", func(ctx context.Context) (string, error) {
				select {
				case <-time.After(100 * time.Millisecond):
					return "stale result", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			})
			g.loadFile("workspace.go")
			phase++
		case 2:
			if !strings.Contains(g.preview.Text(), "package workspace") {
				return
			}
			capture("desktop-conversation")
			g.showFiles()
			g.sideSearch.SetText("WORKSPACE")
			g.filterNavigation()
			if len(g.visibleFiles) != 1 || g.visibleFiles[0].Path != "workspace.go" {
				failed = fmt.Errorf("file filter lost identity: %+v", g.visibleFiles)
			}
			g.setPrompt("Keep my draft.")
			g.mention.Activate()
			if !strings.Contains(g.composer.Text(), "Keep my draft.") || !strings.Contains(g.composer.Text(), "workspace.go") || requests.Load() != 1 {
				failed = fmt.Errorf("mention must preserve draft without sending: %s", g.composer.Text())
			}
			g.setPrompt("")
			g.receive(ui.ChatEvent{Kind: "status", Text: "Fixture activity", Detail: "Read workspace.go"})
			g.activityToggle.Activate()
			activity := g.transcript.native()
			if len(activity[len(activity)-1].Spans) < 2 || !strings.Contains(activity[len(activity)-1].Spans[1].Text, "Read workspace.go") {
				failed = fmt.Errorf("activity details did not expand")
			}
			g.activityToggle.Activate()
			g.toggleInspector()
			g.toggleSidebar()
			g.window.SetSize(800, 720)
			phase++
		case 3:
			capture("desktop-narrow")
			app.TextScale(1.5)
			g.textScale = 1.5
			g.window.SetSize(1200, 900)
			phase++
		case 4:
			capture("desktop-large-text")
			g.setPrompt("Cancel this request")
			g.send.Activate()
			phase++
		case 5:
			if requests.Load() < 2 {
				return
			}
			g.stop.Activate()
			phase++
		case 6:
			if g.busy {
				return
			}
			if !strings.Contains(g.status.Text(), "Stopped") {
				failed = fmt.Errorf("cancel status: %s", g.status.Text())
			}
			g.toggleSidebar()
			g.changeConversation(g.ws.NewSession)
			phase++
		case 7:
			if g.busy {
				return
			}
			if len(g.transcript.messages) != 0 {
				failed = fmt.Errorf("new session kept old messages: %s", g.status.Text())
			}
			phase++
		case 8:
			capture("desktop-welcome-large-text")
			app.TextScale(1)
			g.textScale = 1
			g.window.SetSize(640, 600)
			phase++
		case 9:
			// GTK font/layout changes settle asynchronously across frame clocks.
			// Keep requesting the target until the compact controls have reflowed;
			// the overall deadline still makes a permanent minimum-size failure fatal.
			g.window.SetSize(640, 600)
			if width, height, _ := g.window.Size(); width > 640 || height > 600 {
				return
			}
			phase = 11
		case 11:
			capture("desktop-welcome-narrow")
			if width, height, _ := g.window.Size(); width > 640 || height > 600 {
				failed = fmt.Errorf("compact layout exceeded requested size: %dx%d", width, height)
			}
			g.window.SetSize(640, 480)
			phase++
		case 12:
			if width, height, _ := g.window.Size(); width > 640 || height > 480 {
				failed = fmt.Errorf("short layout exceeded requested size: %dx%d", width, height)
			}
			g.openPalette()
			phase++
		case 13:
			capture("desktop-commands-narrow")
			g.closePalette()
			if !g.composer.HasFocus() {
				failed = fmt.Errorf("command palette did not restore composer focus")
			}
			phase++
		case 14:
			done, err := parity.tick(g)
			if err != nil {
				failed = err
			}
			if done || err != nil {
				g.cancel()
				app.Quit()
			}
		}
	})
	if err := app.Run(); err != nil {
		t.Error(err)
	}
	g.cancel()
	g.workers.Wait()
	if failed != nil {
		t.Fatal(failed)
	}
	if app.Error() != "" {
		t.Fatal(app.Error())
	}
}

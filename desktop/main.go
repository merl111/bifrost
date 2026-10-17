// Bifrost Desktop is an optional CUI frontend. The parent Go module and TUI
// deliberately do not depend on this module or its native libraries.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"

	"bifrost/internal/cli"
	cui "cui.local/cui"
)

func main() {
	runtime.LockOSThread()
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "bifrost-desktop:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Println(`Bifrost Desktop — native CUI workspace

Usage: bifrost-desktop [--root PATH] [--project NAME --repository ALIAS]
  [--config PATH] [--state-dir PATH] [--profile NAME] [--session ID]
  [--allow-write] [--allow-exec] [--model MODEL]

Uses the same provider, permission and session settings as the TUI.
Enter sends · Shift/Alt+Enter inserts a newline · Escape dismisses or stops
Ctrl+K search & commands · Ctrl+P browse files · Ctrl+L focus composer
Ctrl+B sidebar · Ctrl+J source pane · Ctrl+R Git review
Ctrl+N new conversation · Ctrl+T terminal · Ctrl+S save
Drag pane dividers to resize · layouts and editor recovery drafts persist

Workspace: tasks, settings, accounts, marketplace and multi-repository projects.
All TUI command families are available. See docs/desktop.md for coverage and platform limits.`)
			return nil
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ws, err := cli.OpenDesktopWorkspace(ctx, args, os.Stderr)
	if err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}
	defer ws.Close()
	snapshot, err := ws.Snapshot(ctx)
	if err != nil {
		return err
	}
	app, err := cui.New()
	if err != nil {
		return err
	}
	defer app.Close()
	app.Theme(cui.Dark)
	width, height := initialSize()
	g := newDesktop(ctx, app, ws, width, height)
	if scale, e := strconv.ParseFloat(os.Getenv("BIFROST_DESKTOP_TEXT_SCALE"), 64); e == nil {
		if app.TextScale(scale) {
			g.textScale = scale
		}
	}
	g.applySnapshot(snapshot)
	g.loadDirectory(".")
	g.window.Show()
	g.composer.Focus()
	err = app.Run()
	g.cancel()
	g.workers.Wait()
	return err
}

func initialSize() (int, int) {
	width, height := 1440, 920
	if geometry := strings.Split(os.Getenv("BIFROST_DESKTOP_SIZE"), "x"); len(geometry) == 2 {
		if w, e := strconv.Atoi(geometry[0]); e == nil && w >= 640 {
			width = w
		}
		if h, e := strconv.Atoi(geometry[1]); e == nil && h >= 480 {
			height = h
		}
	}
	return width, height
}

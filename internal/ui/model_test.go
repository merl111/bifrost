package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	vt "github.com/charmbracelet/x/vt"
)

func TestFileTreeReadsWorkspaceAndRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "café.txt"), []byte("hello 世界\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "inside.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := newFileTree(root)
	if err != nil {
		t.Fatal(err)
	}
	entries := tree.list()
	if len(entries) != 2 || entries[0].Path != "nested" {
		t.Fatalf("entries = %#v", entries)
	}
	content, err := tree.read("café.txt")
	if err != nil || content != "hello 世界\n" {
		t.Fatalf("read = %q, %v", content, err)
	}
	if _, err := tree.read("../outside"); !errors.Is(err, errOutsideRoot) {
		t.Fatalf("escape read error = %v", err)
	}
	if nested := tree.list("nested"); len(nested) != 2 || nested[0].Path != ".." || nested[1].Path != "inside.txt" {
		t.Fatalf("nested entries = %#v", nested)
	}
	if runtime.GOOS != "windows" {
		outside := filepath.Join(t.TempDir(), "outside.txt")
		if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(root, "link.txt")
		if err := os.Symlink(outside, link); err != nil {
			t.Fatal(err)
		}
		if _, err := tree.read("link.txt"); err == nil {
			t.Fatalf("symlink escape error = %v", err)
		}
	}
}

func TestTerminalOpensAsViewerTab(t *testing.T) {
	m := New(t.TempDir(), WithLayout("studio"))
	m.width, m.height = 120, 36
	if m.terminalPaneHeight() != 0 {
		t.Fatal("terminal still reserves a bottom row")
	}
	before := ansi.Strip(m.render())
	if strings.Contains(before, "VIEWER  ·  TERMINAL") {
		t.Fatal("terminal is visible before it is opened")
	}
	m.handleNormalKey("t", "")
	after := ansi.Strip(m.render())
	if m.mode != ModeNormal || !strings.Contains(after, "VIEWER  ·  TERMINAL") || !strings.Contains(after, "shell") {
		t.Fatalf("viewer terminal missing mode=%v:\n%s", m.mode, after)
	}
}

func TestNormalAndTerminalKeyRouting(t *testing.T) {
	m := &Model{mode: ModeNormal, focus: focusViewer, files: []FileEntry{{Path: "a"}}}
	m.handleNormalKey("ctrl+w", "")
	if !m.prefix {
		t.Fatal("ctrl-w did not enter pane prefix")
	}
	m.handleNormalKey("h", "")
	if m.prefix || m.focus != focusFiles {
		t.Fatalf("ctrl-w h: prefix=%v focus=%v", m.prefix, m.focus)
	}
	m.handleNormalKey("t", "")
	if m.mode != ModeNormal || m.focus != focusViewer || !m.shellOpen || !m.viewerTabIsShell() {
		t.Fatalf("t: mode=%v focus=%v shell=%v tab=%d", m.mode, m.focus, m.shellOpen, m.viewerTab)
	}
	m.handleNormalKey("i", "")
	if m.mode != ModeTerminal {
		t.Fatal("i did not type into the shell")
	}
	_, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape, Text: "\x1b"})
	if m.mode != ModeNormal || m.focus != focusChat || !m.chatEditing {
		t.Fatalf("esc mode=%v focus=%v editing=%v", m.mode, m.focus, m.chatEditing)
	}
	m.focus = focusViewer
	m.chatEditing = false
	m.handleNormalKey("enter", "")
	if m.mode != ModeTerminal {
		t.Fatal("terminal Enter did not type into the shell")
	}
	terminal := &Model{mode: ModeTerminal}
	_, _ = terminal.Update(tea.KeyPressMsg{Code: ']', Mod: tea.ModCtrl, Text: "\x1d"})
	if terminal.mode != ModeNormal || terminal.focus != focusChat {
		t.Fatal("Ctrl-] did not return to the message box")
	}
}

func TestClickTypesIntoTheShellAndClickAwayLeaves(t *testing.T) {
	m := New(t.TempDir(), WithLayout("studio"))
	m.width, m.height = 120, 36
	m.shellOpen = true
	m.viewerTab = 1
	m.mode = ModeNormal
	m.focus = focusChat
	widths := m.paneWidths(m.width)
	_, _ = m.Update(tea.MouseClickMsg{X: widths[0] + 3, Y: 4, Button: tea.MouseLeft})
	if m.mode != ModeTerminal || m.focus != focusViewer {
		t.Fatalf("shell click mode=%v focus=%v", m.mode, m.focus)
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("mouse is disabled while the shell has input")
	}
	_, _ = m.Update(tea.MouseClickMsg{X: 2, Y: 4, Button: tea.MouseLeft})
	if m.mode != ModeNormal || m.focus != focusFiles {
		t.Fatalf("files click mode=%v focus=%v", m.mode, m.focus)
	}
}

func TestEmulatorResizeAndUnicode(t *testing.T) {
	emu := vt.NewSafeEmulator(12, 3)
	if _, err := emu.Write([]byte("one\r\n世界\x1b[2Jtwo")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(emu.String(), "two") {
		t.Fatalf("emulator content = %q", emu.String())
	}
	emu.Resize(4, 2)
	if emu.Width() != 4 || emu.Height() != 2 {
		t.Fatalf("resize = %dx%d", emu.Width(), emu.Height())
	}
}

func TestRenderIsSafeAtNarrowAndUnicodeWidths(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "界.txt"), []byte("välkommen 世界\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(root)
	for _, width := range []int{1, 2, 7, 32, 80, 120} {
		m.width, m.height = width, 8
		view := m.View().Content
		if view == "" {
			t.Fatalf("empty view at width %d", width)
		}
	}
}

func TestRenderGeometryAndSanitization(t *testing.T) {
	m := New(t.TempDir())
	for _, size := range [][2]int{{80, 24}, {32, 12}, {7, 8}, {1, 3}} {
		m.width, m.height = size[0], size[1]
		lines := strings.Split(m.render(), "\n")
		if len(lines) != size[1] {
			t.Fatalf("size %dx%d produced %d rows", size[0], size[1], len(lines))
		}
		for i, line := range lines {
			if got := ansi.StringWidth(line); got != size[0] {
				t.Fatalf("row %d width %d != %d: %q", i, got, size[0], line)
			}
		}
	}
	if got := sanitizeDisplay("\x1b[31m界\x1b[0m\x01"); got != "界�" {
		t.Fatalf("sanitized content = %q", got)
	}
}

func TestColorizeGoPreservesImplicitSemicolonNewlines(t *testing.T) {
	for _, source := range []string{
		"package p",
		"package p\n",
		"package p\n\nfunc f() {\n\treturn 1\n}",
	} {
		got := ansi.Strip(colorizeGo(source, paletteFor("midnight")))
		if got != source {
			t.Fatalf("colorized source changed bytes: got %q want %q", got, source)
		}
	}
}

func TestFileAndViewerScrollingReachLastLines(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		name := filepath.Join(root, fmt.Sprintf("file-%02d.txt", i))
		if err := os.WriteFile(name, []byte("line"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(root)
	m.width, m.height, m.focus = 80, 8, focusFiles
	for i := 0; i < 39; i++ {
		m.handleNormalKey("down", "")
	}
	if !strings.Contains(m.renderFiles(), "file-39.txt") {
		t.Fatalf("file scroll did not reach selected last entry: %q", m.renderFiles())
	}
	m.focus, m.viewPath = focusViewer, "file"
	var lines []string
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	m.viewer = strings.Join(lines, "\n")
	for i := 0; i < 40; i++ {
		m.handleNormalKey("down", "")
	}
	if !strings.Contains(m.renderViewer(), "line-29") {
		t.Fatalf("viewer scroll did not reach last line: %q", m.renderViewer())
	}
}

func TestPTYOutputResizeAndClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creack/pty integration uses Unix shell semantics in this test")
	}
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("/bin/sh unavailable")
	}
	p, err := startPTY(t.TempDir(), 40, 8, "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.resize(50, 10); err != nil {
		t.Fatal(err)
	}
	if err := p.write([]byte("printf 'bifrost-pty\\n'\r")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output := make(chan []byte, 1)
	go func() {
		var all []byte
		for len(all) < 64*1024 {
			result := p.read(ctx)
			all = append(all, result.data...)
			if strings.Contains(string(all), "bifrost-pty") {
				output <- all
				return
			}
			if result.err != nil {
				return
			}
		}
	}()
	select {
	case result := <-output:
		if !strings.Contains(string(result), "bifrost-pty") {
			t.Fatalf("pty output = %q", result)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for PTY output")
	}
}

func TestPTYCloseCancelsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY cancellation test")
	}
	p, err := startPTY(t.TempDir(), 40, 8, "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := p.write([]byte("sleep 30\r")); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("closing PTY took %s", elapsed)
	}
}

func TestPTYLargePasteDoesNotDeadlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY paste test")
	}
	p, err := startPTY(t.TempDir(), 40, 8, "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	if err := p.write([]byte("stty -icanon min 1 time 0 -echo; cat >/dev/null\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- p.paste(strings.Repeat("x", 16<<10)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("large paste blocked")
	}
}

func TestPTYCloseConcurrentOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY output test")
	}
	p, err := startPTY(t.TempDir(), 40, 8, "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		for {
			if result := p.read(ctx); result.err != nil {
				return
			}
		}
	}()
	if err := p.write([]byte("yes\r")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close blocked while PTY produced output")
	}
	select {
	case <-readerDone:
	case <-time.After(time.Second):
		t.Fatal("PTY reader did not stop after close")
	}
}

func TestPTYVimOptIn(t *testing.T) {
	if os.Getenv("BIFROST_TERMINAL_TEST") != "1" {
		t.Skip("set BIFROST_TERMINAL_TEST=1 to run the full-screen Vim probe")
	}
	if runtime.GOOS == "windows" {
		t.Skip("Unix PTY Vim probe")
	}
	if _, err := exec.LookPath("vim"); err != nil {
		t.Skip("vim unavailable")
	}
	root := t.TempDir()
	name := "bifrost-vim.txt"
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := startPTY(root, 80, 24, "/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()
	output := make(chan struct{}, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		first := true
		for {
			result := p.read(ctx)
			if first && len(result.data) > 0 {
				first = false
				output <- struct{}{}
			}
			if result.err != nil {
				return
			}
		}
	}()
	if err := p.write([]byte("vim -Nu NONE -n -- " + name + "\r")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output:
	case <-time.After(3 * time.Second):
		t.Fatal("Vim produced no screen output")
	}
	time.Sleep(200 * time.Millisecond)
	if err := p.write([]byte("iBifrost terminal probe")); err != nil {
		t.Fatal(err)
	}
	if err := p.write([]byte{0x1b}); err != nil {
		t.Fatal(err)
	}
	if err := p.write([]byte(":wq\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		content, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(content), "Bifrost terminal probe") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Vim did not save the edited file")
}

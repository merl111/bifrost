package ui

import (
	"context"
	"errors"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"strings"
)

// NativeTerminal adapts the same PTY and VT emulator used by the TUI. Read must
// run off the native UI thread; Close interrupts it and terminates the child.
type NativeTerminal struct{ session *ptySession }

func OpenNativeTerminal(dir string, argv []string, width, height int) (*NativeTerminal, error) {
	if width < 1 || width > 500 || height < 1 || height > 200 {
		return nil, errors.New("terminal size is outside 1..500 by 1..200")
	}
	var p *ptySession
	var err error
	if len(argv) == 0 {
		p, err = startPTY(dir, width, height, "")
	} else {
		p, err = startPTYArgv(dir, width, height, argv)
	}
	if err != nil {
		return nil, err
	}
	return &NativeTerminal{session: p}, nil
}

func (t *NativeTerminal) Read(ctx context.Context) error { return t.session.read(ctx).err }
func (t *NativeTerminal) Close() error                   { return t.session.close() }
func (t *NativeTerminal) Resize(width, height int) error {
	return t.session.resize(max(1, min(500, width)), max(1, min(200, height)))
}
func (t *NativeTerminal) Key(key uv.KeyPressEvent) error { return t.session.sendKey(key) }
func (t *NativeTerminal) Paste(text string) error        { return t.session.paste(text) }
func (t *NativeTerminal) Text() string {
	return strings.TrimRight(ansi.Strip(t.session.snapshot()), "\n")
}

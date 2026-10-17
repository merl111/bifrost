package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"bifrost/internal/ui"
	cui "cui.local/cui"
	uv "github.com/charmbracelet/ultraviolet"
)

type terminalView struct {
	readonly                    bool
	toolID, command             string
	frame                       ui.TerminalFrame
	argv                        []string
	dir, title                  string
	output, input, state, start cui.Widget
	session                     *ui.NativeTerminal
	operations                  chan func(*ui.NativeTerminal) error
	starting, visible           bool
	width, height               int
	last                        string
	inputText                   string
	clearPending                bool
}

func (g *desktop) buildTerminal() {
	g.terminalPage = g.inspector.Box(cui.Vertical, 8)
	g.terminalPage.Expand(true)
	t := &terminalView{operations: make(chan func(*ui.NativeTerminal) error, 64)}
	g.terminal = t
	g.terminalTabs = []*terminalView{t}
	bar := g.terminalPage.Box(cui.Horizontal, 8)
	g.terminalSelect = bar.Select([]string{"Shell 1"})
	g.terminalSelect.SetSelected(0)
	g.terminalSelect.OnAction(func(w cui.Widget) { g.selectTerminal(w.Selected()) })
	button(bar, "+", func() { g.newTerminal(nil, "") })
	button(bar, "Close", g.closeTerminal)
	t.state = label(bar, "Shell · "+g.ws.Backend.Identity().Kind, 11, muted)
	t.state.Expand(true)
	t.start = button(bar, "Start shell", g.startTerminal)
	g.terminalInterrupt = button(bar, "Interrupt", func() { g.terminalKey('C', cui.ModControl) })
	t.output = g.terminalPage.Code("")
	t.output.ReadOnly(true)
	t.output.Font("monospace", 11, 400)
	t.output.SetTextSpacing(3, 0)
	t.output.Expand(true)
	style(t.output, rail, foreground, 8, false, 8)
	t.input = g.terminalPage.Entry("")
	t.input.Placeholder("Type here to interact with the shell…")
	t.input.Accessibility("Terminal input", "Live PTY input. Escape returns to the composer.")
	style(t.input, background, foreground, 8, true, 8)
	t.input.OnAction(func(w cui.Widget) {
		t := g.terminal
		text := w.Text()
		committed := strings.TrimPrefix(text, t.inputText)
		t.inputText = text
		if committed != "" {
			g.enqueueTerminal(func(p *ui.NativeTerminal) error {
				if len([]rune(committed)) > 1 {
					return p.Paste(committed)
				}
				return p.Key(uv.KeyPressEvent(uv.Key{Text: committed}))
			})
		}
		// GTK emits changes inside its native undo action. Clear the capture
		// field on the next UI drain, after that action has completed.
		t.clearPending = true
	})
	t.input.OnKey(g.terminalKey)
	g.terminalHint = label(g.terminalPage, "Live shell · Escape returns to chat · hiding keeps it running", 9, muted)
	g.terminalPage.SetVisible(false)
}

func (g *desktop) showTerminal() {
	if !g.inspectorVisible {
		g.toggleInspector()
	}
	g.rememberEditor()
	g.sourcePage.SetVisible(false)
	g.terminalPage.SetVisible(true)
	g.terminal.visible = true
	if g.terminal.session == nil && !g.terminal.readonly {
		g.startTerminal()
	}
	g.terminal.input.Focus()
}

func (g *desktop) startTerminal() {
	t := g.terminal
	if t.session != nil || t.starting || t.readonly {
		return
	}
	t.starting = true
	t.start.SetEnabled(false)
	dir, argv := g.ws.TerminalDir, append([]string(nil), g.ws.TerminalArgv...)
	if len(t.argv) > 0 {
		dir, argv = t.dir, append([]string(nil), t.argv...)
	}
	g.work(func(ctx context.Context) func() {
		p, err := ui.OpenNativeTerminal(dir, argv, 80, 24)
		if p != nil {
			context.AfterFunc(g.ctx, func() { _ = p.Close() })
		}
		if ctx.Err() != nil && p != nil {
			p.Close()
			return nil
		}
		return func() {
			t.starting = false
			if err != nil {
				if g.terminal == t {
					t.start.SetEnabled(true)
					t.state.SetText(shortText(err.Error(), 80))
				}
				return
			}
			t.session = p
			t.operations = make(chan func(*ui.NativeTerminal) error, 64)
			t.width, t.height, t.last = 0, 0, ""
			if g.terminal == t {
				t.state.SetText("Shell running")
			}
			g.runTerminalIO(t, p)
		}
	})
}

func (g *desktop) runTerminalIO(t *terminalView, p *ui.NativeTerminal) {
	operations := t.operations
	ctx, cancel := context.WithCancel(g.ctx)
	stopClose := context.AfterFunc(ctx, func() { _ = p.Close() })
	g.work(func(context.Context) func() {
		defer cancel()
		defer func() { stopClose(); _ = p.Close() }()
		for {
			err := p.Read(ctx)
			frame := p.Frame()
			g.post(func() {
				if t.session == p {
					t.frame = frame
					t.last = frame.Text
					if g.terminal == t {
						g.renderTerminal(t)
					}
				}
			})
			if err != nil {
				break
			}
		}
		return func() {
			if t.session == p {
				t.session = nil
				if g.terminal == t {
					t.state.SetText("Shell exited")
					t.start.SetEnabled(true)
				}
			}
		}
	})
	g.work(func(context.Context) func() {
		for {
			select {
			case <-ctx.Done():
				return nil
			case op := <-operations:
				if err := op(p); err != nil && err != io.EOF && ctx.Err() == nil {
					g.post(func() { g.status.SetText(shortText(err.Error(), 100)) })
				}
			}
		}
	})
}

func (g *desktop) enqueueTerminal(op func(*ui.NativeTerminal) error) {
	if g.terminal.session == nil {
		return
	}
	select {
	case g.terminal.operations <- op:
	default:
		g.status.SetText("Terminal input queue is full")
	}
}

func (g *desktop) terminalKey(key int, mods uint) bool {
	// Native paste commits text through OnAction, preserving IME/clipboard text.
	if key == 'V' && mods&cui.ModControl != 0 {
		return false
	}
	if key == cui.KeyEscape {
		g.composer.Focus()
		return true
	}
	code := rune(key)
	switch key {
	case cui.KeyEnter:
		code = uv.KeyEnter
	case cui.KeyBackspace:
		code = uv.KeyBackspace
	case cui.KeyTab:
		code = uv.KeyTab
	case cui.KeyUp:
		code = uv.KeyUp
	case cui.KeyDown:
		code = uv.KeyDown
	case cui.KeyLeft:
		code = uv.KeyLeft
	case cui.KeyRight:
		code = uv.KeyRight
	case cui.KeyHome:
		code = uv.KeyHome
	case cui.KeyEnd:
		code = uv.KeyEnd
	case cui.KeyDelete:
		code = uv.KeyDelete
	case cui.KeyInsert:
		code = uv.KeyInsert
	case cui.KeyPageUp:
		code = uv.KeyPgUp
	case cui.KeyPageDown:
		code = uv.KeyPgDown
	default:
		if mods&cui.ModControl == 0 || key < 'A' || key > 'Z' {
			return false
		}
		code = rune(key + 'a' - 'A')
	}
	var modifier uv.KeyMod
	if mods&cui.ModControl != 0 {
		modifier |= uv.ModCtrl
	}
	if mods&cui.ModAlt != 0 {
		modifier |= uv.ModAlt
	}
	if mods&cui.ModShift != 0 {
		modifier |= uv.ModShift
	}
	g.enqueueTerminal(func(p *ui.NativeTerminal) error { return p.Key(uv.KeyPressEvent(uv.Key{Code: code, Mod: modifier})) })
	return true
}

func (g *desktop) refreshTerminal() {
	t := g.terminal
	if t.clearPending {
		t.input.SetText("")
		t.inputText = ""
		t.clearPending = false
	}
	if t.session == nil || !t.visible || !g.inspectorVisible {
		return
	}
	w, h, ok := t.output.AllocatedSize()
	if !ok {
		return
	}
	width, height := max(10, int(float64(w-20)/(9*g.textScale))), max(3, int(float64(h-20)/(20*g.textScale)))
	if width == t.width && height == t.height {
		return
	}
	t.width, t.height = width, height
	g.enqueueTerminal(func(p *ui.NativeTerminal) error { return p.Resize(width, height) })
	t.state.SetText(fmt.Sprintf("Shell · %d × %d", width, height))
}

func (g *desktop) newTerminal(argv []string, dir string) {
	if len(g.terminalTabs) >= 16 {
		g.status.SetText("Close a terminal before opening more (16-tab limit)")
		return
	}
	old := g.terminal
	t := &terminalView{output: old.output, input: old.input, state: old.state, start: old.start, argv: argv, dir: dir}
	t.title = fmt.Sprintf("Shell %d", len(g.terminalTabs)+1)
	if len(argv) > 0 {
		t.title = "Editor"
	}
	g.terminalTabs = append(g.terminalTabs, t)
	g.selectTerminal(len(g.terminalTabs) - 1)
	g.showTerminal()
}
func (g *desktop) selectTerminal(index int) {
	if index < 0 || index >= len(g.terminalTabs) {
		return
	}
	g.terminal.visible = false
	g.terminalIndex = index
	g.terminal = g.terminalTabs[index]
	t := g.terminal
	t.visible = true
	g.renderTerminal(t)
	t.input.SetText("")
	t.inputText = ""
	t.input.SetEnabled(!t.readonly)
	g.terminalInterrupt.SetEnabled(!t.readonly)
	g.terminalHint.SetText("Live shell · Escape returns to chat · hiding keeps it running")
	if t.readonly {
		g.terminalHint.SetText("Agent command output · retained in this turn's activity")
	}
	t.start.SetEnabled(t.session == nil && !t.starting && !t.readonly)
	t.state.SetText("Shell exited · Start shell to restart")
	if t.session != nil {
		t.state.SetText("Shell running")
	} else if t.readonly {
		t.state.SetText("Agent command output · read-only")
	}
	g.refreshTerminalChoices()
}
func (g *desktop) refreshTerminalChoices() {
	var names []string
	for i, tab := range g.terminalTabs {
		title := tab.title
		if title == "" {
			title = fmt.Sprintf("Shell %d", i+1)
		}
		names = append(names, title)
	}
	g.terminalSelect.Items(names)
	g.terminalSelect.SetSelected(g.terminalIndex)
}
func (g *desktop) closeTerminal() {
	t := g.terminal
	if t.starting {
		return
	}
	if t.session != nil {
		p := t.session
		t.session = nil
		g.work(func(context.Context) func() { _ = p.Close(); return nil })
	}
	if len(g.terminalTabs) == 1 {
		t.last = ""
		t.frame = ui.TerminalFrame{}
		g.selectTerminal(0)
		return
	}
	i := g.terminalIndex
	g.terminalTabs = append(g.terminalTabs[:i], g.terminalTabs[i+1:]...)
	g.selectTerminal(min(i, len(g.terminalTabs)-1))
}
func (g *desktop) cycleTerminal(delta int) {
	g.selectTerminal((g.terminalIndex + delta + len(g.terminalTabs)) % len(g.terminalTabs))
	g.showTerminal()
}
func (g *desktop) externalEditor() {
	path := g.sourcePath()
	if path == "" {
		g.status.SetText("Select a file before opening the external editor")
		return
	}
	g.work(func(context.Context) func() {
		cmd, err := ui.ExternalEditorCommand(g.ws.Root, path, g.ws.Appearance.Editor, g.ws.Backend.Identity().Kind != "local")
		return func() {
			if err != nil {
				g.showError(err)
				return
			}
			g.newTerminal(cmd.Args, cmd.Dir)
		}
	})
}

func (g *desktop) renderTerminal(t *terminalView) {
	t.output.SetText(t.frame.Text)
	for _, s := range t.frame.Styles {
		var decoration uint32
		if s.Underline {
			decoration = 1
		}
		t.output.DecorateText(s.Start, s.End, s.Foreground, s.Background, decoration)
		var emphasis uint
		if s.Italic {
			emphasis |= 1
		}
		if s.Bold {
			emphasis |= 2
		}
		if emphasis != 0 {
			t.output.SetTextEmphasis(uint(s.Start), uint(s.End), emphasis)
		}
	}
	if t.frame.CursorVisible && t.frame.Cursor < len(t.frame.Text) {
		end := t.frame.Cursor + 1
		for end < len(t.frame.Text) && t.frame.Text[end]&0xc0 == 0x80 {
			end++
		}
		t.output.DecorateText(t.frame.Cursor, end, themeColor(background), themeColor(cyan), 0)
	}
}

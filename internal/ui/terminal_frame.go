package ui

import (
	"image/color"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	vt "github.com/charmbracelet/x/vt"
)

type TerminalStyle struct {
	Start, End              int
	Foreground, Background  uint32
	Bold, Italic, Underline bool
}
type TerminalFrame struct {
	Text          string
	Styles        []TerminalStyle
	Cursor        int
	CursorVisible bool
}

func rgba(c color.Color) uint32 {
	if c == nil {
		return 0
	}
	r, g, b, a := c.RGBA()
	return uint32(r>>8)<<24 | uint32(g>>8)<<16 | uint32(b>>8)<<8 | uint32(a>>8)
}

// Frame converts the emulator's atomic ANSI snapshot to native text ranges.
// It never exposes pointers into the concurrently changing emulator cells.
func (t *NativeTerminal) Frame() TerminalFrame {
	raw := t.session.snapshot()
	w, h := t.session.term.Width(), t.session.term.Height()
	cursor := t.session.term.CursorPosition()
	visible := !t.session.term.CursorHidden()
	return terminalFrame(raw, w, h, cursor.X, cursor.Y, visible)
}
func terminalFrame(raw string, w, h, x, y int, visible bool) TerminalFrame {
	copy := vt.NewEmulator(w, h)
	defer copy.Close()
	// Render uses LF between full rows; CR restores the column for VT decoding.
	_, _ = copy.Write([]byte(strings.ReplaceAll(raw, "\n", "\r\n")))
	var text strings.Builder
	frame := TerminalFrame{CursorVisible: visible}
	for row := 0; row < h; row++ {
		if row > 0 {
			text.WriteByte('\n')
		}
		for col := 0; col < w; col++ {
			cell := copy.CellAt(col, row)
			if cell != nil && cell.Width == 0 {
				continue
			}
			start := text.Len()
			if col == x && row == y {
				frame.Cursor = start
			}
			if cell == nil || cell.Content == "" {
				text.WriteByte(' ')
			} else {
				text.WriteString(cell.Content)
			}
			if cell == nil {
				continue
			}
			s := cell.Style
			fg, bg := rgba(s.Fg), rgba(s.Bg)
			if s.Attrs&uv.AttrReverse != 0 {
				fg, bg = bg, fg
				if fg == 0 {
					fg = 0x14171eff
				}
				if bg == 0 {
					bg = 0xe7eaf0ff
				}
			}
			if fg != 0 || bg != 0 || s.Attrs != 0 || s.Underline != 0 {
				style := TerminalStyle{Start: start, End: text.Len(), Foreground: fg, Background: bg, Bold: s.Attrs&uv.AttrBold != 0, Italic: s.Attrs&uv.AttrItalic != 0, Underline: s.Underline != 0}
				n := len(frame.Styles)
				if n > 0 {
					last := frame.Styles[n-1]
					last.Start, last.End = style.Start, style.End
					if last == style && frame.Styles[n-1].End == start {
						frame.Styles[n-1].End = style.End
						continue
					}
				}
				frame.Styles = append(frame.Styles, style)
			}
		}
	}
	frame.Text = text.String()
	return frame
}

package ui

import (
	"bytes"
	"context"
	"os/exec"
	"runtime"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

type clipboardReadMsg struct {
	text string
}

// requestClipboardPaste triggers both OSC 52 terminal clipboard reading and
// OS-level clipboard fallback so Ctrl+V, Shift+Insert, Right-Click, and
// Middle-Click paste work across all terminals and display servers.
func (m *Model) requestClipboardPaste(preferPrimary bool) tea.Cmd {
	oscCmd := tea.ReadClipboard
	if preferPrimary {
		oscCmd = tea.ReadPrimaryClipboard
	}
	osCmd := func() tea.Msg {
		text := readSystemClipboard(preferPrimary)
		return clipboardReadMsg{text: text}
	}
	return tea.Batch(oscCmd, osCmd)
}

func (m *Model) applyClipboardText(raw string) tea.Cmd {
	if raw == "" {
		return nil
	}
	now := time.Now()
	if raw == m.lastPasteText && !m.lastPasteAt.IsZero() && now.Sub(m.lastPasteAt) < 250*time.Millisecond {
		return nil
	}
	m.lastPasteText = raw
	m.lastPasteAt = now
	return m.applyPaste(raw)
}

func (m *Model) applyPaste(raw string) tea.Cmd {
	if raw == "" {
		return nil
	}
	if m.settingsOpen {
		m.settingsPaste(raw)
		return nil
	}
	if m.accountsOpen {
		return m.accountPaste(raw)
	}
	if m.paletteOpen {
		text := strings.TrimSpace(strings.ReplaceAll(sanitizeDisplay(raw), "\n", " "))
		if text == "" {
			return nil
		}
		runes := []rune(m.paletteInput)
		avail := 128 - len(runes)
		if avail > 0 {
			add := []rune(text)
			if len(add) > avail {
				add = add[:avail]
			}
			m.paletteInput += string(add)
			m.paletteSelected = -1
			m.paletteCompletion = paletteCompletionState{}
			m.paletteError, m.paletteHelp = "", ""
		}
		return nil
	}
	if m.marketplaceOpen {
		m.marketplacePaste(raw)
		return nil
	}
	if m.projectOpen {
		m.projectPaste(raw)
		return nil
	}
	if m.helpOpen || m.sessionsOpen {
		return nil
	}
	if m.mode == ModeInline && m.inline != nil {
		m.inlinePaste(raw)
		return nil
	}
	if m.chatTabRenaming {
		text := strings.TrimSpace(strings.ReplaceAll(sanitizeDisplay(raw), "\n", " "))
		for _, r := range text {
			if unicode.IsPrint(r) && len([]rune(m.chatTabRenameInput)) < 32 {
				m.chatTabRenameInput += string(r)
			}
		}
		return nil
	}
	if m.chatModelPicker.open {
		text := strings.TrimSpace(strings.ReplaceAll(sanitizeDisplay(raw), "\n", " "))
		for _, r := range text {
			if unicode.IsPrint(r) && len([]rune(m.chatModelPicker.filter)) < 48 {
				m.chatModelPicker.filter += string(r)
			}
		}
		m.chatModelPicker.selected = 0
		return nil
	}
	if m.mode == ModeTerminal && m.activeShell() != nil && m.viewerTabIsShell() {
		if len(raw) > 64<<10 {
			m.status = "paste exceeds 64 KiB limit"
			return nil
		}
		p := m.activeShell()
		return func() tea.Msg { return terminalInputMsg{err: p.paste(raw)} }
	}
	_, _, chatVisible := m.chatPaneBounds()
	if chatVisible {
		m.focus = focusChat
		m.chatEditing = true
		atBottom := m.chatAtBottom()
		text := sanitizeDisplay(strings.ReplaceAll(raw, "\r\n", "\n"))
		if len([]rune(m.chatInput))+len([]rune(text)) > maxChatPrompt {
			m.status = "prompt exceeds input limit"
		} else {
			m.chatInput += text
			m.chatInputChanged(atBottom)
		}
		return nil
	}
	return nil
}

func (m *Model) inlinePaste(raw string) {
	e := m.inline
	if e == nil {
		return
	}
	clean := strings.ReplaceAll(raw, "\r\n", "\n")
	if e.searching {
		line := strings.ReplaceAll(sanitizeDisplay(clean), "\n", " ")
		for _, r := range line {
			if !unicode.IsControl(r) && len([]rune(e.search)) < 128 {
				e.search += string(r)
			}
		}
		return
	}
	e.insert = true
	var filtered []rune
	for _, r := range clean {
		if r == '\n' || r == '\t' || !unicode.IsControl(r) {
			filtered = append(filtered, r)
		}
	}
	if len(filtered) > 0 {
		m.inlineEdit(e.cursor, e.cursor, filtered)
		m.inlineEnsureCursorVisible()
		m.status = "pasted into inline editor"
	}
}

func (m *Model) projectPaste(raw string) {
	text := strings.TrimSpace(strings.ReplaceAll(sanitizeDisplay(raw), "\n", " "))
	if text == "" {
		return
	}
	if m.projectEditField != "" {
		maxLen := 200
		if m.projectEditField == "name" {
			maxLen = 64
		}
		avail := maxLen - len([]rune(m.projectEditInput))
		if avail > 0 {
			add := []rune(text)
			if len(add) > avail {
				add = add[:avail]
			}
			m.projectEditInput += string(add)
		}
	}
}

func readSystemClipboard(preferPrimary bool) string {
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		if out, err := runClipboardCmd(ctx, "pbpaste"); err == nil && out != "" {
			return out
		}
	case "windows":
		if out, err := runClipboardCmd(ctx, "powershell.exe", "-NoProfile", "-Command", "Get-Clipboard"); err == nil && out != "" {
			return out
		}
	default:
		type clipCmd struct {
			name string
			args []string
		}
		primaryCmds := []clipCmd{
			{name: "wl-paste", args: []string{"--no-newline", "--primary"}},
			{name: "xclip", args: []string{"-o", "-selection", "primary"}},
			{name: "xsel", args: []string{"-op"}},
		}
		clipboardCmds := []clipCmd{
			{name: "wl-paste", args: []string{"--no-newline"}},
			{name: "xclip", args: []string{"-o", "-selection", "clipboard"}},
			{name: "xsel", args: []string{"-ob"}},
		}
		var ordered []clipCmd
		if preferPrimary {
			ordered = append(ordered, primaryCmds...)
			ordered = append(ordered, clipboardCmds...)
		} else {
			ordered = append(ordered, clipboardCmds...)
			ordered = append(ordered, primaryCmds...)
		}
		for _, c := range ordered {
			if _, err := exec.LookPath(c.name); err != nil {
				continue
			}
			if out, err := runClipboardCmd(ctx, c.name, c.args...); err == nil && out != "" {
				return out
			}
		}
		// Fallback for Wayland / X11 desktops (e.g. GNOME/GTK) when wl-paste/xclip are not installed.
		if _, err := exec.LookPath("python3"); err == nil {
			pyOrder := `"CLIPBOARD", "PRIMARY"`
			if preferPrimary {
				pyOrder = `"PRIMARY", "CLIPBOARD"`
			}
			script := `import sys
try:
    import gi
    gi.require_version("Gtk", "3.0")
    from gi.repository import Gtk, Gdk
    for name in (` + pyOrder + `):
        cb = Gtk.Clipboard.get(Gdk.Atom.intern(name, False))
        t = cb.wait_for_text()
        if t:
            sys.stdout.write(t)
            break
except Exception:
    pass`
			if out, err := runClipboardCmd(ctx, "python3", "-c", script); err == nil && out != "" {
				return out
			}
		}
	}
	return ""
}

func runClipboardCmd(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// writeClipboardText writes text to both the terminal OSC 52 clipboard and the
// OS system clipboard so copying works across all terminals and desktop environments.
func (m *Model) writeClipboardText(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	oscCmd := tea.SetClipboard(text)
	osCmd := func() tea.Msg {
		writeSystemClipboard(text)
		return nil
	}
	return tea.Batch(oscCmd, osCmd)
}

func writeSystemClipboard(text string) {
	if text == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		_ = runClipboardWriteCmd(ctx, text, "pbcopy")
	case "windows":
		_ = runClipboardWriteCmd(ctx, text, "clip.exe")
	default:
		type clipCmd struct {
			name string
			args []string
		}
		cmds := []clipCmd{
			{name: "wl-copy", args: nil},
			{name: "xclip", args: []string{"-i", "-selection", "clipboard"}},
			{name: "xsel", args: []string{"-ib"}},
		}
		wrote := false
		for _, c := range cmds {
			if _, err := exec.LookPath(c.name); err != nil {
				continue
			}
			if err := runClipboardWriteCmd(ctx, text, c.name, c.args...); err == nil {
				wrote = true
				break
			}
		}
		if !wrote {
			if _, err := exec.LookPath("python3"); err == nil {
				script := `import sys
try:
    data = sys.stdin.read()
    import gi
    gi.require_version("Gtk", "3.0")
    from gi.repository import Gtk, Gdk
    for name in ("CLIPBOARD", "PRIMARY"):
        cb = Gtk.Clipboard.get(Gdk.Atom.intern(name, False))
        cb.set_text(data, -1)
        cb.store()
except Exception:
    pass`
				_ = runClipboardWriteCmd(ctx, text, "python3", "-c", script)
			}
		}
	}
}

func runClipboardWriteCmd(ctx context.Context, input string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = strings.NewReader(input)
	return cmd.Run()
}

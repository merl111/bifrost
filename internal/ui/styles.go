package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// uiPalette keeps the visual language in one place. The terminal is a dense
// environment, so hierarchy comes from a small number of deliberately reused
// tones instead of a different color for every piece of text.
type uiPalette struct {
	ink         color.Color
	muted       color.Color
	quiet       color.Color
	chrome      color.Color
	surface     color.Color
	elevated    color.Color
	recessed    color.Color
	border      color.Color
	focusBorder color.Color
	accent      color.Color
	cyan        color.Color
	violet      color.Color
	warn        color.Color
	danger      color.Color
}

func paletteFor(theme string) uiPalette {
	if theme == "light" {
		return uiPalette{
			ink: lipgloss.Color("#273047"), muted: lipgloss.Color("#66718a"), quiet: lipgloss.Color("#8993a8"),
			chrome: lipgloss.Color("#e4e9f2"), surface: lipgloss.Color("#f3f5fa"), elevated: lipgloss.Color("#ffffff"), recessed: lipgloss.Color("#eaEEF7"),
			border: lipgloss.Color("#c5ccdc"), focusBorder: lipgloss.Color("#7387b5"), accent: lipgloss.Color("#6d4aff"),
			cyan: lipgloss.Color("#007f91"), violet: lipgloss.Color("#6d4aff"), warn: lipgloss.Color("#ad6a00"), danger: lipgloss.Color("#ba3b57"),
		}
	}
	if theme == "plain" {
		return uiPalette{}
	}
	return uiPalette{
		ink: lipgloss.Color("#edf1ff"), muted: lipgloss.Color("#9ba6bf"), quiet: lipgloss.Color("#66728c"),
		chrome: lipgloss.Color("#0d101b"), surface: lipgloss.Color("#151827"), elevated: lipgloss.Color("#1c2236"), recessed: lipgloss.Color("#0f1220"),
		border: lipgloss.Color("#2d364d"), focusBorder: lipgloss.Color("#526899"), accent: lipgloss.Color("#b29aff"),
		cyan: lipgloss.Color("#66e5d0"), violet: lipgloss.Color("#9a83ff"), warn: lipgloss.Color("#f4c266"), danger: lipgloss.Color("#ff827f"),
	}
}

func (m *Model) colors() uiPalette { return paletteFor(m.theme) }

func colorStyle(color color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(color)
}

// Preserve the panel surface after nested inline styles reset SGR; otherwise
// terminal-default backgrounds show through as stripes beside colored text.
func surfaceText(text string, foreground, background color.Color) string {
	if foreground == nil && background == nil {
		return text
	}
	base := ansi.NewStyle().ForegroundColor(foreground).BackgroundColor(background).String()
	fgOnly := ansi.NewStyle().ForegroundColor(foreground).String()
	bgOnly := ansi.NewStyle().BackgroundColor(background).String()
	text = strings.NewReplacer(
		"\x1b[m", "\x1b[m"+base,
		"\x1b[0m", "\x1b[0m"+base,
		"\x1b[39;49m", base,
		"\x1b[49m", bgOnly,
		"\x1b[39m", fgOnly,
	).Replace(text)
	return base + text + ansi.ResetStyle
}

func (p uiPalette) enabled() bool { return p.ink != nil }

func (p uiPalette) brand(s string) string {
	if !p.enabled() {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#111425")).Background(p.violet).Bold(true).Padding(0, 1).Render(strings.TrimSpace(s))
}

func (p uiPalette) title(s string) string {
	if !p.enabled() {
		return s
	}
	return lipgloss.NewStyle().Foreground(p.ink).Bold(true).Render(s)
}

func (p uiPalette) inkText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.ink).Render(s)
}

func (p uiPalette) badge(s string) string {
	if !p.enabled() {
		return s
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#0d101b")).Background(p.cyan).Bold(true).Render(s)
}

func (p uiPalette) mutedText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.muted).Render(s)
}

func (p uiPalette) quietText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.quiet).Render(s)
}

func (p uiPalette) accentText(s string) string {
	if !p.enabled() {
		return s
	}
	return lipgloss.NewStyle().Foreground(p.accent).Bold(true).Render(s)
}

func (p uiPalette) cyanText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.cyan).Render(s)
}

func (p uiPalette) violetText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.violet).Render(s)
}

func (p uiPalette) warnText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.warn).Render(s)
}

func (p uiPalette) dangerText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.danger).Render(s)
}

func (p uiPalette) okText(s string) string {
	if !p.enabled() {
		return s
	}
	return colorStyle(p.cyan).Render(s)
}

func padRight(s string, width int) string {
	if width <= 0 {
		return ""
	}
	t := ansi.Truncate(s, width, "…")
	w := lipgloss.Width(t)
	if w >= width {
		return t
	}
	return t + strings.Repeat(" ", width-w)
}

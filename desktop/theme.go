package main

import (
	"bifrost/internal/config"
	cui "cui.local/cui"
)

var widgetStyles = make(map[cui.Widget]cui.WidgetStyle)
var currentTheme = "midnight"

// CUI's packed colors are RGBA, independent of the host theme.
const (
	background uint32 = 0x191a20ff
	surface    uint32 = 0x202129ff
	elevated   uint32 = 0x292b36ff
	rail       uint32 = 0x121319ff
	accentSoft uint32 = 0x302b43ff
	border     uint32 = 0x383a48ff
	foreground uint32 = 0xeeeef5ff
	muted      uint32 = 0xa4a9bdff
	violet     uint32 = 0xb4a0ffff
	cyan       uint32 = 0x79dce8ff
)

func style(w cui.Widget, bg, fg uint32, radius float64, outline bool, padding ...int) {
	line := 0.0
	if outline {
		line = 1
	}
	pad := 0
	if len(padding) > 0 {
		pad = padding[0]
	}
	value := cui.WidgetStyle{Background: bg, Foreground: fg, Border: border, Radius: radius, BorderWidth: line, Padding: pad}
	widgetStyles[w] = value
	value.Background, value.Foreground, value.Border = themeColor(bg), themeColor(fg), themeColor(border)
	if !w.SetStyle(&value) {
		panic("CUI rejected the desktop palette")
	}
}

func label(parent cui.Widget, text string, size float64, color uint32) cui.Widget {
	w := parent.Label(text)
	w.Font("", size, 400)
	style(w, 0, color, 0, false)
	return w
}

func button(parent cui.Widget, text string, action func()) cui.Widget {
	w := parent.Button(text)
	w.MinSize(0, 32)
	style(w, elevated, foreground, 8, false, 6)
	w.OnAction(func(cui.Widget) { action() })
	return w
}

func chatTheme() cui.ChatTheme {
	t, _ := cui.DefaultChatTheme(cui.ChatNebula)
	t.Background, t.Surface, t.Foreground = background, surface, foreground
	t.Surface = background
	t.Muted, t.Border, t.Accent, t.OnAccent = muted, border, violet, background
	t.Soft, t.Hover, t.Rail, t.Online = accentSoft, elevated, surface, cyan
	t.FontSize = 16
	t.Background, t.Surface, t.Foreground = themeColor(t.Background), themeColor(t.Surface), themeColor(t.Foreground)
	t.OnAccent = themeColor(t.OnAccent)
	t.Muted, t.Border, t.Accent, t.Soft, t.Hover, t.Rail, t.Online = themeColor(t.Muted), themeColor(t.Border), themeColor(t.Accent), themeColor(t.Soft), themeColor(t.Hover), themeColor(t.Rail), themeColor(t.Online)
	return t
}

func themeColor(color uint32) uint32 {
	if currentTheme == "plain" {
		if color == violet || color == cyan {
			return foreground
		}
		return color
	}
	if currentTheme != "light" {
		return color
	}
	switch color {
	case background:
		return 0xf7f8fcff
	case surface:
		return 0xffffffff
	case elevated:
		return 0xe9ebf3ff
	case rail:
		return 0xedeff6ff
	case accentSoft:
		return 0xe8dffaff
	case border:
		return 0xc7ccdaff
	case foreground:
		return 0x202432ff
	case muted:
		return 0x596278ff
	case violet:
		return 0x7552c8ff
	case cyan:
		return 0x007886ff
	}
	return color
}

func (g *desktop) applyAppearance(settings config.UISettings) {
	previous := g.ws.Appearance
	g.ws.Appearance = settings
	if previous.Layout != settings.Layout || previous.PaneProportions != settings.PaneProportions {
		g.restoreLayout(settings)
	}
	currentTheme = settings.Theme
	if currentTheme == "light" {
		g.app.Theme(cui.Light)
	} else {
		g.app.Theme(cui.Dark)
	}
	for widget, value := range widgetStyles {
		value.Background, value.Foreground, value.Border = themeColor(value.Background), themeColor(value.Foreground), themeColor(value.Border)
		widget.SetStyle(&value)
	}
	g.timeline.SetTheme(chatTheme())
	g.dirty = true
}

func iconButton(parent cui.Widget, symbol int, description string, action func()) cui.Widget {
	w := parent.SymbolButton(symbol, description)
	w.IconOnly(true)
	w.IconSize(17)
	w.MinSize(32, 32)
	style(w, 0, muted, 7, false, 6)
	w.Tooltip(description)
	w.Accessibility(description, "")
	w.OnAction(func(cui.Widget) { action() })
	return w
}

func shortText(text string, limit int) string {
	runes := []rune(safeText(text))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}

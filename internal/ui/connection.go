package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// ConnectionInfo describes the active model connection shown in the status
// bar. Credentials stay outside the UI; these fields are display metadata.
type ConnectionInfo struct {
	Provider         string
	Account          string
	Model            string
	Location         string
	Connected        bool
	ContextTokens    int
	ContextMaxTokens int
}

// WithConnectionInfo supplies provider metadata for the bottom status bar.
func WithConnectionInfo(info ConnectionInfo) Option {
	return func(m *Model) { m.connection = sanitizeConnectionInfo(info) }
}

func sanitizeConnectionInfo(info ConnectionInfo) ConnectionInfo {
	info.Provider = sanitizeDisplay(strings.TrimSpace(info.Provider))
	info.Account = sanitizeDisplay(strings.TrimSpace(info.Account))
	info.Model = sanitizeDisplay(strings.TrimSpace(info.Model))
	info.Location = sanitizeDisplay(strings.TrimSpace(info.Location))
	return info
}

func providerGlyph(provider string) string {
	switch strings.ToLower(provider) {
	case "openai", "responses", "chatgpt":
		return "◉"
	case "anthropic", "claude":
		return "✳"
	case "cursor", "cursor-native":
		return "➤"
	case "gemini", "google":
		return "◇"
	case "openrouter":
		return "◆"
	default:
		return "●"
	}
}

func (m *Model) connectionSegments() []string {
	info := m.connection
	if info.Account == "" {
		info.Account = sanitizeDisplay(m.accountLabel)
	}
	if info.Provider == "" {
		info.Provider = "offline"
	}
	provider := m.providerIcon(info.Provider) + " " + providerDisplayName(info.Provider)
	model := valueOr(info.Model, "—")
	location := "⌂ " + valueOr(info.Location, displayDir(m.dir))
	state := "○"
	if info.Connected {
		state = "●"
	}
	segments := []string{provider}
	if account := connectionAccountLabel(info.Provider, info.Account, m.accountLabel); account != "" {
		segments = append(segments, account)
	}
	segments = append(segments, model, location, state)
	if used, maxTok, pct := m.effectiveContextSaturation(); maxTok > 0 {
		segments = append(segments, fmt.Sprintf("🧠 %s/%s (%d%%)", formatTokenCountShort(used), formatTokenCountShort(maxTok), pct))
	} else if used > 0 {
		segments = append(segments, "🧠 "+formatTokenCountShort(used))
	}
	if len(m.terminalTabs) > 0 {
		runningTitle := ""
		for _, t := range m.terminalTabs {
			if t.Running {
				runningTitle = t.Title
				break
			}
		}
		if runningTitle != "" {
			segments = append(segments, "⚡ "+runningTitle+" (t)")
		} else {
			segments = append(segments, fmt.Sprintf("🐚 %d term tabs (t)", len(m.terminalTabs)+1))
		}
	}
	return segments
}

func connectionAccountLabel(provider, account, fallback string) string {
	raw := strings.TrimSpace(account)
	if raw == "" {
		raw = strings.TrimSpace(fallback)
	}
	if raw == "" {
		return "@local"
	}
	short := shortAccountName(provider, raw, "")
	if short == "" {
		return ""
	}
	return "@" + short
}

func valueOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (m *Model) renderConnectionBar(width int) string {
	p := m.colors()
	parts := m.connectionSegments()
	if width < 44 {
		parts = parts[:minInt(2, len(parts))]
	} else if width < 66 {
		parts = parts[:minInt(3, len(parts))]
	} else if width < 84 {
		parts = parts[:minInt(4, len(parts))]
	}
	if !p.enabled() {
		return truncatePad(strings.Join(parts, "  "), width)
	}
	backgrounds := []string{"#2d2550", "#193744", "#26314b", "#273548", "#23453f", "#3a294d"}
	foreground := []string{"#e9e2ff", "#d8fbff", "#e5eaff", "#dce7ff", "#d8fff2", "#ffdff8"}
	styled := make([]string, 0, len(parts))
	for i, part := range parts {
		bg := backgrounds[i%len(backgrounds)]
		fg := foreground[i%len(foreground)]
		styled = append(styled, lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg)).Padding(0, 1).Render(part))
	}
	return surfaceText(truncatePad(strings.Join(styled, " "), width), p.ink, p.chrome)
}

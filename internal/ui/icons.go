package ui

import "strings"

// Icon modes control the small provider mark shown in the connection bar.
// Unicode preserves the original Bifrost symbols, nerd uses verified Nerd
// Font glyphs, and ascii uses text abbreviations for terminals without glyph
// support.
const (
	IconsUnicode = "unicode"
	IconsNerd    = "nerd"
	IconsASCII   = "ascii"
)

// NormalizeIconMode accepts the persisted/configured spelling and keeps an
// unknown value safe and backwards compatible. Empty means the default mode.
func NormalizeIconMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case IconsNerd:
		return IconsNerd
	case IconsASCII:
		return IconsASCII
	case "", IconsUnicode:
		return IconsUnicode
	default:
		return IconsUnicode
	}
}

// ValidIconMode reports whether mode is a supported persisted value. The
// empty value is accepted for configs written before ui.icons existed.
func ValidIconMode(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", IconsUnicode, IconsNerd, IconsASCII:
		return true
	default:
		return false
	}
}

// WithIcons selects how provider marks are rendered in the UI. Unknown
// values intentionally fall back to unicode to retain backwards compatibility
// for callers constructing a Model directly.
func WithIcons(mode string) Option {
	return func(m *Model) { m.icons = NormalizeIconMode(mode) }
}

// nerdProviderIcons contains only glyphs present in Nerd Fonts' current
// generated glyph catalogue. These are private-use characters and therefore
// require a Nerd Font patched with the matching Codicons/Devicons set.
var nerdProviderIcons = map[string]string{
	"openai":    "\uec81", // nf-cod-openai
	"responses": "\uec81", // OpenAI Responses API
	"chatgpt":   "\uec81", // OpenAI ChatGPT
	"anthropic": "\uec82", // nf-cod-claude
	"claude":    "\uec82", // nf-cod-claude
	"google":    "\ue7f0", // nf-dev-google
}

// asciiProviderLabels are deliberately readable abbreviations, rather than
// generic shapes that could be mistaken for a provider logo.
var asciiProviderLabels = map[string]string{
	"offline":       "LOC",
	"local":         "LOC",
	"openai":        "OAI",
	"responses":     "OAI",
	"chatgpt":       "OAI",
	"anthropic":     "ANT",
	"claude":        "ANT",
	"cursor":        "CUR",
	"cursor-native": "CUR",
	"gemini":        "GEM",
	"google":        "GOO",
	"openrouter":    "OR",
}

func providerIcon(mode, provider string) string {
	key := strings.ToLower(strings.TrimSpace(provider))
	switch NormalizeIconMode(mode) {
	case IconsNerd:
		if icon := nerdProviderIcons[key]; icon != "" {
			return icon
		}
		return asciiProviderIcon(key)
	case IconsASCII:
		return asciiProviderIcon(key)
	default:
		return providerGlyph(provider)
	}
}

func asciiProviderIcon(provider string) string {
	if label := asciiProviderLabels[provider]; label != "" {
		return label
	}
	provider = sanitizeDisplay(strings.TrimSpace(provider))
	if provider == "" {
		return "AI"
	}
	// Keep unknown provider names readable without emitting arbitrary glyphs.
	runes := []rune(provider)
	if len(runes) > 3 {
		runes = runes[:3]
	}
	return strings.ToUpper(string(runes))
}

func (m *Model) providerIcon(provider string) string {
	return providerIcon(m.icons, provider)
}

// providerDisplayName gives chat headings a stable, human-facing provider
// label without coupling them to profile IDs or API implementation names.
func providerDisplayName(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "", "offline", "local":
		return "Local"
	case "openai", "responses", "chatgpt":
		return "OpenAI"
	case "anthropic", "claude":
		return "Claude"
	case "cursor", "cursor-native":
		return "Cursor"
	case "gemini":
		return "Gemini"
	case "google":
		return "Google"
	case "openrouter":
		return "OpenRouter"
	default:
		provider = strings.TrimSpace(provider)
		if provider == "" {
			return "Local"
		}
		return sanitizeDisplay(provider)
	}
}

// providerChoiceLabel is the name shown on the chat pill and model switcher.
// Profile ids repeat the provider kind (openrouter-openrouter-openrouter);
// those collapse to the provider name. A distinct account name is kept in
// parentheses so two profiles of the same provider stay distinguishable.
func providerChoiceLabel(provider, account, id string) string {
	name := providerDisplayName(provider)
	short := shortAccountName(provider, account, id)
	if short == "" || strings.EqualFold(short, name) {
		return name
	}
	return name + " (" + short + ")"
}

func shortAccountName(provider, account, id string) string {
	raw := strings.TrimSpace(account)
	if raw == "" {
		raw = strings.TrimSpace(id)
	}
	kind := strings.ToLower(strings.TrimSpace(provider))
	aliases := []string{kind}
	if kind == "cursor-native" {
		aliases = append(aliases, "cursor")
	}
	for raw != "" {
		lower := strings.ToLower(raw)
		stripped := false
		for _, alias := range aliases {
			if alias == "" {
				continue
			}
			if strings.HasPrefix(lower, alias+"-") {
				raw = strings.TrimSpace(raw[len(alias)+1:])
				stripped = true
				break
			}
			if strings.EqualFold(raw, alias) || strings.EqualFold(raw, providerDisplayName(alias)) {
				return ""
			}
		}
		if !stripped {
			break
		}
	}
	return strings.TrimSpace(raw)
}

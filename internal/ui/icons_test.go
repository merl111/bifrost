package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestProviderIconModes(t *testing.T) {
	tests := []struct {
		mode, provider, want string
	}{
		{IconsUnicode, "openai", "◉"},
		{IconsASCII, "openai", "OAI"},
		{IconsASCII, "anthropic", "ANT"},
		{IconsASCII, "gemini", "GEM"},
		{IconsASCII, "offline", "LOC"},
		{IconsNerd, "openai", "\uec81"},
		{IconsNerd, "claude", "\uec82"},
		{IconsNerd, "google", "\ue7f0"},
		// Cursor has no verified product logo in the current catalogue.
		{IconsNerd, "cursor", "CUR"},
		// OpenRouter has no verified brand glyph in the current Nerd Fonts
		// catalogue, so nerd mode remains readable instead of guessing a logo.
		{IconsNerd, "openrouter", "OR"},
	}
	for _, tt := range tests {
		if got := providerIcon(tt.mode, tt.provider); got != tt.want {
			t.Errorf("providerIcon(%q, %q) = %q, want %q", tt.mode, tt.provider, got, tt.want)
		}
	}
}

func TestProviderIconUnknownModeFallsBackToUnicode(t *testing.T) {
	if got := providerIcon("future", "openai"); got != providerGlyph("openai") {
		t.Fatalf("unknown mode icon = %q, want unicode %q", got, providerGlyph("openai"))
	}
}

func TestProviderDisplayName(t *testing.T) {
	for provider, want := range map[string]string{
		"responses": "OpenAI",
		"chatgpt":   "OpenAI",
		"anthropic": "Claude",
		"claude":    "Claude",
		"gemini":    "Gemini",
		"":          "Local",
		"offline":   "Local",
		"other":     "other",
	} {
		if got := providerDisplayName(provider); got != want {
			t.Errorf("providerDisplayName(%q) = %q, want %q", provider, got, want)
		}
	}
}

func TestConnectionBarIconModesRemainBounded(t *testing.T) {
	for _, mode := range []string{IconsUnicode, IconsNerd, IconsASCII} {
		m := New(t.TempDir(), WithIcons(mode), WithConnectionInfo(ConnectionInfo{Provider: "openai", Model: "gpt", Connected: true}))
		for _, width := range []int{8, 24, 80} {
			got := m.renderConnectionBar(width)
			if strings.Contains(got, "\n") {
				t.Fatalf("mode %q width %d rendered a multiline footer: %q", mode, width, got)
			}
			if ansi.StringWidth(got) > width {
				t.Fatalf("mode %q width %d rendered width %d: %q", mode, width, ansi.StringWidth(got), got)
			}
		}
	}
}

func TestProviderIconFallbackSanitizesUnknownProvider(t *testing.T) {
	for _, mode := range []string{IconsNerd, IconsASCII} {
		got := providerIcon(mode, "\x1b[31mprovider")
		if strings.ContainsAny(got, "\x1b\r\n") {
			t.Fatalf("mode %q emitted control bytes in fallback %q", mode, got)
		}
	}
}

func TestConnectionSegmentsUseCanonicalProviderNamesAndIcons(t *testing.T) {
	m := New(t.TempDir(), WithIcons(IconsASCII), WithConnectionInfo(ConnectionInfo{Provider: "responses", Model: "gpt"}))
	parts := m.connectionSegments()
	if len(parts) == 0 {
		t.Fatal("connection segments are empty")
	}
	if parts[0] != "OAI OpenAI" {
		t.Fatalf("connection provider segment = %q, want %q", parts[0], "OAI OpenAI")
	}
	if got := providerIcon(IconsNerd, "openrouter"); got != "OR" {
		t.Fatalf("missing Nerd brand fallback = %q, want OR", got)
	}
}

package ui

import (
	"context"
	"strings"
	"testing"
)

func TestProviderChoiceLabelCollapsesRepeatedProfileID(t *testing.T) {
	if got := providerChoiceLabel("openrouter", "openrouter-openrouter-openrouter", "openrouter-openrouter-openrouter"); got != "OpenRouter" {
		t.Fatalf("label = %q", got)
	}
	if got := providerChoiceLabel("openrouter", "openrouter-work", "openrouter-work"); got != "OpenRouter (work)" {
		t.Fatalf("distinct account label = %q", got)
	}
	if got := shortAccountName("openrouter", "openrouter-openrouter-openrouter", ""); got != "" {
		t.Fatalf("short name = %q", got)
	}
}

func TestChatPillAndBadgeUseCatalogContext(t *testing.T) {
	const slug = "anthropic/claude-haiku-4.5"
	m := New(t.TempDir(), WithAccountManager(nil, nil, func(context.Context, AccountAddRequest, bool) ([]AccountModelOption, string, error) {
		return []AccountModelOption{{
			Slug:          slug,
			DisplayName:   "Claude Haiku 4.5",
			ContextLength: 1_000_000,
		}}, slug, nil
	}), WithAccounts([]AccountChoice{{
		ID:       "openrouter-openrouter-openrouter",
		Label:    "openrouter-openrouter-openrouter",
		Provider: "openrouter",
		Model:    slug,
		Active:   true,
	}}, nil), WithConnectionInfo(ConnectionInfo{
		Provider:         "openrouter",
		Account:          "openrouter-openrouter-openrouter",
		Model:            slug,
		Connected:        true,
		ContextTokens:    281900,
		ContextMaxTokens: 200000,
	}))
	m.theme = "plain"
	m.width, m.height = 160, 40

	cmd := m.refreshActiveCatalog()
	if cmd == nil {
		t.Fatal("expected catalog fetch")
	}
	m.Update(cmd())
	if m.connection.ContextMaxTokens != 1_000_000 {
		t.Fatalf("context window = %d, want catalog length", m.connection.ContextMaxTokens)
	}
	used, maxTok, pct := m.effectiveContextSaturation()
	if used != 281900 || maxTok != 1_000_000 || pct != 28 {
		t.Fatalf("saturation = %d/%d (%d%%)", used, maxTok, pct)
	}
	m.chatLines = []chatLine{{text: strings.Repeat("x", 2_000_000)}}
	used, _, _ = m.effectiveContextSaturation()
	if used != 281900 {
		t.Fatalf("rendered transcript replaced reported tokens: %d", used)
	}

	bar := strings.Join(m.renderChatTopBar(160), "\n")
	if strings.Contains(bar, "openrouter-openrouter") {
		t.Fatalf("pill still shows the profile id:\n%s", bar)
	}
	if !strings.Contains(bar, "OpenRouter") || !strings.Contains(bar, slug) || !strings.Contains(bar, "281.9k/1M (28%)") {
		t.Fatalf("pill/badge = %q", bar)
	}

	m.appendChatEvent(ChatEvent{Kind: "connection", Connection: &ConnectionInfo{
		Provider: "openrouter", Account: "openrouter-openrouter-openrouter", Model: slug,
		Connected: true, ContextTokens: 281900, ContextMaxTokens: 200000,
	}})
	if m.connection.ContextMaxTokens != 1_000_000 {
		t.Fatalf("connection event restored the heuristic window: %d", m.connection.ContextMaxTokens)
	}

	m.focus = focusChat
	m.handleNormalKey("m", "m")
	overlay := strings.Join(m.renderChatModelPickerOverlay(160), "\n")
	if strings.Contains(overlay, "openrouter-openrouter") {
		t.Fatalf("picker row still shows the profile id:\n%s", overlay)
	}
	if !strings.Contains(overlay, "OpenRouter") || !strings.Contains(overlay, slug) || !strings.Contains(overlay, "1M") {
		t.Fatalf("picker = %q", overlay)
	}
	parts := m.connectionSegments()
	for _, part := range parts {
		if strings.Contains(part, "openrouter-openrouter") {
			t.Fatalf("status segment = %q", part)
		}
	}
}

func TestContextBadgeOmitsUnknownWindow(t *testing.T) {
	m := New(t.TempDir(), WithConnectionInfo(ConnectionInfo{
		Provider: "chatgpt", Model: "gpt-6-luna", Connected: true,
		ContextTokens: 20000,
	}))
	m.theme = "plain"
	m.width, m.height = 120, 36
	used, maxTok, pct := m.effectiveContextSaturation()
	if used != 20000 || maxTok != 0 || pct != 0 {
		t.Fatalf("saturation = %d/%d (%d%%)", used, maxTok, pct)
	}
	badge := m.formatContextSaturationBadge()
	if strings.Contains(badge, "200.0k") || strings.Contains(badge, "%") || !strings.Contains(badge, "20.0k") {
		t.Fatalf("badge = %q", badge)
	}
}

func TestContextSaturationShowsOverflowPastTheWindow(t *testing.T) {
	m := New(t.TempDir(), WithConnectionInfo(ConnectionInfo{
		Provider: "openrouter", Model: "custom-model", Connected: true,
		ContextTokens: 250000, ContextMaxTokens: 200000,
	}))
	used, maxTok, pct := m.effectiveContextSaturation()
	if used != 250000 || maxTok != 200000 || pct != 125 {
		t.Fatalf("saturation = %d/%d (%d%%)", used, maxTok, pct)
	}
}

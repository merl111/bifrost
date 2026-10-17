package ui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestAccountSelectorReplacesChatHistory(t *testing.T) {
	called := ""
	m := New(t.TempDir(), WithAccounts([]AccountChoice{
		{ID: "personal", Label: "Personal", Active: true},
		{ID: "work", Label: "Work", Detail: "production profile"},
	}, func(_ context.Context, id string) ([]ChatEvent, error) {
		called = id
		return []ChatEvent{{Kind: "assistant", Text: "Welcome to the work workspace."}}, nil
	}))
	m.appendChatEvent(ChatEvent{Kind: "user", Text: "old account prompt"})
	m.openAccounts()
	m.accountSelected = 1
	cmd := m.accountKey("enter")
	if cmd == nil || !m.accountLoading {
		t.Fatalf("switch command=%v loading=%v", cmd != nil, m.accountLoading)
	}
	_, _ = m.Update(cmd())
	if called != "work" || m.accountLabel != "Work" || m.accountLoading || m.accountsOpen {
		t.Fatalf("account state: called=%q label=%q loading=%v open=%v", called, m.accountLabel, m.accountLoading, m.accountsOpen)
	}
	rendered := strings.Join(strings.Fields(m.renderChat()), " ")
	if strings.Contains(rendered, "old account prompt") || !strings.Contains(rendered, "Welcome to the work workspace") {
		t.Fatalf("chat history was not replaced: %q", m.renderChat())
	}
}

func TestAccountSelectorRefusesActiveChat(t *testing.T) {
	m := New(t.TempDir(), WithAccounts([]AccountChoice{
		{ID: "a", Label: "A", Active: true},
		{ID: "b", Label: "B"},
	}, func(context.Context, string) ([]ChatEvent, error) { return nil, nil }))
	m.chatRunning = true
	m.openAccounts()
	m.accountSelected = 1
	if cmd := m.accountKey("enter"); cmd != nil {
		t.Fatal("active chat created account switch command")
	}
	if !strings.Contains(m.status, "active chat") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestAccountDropdownsLiveModelLoadAndChatTabs(t *testing.T) {
	var discoveredReq AccountAddRequest
	var addedReq AccountAddRequest
	m := New(t.TempDir(),
		WithAccounts([]AccountChoice{
			{ID: "claude-main", Label: "claude-main", Provider: "claude", Model: "claude-sonnet-4-5", Active: true},
			{ID: "openai-work", Label: "openai-work", Provider: "responses", Model: "gpt-5"},
		}, func(_ context.Context, id string) ([]ChatEvent, error) {
			return []ChatEvent{{Kind: "status", Text: "switched to " + id}}, nil
		}),
		WithAccountManager(
			func(_ context.Context, req AccountAddRequest) ([]AccountChoice, AccountChoice, []ChatEvent, error) {
				addedReq = req
				ch := AccountChoice{ID: req.Name, Label: req.Name, Provider: req.Kind, Model: req.Model, Active: true}
				return []AccountChoice{ch}, ch, []ChatEvent{{Kind: "status", Text: "added"}}, nil
			},
			nil,
			func(_ context.Context, req AccountAddRequest, doOAuth bool) ([]AccountModelOption, string, error) {
				discoveredReq = req
				return []AccountModelOption{
					{Slug: "claude-opus-4-6", DisplayName: "Claude Opus 4.6"},
					{Slug: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5"},
				}, "claude-opus-4-6", nil
			},
		),
	)
	m.width, m.height = 140, 40

	// 1. Open account form, open Provider Type dropdown, select Anthropic
	m.openAccounts()
	cmd := m.accountKey("a")
	if cmd != nil {
		m.Update(cmd())
	}
	if !m.accountForm.open {
		t.Fatal("account form did not open")
	}
	// Press Enter on Provider Type (field 0) to open dropdown
	m.accountKey("enter")
	if !m.accountForm.providerDropOpen {
		t.Fatal("provider dropdown did not open on Enter")
	}
	m.accountKey("down")
	m.accountKey("down") // index 2 = anthropic
	cmd = m.accountKey("enter")
	if m.accountForm.providerDropOpen || accountProviderPresets[m.accountForm.presetIdx].Kind != "anthropic" {
		t.Fatalf("expected anthropic preset selected, got %s", accountProviderPresets[m.accountForm.presetIdx].Kind)
	}
	if cmd != nil {
		m.Update(cmd())
	}

	// 2. Paste a raw API key via tea.PasteMsg while on field 0 — should route to field 2 and trigger API model discovery
	_, pasteCmd := m.Update(tea.PasteMsg{Content: "sk-ant-api03-testkey1234567890\n"})
	if pasteCmd == nil {
		t.Fatal("expected model discovery command after pasting API key")
	}
	m.Update(pasteCmd())
	if discoveredReq.RawAPIKey != "sk-ant-api03-testkey1234567890" {
		t.Fatalf("expected RawAPIKey passed to discoverModels, got %q", discoveredReq.RawAPIKey)
	}

	// 3. Open Model dropdown (field 4) and pick second model
	m.accountForm.fieldIdx = 4
	m.accountKey("enter")
	if !m.accountForm.modelDropOpen {
		t.Fatal("model dropdown did not open on Enter")
	}
	m.accountKey("down")
	m.accountKey("enter")
	if m.accountForm.model != "claude-sonnet-4-5" {
		t.Fatalf("model = %q, want claude-sonnet-4-5", m.accountForm.model)
	}

	// Save form
	saveCmd := m.accountKey("ctrl+s")
	if saveCmd == nil {
		t.Fatal("expected save command on ctrl+s")
	}
	m.Update(saveCmd())
	if addedReq.Model != "claude-sonnet-4-5" || addedReq.Kind != "anthropic" {
		t.Fatalf("unexpected addedReq: %+v", addedReq)
	}

	// 4. Test parallel named chat tabs & quick model switcher in chat
	m.focus = focusChat
	m.appendChatEvent(ChatEvent{Kind: "user", Text: "First tab message"})
	m.executePaletteCommand("chat new Backend API")
	if len(m.chatTabs) != 2 || m.chatTab != 1 || m.chatTabs[1].Title != "Backend API" {
		t.Fatalf("expected 2 chat tabs with active 'Backend API', got len=%d idx=%d title=%q", len(m.chatTabs), m.chatTab, m.chatTabs[m.chatTab].Title)
	}
	if len(m.chatLines) != 0 {
		t.Fatal("new chat tab should start with empty transcript")
	}
	m.executePaletteCommand("chat rename Auth Service")
	if m.chatTabs[1].Title != "Auth Service" {
		t.Fatalf("renamed tab title = %q, want Auth Service", m.chatTabs[1].Title)
	}
	m.executePaletteCommand("chat prev")
	if m.chatTab != 0 || len(m.chatLines) != 2 {
		t.Fatalf("switching back to tab 0 failed: tab=%d lines=%d", m.chatTab, len(m.chatLines))
	}

	// 5. Open quick model picker in chat with 'm'
	m.handleNormalKey("m", "m")
	if !m.chatModelPicker.open || len(m.chatModelPicker.items) == 0 {
		t.Fatal("quick model picker did not open in chat")
	}
}

func TestChatGPTSignInEnterWaitsInsteadOfOpeningAnotherBrowser(t *testing.T) {
	preset := -1
	for i, p := range accountProviderPresets {
		if p.Kind == "chatgpt" {
			preset = i
		}
	}
	if preset < 0 {
		t.Fatal("chatgpt preset missing")
	}
	m := New(t.TempDir(), WithAccountManager(
		func(context.Context, AccountAddRequest) ([]AccountChoice, AccountChoice, []ChatEvent, error) {
			return nil, AccountChoice{}, nil, nil
		},
		nil,
		func(_ context.Context, _ AccountAddRequest, doOAuth bool) ([]AccountModelOption, string, error) {
			if doOAuth {
				t.Fatal("sign-in command ran during the waiting check")
			}
			return nil, "", nil
		},
	))
	m.width, m.height = 120, 40
	m.openAccounts()
	if cmd := m.initAccountForm(preset); cmd != nil {
		m.Update(cmd())
	}
	m.accountForm.fieldIdx = 2
	m.accountForm.name = "chatgpt-apple"
	cmd := m.accountKey("enter")
	if cmd == nil || !m.accountForm.oauthWaiting {
		t.Fatalf("first enter did not start browser sign-in: cmd=%v waiting=%v", cmd != nil, m.accountForm.oauthWaiting)
	}
	m.accountForm.fieldIdx = 4
	if again := m.accountKey("enter"); again != nil {
		t.Fatal("enter while waiting started another browser sign-in")
	}
	if !strings.Contains(m.status, "still waiting") {
		t.Fatalf("status = %q", m.status)
	}
	rendered := m.renderAccounts()
	if !strings.Contains(rendered, "waiting for browser sign-in") || strings.Contains(rendered, "querying models") {
		t.Fatalf("model row = %q", rendered)
	}
	m.cancelAccountOp()
	if m.accountForm.oauthWaiting {
		t.Fatal("cancel left browser sign-in waiting")
	}
}

func TestNoFakeContextSaturationOrBuiltInModelsWhenDisconnected(t *testing.T) {
	m := New(t.TempDir())
	m.width, m.height = 140, 40

	used, maxTok, pct := m.effectiveContextSaturation()
	if used != 0 || maxTok != 0 || pct != 0 {
		t.Fatalf("expected 0/0/0 context saturation when disconnected, got %d/%d (%d%%)", used, maxTok, pct)
	}
	footer := m.renderFooter()
	if strings.Contains(footer, "🧠") || strings.Contains(footer, "128.0k") {
		t.Fatalf("disconnected footer must not show fake context window: %q", footer)
	}

	m.openAccounts()
	m.initAccountForm(4) // chatgpt preset
	if len(m.accountForm.models) != 0 {
		t.Fatalf("expected 0 pre-loaded models before API auth, got %d", len(m.accountForm.models))
	}
}

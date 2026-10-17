package ui

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// AccountChoice describes one provider account or profile available to the
// workspace. The UI deliberately knows only its label and stable ID; callers
// own credentials, persistence, and session replacement.
type AccountChoice struct {
	ID       string
	Label    string
	Detail   string
	Provider string
	Model    string
	Location string
	Active   bool
}

// AccountModelOption represents one selectable model slug returned by the provider API.
type AccountModelOption struct {
	Slug          string
	DisplayName   string
	ContextLength int
}

// AccountAddRequest describes a new or updated provider/account profile created from the TUI.
type AccountAddRequest struct {
	Kind       string
	Name       string
	Account    string
	Model      string
	APIKeyEnv  string
	RawAPIKey  string
	BaseURL    string
	Executable string
}

type accountProviderPreset struct {
	Kind        string
	Title       string
	DefaultName string
	DefaultMod  string
	DefaultEnv  string
	DefaultExec string
	DefaultURL  string
	AuthHint    string
}

func (p accountProviderPreset) browserSignIn() bool {
	return p.Kind == "chatgpt" || p.Kind == "cursor-native"
}

var accountProviderPresets = []accountProviderPreset{
	{
		Kind: "claude", Title: "Claude Code CLI (Subscription / Agent)", DefaultName: "claude", DefaultMod: "auto", DefaultExec: "claude",
		AuthHint: "Uses installed `claude` CLI (`claude auth login`)",
	},
	{
		Kind: "cursor", Title: "Cursor Agent CLI (Subscription / Agent)", DefaultName: "cursor", DefaultMod: "auto", DefaultExec: "cursor-agent",
		AuthHint: "Uses installed `cursor-agent` CLI (`cursor-agent login`)",
	},
	{
		Kind: "anthropic", Title: "Anthropic API (Claude Models)", DefaultName: "anthropic", DefaultEnv: "ANTHROPIC_API_KEY",
		AuthHint: "Paste API key (Ctrl+V / Right-Click) or env var — loads models live from Anthropic API",
	},
	{
		Kind: "responses", Title: "OpenAI Responses API", DefaultName: "openai", DefaultEnv: "OPENAI_API_KEY",
		AuthHint: "Paste API key (Ctrl+V / Right-Click) or env var — loads models live from OpenAI API",
	},
	{
		Kind: "chatgpt", Title: "ChatGPT OAuth (Browser Sign-In)", DefaultName: "chatgpt",
		AuthHint: "Press Enter on OAuth Sign-In to open browser and load your ChatGPT plan models from API",
	},
	{
		Kind: "gemini", Title: "Google Gemini API", DefaultName: "gemini", DefaultEnv: "GEMINI_API_KEY",
		AuthHint: "Paste API key (Ctrl+V / Right-Click) or env var — loads models live from Gemini API",
	},
	{
		Kind: "openrouter", Title: "OpenRouter API", DefaultName: "openrouter", DefaultEnv: "OPENROUTER_API_KEY",
		AuthHint: "Paste API key (Ctrl+V / Right-Click) or env var — loads models live from OpenRouter API",
	},
	{
		Kind: "compatible", Title: "OpenAI-Compatible / Local (Ollama, vLLM)", DefaultName: "local-llm", DefaultEnv: "OPENAI_API_KEY", DefaultURL: "http://127.0.0.1:11434/v1",
		AuthHint: "Connects to any OpenAI-compatible endpoint and loads models from /v1/models",
	},
	{
		Kind: "cursor-native", Title: "Cursor Native (Browser / API Key)", DefaultName: "cursor-native", DefaultMod: "auto",
		AuthHint: "Browser sign-in or a Cursor API key. Separate from the cursor-agent CLI.",
	},
}

// Field indices in the Add Account form:
// 0: Provider Type (Dropdown)
// 1: Profile Name
// 2: Auth / API Key / OAuth Sign-In / CLI Executable
// 3: Base URL (only for compatible, otherwise skipped)
// 4: Model (Dropdown loaded from API + custom typing)
// 5: [ Save & Connect Account ]
type accountFormState struct {
	open             bool
	presetIdx        int
	fieldIdx         int
	providerDropOpen bool
	providerDropIdx  int
	modelDropOpen    bool
	modelDropIdx     int
	modelFilter      string
	models           []AccountModelOption
	modelsLoading    bool
	oauthWaiting     bool
	oauthConnected   bool
	name             string
	model            string
	envOrExec        string
	baseURL          string
	notice           string
	err              string
}

// WithAccounts adds an optional account switcher. selectAccount is invoked in
// a Tea command and its returned events replace the visible chat history.
func WithAccounts(choices []AccountChoice, selectAccount func(context.Context, string) ([]ChatEvent, error)) Option {
	copyChoices := append([]AccountChoice(nil), choices...)
	return func(m *Model) {
		m.accounts = copyChoices
		m.accountSelect = selectAccount
		for _, choice := range copyChoices {
			if choice.Active {
				m.accountLabel = choice.Label
				info := m.connection
				if choice.Provider != "" {
					info.Provider = choice.Provider
				}
				if choice.Model != "" {
					info.Model = choice.Model
				}
				if choice.Location != "" {
					info.Location = choice.Location
				}
				info.Account, info.Connected = choice.Label, true
				m.connection = sanitizeConnectionInfo(info)
				break
			}
		}
	}
}

// WithAccountManager wires adding and removing accounts/provider profiles directly from the TUI.
func WithAccountManager(
	addAccount func(context.Context, AccountAddRequest) ([]AccountChoice, AccountChoice, []ChatEvent, error),
	removeAccount func(context.Context, string) ([]AccountChoice, error),
	discoverModels ...func(context.Context, AccountAddRequest, bool) ([]AccountModelOption, string, error),
) Option {
	return func(m *Model) {
		m.accountAdd = addAccount
		m.accountRemove = removeAccount
		if len(discoverModels) > 0 {
			m.accountDiscoverModels = discoverModels[0]
		}
	}
}

// WithAccountModelSwitcher wires a callback that switches both the active account and its model slug.
func WithAccountModelSwitcher(fn func(context.Context, string, string) ([]AccountChoice, AccountChoice, []ChatEvent, error)) Option {
	return func(m *Model) {
		m.accountSelectModel = fn
	}
}

// WithAccountLabel sets the label shown in the workspace header when the
// caller has a current account but does not expose a switchable list.
func WithAccountLabel(label string) Option {
	return func(m *Model) {
		m.accountLabel = strings.TrimSpace(sanitizeDisplay(label))
		m.connection.Account = m.accountLabel
	}
}

type accountSwitchMsg struct {
	id      uint64
	choices []AccountChoice
	choice  AccountChoice
	events  []ChatEvent
	err     error
}

type accountRemoveMsg struct {
	id      uint64
	choices []AccountChoice
	removed string
	err     error
}

type accountModelsLoadedMsg struct {
	id             uint64
	models         []AccountModelOption
	defaultModel   string
	oauthConnected bool
	err            error
}

func (m *Model) cancelAccountOp() {
	if m.accountCancel != nil {
		m.accountCancel()
		m.accountCancel = nil
	}
	m.accountLoading = false
	m.accountForm.modelsLoading = false
	m.accountForm.oauthWaiting = false
}

func (m *Model) openAccounts() {
	m.cancelAccountOp()
	m.accountsOpen = true
	m.accountForm = accountFormState{}
	m.focus = focusViewer
	m.accountSelected = 0
	for i, choice := range m.accounts {
		if choice.Active {
			m.accountSelected = i
			break
		}
	}
	if len(m.accounts) == 0 {
		m.status = "no accounts configured — press 'a' to add an account"
	} else {
		m.status = "choose an account (or press 'a' to add, 'm' to change model)"
	}
}

func (m *Model) initAccountForm(presetIdx int) tea.Cmd {
	m.cancelAccountOp()
	if presetIdx < 0 || presetIdx >= len(accountProviderPresets) {
		presetIdx = 0
	}
	p := accountProviderPresets[presetIdx]
	envOrExec := ""
	if p.DefaultExec != "" {
		envOrExec = p.DefaultExec
	}
	m.accountForm = accountFormState{
		open:            true,
		presetIdx:       presetIdx,
		providerDropIdx: presetIdx,
		fieldIdx:        0,
		name:            p.DefaultName,
		model:           p.DefaultMod,
		envOrExec:       envOrExec,
		baseURL:         p.DefaultURL,
	}
	return m.startAccountModelDiscovery(false)
}

func (m *Model) applyAccountPreset(presetIdx int) tea.Cmd {
	m.cancelAccountOp()
	if presetIdx < 0 {
		presetIdx = len(accountProviderPresets) - 1
	}
	if presetIdx >= len(accountProviderPresets) {
		presetIdx = 0
	}
	p := accountProviderPresets[presetIdx]
	m.accountForm.presetIdx = presetIdx
	m.accountForm.providerDropIdx = presetIdx
	m.accountForm.name = p.DefaultName
	m.accountForm.model = p.DefaultMod
	if p.DefaultExec != "" {
		m.accountForm.envOrExec = p.DefaultExec
	} else {
		m.accountForm.envOrExec = ""
	}
	m.accountForm.baseURL = p.DefaultURL
	m.accountForm.models = nil
	m.accountForm.modelDropIdx = 0
	m.accountForm.modelFilter = ""
	m.accountForm.oauthConnected = false
	m.accountForm.err = ""
	m.accountForm.notice = ""
	return m.startAccountModelDiscovery(false)
}

func (m *Model) buildAccountFormRequest() AccountAddRequest {
	preset := accountProviderPresets[m.accountForm.presetIdx]
	rawAuth := strings.TrimSpace(m.accountForm.envOrExec)
	req := AccountAddRequest{
		Kind:    preset.Kind,
		Name:    strings.TrimSpace(m.accountForm.name),
		Model:   strings.TrimSpace(m.accountForm.model),
		BaseURL: strings.TrimSpace(m.accountForm.baseURL),
	}
	if preset.Kind == "claude" || preset.Kind == "cursor" {
		req.Executable = rawAuth
	} else if preset.browserSignIn() {
		if looksLikeRawAPIKey(rawAuth) {
			req.RawAPIKey = rawAuth
		}
	} else {
		if rawAuth == "" {
			req.APIKeyEnv = preset.DefaultEnv
		} else if looksLikeRawAPIKey(rawAuth) {
			req.RawAPIKey = rawAuth
			req.APIKeyEnv = preset.DefaultEnv
		} else {
			req.APIKeyEnv = rawAuth
		}
	}
	return req
}

func (m *Model) startAccountModelDiscovery(doOAuth bool) tea.Cmd {
	if m.accountDiscoverModels == nil || !m.accountForm.open {
		return nil
	}
	preset := accountProviderPresets[m.accountForm.presetIdx]
	if preset.Kind == "claude" && !doOAuth {
		return nil
	}
	if doOAuth && m.accountForm.oauthWaiting {
		m.status = "still waiting for the browser sign-in — Esc cancels"
		return nil
	}
	m.cancelAccountOp()
	req := m.buildAccountFormRequest()
	m.accountForm.modelsLoading = true
	m.accountForm.err = ""
	if doOAuth {
		m.accountForm.oauthWaiting = true
		label := "ChatGPT"
		if preset.Kind == "cursor-native" {
			label = "Cursor"
		}
		m.accountForm.notice = "opening browser for " + label + " sign-in…"
		if preset.Kind == "chatgpt" {
			m.accountForm.notice = "opening browser for ChatGPT sign-in… allow local-network access if asked"
		}
		m.status = m.accountForm.notice
	} else {
		m.accountForm.notice = "querying available models from API…"
	}
	m.accountID++
	id := m.accountID
	ctx, cancel := context.WithCancel(context.Background())
	m.accountCancel = cancel
	discoverFn := m.accountDiscoverModels
	return func() tea.Msg {
		models, defModel, err := discoverFn(ctx, req, doOAuth)
		return accountModelsLoadedMsg{
			id:             id,
			models:         models,
			defaultModel:   defModel,
			oauthConnected: doOAuth && err == nil,
			err:            err,
		}
	}
}

func looksLikeRawAPIKey(val string) bool {
	val = strings.TrimSpace(val)
	if val == "" {
		return false
	}
	if strings.HasPrefix(val, "sk-") || strings.HasPrefix(val, "AIza") || strings.HasPrefix(val, "gsk_") || strings.HasPrefix(val, "xai-") {
		return true
	}
	// Environment variable names are uppercase letters, digits, and underscores.
	for _, r := range val {
		if !((r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_') {
			return len(val) >= 12
		}
	}
	return false
}

func (m *Model) updateAccountModelsLoaded(msg accountModelsLoadedMsg) tea.Cmd {
	if msg.id != m.accountID {
		return nil
	}
	if msg.err == nil && len(msg.models) > 0 {
		m.rememberCatalogContexts(msg.models)
		m.applyCatalogContextWindow()
	}
	if m.chatModelPicker.open && m.chatModelPicker.loading {
		m.chatModelPicker.loading = false
		if msg.err == nil && len(msg.models) > 0 {
			m.populateChatModelPickerFromAPI(msg.models)
		} else if msg.err != nil {
			m.status = "API model query failed: " + sanitizeDisplay(msg.err.Error())
		}
		return nil
	}
	if !m.accountForm.open {
		return nil
	}
	m.accountForm.modelsLoading = false
	m.accountForm.oauthWaiting = false
	m.accountCancel = nil
	if msg.err != nil {
		m.accountForm.models = nil
		m.accountForm.err = "API model query failed: " + sanitizeDisplay(msg.err.Error())
		m.accountForm.notice = ""
		m.status = m.accountForm.err
		return nil
	}
	m.accountForm.models = msg.models
	if len(msg.models) == 0 {
		m.accountForm.notice = ""
		return nil
	}
	m.accountForm.err = ""
	if m.accountForm.model == "" {
		if msg.defaultModel != "" {
			m.accountForm.model = msg.defaultModel
		} else {
			m.accountForm.model = msg.models[0].Slug
		}
	}
	m.accountForm.modelDropIdx = 0
	for i, opt := range m.accountForm.models {
		if opt.Slug == m.accountForm.model {
			m.accountForm.modelDropIdx = i
			break
		}
	}
	if msg.oauthConnected {
		m.accountForm.oauthConnected = true
		m.accountForm.notice = fmt.Sprintf("✓ OAuth authenticated — loaded %d models from API", len(m.accountForm.models))
		m.accountForm.fieldIdx = 4
		m.accountForm.modelDropOpen = true
		m.status = m.accountForm.notice
	} else {
		m.accountForm.notice = fmt.Sprintf("✓ loaded %d models from API", len(m.accountForm.models))
	}
	return nil
}

func (m *Model) filteredAccountModels() []AccountModelOption {
	q := strings.ToLower(strings.TrimSpace(m.accountForm.modelFilter))
	if q == "" {
		return m.accountForm.models
	}
	var out []AccountModelOption
	for _, opt := range m.accountForm.models {
		if strings.Contains(strings.ToLower(opt.Slug), q) || strings.Contains(strings.ToLower(opt.DisplayName), q) {
			out = append(out, opt)
		}
	}
	return out
}

func (m *Model) accountPaste(raw string) tea.Cmd {
	text := strings.TrimSpace(strings.ReplaceAll(sanitizeDisplay(raw), "\n", ""))
	text = strings.ReplaceAll(text, "\r", "")
	if text == "" {
		return nil
	}
	if !m.accountForm.open {
		cmd := m.initAccountForm(0)
		m.status = "opened Add Account form"
		return cmd
	}
	preset := accountProviderPresets[m.accountForm.presetIdx]
	if m.accountForm.modelDropOpen {
		m.accountForm.modelFilter += text
		m.accountForm.modelDropIdx = 0
		return nil
	}
	// If user pastes an API key while on Provider Type (0) or Save button (5), route it to field 2 (API Key)
	if (m.accountForm.fieldIdx == 0 || m.accountForm.fieldIdx == 5) && preset.Kind != "chatgpt" && preset.Kind != "claude" && preset.Kind != "cursor" && preset.Kind != "cursor-native" && looksLikeRawAPIKey(text) {
		m.accountForm.fieldIdx = 2
		m.accountForm.envOrExec = text
		m.status = "pasted API key — loading models from API…"
		return m.startAccountModelDiscovery(false)
	}
	switch m.accountForm.fieldIdx {
	case 1:
		m.accountForm.name = trimRunes(m.accountForm.name+text, 64)
		m.status = "pasted profile name"
	case 2:
		if preset.Kind == "chatgpt" {
			return nil
		}
		if m.accountForm.envOrExec == preset.DefaultEnv && looksLikeRawAPIKey(text) {
			m.accountForm.envOrExec = text
		} else {
			m.accountForm.envOrExec = trimRunes(m.accountForm.envOrExec+text, 256)
		}
		m.status = "pasted API key — loading models from API…"
		if preset.Kind != "claude" && preset.Kind != "cursor" {
			return m.startAccountModelDiscovery(false)
		}
	case 3:
		m.accountForm.baseURL = trimRunes(m.accountForm.baseURL+text, 256)
		m.status = "pasted base URL — loading models from API…"
		return m.startAccountModelDiscovery(false)
	case 4:
		m.accountForm.model = trimRunes(m.accountForm.model+text, 128)
		m.status = "pasted model slug"
	}
	return nil
}

func trimRunes(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max])
	}
	return s
}

func (m *Model) accountsMouse(mouse tea.Mouse, forPaste bool) tea.Cmd {
	if !m.accountForm.open {
		return nil
	}
	preset := accountProviderPresets[m.accountForm.presetIdx]
	hasBaseURL := preset.Kind == "compatible"
	// Approximate row mapping inside the ACCOUNTS viewer panel
	row := mouse.Y
	switch {
	case row >= 6 && row <= 7:
		m.accountForm.fieldIdx = 0
		if !forPaste {
			m.accountForm.providerDropOpen = !m.accountForm.providerDropOpen
		}
	case row >= 8 && row <= 9:
		m.accountForm.fieldIdx = 1
	case row >= 10 && row <= 11:
		m.accountForm.fieldIdx = 2
		if !forPaste && preset.Kind == "chatgpt" {
			return m.startAccountModelDiscovery(true)
		}
	case hasBaseURL && row >= 12 && row <= 13:
		m.accountForm.fieldIdx = 3
	case (!hasBaseURL && row >= 12 && row <= 13) || (hasBaseURL && row >= 14 && row <= 15):
		m.accountForm.fieldIdx = 4
		if !forPaste && len(m.accountForm.models) > 0 {
			m.accountForm.modelDropOpen = !m.accountForm.modelDropOpen
		}
	case (!hasBaseURL && row >= 15 && row <= 17) || (hasBaseURL && row >= 17 && row <= 19):
		if !forPaste {
			m.accountForm.fieldIdx = 5
			return m.submitAccountForm()
		}
	}
	return nil
}

func (m *Model) accountKey(stroke string, text ...string) tea.Cmd {
	rawText := ""
	if len(text) > 0 {
		rawText = text[0]
	}
	if m.accountForm.open {
		return m.handleAccountFormKey(stroke, rawText)
	}
	switch stroke {
	case "escape", "esc", "q":
		m.cancelAccountOp()
		m.accountsOpen = false
		m.normalizeFocus()
		m.status = "account selector closed"
	case "a", "n":
		cmd := m.initAccountForm(0)
		m.status = "add account: Enter opens dropdowns · Ctrl+V/Right-Click paste · Ctrl+S save"
		return cmd
	case "e", "m":
		if len(m.accounts) == 0 {
			return m.initAccountForm(0)
		}
		choice := m.accounts[m.accountSelected]
		presetIdx := 0
		for i, p := range accountProviderPresets {
			if p.Kind == choice.Provider {
				presetIdx = i
				break
			}
		}
		cmd := m.initAccountForm(presetIdx)
		m.accountForm.name = choice.Label
		if choice.Model != "" {
			m.accountForm.model = choice.Model
		}
		m.accountForm.fieldIdx = 4
		m.accountForm.modelDropOpen = true
		m.status = "select model for " + sanitizeDisplay(choice.Label) + " (Enter to pick, Ctrl+S to save)"
		return cmd
	case "d", "x":
		if m.accountLoading || len(m.accounts) == 0 || m.accountRemove == nil {
			return nil
		}
		choice := m.accounts[m.accountSelected]
		m.accountLoading = true
		m.accountID++
		id := m.accountID
		rmFn := m.accountRemove
		m.status = "removing account " + sanitizeDisplay(choice.Label) + "…"
		return func() tea.Msg {
			choices, err := rmFn(context.Background(), choice.ID)
			return accountRemoveMsg{id: id, choices: choices, removed: choice.Label, err: err}
		}
	case "up", "k":
		if len(m.accounts) > 0 {
			m.accountSelected = (m.accountSelected + len(m.accounts) - 1) % len(m.accounts)
		}
	case "down", "j":
		if len(m.accounts) > 0 {
			m.accountSelected = (m.accountSelected + 1) % len(m.accounts)
		}
	case "enter", "return":
		if m.accountLoading {
			return nil
		}
		if len(m.accounts) == 0 {
			cmd := m.initAccountForm(0)
			m.status = "add account: Enter opens dropdowns · Ctrl+V/Right-Click paste · Ctrl+S save"
			return cmd
		}
		if m.chatRunning {
			m.status = "finish or cancel the active chat before switching"
			return nil
		}
		choice := m.accounts[m.accountSelected]
		if choice.Active {
			m.accountsOpen = false
			m.normalizeFocus()
			m.status = "account already active"
			return nil
		}
		if m.accountSelect == nil {
			m.status = "account switching is unavailable"
			return nil
		}
		m.accountLoading = true
		m.accountID++
		id := m.accountID
		selectAccount := m.accountSelect
		m.status = "switching account…"
		return func() tea.Msg {
			events, err := selectAccount(context.Background(), choice.ID)
			return accountSwitchMsg{id: id, choice: choice, events: events, err: err}
		}
	}
	return nil
}

func (m *Model) nextAccountFormField(cur, delta int, hasBaseURL bool) int {
	order := []int{0, 1, 2, 4, 5}
	if hasBaseURL {
		order = []int{0, 1, 2, 3, 4, 5}
	}
	pos := 0
	for i, idx := range order {
		if idx == cur {
			pos = i
			break
		}
	}
	nextPos := (pos + delta + len(order)) % len(order)
	return order[nextPos]
}

func (m *Model) handleAccountFormKey(stroke, text string) tea.Cmd {
	preset := accountProviderPresets[m.accountForm.presetIdx]
	hasBaseURL := preset.Kind == "compatible"

	// 1. Handle open Provider Type dropdown
	if m.accountForm.providerDropOpen {
		switch stroke {
		case "esc", "escape":
			m.accountForm.providerDropOpen = false
			return nil
		case "up", "k":
			m.accountForm.providerDropIdx = (m.accountForm.providerDropIdx - 1 + len(accountProviderPresets)) % len(accountProviderPresets)
			return nil
		case "down", "j", "tab":
			m.accountForm.providerDropIdx = (m.accountForm.providerDropIdx + 1) % len(accountProviderPresets)
			return nil
		case "enter", "return", "space":
			m.accountForm.providerDropOpen = false
			return m.applyAccountPreset(m.accountForm.providerDropIdx)
		}
		return nil
	}

	// 2. Handle open Model dropdown (supports live typing filter!)
	if m.accountForm.modelDropOpen {
		filtered := m.filteredAccountModels()
		count := len(filtered)
		switch stroke {
		case "esc", "escape":
			m.accountForm.modelDropOpen = false
			m.accountForm.modelFilter = ""
			return nil
		case "up":
			if count > 0 {
				m.accountForm.modelDropIdx = (m.accountForm.modelDropIdx - 1 + count) % count
			}
			return nil
		case "down", "tab":
			if count > 0 {
				m.accountForm.modelDropIdx = (m.accountForm.modelDropIdx + 1) % count
			}
			return nil
		case "enter", "return":
			if count > 0 && m.accountForm.modelDropIdx >= 0 && m.accountForm.modelDropIdx < count {
				m.accountForm.model = filtered[m.accountForm.modelDropIdx].Slug
			}
			m.accountForm.modelDropOpen = false
			m.accountForm.modelFilter = ""
			return nil
		case "ctrl+r":
			if m.accountForm.oauthWaiting {
				m.cancelAccountOp()
			}
			return m.startAccountModelDiscovery(preset.browserSignIn())
		case "ctrl+u":
			m.accountForm.modelFilter = ""
			m.accountForm.modelDropIdx = 0
			return nil
		case "backspace", "delete":
			runes := []rune(m.accountForm.modelFilter)
			if len(runes) > 0 {
				m.accountForm.modelFilter = string(runes[:len(runes)-1])
				m.accountForm.modelDropIdx = 0
			}
			return nil
		}
		add := text
		if add == "" && len([]rune(stroke)) == 1 {
			add = stroke
		}
		for _, r := range add {
			if unicode.IsPrint(r) && len([]rune(m.accountForm.modelFilter)) < 48 {
				m.accountForm.modelFilter += string(r)
				m.accountForm.modelDropIdx = 0
			}
		}
		return nil
	}

	switch stroke {
	case "esc", "escape":
		m.cancelAccountOp()
		m.accountForm = accountFormState{}
		m.status = "canceled adding account"
		return nil
	case "ctrl+s":
		return m.submitAccountForm()
	case "ctrl+r":
		if m.accountForm.oauthWaiting {
			m.cancelAccountOp()
		}
		return m.startAccountModelDiscovery(preset.browserSignIn())
	case "tab", "down":
		prevField := m.accountForm.fieldIdx
		m.accountForm.fieldIdx = m.nextAccountFormField(prevField, 1, hasBaseURL)
		if (prevField == 2 || prevField == 3) && !preset.browserSignIn() && preset.Kind != "claude" && preset.Kind != "cursor" {
			return m.startAccountModelDiscovery(false)
		}
		return nil
	case "shift+tab", "up":
		m.accountForm.fieldIdx = m.nextAccountFormField(m.accountForm.fieldIdx, -1, hasBaseURL)
		return nil
	case "left":
		if m.accountForm.fieldIdx == 0 {
			return m.applyAccountPreset(m.accountForm.presetIdx - 1)
		}
		if m.accountForm.fieldIdx == 4 && len(m.accountForm.models) > 0 {
			m.accountForm.modelDropIdx = (m.accountForm.modelDropIdx - 1 + len(m.accountForm.models)) % len(m.accountForm.models)
			m.accountForm.model = m.accountForm.models[m.accountForm.modelDropIdx].Slug
			return nil
		}
	case "right":
		if m.accountForm.fieldIdx == 0 {
			return m.applyAccountPreset(m.accountForm.presetIdx + 1)
		}
		if m.accountForm.fieldIdx == 4 && len(m.accountForm.models) > 0 {
			m.accountForm.modelDropIdx = (m.accountForm.modelDropIdx + 1) % len(m.accountForm.models)
			m.accountForm.model = m.accountForm.models[m.accountForm.modelDropIdx].Slug
			return nil
		}
	case "space":
		if m.accountForm.fieldIdx == 0 {
			m.accountForm.providerDropOpen = true
			m.accountForm.providerDropIdx = m.accountForm.presetIdx
			return nil
		}
		if m.accountForm.fieldIdx == 2 && preset.browserSignIn() && !looksLikeRawAPIKey(m.accountForm.envOrExec) {
			return m.startAccountModelDiscovery(true)
		}
		if m.accountForm.fieldIdx == 4 && len(m.accountForm.models) > 0 {
			m.accountForm.modelDropOpen = true
			m.accountForm.modelFilter = ""
			return nil
		}
	case "enter", "return":
		switch m.accountForm.fieldIdx {
		case 0:
			m.accountForm.providerDropOpen = true
			m.accountForm.providerDropIdx = m.accountForm.presetIdx
			return nil
		case 2:
			if preset.browserSignIn() && !looksLikeRawAPIKey(m.accountForm.envOrExec) {
				return m.startAccountModelDiscovery(true)
			}
			m.accountForm.fieldIdx = m.nextAccountFormField(2, 1, hasBaseURL)
			if preset.Kind != "claude" && preset.Kind != "cursor" {
				return m.startAccountModelDiscovery(false)
			}
			return nil
		case 3:
			m.accountForm.fieldIdx = m.nextAccountFormField(3, 1, hasBaseURL)
			return m.startAccountModelDiscovery(false)
		case 4:
			if len(m.accountForm.models) > 0 {
				m.accountForm.modelDropOpen = true
				m.accountForm.modelFilter = ""
				return nil
			}
			if preset.Kind != "claude" && preset.Kind != "cursor" {
				return m.startAccountModelDiscovery(preset.browserSignIn())
			}
			m.accountForm.fieldIdx = 5
			return nil
		case 5:
			return m.submitAccountForm()
		default:
			m.accountForm.fieldIdx = m.nextAccountFormField(m.accountForm.fieldIdx, 1, hasBaseURL)
			return nil
		}
	case "ctrl+u":
		m.setAccountFormFieldText("")
		return nil
	case "backspace", "delete":
		cur := m.getAccountFormFieldText()
		runes := []rune(cur)
		if len(runes) > 0 {
			m.setAccountFormFieldText(string(runes[:len(runes)-1]))
		}
		return nil
	}

	if m.accountForm.fieldIdx == 1 || m.accountForm.fieldIdx == 2 || m.accountForm.fieldIdx == 3 || m.accountForm.fieldIdx == 4 {
		if m.accountForm.fieldIdx == 2 && preset.Kind == "chatgpt" {
			return nil
		}
		add := text
		if add == "" && len([]rune(stroke)) == 1 {
			add = stroke
		}
		if add != "" {
			for _, r := range add {
				if unicode.IsPrint(r) {
					cur := m.getAccountFormFieldText()
					if len([]rune(cur)) < 256 {
						m.setAccountFormFieldText(cur + string(r))
					}
				}
			}
		}
	}
	return nil
}

func (m *Model) getAccountFormFieldText() string {
	switch m.accountForm.fieldIdx {
	case 1:
		return m.accountForm.name
	case 2:
		return m.accountForm.envOrExec
	case 3:
		return m.accountForm.baseURL
	case 4:
		return m.accountForm.model
	default:
		return ""
	}
}

func (m *Model) setAccountFormFieldText(val string) {
	switch m.accountForm.fieldIdx {
	case 1:
		m.accountForm.name = val
	case 2:
		m.accountForm.envOrExec = val
	case 3:
		m.accountForm.baseURL = val
	case 4:
		m.accountForm.model = val
	}
}

func (m *Model) submitAccountForm() tea.Cmd {
	if m.accountAdd == nil {
		m.accountForm.err = "adding accounts is unavailable in this session"
		m.status = m.accountForm.err
		return nil
	}
	preset := accountProviderPresets[m.accountForm.presetIdx]
	req := m.buildAccountFormRequest()
	if req.Name == "" {
		m.accountForm.err = "profile name is required"
		return nil
	}
	if req.Model == "" && preset.DefaultMod != "" {
		req.Model = preset.DefaultMod
	}
	m.cancelAccountOp()
	m.accountLoading = true
	m.accountForm.err = ""
	m.accountID++
	id := m.accountID
	ctx, cancel := context.WithCancel(context.Background())
	m.accountCancel = cancel
	addFn := m.accountAdd
	if preset.browserSignIn() && !m.accountForm.oauthConnected && !looksLikeRawAPIKey(m.accountForm.envOrExec) {
		label := "ChatGPT"
		if preset.Kind == "cursor-native" {
			label = "Cursor"
		}
		m.status = fmt.Sprintf("opening browser for %s sign-in (%s)…", label, sanitizeDisplay(req.Name))
	} else {
		m.status = fmt.Sprintf("saving & connecting %s (%s)…", sanitizeDisplay(req.Name), preset.Kind)
	}
	return func() tea.Msg {
		choices, activeChoice, events, err := addFn(ctx, req)
		return accountSwitchMsg{id: id, choices: choices, choice: activeChoice, events: events, err: err}
	}
}

func (m *Model) updateAccountSwitch(msg accountSwitchMsg) tea.Cmd {
	if msg.id != m.accountID {
		return nil
	}
	m.accountLoading = false
	m.accountCancel = nil
	if msg.err != nil {
		errText := sanitizeDisplay(msg.err.Error())
		if m.accountForm.open {
			m.accountForm.err = errText
		}
		if len(msg.choices) > 0 {
			m.accounts = msg.choices
		}
		m.status = "account setup/switch failed: " + errText
		return nil
	}
	m.accountForm = accountFormState{}
	if len(msg.choices) > 0 {
		m.accounts = msg.choices
	}
	for i := range m.accounts {
		m.accounts[i].Active = m.accounts[i].ID == msg.choice.ID
	}
	m.accountLabel = sanitizeDisplay(msg.choice.Label)
	if m.accountLabel == "" {
		m.accountLabel = sanitizeDisplay(msg.choice.ID)
	}
	info := m.connection
	if msg.choice.Provider != "" {
		info.Provider = msg.choice.Provider
	}
	if msg.choice.Model != "" {
		info.Model = msg.choice.Model
	}
	if msg.choice.Location != "" {
		info.Location = msg.choice.Location
	}
	info.Account, info.Connected = m.accountLabel, true
	m.connection = sanitizeConnectionInfo(info)
	m.chatLines = nil
	m.chatFoldSelected = -1
	for _, event := range msg.events {
		event.Text = sanitizeDisplay(event.Text)
		if strings.TrimSpace(event.Text) != "" {
			m.appendChatEvent(event)
		}
	}
	m.chatScroll = m.chatScrollMax()
	m.accountsOpen = false
	m.normalizeFocus()
	m.saveActiveChatTab()
	m.applyCatalogContextWindow()
	m.status = fmt.Sprintf("connected to %s (%s)", providerChoiceLabel(info.Provider, m.accountLabel, msg.choice.ID), info.Model)
	return m.refreshActiveCatalog()
}

func (m *Model) updateAccountRemove(msg accountRemoveMsg) tea.Cmd {
	if msg.id != m.accountID {
		return nil
	}
	m.accountLoading = false
	m.accountCancel = nil
	if msg.err != nil {
		m.status = "remove account failed: " + sanitizeDisplay(msg.err.Error())
		return nil
	}
	m.accounts = msg.choices
	if m.accountSelected >= len(m.accounts) {
		m.accountSelected = maxInt(len(m.accounts)-1, 0)
	}
	m.status = "removed account profile " + sanitizeDisplay(msg.removed)
	return nil
}

func (m *Model) renderAccounts() string {
	p := m.colors()
	var b strings.Builder

	if m.accountForm.open {
		preset := accountProviderPresets[m.accountForm.presetIdx]
		hasBaseURL := preset.Kind == "compatible"

		b.WriteString(p.accentText("ADD / CONFIGURE ACCOUNT & PROVIDER PROFILE") + "\n")
		b.WriteString(p.mutedText("Enter opens dropdowns · Tab/↑/↓ move · Ctrl+V/Right-Click paste · Ctrl+R refresh API · Ctrl+S Save") + "\n\n")

		if m.accountForm.err != "" {
			b.WriteString(p.dangerText("Error: "+m.accountForm.err) + "\n\n")
		} else if m.accountForm.notice != "" {
			b.WriteString(p.okText(m.accountForm.notice) + "\n\n")
		}

		renderField := func(idx int, label, value, hint string, isDropdown bool) {
			active := m.accountForm.fieldIdx == idx
			cursor := "  "
			valDisplay := sanitizeDisplay(value)
			if active {
				cursor = "› "
				if !isDropdown && idx != 5 && !(idx == 2 && preset.browserSignIn() && !looksLikeRawAPIKey(m.accountForm.envOrExec)) {
					valDisplay += "▏"
				}
			}
			line := fmt.Sprintf("%s%-18s %s", cursor, label+":", valDisplay)
			if active && p.enabled() {
				line = lipgloss.NewStyle().Foreground(p.ink).Background(lipgloss.Color("#2d2550")).Bold(true).Render(padRight(line, 74))
			} else if active {
				line = "▶ " + line
			} else {
				line = p.mutedText(fmt.Sprintf("  %-18s ", label+":")) + p.cyanText(valDisplay)
			}
			b.WriteString(line + "\n")
			if hint != "" {
				b.WriteString("     " + p.quietText(hint) + "\n")
			}
		}

		renderField(0, "Provider Type", fmt.Sprintf("▾ %s (%s)", preset.Title, preset.Kind), preset.AuthHint, true)
		if m.accountForm.providerDropOpen {
			for i, opt := range accountProviderPresets {
				sel := i == m.accountForm.providerDropIdx
				prefix := "      "
				if sel {
					prefix = "    › "
				}
				row := fmt.Sprintf("%s%-42s (%s)", prefix, opt.Title, opt.Kind)
				if sel && p.enabled() {
					row = lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(padRight(row, 68))
				} else if sel {
					row = "▶ " + row
				} else {
					row = p.cyanText(row)
				}
				b.WriteString(row + "\n")
			}
			b.WriteByte('\n')
		}

		renderField(1, "Profile Name", m.accountForm.name, "Unique profile alias (e.g. work, personal, claude-main)", false)

		if preset.Kind == "claude" || preset.Kind == "cursor" {
			renderField(2, "CLI Executable", m.accountForm.envOrExec, "Command binary on PATH (default: "+preset.DefaultExec+")", false)
		} else if preset.browserSignIn() {
			label := "ChatGPT"
			if preset.Kind == "cursor-native" {
				label = "Cursor"
			}
			authBadge := "[ Authenticate in Browser & Load Models (Enter) ]"
			if looksLikeRawAPIKey(m.accountForm.envOrExec) {
				authBadge = "API key pasted — Enter loads models"
			}
			if m.accountForm.oauthConnected {
				authBadge = "✓ Authenticated via browser (Enter to re-authenticate)"
			}
			hint := "Opens your browser for " + label + " sign-in and loads models from the API"
			if preset.Kind == "chatgpt" {
				hint = "Opens your browser. If it asks to access your local network, choose Allow."
			}
			if preset.Kind == "cursor-native" {
				hint = "Enter opens Cursor in the browser, or paste a Cursor API key"
			}
			renderField(2, "OAuth Sign-In", authBadge, hint, !looksLikeRawAPIKey(m.accountForm.envOrExec))
		} else {
			disp := m.accountForm.envOrExec
			if disp == "" && m.accountForm.fieldIdx != 2 {
				disp = "(paste API key or leave blank to use $" + preset.DefaultEnv + ")"
			} else if looksLikeRawAPIKey(disp) && len(disp) > 8 {
				disp = disp[:5] + "••••••••" + disp[len(disp)-3:] + " (will set " + preset.DefaultEnv + ")"
			}
			renderField(2, "API Key / Env", disp, "Paste API key (Ctrl+V / Right-Click) or env var name; press Enter to load API models", false)
		}

		if hasBaseURL {
			renderField(3, "Base URL", m.accountForm.baseURL, "OpenAI-compatible API base URL (e.g. http://127.0.0.1:11434/v1)", false)
		}

		var modelLabel string
		switch {
		case m.accountForm.modelsLoading && m.accountForm.oauthWaiting:
			modelLabel = "waiting for browser sign-in…"
		case m.accountForm.modelsLoading:
			modelLabel = "⠋ querying models from API…"
		case len(m.accountForm.models) > 0:
			curMod := m.accountForm.model
			if curMod == "" {
				curMod = m.accountForm.models[0].Slug
			}
			modelLabel = fmt.Sprintf("▾ %s  (%d from API)", curMod, len(m.accountForm.models))
		case m.accountForm.model != "":
			modelLabel = m.accountForm.model
		case preset.browserSignIn():
			modelLabel = "(authenticate via OAuth first to load models from API)"
		default:
			modelLabel = "(enter API key above & press Enter/Ctrl+R to load models from API)"
		}
		renderField(4, "Model", modelLabel, "Press Enter/Space to open API model dropdown (type to filter), ←/→ cycle, or type slug", len(m.accountForm.models) > 0)

		if m.accountForm.modelDropOpen && len(m.accountForm.models) > 0 {
			filtered := m.filteredAccountModels()
			filterLine := fmt.Sprintf("    🔍 Search (%d/%d): %s▏", len(filtered), len(m.accountForm.models), m.accountForm.modelFilter)
			if m.accountForm.modelFilter == "" {
				filterLine = fmt.Sprintf("    🔍 Search (%d API models — type to filter): ▏", len(m.accountForm.models))
			}
			b.WriteString(p.inkText(filterLine) + "\n")
			maxVisible := 8
			start := 0
			if m.accountForm.modelDropIdx >= maxVisible {
				start = m.accountForm.modelDropIdx - maxVisible + 1
			}
			end := minInt(start+maxVisible, len(filtered))
			for i := start; i < end; i++ {
				opt := filtered[i]
				sel := i == m.accountForm.modelDropIdx
				prefix := "      "
				if sel {
					prefix = "    › "
				}
				label := opt.Slug
				if opt.DisplayName != "" && opt.DisplayName != opt.Slug {
					label = fmt.Sprintf("%-34s · %s", opt.Slug, opt.DisplayName)
				}
				if opt.ContextLength > 0 {
					label += fmt.Sprintf(" (%s ctx)", formatTokenCountShort(opt.ContextLength))
				}
				row := prefix + label
				if sel && p.enabled() {
					row = lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(padRight(row, 68))
				} else if sel {
					row = "▶ " + row
				} else {
					row = p.cyanText(row)
				}
				b.WriteString(row + "\n")
			}
			if len(filtered) == 0 {
				b.WriteString(p.warnText("      (no API models match filter)") + "\n")
			}
			b.WriteByte('\n')
		}

		b.WriteString("\n")
		btnText := "  [ Save & Connect Account ]  "
		if m.accountForm.fieldIdx == 5 {
			if p.enabled() {
				btnText = lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(" › Save & Connect Account ‹ ")
			} else {
				btnText = "▶ [ Save & Connect Account ]"
			}
		} else {
			btnText = p.cyanText(btnText)
		}
		b.WriteString(btnText + "    " + p.quietText("Esc cancel") + "\n")
		if m.accountLoading {
			if preset.browserSignIn() && !m.accountForm.oauthConnected {
				label := "ChatGPT"
				if preset.Kind == "cursor-native" {
					label = "Cursor"
				}
				b.WriteString("\n" + p.accentText("Waiting for "+label+" sign-in in your browser… (press Esc to cancel)"))
			} else {
				b.WriteString("\n" + p.accentText("Saving and connecting account… (press Esc to cancel)"))
			}
		}
		return b.String()
	}

	b.WriteString(p.accentText("ACCOUNTS & PROVIDER PROFILES") + "\n")
	b.WriteString(p.mutedText("Switch the active model/account for this conversation, press 'm' to change model, or 'a' to add.") + "\n\n")

	if len(m.accounts) == 0 {
		b.WriteString(p.warnText("No accounts configured yet.") + "\n\n")
		b.WriteString("Press " + p.cyanText("a") + " (or " + p.cyanText("Enter") + ") to configure Claude, Cursor, Anthropic, OpenAI, ChatGPT OAuth, Gemini, or OpenRouter.\n")
		b.WriteString("\n" + p.quietText("a / Enter add account   ·   Esc close"))
		return b.String()
	}

	for i, choice := range m.accounts {
		marker := "  "
		if i == m.accountSelected {
			marker = "› "
		}
		active := ""
		if choice.Active {
			active = "  • active"
		}
		label := sanitizeDisplay(choice.Label)
		if choice.Provider != "" {
			label = m.providerIcon(choice.Provider) + " " + providerDisplayName(choice.Provider) + " · " + label
		}
		line := marker + label + active
		if choice.Detail != "" {
			line += "\n     " + sanitizeDisplay(choice.Detail)
		}
		b.WriteString(line)
		if i < len(m.accounts)-1 {
			b.WriteString("\n")
		}
	}
	if m.accountLoading {
		b.WriteString("\n\nConnecting…")
	} else {
		b.WriteString("\n\nEnter connect   ·   m change model   ·   a add account   ·   d remove   ·   Esc close")
	}
	return b.String()
}

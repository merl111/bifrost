package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// chatTabEntry stores the complete per-tab state so multiple chats can be open
// in parallel, including while one tab is streaming in the background.
type chatTabEntry struct {
	SessionID   string
	Title       string
	CustomTitle bool
	Lines       []chatLine
	Input       string
	History     []string
	Scroll      int
	Running     bool
	StreamID    uint64
	Stream      *chatStream
	Ctx         context.Context
	Cancel      context.CancelFunc
	FoldSelect  int
	Connection  ConnectionInfo
}

type chatModelPickerState struct {
	open     bool
	loading  bool
	filter   string
	selected int
	items    []chatModelPickerItem
}

type chatModelPickerItem struct {
	AccountID     string
	Account       string
	Provider      string
	Model         string
	DisplayName   string
	ContextLength int
	Active        bool
}

type catalogModelInfo struct {
	ContextLength int
	DisplayName   string
}

type chatCompactMsg struct {
	id         uint64
	summary    SessionSummary
	events     []ChatEvent
	compacted  bool
	tokensUsed int
	tokensMax  int
	err        error
}

func (m *Model) ensureChatTabs() {
	if len(m.chatTabs) > 0 {
		if m.chatTab < 0 || m.chatTab >= len(m.chatTabs) {
			m.chatTab = 0
		}
		return
	}
	title := m.deriveChatTabTitle(m.chatLines, "")
	m.chatTabs = []chatTabEntry{{
		Title:      title,
		Lines:      append([]chatLine(nil), m.chatLines...),
		Input:      m.chatInput,
		History:    append([]string(nil), m.chatHistory...),
		Scroll:     m.chatScroll,
		Running:    m.chatRunning,
		StreamID:   m.chatID,
		Stream:     m.chatStream,
		Ctx:        m.chatCtx,
		Cancel:     m.chatCancel,
		FoldSelect: m.chatFoldSelected,
		Connection: m.connection,
	}}
	m.chatTab = 0
}

func (m *Model) deriveChatTabTitle(lines []chatLine, fallback string) string {
	for _, line := range lines {
		text := strings.TrimSpace(line.text)
		if line.kind == "user" && text != "" {
			return shortenTabTitle(text)
		}
		if line.kind == "status" && strings.HasPrefix(text, "you: ") {
			return shortenTabTitle(strings.TrimPrefix(text, "you: "))
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return shortenTabTitle(fallback)
	}
	return "Main Chat"
}

func shortenTabTitle(s string) string {
	s = strings.Split(strings.TrimSpace(s), "\n")[0]
	runes := []rune(sanitizeDisplay(s))
	if len(runes) > 18 {
		return string(runes[:17]) + "…"
	}
	if len(runes) == 0 {
		return "Chat"
	}
	return string(runes)
}

func (m *Model) saveActiveChatTab() {
	m.ensureChatTabs()
	tab := &m.chatTabs[m.chatTab]
	tab.Lines = append([]chatLine(nil), m.chatLines...)
	tab.Input = m.chatInput
	tab.History = append([]string(nil), m.chatHistory...)
	tab.Scroll = m.chatScroll
	tab.Running = m.chatRunning
	tab.StreamID = m.chatID
	tab.Stream = m.chatStream
	tab.Ctx = m.chatCtx
	tab.Cancel = m.chatCancel
	tab.FoldSelect = m.chatFoldSelected
	tab.Connection = m.connection
	if !tab.CustomTitle {
		tab.Title = m.deriveChatTabTitle(m.chatLines, tab.Title)
	}
}

func (m *Model) loadActiveChatTab() {
	m.ensureChatTabs()
	tab := m.chatTabs[m.chatTab]
	m.chatLines = append([]chatLine(nil), tab.Lines...)
	m.chatInput = tab.Input
	m.chatHistory = append([]string(nil), tab.History...)
	m.chatScroll = tab.Scroll
	m.chatRunning = tab.Running
	m.chatID = tab.StreamID
	m.chatStream = tab.Stream
	m.chatCtx = tab.Ctx
	m.chatCancel = tab.Cancel
	m.chatFoldSelected = tab.FoldSelect
	if tab.Connection.Provider != "" || tab.Connection.Model != "" {
		m.connection = tab.Connection
		if tab.Connection.Account != "" {
			m.accountLabel = tab.Connection.Account
		}
	}
	m.clampChatScroll()
}

func (m *Model) openNewChatTab(customName string) tea.Cmd {
	m.saveActiveChatTab()
	name := strings.TrimSpace(customName)
	custom := name != ""
	if name == "" {
		name = fmt.Sprintf("Chat %d", len(m.chatTabs)+1)
	}
	newTab := chatTabEntry{
		Title:       shortenTabTitle(name),
		CustomTitle: custom,
		FoldSelect:  -1,
		Connection:  m.connection,
	}
	m.chatTabs = append(m.chatTabs, newTab)
	m.chatTab = len(m.chatTabs) - 1
	m.loadActiveChatTab()
	m.focus = focusChat
	if m.sessionCallbacks.New != nil && !m.anyChatTabRunning() {
		newFn := m.sessionCallbacks.New
		m.sessionsID++
		reqID := m.sessionsID
		m.status = fmt.Sprintf("opened new chat tab %q", newTab.Title)
		return func() tea.Msg {
			summary, events, err := newFn(context.Background())
			return sessionSwitchedMsg{id: reqID, summary: summary, events: events, isNew: true, err: err}
		}
	}
	m.status = fmt.Sprintf("opened parallel chat tab %d: %s", m.chatTab+1, newTab.Title)
	return nil
}

func (m *Model) anyChatTabRunning() bool {
	if m.chatRunning {
		return true
	}
	for _, t := range m.chatTabs {
		if t.Running {
			return true
		}
	}
	return false
}

func (m *Model) switchChatTab(delta int) tea.Cmd {
	m.saveActiveChatTab()
	if len(m.chatTabs) <= 1 {
		m.status = "only 1 chat tab open — press N (or :chat new) to open a parallel chat"
		return nil
	}
	m.chatTab = (m.chatTab + delta + len(m.chatTabs)) % len(m.chatTabs)
	m.loadActiveChatTab()
	target := m.chatTabs[m.chatTab]
	m.status = fmt.Sprintf("chat tab %d/%d: %s", m.chatTab+1, len(m.chatTabs), target.Title)
	if target.SessionID != "" && m.sessionCallbacks.Activate != nil && !m.anyChatTabRunning() {
		actFn := m.sessionCallbacks.Activate
		sid := target.SessionID
		return func() tea.Msg {
			_ = actFn(context.Background(), sid)
			return nil
		}
	}
	return nil
}

func (m *Model) selectChatTabIndex(idx int) tea.Cmd {
	m.saveActiveChatTab()
	if idx < 0 || idx >= len(m.chatTabs) || idx == m.chatTab {
		return nil
	}
	m.chatTab = idx
	m.loadActiveChatTab()
	target := m.chatTabs[m.chatTab]
	m.status = fmt.Sprintf("chat tab %d/%d: %s", m.chatTab+1, len(m.chatTabs), target.Title)
	if target.SessionID != "" && m.sessionCallbacks.Activate != nil && !m.anyChatTabRunning() {
		actFn := m.sessionCallbacks.Activate
		sid := target.SessionID
		return func() tea.Msg {
			_ = actFn(context.Background(), sid)
			return nil
		}
	}
	return nil
}

func (m *Model) closeActiveChatTab() tea.Cmd {
	m.ensureChatTabs()
	if len(m.chatTabs) <= 1 {
		if m.chatRunning {
			m.cancelChat()
		}
		m.chatLines = nil
		m.chatScroll = 0
		m.chatFoldSelected = -1
		m.chatTabs[0].Lines = nil
		m.chatTabs[0].Title = "Main Chat"
		m.chatTabs[0].CustomTitle = false
		m.status = "cleared chat tab"
		return nil
	}
	if m.chatRunning {
		m.cancelChat()
	}
	closedTitle := m.chatTabs[m.chatTab].Title
	m.chatTabs = append(m.chatTabs[:m.chatTab], m.chatTabs[m.chatTab+1:]...)
	if m.chatTab >= len(m.chatTabs) {
		m.chatTab = len(m.chatTabs) - 1
	}
	m.loadActiveChatTab()
	m.status = fmt.Sprintf("closed chat tab %q", closedTitle)
	return nil
}

func (m *Model) renameActiveChatTab(name string) {
	m.ensureChatTabs()
	name = strings.TrimSpace(sanitizeDisplay(name))
	if name == "" {
		m.status = "chat tab name cannot be empty"
		return
	}
	m.chatTabs[m.chatTab].Title = shortenTabTitle(name)
	m.chatTabs[m.chatTab].CustomTitle = true
	m.status = fmt.Sprintf("renamed chat tab to %q", m.chatTabs[m.chatTab].Title)
}

func (m *Model) startChatTabRename() {
	m.ensureChatTabs()
	m.chatTabRenaming = true
	m.chatTabRenameInput = m.chatTabs[m.chatTab].Title
	m.status = "rename chat tab: type name and press Enter (Esc to cancel)"
}

func (m *Model) handleChatTabRenameKey(stroke, text string) tea.Cmd {
	switch stroke {
	case "esc", "escape":
		m.chatTabRenaming = false
		m.chatTabRenameInput = ""
		m.status = "canceled rename"
		return nil
	case "enter", "return":
		name := strings.TrimSpace(m.chatTabRenameInput)
		m.chatTabRenaming = false
		m.chatTabRenameInput = ""
		if name != "" {
			m.renameActiveChatTab(name)
		}
		return nil
	case "ctrl+u":
		m.chatTabRenameInput = ""
		return nil
	case "backspace", "delete":
		runes := []rune(m.chatTabRenameInput)
		if len(runes) > 0 {
			m.chatTabRenameInput = string(runes[:len(runes)-1])
		}
		return nil
	}
	add := text
	if add == "" && len([]rune(stroke)) == 1 {
		add = stroke
	}
	for _, r := range add {
		if unicode.IsPrint(r) && len([]rune(m.chatTabRenameInput)) < 32 {
			m.chatTabRenameInput += string(r)
		}
	}
	return nil
}

func (m *Model) openChatModelPicker() tea.Cmd {
	var items []chatModelPickerItem
	activeAccount := m.accountLabel
	activeModel := m.connection.Model
	var activeChoice AccountChoice

	for _, acc := range m.accounts {
		isCurrent := (acc.Active || acc.Label == activeAccount || acc.ID == activeAccount)
		if isCurrent {
			activeChoice = acc
		}
		if acc.Model != "" {
			items = append(items, chatModelPickerItem{
				AccountID:     acc.ID,
				Account:       acc.Label,
				Provider:      acc.Provider,
				Model:         acc.Model,
				DisplayName:   m.catalogDisplay(acc.Model),
				ContextLength: m.catalogContext(acc.Model),
				Active:        isCurrent && acc.Model == activeModel,
			})
		}
	}

	sel := 0
	for i, it := range items {
		if it.Active {
			sel = i
			break
		}
	}
	m.chatModelPicker = chatModelPickerState{
		open:     true,
		selected: sel,
		items:    items,
	}
	m.status = "quick model/account switcher: j/k select · type to filter · Enter switch · a add account · Esc close"

	if m.accountDiscoverModels != nil && activeChoice.ID != "" {
		m.chatModelPicker.loading = true
		m.accountID++
		id := m.accountID
		discoverFn := m.accountDiscoverModels
		req := AccountAddRequest{
			Kind:  activeChoice.Provider,
			Name:  activeChoice.ID,
			Model: activeChoice.Model,
		}
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			models, defModel, err := discoverFn(ctx, req, false)
			return accountModelsLoadedMsg{
				id:           id,
				models:       models,
				defaultModel: defModel,
				err:          err,
			}
		}
	}
	return nil
}

func (m *Model) populateChatModelPickerFromAPI(models []AccountModelOption) {
	activeAccount := m.accountLabel
	activeModel := m.connection.Model
	var activeChoice AccountChoice
	for _, acc := range m.accounts {
		if acc.Active || acc.Label == activeAccount || acc.ID == activeAccount {
			activeChoice = acc
			break
		}
	}
	if activeChoice.ID == "" && len(m.accounts) > 0 {
		activeChoice = m.accounts[0]
	}
	if activeChoice.ID == "" {
		return
	}
	seen := make(map[string]int)
	var next []chatModelPickerItem
	for _, it := range m.chatModelPicker.items {
		key := it.AccountID + "\x00" + it.Model
		seen[key] = len(next)
		next = append(next, it)
	}
	for _, mod := range models {
		key := activeChoice.ID + "\x00" + mod.Slug
		if mod.Slug == "" {
			continue
		}
		if idx, ok := seen[key]; ok {
			if mod.ContextLength > next[idx].ContextLength {
				next[idx].ContextLength = mod.ContextLength
			}
			if next[idx].DisplayName == "" {
				next[idx].DisplayName = mod.DisplayName
			}
			continue
		}
		seen[key] = len(next)
		next = append(next, chatModelPickerItem{
			AccountID:     activeChoice.ID,
			Account:       activeChoice.Label,
			Provider:      activeChoice.Provider,
			Model:         mod.Slug,
			DisplayName:   mod.DisplayName,
			ContextLength: mod.ContextLength,
			Active:        mod.Slug == activeModel,
		})
	}
	m.chatModelPicker.items = next
	m.rememberCatalogContexts(models)
	m.applyCatalogContextWindow()
	for i, it := range m.chatModelPicker.items {
		if it.Active {
			m.chatModelPicker.selected = i
			break
		}
	}
}

func (m *Model) filteredChatModelPickerItems() []chatModelPickerItem {
	q := strings.ToLower(strings.TrimSpace(m.chatModelPicker.filter))
	if q == "" {
		return m.chatModelPicker.items
	}
	words := strings.Fields(q)
	var out []chatModelPickerItem
	for _, it := range m.chatModelPicker.items {
		haystack := strings.ToLower(it.Model + " " + it.DisplayName + " " + it.Account + " " + it.Provider)
		match := true
		for _, w := range words {
			if !strings.Contains(haystack, w) {
				match = false
				break
			}
		}
		if match {
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) handleChatModelPickerKey(stroke string, text ...string) tea.Cmd {
	rawText := ""
	if len(text) > 0 {
		rawText = text[0]
	}
	filtered := m.filteredChatModelPickerItems()
	count := len(filtered)
	switch stroke {
	case "esc", "escape":
		if m.chatModelPicker.filter != "" {
			m.chatModelPicker.filter = ""
			m.chatModelPicker.selected = 0
			return nil
		}
		m.chatModelPicker.open = false
		m.chatModelPicker.filter = ""
		m.status = "closed model switcher"
		return nil
	case "/":
		return nil
	case "a":
		if m.chatModelPicker.filter == "" && len(m.chatModelPicker.items) == 0 {
			m.chatModelPicker.open = false
			m.openAccounts()
			return m.initAccountForm(0)
		}
	case "ctrl+a":
		m.chatModelPicker.open = false
		m.openAccounts()
		return m.initAccountForm(0)
	case "down", "tab":
		if count > 0 {
			m.chatModelPicker.selected = (m.chatModelPicker.selected + 1) % count
		}
		return nil
	case "up", "shift+tab":
		if count > 0 {
			m.chatModelPicker.selected = (m.chatModelPicker.selected - 1 + count) % count
		}
		return nil
	case "pgdown", "pagedown":
		if count > 0 {
			m.chatModelPicker.selected = minInt(count-1, m.chatModelPicker.selected+8)
		}
		return nil
	case "pgup", "pageup":
		if count > 0 {
			m.chatModelPicker.selected = maxInt(0, m.chatModelPicker.selected-8)
		}
		return nil
	case "ctrl+u":
		m.chatModelPicker.filter = ""
		m.chatModelPicker.selected = 0
		return nil
	case "backspace", "delete":
		runes := []rune(m.chatModelPicker.filter)
		if len(runes) > 0 {
			m.chatModelPicker.filter = string(runes[:len(runes)-1])
			m.chatModelPicker.selected = 0
		}
		return nil
	case "enter", "return":
		if count == 0 {
			m.chatModelPicker.open = false
			m.openAccounts()
			return m.initAccountForm(0)
		}
		if m.chatModelPicker.selected < 0 || m.chatModelPicker.selected >= count {
			m.chatModelPicker.selected = 0
		}
		item := filtered[m.chatModelPicker.selected]
		m.chatModelPicker.open = false
		m.chatModelPicker.filter = ""
		if m.chatRunning {
			m.status = "finish or cancel the active chat turn before switching model"
			return nil
		}
		if m.accountSelectModel != nil {
			m.accountLoading = true
			m.accountID++
			id := m.accountID
			fn := m.accountSelectModel
			m.status = fmt.Sprintf("switching to %s / %s…", providerChoiceLabel(item.Provider, item.Account, item.AccountID), item.Model)
			return func() tea.Msg {
				choices, choice, events, err := fn(context.Background(), item.AccountID, item.Model)
				return accountSwitchMsg{id: id, choices: choices, choice: choice, events: events, err: err}
			}
		}
		if m.accountSelect != nil {
			m.accountLoading = true
			m.accountID++
			id := m.accountID
			fn := m.accountSelect
			choice := AccountChoice{ID: item.AccountID, Label: item.Account, Provider: item.Provider, Model: item.Model, Active: true}
			return func() tea.Msg {
				events, err := fn(context.Background(), item.AccountID)
				return accountSwitchMsg{id: id, choice: choice, events: events, err: err}
			}
		}
		m.connection.Provider = item.Provider
		m.connection.Account = item.Account
		m.connection.Model = item.Model
		if item.ContextLength > 0 {
			m.connection.ContextMaxTokens = item.ContextLength
		}
		m.accountLabel = item.Account
		m.applyCatalogContextWindow()
		m.status = fmt.Sprintf("selected %s / %s", providerChoiceLabel(item.Provider, item.Account, item.AccountID), item.Model)
		return nil
	}
	add := rawText
	if add == "" && len([]rune(stroke)) == 1 {
		add = stroke
	}
	if add == "" && stroke == "space" {
		add = " "
	}
	for _, r := range add {
		if unicode.IsPrint(r) && len([]rune(m.chatModelPicker.filter)) < 48 {
			m.chatModelPicker.filter += string(r)
			m.chatModelPicker.selected = 0
		}
	}
	return nil
}

// renderChatTopBar renders the parallel chat tab bar and the quick account/model/context pill.
func (m *Model) renderChatTopBar(width int) []string {
	m.ensureChatTabs()
	p := m.colors()

	// 1. Build right-side quick Account/Model & Context Saturation pill
	prov := valueOr(m.connection.Provider, "offline")
	mod := valueOr(m.connection.Model, "—")
	acct := valueOr(m.connection.Account, m.accountLabel)
	modelShort := ansi.Truncate(mod, 36, "…")
	pillText := fmt.Sprintf(" %s %s · %s ▾ (m) ", m.providerIcon(prov), providerChoiceLabel(prov, acct, ""), modelShort)
	ctxBadge := m.formatContextSaturationBadge()

	// 2. Build left-side chat tabs
	var tabParts []string
	for i, tab := range m.chatTabs {
		active := i == m.chatTab
		title := tab.Title
		if active && m.chatTabRenaming {
			title = m.chatTabRenameInput + "▏"
		}
		busy := ""
		if (active && m.chatRunning) || (!active && tab.Running) {
			busy = " ●"
		}
		label := fmt.Sprintf(" %d:%s%s ", i+1, title, busy)
		if !p.enabled() {
			if active {
				tabParts = append(tabParts, "["+strings.TrimSpace(label)+"*]")
			} else {
				tabParts = append(tabParts, " "+strings.TrimSpace(label)+" ")
			}
			continue
		}
		if active {
			tabParts = append(tabParts, lipgloss.NewStyle().
				Foreground(lipgloss.Color("#090b12")).
				Background(p.cyan).
				Bold(true).
				Render(label))
		} else {
			tabParts = append(tabParts, lipgloss.NewStyle().
				Foreground(p.muted).
				Background(lipgloss.Color("#1f2437")).
				Render(label))
		}
	}
	newHint := p.quietText("+N")
	tabsJoined := strings.Join(tabParts, " ") + " " + newHint

	rightJoined := ""
	if p.enabled() {
		pillStyled := lipgloss.NewStyle().
			Foreground(lipgloss.Color("#e9e2ff")).
			Background(lipgloss.Color("#2d2550")).
			Bold(true).
			Render(pillText)
		if ctxBadge != "" {
			rightJoined = ctxBadge + " " + pillStyled
		} else {
			rightJoined = pillStyled
		}
	} else {
		if ctxBadge != "" {
			rightJoined = ctxBadge + " [" + strings.TrimSpace(pillText) + "]"
		} else {
			rightJoined = "[" + strings.TrimSpace(pillText) + "]"
		}
	}

	leftW := ansi.StringWidth(tabsJoined)
	rightW := ansi.StringWidth(rightJoined)
	var bar string
	if leftW+rightW+2 <= width {
		gap := strings.Repeat(" ", maxInt(width-leftW-rightW, 1))
		bar = tabsJoined + gap + rightJoined
	} else {
		bar = ansi.Truncate(tabsJoined+"  "+rightJoined, width, "…")
	}

	divider := p.quietText(strings.Repeat("─", maxInt(width, 1)))
	out := []string{bar, divider}

	// Render inline quick model/account picker dropdown if open
	if m.chatModelPicker.open {
		out = append(out, m.renderChatModelPickerOverlay(width)...)
	}
	return out
}

func (m *Model) renderChatModelPickerOverlay(width int) []string {
	p := m.colors()
	boxW := minInt(maxInt(width-4, 32), 84)
	margin := strings.Repeat(" ", maxInt((width-boxW)/2, 0))
	var rows []string
	title := " Switch Account / Model (↑/↓ · Enter · Ctrl+A add · Esc) "
	top := "╭" + ansi.Truncate(title, boxW-2, "") + strings.Repeat("─", maxInt(boxW-2-ansi.StringWidth(title), 0)) + "╮"
	rows = append(rows, margin+p.cyanText(top))

	filtered := m.filteredChatModelPickerItems()
	searchPrompt := "🔍 Search models: " + m.chatModelPicker.filter + "▏"
	if m.chatModelPicker.filter == "" {
		searchPrompt = "🔍 Search models (type to filter): ▏"
	}
	if len(m.chatModelPicker.items) > 1 {
		countTag := fmt.Sprintf(" (%d/%d)", len(filtered), len(m.chatModelPicker.items))
		maxSearchW := maxInt(boxW-4-len(countTag), 12)
		sLine := padRight(ansi.Truncate(searchPrompt, maxSearchW, "…"), maxSearchW) + countTag
		rows = append(rows, margin+p.cyanText("│ ")+p.inkText(padRight(sLine, boxW-4))+p.cyanText(" │"))
	} else {
		rows = append(rows, margin+p.cyanText("│ ")+p.inkText(padRight(ansi.Truncate(searchPrompt, boxW-4, "…"), boxW-4))+p.cyanText(" │"))
	}
	divLine := "├" + strings.Repeat("─", maxInt(boxW-2, 0)) + "┤"
	rows = append(rows, margin+p.quietText(divLine))

	if m.chatModelPicker.loading {
		lRow := "│ " + padRight("⠋ querying available models…", boxW-4) + " │"
		rows = append(rows, margin+p.quietText(lRow))
	}

	if len(filtered) == 0 {
		msg := "No accounts configured — press 'a' or Ctrl+A to add one"
		if len(m.chatModelPicker.items) > 0 {
			msg = "No models match filter — press Backspace or Ctrl+U"
		}
		row := "│ " + padRight(ansi.Truncate(msg, boxW-4, "…"), boxW-4) + " │"
		rows = append(rows, margin+p.warnText(row))
	} else {
		maxVis := 8
		start := 0
		if m.chatModelPicker.selected >= maxVis {
			start = m.chatModelPicker.selected - maxVis + 1
		}
		end := minInt(start+maxVis, len(filtered))
		for i := start; i < end; i++ {
			it := filtered[i]
			sel := i == m.chatModelPicker.selected
			prefix := "  "
			if sel {
				prefix = "› "
			}
			activeTag := ""
			if it.Active {
				activeTag = " • active"
			}
			contentW := boxW - 4
			label := providerChoiceLabel(it.Provider, it.Account, it.AccountID)
			modelText := it.Model
			if modelText == "" {
				modelText = "—"
			}
			left := fmt.Sprintf("%s%s %s · %s%s", prefix, m.providerIcon(it.Provider), label, modelText, activeTag)
			ctx := ""
			if it.ContextLength > 0 {
				ctx = formatTokenCountShort(it.ContextLength)
			}
			var line string
			if ctx != "" && contentW > ansi.StringWidth(ctx)+16 {
				room := contentW - ansi.StringWidth(ctx) - 2
				line = padRight(left, room) + "  " + ctx
			} else {
				line = left
			}
			line = ansi.Truncate(line, contentW, "…")
			padded := padRight(line, boxW-4)
			if sel && p.enabled() {
				styled := lipgloss.NewStyle().Foreground(lipgloss.Color("#090b12")).Background(p.cyan).Bold(true).Render(padded)
				rows = append(rows, margin+p.cyanText("│ ")+styled+p.cyanText(" │"))
			} else {
				rows = append(rows, margin+p.cyanText("│ ")+p.mutedText(padded)+p.cyanText(" │"))
			}
		}
	}
	bot := "╰" + strings.Repeat("─", maxInt(boxW-2, 0)) + "╯"
	rows = append(rows, margin+p.cyanText(bot))
	return rows
}

func (m *Model) formatContextSaturationBadge() string {
	p := m.colors()
	used, maxTok, pct := m.effectiveContextSaturation()
	if used <= 0 && maxTok <= 0 {
		return ""
	}
	label := fmt.Sprintf(" 🧠 %s ", formatTokenCountShort(used))
	if maxTok > 0 {
		label = fmt.Sprintf(" 🧠 %s/%s (%d%%) ", formatTokenCountShort(used), formatTokenCountShort(maxTok), pct)
	}
	if !p.enabled() {
		return strings.TrimSpace(label)
	}
	bg := "#1e3a2f"
	fg := "#c6f6d5"
	if pct >= 80 {
		bg = "#4a1c24"
		fg = "#fed7d7"
	} else if pct >= 55 {
		bg = "#4a3719"
		fg = "#fefcbf"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(fg)).Background(lipgloss.Color(bg)).Render(label)
}

func (m *Model) effectiveContextSaturation() (used int, maxTok int, pct int) {
	if !m.connection.Connected || strings.TrimSpace(m.connection.Model) == "" || strings.TrimSpace(m.connection.Model) == "—" {
		return 0, 0, 0
	}
	maxTok = m.catalogContext(m.connection.Model)
	if maxTok <= 0 {
		maxTok = m.connection.ContextMaxTokens
	}
	used = m.connection.ContextTokens
	if used <= 0 {
		used = m.estimateTranscriptTokens()
	}
	if maxTok <= 0 {
		return used, 0, 0
	}
	pct = (used * 100) / maxTok
	if pct > 999 {
		pct = 999
	}
	if pct < 0 {
		pct = 0
	}
	return used, maxTok, pct
}

func (m *Model) rememberCatalogContexts(models []AccountModelOption) {
	if m == nil || len(models) == 0 {
		return
	}
	if m.catalogModels == nil {
		m.catalogModels = map[string]catalogModelInfo{}
	}
	for _, mod := range models {
		slug := strings.TrimSpace(mod.Slug)
		if slug == "" || (mod.ContextLength <= 0 && strings.TrimSpace(mod.DisplayName) == "") {
			continue
		}
		info := m.catalogModels[slug]
		if mod.ContextLength > info.ContextLength {
			info.ContextLength = mod.ContextLength
		}
		if strings.TrimSpace(mod.DisplayName) != "" {
			info.DisplayName = strings.TrimSpace(mod.DisplayName)
		}
		m.catalogModels[slug] = info
	}
}

func (m *Model) catalogContext(model string) int {
	if m == nil || m.catalogModels == nil {
		return 0
	}
	return m.catalogModels[strings.TrimSpace(model)].ContextLength
}

func (m *Model) catalogDisplay(model string) string {
	if m == nil || m.catalogModels == nil {
		return ""
	}
	return m.catalogModels[strings.TrimSpace(model)].DisplayName
}

func (m *Model) applyCatalogContextWindow() {
	if n := m.catalogContext(m.connection.Model); n > 0 {
		m.connection.ContextMaxTokens = n
	}
}

// refreshActiveCatalog loads the active account's model list so the context
// badge can use the provider's advertised window instead of a name heuristic.
func (m *Model) refreshActiveCatalog() tea.Cmd {
	if m == nil || m.accountDiscoverModels == nil {
		return nil
	}
	var active AccountChoice
	for _, acc := range m.accounts {
		if acc.Active || acc.Label == m.accountLabel || acc.ID == m.accountLabel {
			active = acc
			break
		}
	}
	if active.ID == "" || active.Provider == "" {
		return nil
	}
	m.accountID++
	id := m.accountID
	discoverFn := m.accountDiscoverModels
	req := AccountAddRequest{Kind: active.Provider, Name: active.ID, Model: active.Model}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		models, defModel, err := discoverFn(ctx, req, false)
		return accountModelsLoadedMsg{id: id, models: models, defaultModel: defModel, err: err}
	}
}

func (m *Model) estimateTranscriptTokens() int {
	chars := 0
	for _, line := range m.chatLines {
		chars += len(line.text) + len(line.detail) + 12
	}
	chars += len(m.chatInput)
	return chars / 4
}

func formatTokenCountShort(n int) string {
	if n >= 1_000_000 {
		if n%1_000_000 == 0 {
			return fmt.Sprintf("%dM", n/1_000_000)
		}
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000.0)
	}
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000.0)
	}
	return fmt.Sprintf("%d", n)
}

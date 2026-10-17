package ui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// SessionSummary describes a saved conversation in the workspace/project.
type SessionSummary struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	MessageCount int    `json:"message_count"`
	Revision     uint64 `json:"revision"`
	UpdatedAt    string `json:"updated_at"`
	Active       bool   `json:"active"`
}

// SessionCallbacks wire historic conversation browsing, resumption, and new-session creation.
type SessionCallbacks struct {
	List     func(ctx context.Context) ([]SessionSummary, error)
	Resume   func(ctx context.Context, id string) (SessionSummary, []ChatEvent, error)
	New      func(ctx context.Context) (SessionSummary, []ChatEvent, error)
	Activate func(ctx context.Context, id string) error
	Compact  func(ctx context.Context) (SessionSummary, []ChatEvent, int, int, error)
}

// WithSessions configures conversation history callbacks on the UI model.
func WithSessions(callbacks SessionCallbacks) Option {
	return func(m *Model) {
		m.sessionCallbacks = callbacks
	}
}

type sessionsLoadedMsg struct {
	id       uint64
	sessions []SessionSummary
	err      error
}

type sessionSwitchedMsg struct {
	id      uint64
	summary SessionSummary
	events  []ChatEvent
	isNew   bool
	err     error
}

func (m *Model) startSessionsView() tea.Cmd {
	m.sessionsOpen = true
	m.sessionsError = ""
	if m.sessionCallbacks.List == nil {
		m.sessionsLoading = false
		m.status = "conversation history unavailable (connect a provider first)"
		return nil
	}
	m.sessionsLoading = true
	m.sessionsID++
	reqID := m.sessionsID
	listFn := m.sessionCallbacks.List
	m.status = "loading historic conversations…"
	return func() tea.Msg {
		items, err := listFn(context.Background())
		return sessionsLoadedMsg{id: reqID, sessions: items, err: err}
	}
}

func (m *Model) startResumeSession(sessionID string) tea.Cmd {
	if m.chatRunning {
		m.status = "finish or cancel the active chat turn before switching conversations"
		return nil
	}
	if m.sessionCallbacks.Resume == nil {
		m.status = "conversation resume is unavailable"
		return nil
	}
	m.sessionsLoading = true
	m.sessionsError = ""
	m.sessionsID++
	reqID := m.sessionsID
	resumeFn := m.sessionCallbacks.Resume
	m.status = "resuming conversation " + sanitizeDisplay(sessionID) + "…"
	return func() tea.Msg {
		summary, events, err := resumeFn(context.Background(), sessionID)
		return sessionSwitchedMsg{id: reqID, summary: summary, events: events, err: err}
	}
}

func (m *Model) startNewSession() tea.Cmd {
	if m.chatRunning {
		m.status = "finish or cancel the active chat turn before starting a new conversation"
		return nil
	}
	if m.sessionCallbacks.New == nil {
		m.chatLines = nil
		m.chatFoldSelected = -1
		m.chatScroll = 0
		m.sessionsOpen = false
		m.normalizeFocus()
		m.status = "started new conversation"
		return nil
	}
	m.sessionsLoading = true
	m.sessionsError = ""
	m.sessionsID++
	reqID := m.sessionsID
	newFn := m.sessionCallbacks.New
	m.status = "starting new conversation…"
	return func() tea.Msg {
		summary, events, err := newFn(context.Background())
		return sessionSwitchedMsg{id: reqID, summary: summary, events: events, isNew: true, err: err}
	}
}

func (m *Model) updateSessionsLoaded(msg sessionsLoadedMsg) tea.Cmd {
	if msg.id != m.sessionsID {
		return nil
	}
	m.sessionsLoading = false
	if msg.err != nil {
		m.sessionsError = sanitizeDisplay(msg.err.Error())
		m.status = "failed to load conversations: " + m.sessionsError
		return nil
	}
	m.sessionsList = msg.sessions
	if m.sessionsSelected >= len(m.sessionsList) {
		m.sessionsSelected = maxInt(len(m.sessionsList)-1, 0)
	}
	for i, s := range m.sessionsList {
		if s.Active {
			m.sessionsSelected = i
			break
		}
	}
	m.status = fmt.Sprintf("%d saved conversations", len(m.sessionsList))
	return nil
}

func (m *Model) updateSessionSwitched(msg sessionSwitchedMsg) tea.Cmd {
	if msg.id != m.sessionsID {
		return nil
	}
	m.sessionsLoading = false
	if msg.err != nil {
		m.sessionsError = sanitizeDisplay(msg.err.Error())
		m.status = "conversation switch failed: " + m.sessionsError
		return nil
	}
	m.chatLines = nil
	m.chatFoldSelected = -1
	for _, event := range msg.events {
		event.Text = sanitizeDisplay(event.Text)
		if strings.TrimSpace(event.Text) != "" {
			m.appendChatEvent(event)
		}
	}
	m.chatScroll = m.chatScrollMax()
	m.sessionsOpen = false
	m.focus = focusChat
	m.ensureChatTabs()
	m.chatTabs[m.chatTab].SessionID = msg.summary.ID
	if !m.chatTabs[m.chatTab].CustomTitle && strings.TrimSpace(msg.summary.Title) != "" && msg.summary.Title != "(empty conversation)" {
		m.chatTabs[m.chatTab].Title = shortenTabTitle(msg.summary.Title)
	}
	m.saveActiveChatTab()
	if msg.isNew {
		m.status = fmt.Sprintf("started new conversation (%s)", sanitizeDisplay(msg.summary.ID))
	} else {
		m.status = fmt.Sprintf("resumed conversation %s (%d messages)", sanitizeDisplay(msg.summary.ID), msg.summary.MessageCount)
	}
	return nil
}

func (m *Model) startCompactSession() tea.Cmd {
	if m.chatRunning {
		m.status = "finish or cancel the active chat turn before compacting"
		return nil
	}
	if m.sessionCallbacks.Compact == nil {
		m.status = "conversation compaction is unavailable"
		return nil
	}
	m.sessionsID++
	reqID := m.sessionsID
	compactFn := m.sessionCallbacks.Compact
	m.status = "compacting & summarizing conversation history…"
	return func() tea.Msg {
		summary, events, used, maxTok, err := compactFn(context.Background())
		return chatCompactMsg{id: reqID, summary: summary, events: events, compacted: err == nil, tokensUsed: used, tokensMax: maxTok, err: err}
	}
}

func (m *Model) updateChatCompact(msg chatCompactMsg) tea.Cmd {
	if msg.err != nil {
		m.status = "compaction failed: " + sanitizeDisplay(msg.err.Error())
		return nil
	}
	m.chatLines = nil
	m.chatFoldSelected = -1
	for _, event := range msg.events {
		event.Text = sanitizeDisplay(event.Text)
		if strings.TrimSpace(event.Text) != "" {
			m.appendChatEvent(event)
		}
	}
	if msg.tokensUsed >= 0 {
		m.connection.ContextTokens = msg.tokensUsed
	}
	if msg.tokensMax > 0 {
		m.connection.ContextMaxTokens = msg.tokensMax
	}
	m.applyCatalogContextWindow()
	m.chatScroll = m.chatScrollMax()
	m.ensureChatTabs()
	if msg.summary.ID != "" {
		m.chatTabs[m.chatTab].SessionID = msg.summary.ID
	}
	m.saveActiveChatTab()
	window := formatTokenCountShort(m.connection.ContextTokens)
	if m.connection.ContextMaxTokens > 0 {
		window = fmt.Sprintf("%s/%s", window, formatTokenCountShort(m.connection.ContextMaxTokens))
	}
	m.status = fmt.Sprintf("compacted conversation (%s) — context now %s tokens", sanitizeDisplay(msg.summary.ID), window)
	return nil
}

func (m *Model) handleSessionsKey(stroke string) tea.Cmd {
	switch stroke {
	case "esc", "escape", "q":
		m.sessionsOpen = false
		m.sessionsError = ""
		m.normalizeFocus()
		m.status = "conversation history closed"
		return nil
	case "j", "down":
		if len(m.sessionsList) > 0 {
			m.sessionsSelected = minInt(m.sessionsSelected+1, len(m.sessionsList)-1)
		}
		return nil
	case "k", "up":
		if len(m.sessionsList) > 0 {
			m.sessionsSelected = maxInt(m.sessionsSelected-1, 0)
		}
		return nil
	case "n":
		return m.startNewSession()
	case "r":
		return m.startSessionsView()
	case "enter", "return":
		if m.sessionsLoading || len(m.sessionsList) == 0 {
			return nil
		}
		item := m.sessionsList[m.sessionsSelected]
		return m.startResumeSession(item.ID)
	}
	return nil
}

func (m *Model) renderSessionsView() string {
	p := m.colors()
	width := maxInt(m.width-4, 40)
	var lines []string

	lines = append(lines, p.accentText("CONVERSATION HISTORY  ·  AUTO-SAVED SESSIONS"))
	lines = append(lines, p.mutedText("Every turn is automatically saved to disk. Select any past conversation to resume it or press 'n' for a new chat."))
	lines = append(lines, p.mutedText("Keys: j/k select  ·  Enter resume conversation  ·  n new conversation  ·  r refresh  ·  Esc close"))
	lines = append(lines, "")

	if m.sessionsError != "" {
		lines = append(lines, p.dangerText("Error: "+m.sessionsError), "")
	}
	if m.sessionsLoading {
		lines = append(lines, p.cyanText("Loading conversations…"), "")
	}

	if len(m.sessionsList) == 0 && !m.sessionsLoading {
		lines = append(lines,
			p.mutedText("No saved conversations found for this workspace yet."),
			p.quietText("Start chatting or press 'n' to create a conversation."),
		)
		return strings.Join(lines, "\n")
	}

	header := fmt.Sprintf("  %-10s  %-22s  %-8s  %-16s  %s", "STATE", "SESSION ID", "MSGS", "UPDATED", "PREVIEW / FIRST PROMPT")
	lines = append(lines, p.quietText(ansi.Truncate(header, width, "…")))
	lines = append(lines, p.quietText(strings.Repeat("─", minInt(width, 108))))

	maxRows := maxInt(m.mainPaneHeight()-len(lines)-4, 4)
	start := 0
	if m.sessionsSelected >= maxRows {
		start = m.sessionsSelected - maxRows + 1
	}
	end := minInt(start+maxRows, len(m.sessionsList))

	for i := start; i < end; i++ {
		item := m.sessionsList[i]
		selected := i == m.sessionsSelected
		cursor := "  "
		if selected {
			cursor = "› "
		}
		stateText := fmt.Sprintf("%-10s", "["+strings.ToUpper(valueOr(item.Status, "saved"))+"]")
		if item.Active {
			stateText = fmt.Sprintf("%-10s", "[ACTIVE]")
		}
		shortID := item.ID
		if len(shortID) > 20 {
			shortID = shortID[:20] + "…"
		}
		idCol := fmt.Sprintf("%-22s", sanitizeDisplay(shortID))
		msgCol := fmt.Sprintf("%-8d", item.MessageCount)
		timeCol := fmt.Sprintf("%-16s", sanitizeDisplay(valueOr(item.UpdatedAt, "—")))
		title := sanitizeDisplay(strings.ReplaceAll(strings.TrimSpace(item.Title), "\n", " "))
		if title == "" {
			title = "(new conversation)"
		}
		plainRow := fmt.Sprintf("%s%s  %s  %s  %s  %s", cursor, stateText, idCol, msgCol, timeCol, title)
		plainRow = ansi.Truncate(plainRow, width, "…")

		if selected {
			if p.enabled() {
				plainRow = lipgloss.NewStyle().
					Foreground(p.ink).
					Background(lipgloss.Color("#2d2550")).
					Bold(true).
					Render(padRight(plainRow, minInt(width, 108)))
			}
			lines = append(lines, plainRow)
		} else {
			stateStyled := p.mutedText(stateText)
			if item.Active {
				stateStyled = p.okText(stateText)
			} else if item.Status == "interrupted" {
				stateStyled = p.warnText(stateText)
			} else if item.Status == "error" {
				stateStyled = p.dangerText(stateText)
			}
			row := cursor + stateStyled + "  " + p.cyanText(idCol) + "  " + p.mutedText(msgCol) + "  " + p.quietText(timeCol) + "  " + colorStyle(p.ink).Render(ansi.Truncate(title, maxInt(width-66, 16), "…"))
			lines = append(lines, row)
		}
	}

	lines = append(lines, "")
	lines = append(lines, p.quietText("Commands: :history  ·  :sessions  ·  :new (start fresh conversation)"))
	return strings.Join(lines, "\n")
}

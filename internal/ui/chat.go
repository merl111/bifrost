package ui

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// ChatEvent is one provider event delivered to the local UI. Delta and final
// contain assistant text; tool and status contain activity text.
type ChatEvent struct {
	Kind       string
	Text       string
	ID         string
	Detail     string
	Connection *ConnectionInfo
}

// ChatRunner adapts a provider without making the UI depend on its package.
// It should return when ctx is canceled and may emit any number of events.
type ChatRunner func(context.Context, string, func(ChatEvent)) error

// WithComposer configures the maximum visible composer body rows and its
// primary newline binding. A zero maxRows or empty newlineKey keeps the
// defaults (eight rows and Shift+Enter). Alt+Enter and Ctrl+J remain accepted
// as terminal-friendly newline fallbacks regardless of the primary binding.
func WithComposer(maxRows int, newlineKey string) Option {
	return func(m *Model) {
		m.composerMaxRows = maxRows
		m.composerNewline = strings.ToLower(strings.TrimSpace(newlineKey))
	}
}

func WithShiftDetector(fn func() bool) Option {
	return func(m *Model) {
		m.shiftHeld = fn
	}
}

const (
	maxChatLines  = 256
	maxChatRunes  = 128 << 10
	maxChatPrompt = 16 << 10
)

type chatLine struct {
	kind      string
	text      string
	id        string
	detail    string
	collapsed bool
	provider  string
	model     string
	rendered  *chatRenderCache
}

type chatStreamItem struct {
	event ChatEvent
	err   error
	done  bool
}

type chatStream struct {
	items chan chatStreamItem
}

type chatStreamMsg struct {
	id     uint64
	stream *chatStream
	item   chatStreamItem
}

func startChatStream(ctx context.Context, runner ChatRunner, prompt string, id uint64) (*chatStream, tea.Cmd) {
	stream := &chatStream{items: make(chan chatStreamItem, 32)}
	go func() {
		defer close(stream.items)
		emit := func(event ChatEvent) {
			select {
			case stream.items <- chatStreamItem{event: event}:
			case <-ctx.Done():
			}
		}
		err := runner(ctx, prompt, emit)
		select {
		case stream.items <- chatStreamItem{done: true, err: err}:
		case <-ctx.Done():
			// The model already has the cancellation state; do not block a
			// provider that is returning after the user canceled.
		}
	}()
	return stream, waitChatStream(stream, id)
}

func waitChatStream(stream *chatStream, id uint64) tea.Cmd {
	return func() tea.Msg {
		item, ok := <-stream.items
		if !ok {
			return chatStreamMsg{id: id, stream: stream, item: chatStreamItem{done: true}}
		}
		return chatStreamMsg{id: id, stream: stream, item: item}
	}
}

func (m *Model) appendChatEvent(event ChatEvent) {
	kind := strings.ToLower(strings.TrimSpace(event.Kind))
	if kind == "" {
		kind = "status"
	}
	if event.Connection != nil {
		info := sanitizeConnectionInfo(*event.Connection)
		if n := m.catalogContext(info.Model); n > 0 {
			info.ContextMaxTokens = n
		}
		m.connection = info
		if info.Account != "" {
			m.accountLabel = info.Account
		}
	}
	if kind == "connection" {
		// Connection metadata updates the footer and intentionally never enters
		// the transcript.
		return
	}
	if kind == "viewer" {
		m.applyViewerChatEvent(event)
		return
	}
	m.recordTerminalToolEvent(event)
	text := event.Text
	line := chatLine{kind: kind, text: text, id: event.ID, detail: event.Detail, collapsed: chatActivityKind(kind)}
	if event.ID != "" && chatActivityKind(kind) {
		for i := len(m.chatLines) - 1; i >= 0; i-- {
			previous := &m.chatLines[i]
			if previous.kind == "user" {
				break
			}
			if previous.id == event.ID && previous.kind == kind {
				previous.text = text
				if event.Detail != "" {
					previous.detail = event.Detail
				}
				m.trimChatLines()
				return
			}
		}
	}
	if kind == "delta" || kind == "final" {
		line.provider, line.model = m.connection.Provider, m.connection.Model
		line.kind = "assistant"
	}
	if kind == "delta" {
		kind = "assistant"
		if n := len(m.chatLines); n > 0 && m.chatLines[n-1].kind == kind {
			m.chatLines[n-1].text += text
		} else {
			m.chatLines = append(m.chatLines, line)
		}
	} else if kind == "final" {
		kind = "assistant"
		if n := len(m.chatLines); n > 0 && m.chatLines[n-1].kind == kind {
			if m.chatLines[n-1].text == text {
				return
			}
			m.chatLines[n-1].text = text
			m.trimChatLines()
			return
		}
		m.chatLines = append(m.chatLines, line)
	} else {
		m.chatLines = append(m.chatLines, line)
	}
	m.trimChatLines()
}

func (m *Model) trimChatLines() {
	for len(m.chatLines) > maxChatLines || chatRuneCount(m.chatLines) > maxChatRunes {
		if len(m.chatLines) == 0 {
			return
		}
		m.chatLines = m.chatLines[1:]
		if m.chatFoldSelected >= 0 {
			m.chatFoldSelected--
		}
	}
}

func chatRuneCount(lines []chatLine) int {
	n := 0
	for _, line := range lines {
		n += len([]rune(line.kind)) + len([]rune(line.text)) + len([]rune(line.detail)) + 4
	}
	return n
}

func (m *Model) cancelChat() {
	if m.chatCancel != nil {
		m.chatCancel()
		m.chatCancel = nil
		// Detach messages already buffered by the old runner before a new
		// prompt or a later cancellation can process them.
		m.chatID++
	}
	m.chatRunning = false
}

func (m *Model) chatTopBarVisible(innerHeight int) bool {
	if innerHeight < 10 || m.chatPaneInnerWidth() < 32 {
		return false
	}
	return len(m.chatTabs) > 1 || m.chatTabRenaming || m.chatModelPicker.open || len(m.accounts) > 0 || m.connection.Provider != "" || m.accountLabel != ""
}

func (m *Model) chatViewport() int {
	innerHeight := maxInt(m.mainPaneHeight()-2, 1)
	topRows := 0
	if m.chatTopBarVisible(innerHeight) {
		topRows = len(m.renderChatTopBar(m.chatPaneInnerWidth()))
	}
	return maxInt(innerHeight-topRows-len(m.chatFooterLines()), 1)
}

func (m *Model) chatPaneInnerWidth() int {
	width := m.width
	if width >= 60 {
		width = m.paneWidths(width)[2]
	}
	return maxInt(width-2, 1)
}

func wrapChatText(text string, width int) []string {
	text = sanitizeDisplay(text)
	physical := strings.Split(text, "\n")
	lines := make([]string, 0, len(physical))
	for _, line := range physical {
		wrapped := ansi.Wrap(line, maxInt(width, 1), " ")
		lines = append(lines, strings.Split(wrapped, "\n")...)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func (m *Model) chatScrollMax() int {
	return maxInt(len(m.chatTranscriptLines())-m.chatViewport(), 0)
}

// chatInputChanged keeps the transcript anchored while the composer changes
// height. A user who has scrolled up should retain that position; only a view
// that was already at the bottom follows the newly exposed rows.
func (m *Model) chatInputChanged(atBottom bool) {
	m.syncChatMentionState()
	if atBottom {
		m.chatScroll = m.chatScrollMax()
		return
	}
	m.clampChatScroll()
}

func stripLockModifiers(mod tea.KeyMod) tea.KeyMod {
	return mod &^ (tea.ModCapsLock | tea.ModNumLock | tea.ModScrollLock)
}

func isEnterStroke(stroke string) bool {
	switch stroke {
	case "enter", "return", "kp_enter", "shift+enter", "alt+enter", "ctrl+enter",
		"ctrl+shift+enter", "alt+shift+enter", "ctrl+alt+enter":
		return true
	default:
		return strings.HasSuffix(stroke, "+enter") || strings.HasSuffix(stroke, "+return")
	}
}

func (m *Model) chatNewlineRequested(stroke string, mod tea.KeyMod) bool {
	mod = stripLockModifiers(mod)
	// Alt+Enter, Ctrl+Enter, and Ctrl+J are retained as universal newline
	// bindings alongside Shift+Enter across terminal emulators.
	if stroke == "alt+enter" || stroke == "ctrl+enter" || stroke == "ctrl+j" ||
		stroke == "shift+enter" || stroke == "ctrl+shift+enter" || stroke == "alt+shift+enter" ||
		(isEnterStroke(stroke) && (mod&(tea.ModAlt|tea.ModCtrl|tea.ModShift) != 0)) ||
		(stroke == "j" && mod&tea.ModCtrl != 0) {
		return true
	}
	key := strings.ToLower(strings.TrimSpace(m.composerNewline))
	if key == "" {
		key = "shift+enter"
	}
	switch key {
	case "alt+enter":
		return stroke == "alt+enter" || (isEnterStroke(stroke) && mod&tea.ModAlt != 0)
	case "ctrl+j":
		return stroke == "ctrl+j" || (stroke == "j" && mod&tea.ModCtrl != 0)
	case "shift+enter":
		fallthrough
	default:
		return stroke == "shift+enter" || (isEnterStroke(stroke) && mod&tea.ModShift != 0)
	}
}

func (m *Model) chatAtBottom() bool {
	return m.chatScroll >= m.chatScrollMax()
}

func (m *Model) clampChatScroll() {
	maxOffset := m.chatScrollMax()
	if m.chatScroll < 0 {
		m.chatScroll = 0
	}
	if m.chatScroll > maxOffset {
		m.chatScroll = maxOffset
	}
}

func (m *Model) chatInputKeyPress(msg tea.KeyPressMsg) tea.Cmd {
	stroke := msg.Keystroke()
	if stroke == "" {
		stroke = msg.String()
	}
	if msg.Code == tea.KeyEnter || msg.Code == tea.KeyKpEnter {
		if stripLockModifiers(msg.Mod) == 0 && (stroke == "" || stroke == "\n" || stroke == "\r") {
			stroke = "enter"
		}
	}
	return m.chatInputKey(stroke, msg.Text, msg.Mod)
}

func (m *Model) chatInputKey(stroke, text string, mod tea.KeyMod) tea.Cmd {
	activeMod := stripLockModifiers(mod)
	if stroke == "escape" || stroke == "esc" {
		if m.chatMention.open {
			m.chatMention.open = false
			m.chatMention.dismissedAt = m.chatMention.triggerByte
			return nil
		}
		m.chatEditing = false
		return nil
	}
	if m.chatMention.open && len(m.chatMention.candidates) > 0 {
		switch stroke {
		case "down", "ctrl+n":
			m.chatMention.selected = (m.chatMention.selected + 1) % len(m.chatMention.candidates)
			return nil
		case "up", "ctrl+p":
			m.chatMention.selected = (m.chatMention.selected - 1 + len(m.chatMention.candidates)) % len(m.chatMention.candidates)
			return nil
		case "tab":
			m.applySelectedChatMention()
			return nil
		}
	}
	if stroke == "ctrl+c" || (stroke == "ctrl+d" && m.chatInput == "") {
		return m.handleExitInterrupt(stroke)
	}
	if stroke == "backspace" || stroke == "delete" {
		atBottom := m.chatAtBottom()
		last := 0
		for offset := 0; offset < len(m.chatInput); {
			last = offset
			cluster, _ := ansi.FirstGraphemeCluster(m.chatInput[offset:], ansi.GraphemeWidth)
			offset += len(cluster)
		}
		m.chatInput = m.chatInput[:last]
		m.chatInputChanged(atBottom)
		return nil
	}
	if isEnterStroke(stroke) || stroke == "ctrl+j" || (stroke == "j" && activeMod&tea.ModCtrl != 0) || text == "\n" || text == "\r" {
		if (stroke == "enter" || stroke == "return" || stroke == "kp_enter") && activeMod == 0 && !m.keyboardEnhanced && m.shiftHeld != nil && m.shiftHeld() {
			activeMod |= tea.ModShift
			stroke = "shift+enter"
		}
		if text == "\n" && stroke != "enter" && stroke != "return" && stroke != "kp_enter" {
			activeMod |= tea.ModShift
			stroke = "shift+enter"
		}
		if m.chatNewlineRequested(stroke, activeMod) {
			atBottom := m.chatAtBottom()
			if len([]rune(m.chatInput)) < maxChatPrompt {
				m.chatInput += "\n"
				m.chatInputChanged(atBottom)
			}
			return nil
		}
		if m.chatMention.open && len(m.chatMention.candidates) > 0 {
			m.applySelectedChatMention()
			return nil
		}
		return m.submitChat()
	}
	if text != "" {
		if len([]rune(m.chatInput))+len([]rune(text)) <= maxChatPrompt {
			atBottom := m.chatAtBottom()
			m.chatInput += text
			m.chatInputChanged(atBottom)
		}
	}
	return nil
}

func (m *Model) submitChat() tea.Cmd {
	prompt := strings.TrimSpace(m.chatInput)
	if prompt == "" {
		m.status = "chat prompt is empty"
		return nil
	}
	if m.chatRunner == nil {
		m.status = "provider not connected yet"
		return nil
	}
	m.cancelChat()
	m.chatHistory = append(m.chatHistory, prompt)
	if len(m.chatHistory) > 64 {
		m.chatHistory = m.chatHistory[len(m.chatHistory)-64:]
	}
	expandedPrompt, attachedRefs := m.expandChatPromptMentions(prompt)
	m.appendChatEvent(ChatEvent{Kind: "user", Text: prompt})
	if len(attachedRefs) > 0 {
		m.appendChatEvent(ChatEvent{Kind: "status", Text: "attached context: " + strings.Join(attachedRefs, ", ")})
	}
	m.chatInput = ""
	m.chatMention.open = false
	m.chatMention.dismissedAt = -1
	m.focus = focusChat
	m.chatEditing = true
	m.chatRunning = true
	m.chatID++
	m.chatCtx, m.chatCancel = context.WithCancel(context.Background())
	stream, cmd := startChatStream(m.chatCtx, m.chatRunner, expandedPrompt, m.chatID)
	m.chatStream = stream
	m.status = "chat running"
	m.chatScroll = m.chatScrollMax()
	m.saveActiveChatTab()
	return cmd
}

func chatCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func (m *Model) handleChatStream(msg chatStreamMsg) tea.Cmd {
	if msg.id != m.chatID || msg.stream != m.chatStream {
		return m.handleBackgroundTabStream(msg)
	}
	if msg.item.done {
		atBottom := m.chatAtBottom()
		m.chatRunning = false
		m.chatCancel = nil
		m.chatCtx = nil
		if msg.item.err != nil && !chatCanceled(msg.item.err) {
			m.appendChatEvent(ChatEvent{Kind: "error", Text: msg.item.err.Error()})
			m.status = "chat failed"
		} else if m.status == "chat running" {
			m.status = "chat complete"
		}
		if atBottom {
			m.chatScroll = m.chatScrollMax()
		}
		m.clampChatScroll()
		m.saveActiveChatTab()
		if m.viewPath == "review" && m.reviewLoader != nil {
			return m.startReview()
		}
		return nil
	}
	atBottom := m.chatAtBottom()
	m.appendChatEvent(msg.item.event)
	if atBottom {
		m.chatScroll = m.chatScrollMax()
	} else {
		m.clampChatScroll()
	}
	m.saveActiveChatTab()
	return waitChatStream(msg.stream, msg.id)
}

func (m *Model) handleBackgroundTabStream(msg chatStreamMsg) tea.Cmd {
	for i := range m.chatTabs {
		if i == m.chatTab {
			continue
		}
		tab := &m.chatTabs[i]
		if tab.StreamID != msg.id || tab.Stream != msg.stream {
			continue
		}
		if msg.item.done {
			tab.Running = false
			tab.Cancel = nil
			tab.Ctx = nil
			if msg.item.err != nil && !chatCanceled(msg.item.err) {
				tab.Lines = append(tab.Lines, chatLine{kind: "error", text: sanitizeDisplay(msg.item.err.Error())})
			}
			return nil
		}
		ev := msg.item.event
		kind := strings.ToLower(strings.TrimSpace(ev.Kind))
		if kind == "connection" {
			return waitChatStream(msg.stream, msg.id)
		}
		text := sanitizeDisplay(ev.Text)
		if kind == "delta" || kind == "final" {
			if n := len(tab.Lines); n > 0 && tab.Lines[n-1].kind == "assistant" {
				if kind == "delta" {
					tab.Lines[n-1].text += text
				} else {
					tab.Lines[n-1].text = text
				}
			} else {
				tab.Lines = append(tab.Lines, chatLine{kind: "assistant", text: text, provider: tab.Connection.Provider, model: tab.Connection.Model})
			}
		} else if text != "" {
			tab.Lines = append(tab.Lines, chatLine{kind: kind, text: text, id: ev.ID, detail: ev.Detail, collapsed: chatActivityKind(kind)})
		}
		return waitChatStream(msg.stream, msg.id)
	}
	return nil
}

package ui

import (
	"bifrost/internal/execpolicy"
	"context"
	"errors"
	"fmt"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bifrost/internal/tasks"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// Mode determines who receives keyboard input.
type terminalInputMsg struct{ err error }

type Mode uint8

const (
	ModeNormal Mode = iota
	ModeTerminal
	ModeInline
)

func (m Mode) String() string {
	if m == ModeTerminal {
		return "TERMINAL"
	}
	if m == ModeInline {
		return "EDITOR"
	}
	return "NORMAL"
}

type focus uint8

const (
	focusFiles focus = iota
	focusViewer
	focusChat
	focusTerminal
)

func (f focus) String() string {
	switch f {
	case focusFiles:
		return "files"
	case focusViewer:
		return "viewer"
	case focusChat:
		return "chat"
	default:
		return "terminal"
	}
}

// Option customizes the local UI without coupling it to the process service.
type Option func(*Model)

// WithShell selects the executable used for the embedded terminal. It is
// passed as an argv[0] directly; shell syntax is never parsed by Bifrost.
func WithShell(shell string) Option { return func(m *Model) { m.shell = shell } }

// WithFilesystem supplies the tree/viewer read boundary. This is useful for
// a worker-backed UI where paths must never be opened by the host process.
func WithFilesystem(list func(context.Context, string) ([]FileEntry, error), read func(context.Context, string) (string, error)) Option {
	return func(m *Model) { m.fs = callbackFilesystem{list: list, read: read}; m.remoteFS = true }
}

// WithFilesystemBackend supplies a complete filesystem implementation,
// including lifecycle cleanup, for local or remote environments.
func WithFilesystemBackend(fs Filesystem) Option {
	return func(m *Model) { m.fs = fs; m.remoteFS = fs != nil }
}

// WithTerminalCommand runs an explicit argv under the embedded PTY. The
// command is useful for a remote SSH terminal; no shell parsing is performed.
func WithTerminalCommand(argv []string, dir string) Option {
	copyArgv := append([]string(nil), argv...)
	return func(m *Model) { m.terminalArgv = copyArgv; m.terminalDir = dir }
}

// WithEditor selects the editor executable and its already-separated
// arguments. The UI passes the selected file as the final argument and never
// invokes a shell. An empty value uses VISUAL, EDITOR, or vim.
func WithEditor(argv []string) Option {
	copyArgv := append([]string(nil), argv...)
	return func(m *Model) { m.editor = copyArgv }
}

// WithChatRunner connects provider-independent chat streaming to the UI.
func WithChatRunner(runner ChatRunner) Option { return func(m *Model) { m.chatRunner = runner } }

// WithTheme selects the configured UI palette. Plain deliberately avoids
// terminal color escapes for logs and accessibility; unknown values fall back
// to plain rather than inventing a palette.
// WithStatus sets the initial status-bar message.
func WithStatus(text string) Option {
	return func(m *Model) {
		if text = strings.TrimSpace(text); text != "" {
			m.status = sanitizeDisplay(text)
		}
	}
}

func WithTheme(theme string) Option {
	return func(m *Model) {
		switch strings.ToLower(strings.TrimSpace(theme)) {
		case "midnight", "light":
			m.theme = strings.ToLower(strings.TrimSpace(theme))
		case "plain":
			m.theme = "plain"
		default:
			m.theme = "plain"
		}
	}
}

// WithChatHistory seeds the chat pane with a bounded, display-safe transcript.
// Callers should provide only user/assistant/status summaries; large tool
// results belong in the activity or review views rather than the initial chat.
// The events are copied at option construction and retained in the supplied
// order, so later caller mutations cannot alter the model.
func WithChatHistory(events []ChatEvent) Option {
	copyEvents := append([]ChatEvent(nil), events...)
	return func(m *Model) {
		for _, event := range copyEvents {
			event.Text = sanitizeDisplay(event.Text)
			if strings.TrimSpace(event.Text) == "" {
				continue
			}
			event.Kind = strings.ToLower(strings.TrimSpace(sanitizeDisplay(event.Kind)))
			if event.Kind == "" {
				event.Kind = "status"
			}
			m.appendChatEvent(event)
		}
		m.chatScroll = m.chatScrollMax()
	}
}

// WithReviewLoader connects a read-only status/diff loader to the review view.
func WithReviewLoader(loader func(context.Context) (string, error)) Option {
	return func(m *Model) { m.reviewLoader = ReviewLoader(loader) }
}

// Model is the local coding workspace. Files remain read-only in the initial
// UI, while chat, review, editor, and terminal activity are supplied through
// explicit options and callbacks.
type Model struct {
	executionApprovals []*execpolicy.Request
	mu                 sync.Mutex

	root                    string
	tree                    fileTree
	dir                     string
	files                   []FileEntry
	selected                int
	fileOffset              int
	viewer                  string
	viewPath                string
	viewErr                 string
	viewerOffset            int
	viewerHighlights        []ViewerHighlightRange
	viewerBanner            string
	viewerFlowSteps         []ViewerStep
	viewerFlowIndex         int
	viewerFlowTitle         string
	viewerExplanation       string
	viewerExplanationOpen   bool
	viewerExplanationOffset int

	width                                                                        int
	height                                                                       int
	mode                                                                         Mode
	focus                                                                        focus
	prefix                                                                       bool
	shell                                                                        string
	terminalArgv                                                                 []string
	terminalDir                                                                  string
	editor                                                                       []string
	fs                                                                           Filesystem
	remoteFS                                                                     bool
	pty                                                                          *ptySession
	ptyErr                                                                       string
	status                                                                       string
	theme                                                                        string
	closed                                                                       bool
	layout                                                                       layoutState
	layoutChange                                                                 func(string) error
	layoutSettingsChange                                                         func(LayoutSettings) error
	icons                                                                        string
	composerMaxRows                                                              int
	composerNewline                                                              string
	paneDrag                                                                     *paneDragState
	settingsCallbacks                                                            SettingsCallbacks
	settingsOpen                                                                 bool
	settingsEditor                                                               settingsEditorState
	connection                                                                   ConnectionInfo
	catalogModels                                                                map[string]catalogModelInfo
	extensions                                                                   ExtensionCommand
	extensionCancel                                                              context.CancelFunc
	marketplace                                                                  MarketplaceCallbacks
	marketplaceOpen, marketplaceSearching                                        bool
	marketplaceQuery, marketplacePreview, marketplacePreviewID, marketplaceError string
	marketplaceEntries                                                           []MarketplaceEntry
	marketplaceSelected                                                          int
	marketplaceTab                                                               int // 0 = Skills, 1 = MCP Servers
	marketplaceLoading, marketplaceInstalling                                    bool
	marketplaceInstallScope                                                      string
	marketplaceID                                                                uint64
	marketplaceCancel                                                            context.CancelFunc
	marketplaceView                                                              marketplaceViewState

	chatRunner       ChatRunner
	chatInput        string
	chatEditing      bool
	chatLines        []chatLine
	chatHistory      []string
	chatScroll       int
	chatRunning      bool
	chatID           uint64
	chatStream       *chatStream
	chatCtx          context.Context
	chatCancel       context.CancelFunc
	chatFolds        []chatFoldRow
	chatFoldSelected int

	accounts              []AccountChoice
	accountLabel          string
	accountSelect         func(context.Context, string) ([]ChatEvent, error)
	accountSelectModel    func(context.Context, string, string) ([]AccountChoice, AccountChoice, []ChatEvent, error)
	accountAdd            func(context.Context, AccountAddRequest) ([]AccountChoice, AccountChoice, []ChatEvent, error)
	accountRemove         func(context.Context, string) ([]AccountChoice, error)
	accountDiscoverModels func(context.Context, AccountAddRequest, bool) ([]AccountModelOption, string, error)
	accountForm           accountFormState
	accountsOpen          bool
	accountLoading        bool
	accountSelected       int
	accountID             uint64
	accountCancel         context.CancelFunc

	reviewLoader ReviewLoader
	reviewID     uint64
	reviewCancel context.CancelFunc

	inlineLoader      EditorLoader
	inlineSaver       EditorSaver
	inline            *inlineEditorState
	inlineID          uint64
	inlineCancel      context.CancelFunc
	draftStateDir     string
	paletteOpen       bool
	paletteInput      string
	paletteSelected   int
	paletteCompletion paletteCompletionState
	paletteError      string
	paletteHelp       string
	helpOpen          bool
	helpScroll        int
	helpLineCount     int
	helpInnerRows     int
	helpModalX        int
	helpModalY        int
	helpModalW        int
	helpModalH        int
	taskMonitor       TaskMonitorCallbacks
	taskSummaries     []tasks.Summary
	taskEvents        []tasks.Event
	taskCursor        uint64
	taskErr           string
	taskID            uint64
	taskCancel        context.CancelFunc

	projectCallbacks        ProjectCallbacks
	projectStatus           ProjectStatus
	projectDetails          ProjectDetails
	projectOpen             bool
	projectSettingsOpen     bool
	projectDetailsOpen      bool
	projectDetailsLoading   bool
	projectDetailsTab       int
	projectDetailsScroll    int
	projectDetailsSelected  int
	projectSettingsSelected int
	projectLoading          bool
	projectSelected         int
	projectError            string
	projectEditField        string
	projectEditInput        string
	projectID               uint64
	projectCancel           context.CancelFunc

	sessionCallbacks SessionCallbacks
	sessionsOpen     bool
	sessionsLoading  bool
	sessionsSelected int
	sessionsList     []SessionSummary
	sessionsError    string
	sessionsID       uint64

	terminalTabs   []terminalTab
	terminalTab    int
	terminalScroll int
	viewerTab      int
	shellOpen      bool
	extraShells    []*ptySession
	viewerTabHits  []viewerTabHit

	chatTabs           []chatTabEntry
	chatTab            int
	chatTabRenaming    bool
	chatTabRenameInput string
	chatModelPicker    chatModelPickerState

	exitArmedAt  time.Time
	exitArmedKey string

	lastPasteText string
	lastPasteAt   time.Time

	keyboardEnhanced bool
	shiftHeld        func() bool
	chatMention      chatMentionState
	chatSelection    chatSelectionState
}

// New constructs a model. It does not launch a child process until Init, so
// tests and embedders can construct it without side effects.
func New(root string, options ...Option) *Model {
	m := &Model{root: root, width: defaultPTYWidth, height: defaultPTYHeight, status: "ready", theme: "midnight", layout: studioLayout(), chatFoldSelected: -1, paletteSelected: -1, shiftHeld: platformShiftHeld, chatMention: chatMentionState{dismissedAt: -1}}
	for _, option := range options {
		if option != nil {
			option(m)
		}
	}
	if m.fs != nil {
		m.root = root
		m.files = m.listFiles("")
		return m
	}
	tree, err := newFileTree(root)
	if err != nil {
		m.ptyErr = err.Error()
		return m
	}
	m.tree = tree
	m.fs = localFilesystem{tree: &m.tree}
	m.root = tree.rootPath
	m.files = m.listFiles("")
	return m
}

func (m *Model) listFiles(path string) []FileEntry {
	if m.fs == nil {
		return []FileEntry{{Path: path, Error: "filesystem is unavailable"}}
	}
	entries, err := m.fs.List(context.Background(), path)
	if err != nil {
		return []FileEntry{{Path: path, Error: err.Error()}}
	}
	return entries
}

func (m *Model) readFile(path string) (string, error) {
	if m.fs == nil {
		return "", errors.New("filesystem is unavailable")
	}
	return m.fs.Read(context.Background(), path)
}

// Run starts the interactive workspace rooted at root and returns when the
// Bubble Tea program exits. Child cleanup is guaranteed before returning.
func Run(root string, options ...Option) error {
	return RunContext(context.Background(), root, options...)
}

// RunContext starts the workspace and stops Bubble Tea when ctx is canceled.
func RunContext(ctx context.Context, root string, options ...Option) error {
	m := New(root, options...)
	program := tea.NewProgram(m, tea.WithContext(ctx))
	_, err := program.Run()
	_ = m.Close()
	return err
}

func (m *Model) Init() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.pty == nil && m.ptyErr == "" {
		if err := m.startTerminal(); err != nil {
			m.ptyErr = err.Error()
			m.status = "terminal unavailable"
		}
	}
	var cmds []tea.Cmd
	if m.pty != nil {
		cmds = append(cmds, readPTY(m.pty))
	}
	if cmd := m.refreshActiveCatalog(); cmd != nil {
		cmds = append(cmds, cmd)
	}
	return tea.Batch(cmds...)
}

func readPTY(p *ptySession) tea.Cmd {
	return func() tea.Msg { return readPTYMessage(p) }
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	switch msg := msg.(type) {
	case tea.KeyboardEnhancementsMsg:
		m.keyboardEnhanced = msg.Flags != 0
		return m, nil
	case tea.WindowSizeMsg:
		return m, m.updateWindowSize(msg)
	case tea.KeyPressMsg:
		if m.handleExecutionApproval(msg.String()) {
			return m, nil
		}
		return m, m.updateKey(msg)
	case tea.MouseClickMsg:
		mouse := msg.Mouse()
		if mouse.Button == tea.MouseMiddle || mouse.Button == tea.MouseRight {
			if m.accountsOpen {
				_ = m.accountsMouse(mouse, true)
			} else if m.settingsOpen {
				m.settingsMouse(mouse, false)
			}
			return m, m.requestClipboardPaste(mouse.Button == tea.MouseMiddle)
		}
		if m.marketplaceOpen {
			return m, m.marketplaceMouse(mouse, false)
		}
		if m.settingsOpen {
			m.settingsMouse(mouse, false)
			return m, nil
		}
		if m.accountsOpen {
			return m, m.accountsMouse(mouse, false)
		}
		if m.helpOpen {
			if m.helpModalW > 0 && (mouse.X < m.helpModalX || mouse.X >= m.helpModalX+m.helpModalW || mouse.Y < m.helpModalY || mouse.Y >= m.helpModalY+m.helpModalH) {
				m.helpOpen = false
				m.status = "help closed"
			}
			return m, nil
		}
		if m.shellBodyContains(mouse) {
			m.beginShellInput()
			return m, nil
		}
		if m.mode == ModeTerminal {
			m.stopShellInput()
		}
		if m.startPaneDrag(mouse) {
			return m, nil
		}
		if m.walkthroughMouse(mouse, false) {
			return m, nil
		}
		if cmd, handled := m.clickViewerTab(mouse); handled {
			return m, cmd
		}
		m.focusPaneAt(mouse)
		return m, m.handleChatMouse(mouse, false)
	case tea.MouseMotionMsg:
		if m.helpOpen {
			return m, nil
		}
		m.movePaneDrag(msg.Mouse())
		m.handleChatMouseMotion(msg.Mouse())
		return m, nil
	case tea.MouseReleaseMsg:
		if m.helpOpen {
			return m, nil
		}
		m.finishPaneDrag()
		return m, m.handleChatMouseRelease(msg.Mouse())
	case tea.MouseWheelMsg:
		if m.helpOpen {
			if msg.Button == tea.MouseWheelDown {
				m.helpScroll += 3
			} else {
				m.helpScroll -= 3
			}
			m.clampHelpScroll()
			return m, nil
		}
		if m.marketplaceOpen {
			return m, m.marketplaceMouse(msg.Mouse(), true)
		}
		if m.settingsOpen {
			m.settingsMouse(msg.Mouse(), true)
			return m, nil
		}
		if m.walkthroughMouse(msg.Mouse(), true) {
			return m, nil
		}
		return m, m.handleChatMouse(msg.Mouse(), true)
	case tea.PasteMsg:
		return m, m.applyClipboardText(msg.Content)
	case tea.ClipboardMsg:
		return m, m.applyClipboardText(msg.Content)
	case clipboardReadMsg:
		return m, m.applyClipboardText(msg.text)
	case terminalInputMsg:
		return m, m.updateTerminalInput(msg)
	case chatStreamMsg:
		return m, m.handleChatStream(msg)
	case inlineLoadedMsg:
		return m, m.handleInlineLoaded(msg)
	case inlineSavedMsg:
		return m, m.handleInlineSaved(msg)
	case reviewResultMsg:
		return m, m.updateReviewResult(msg)
	case taskMonitorMsg:
		return m, m.updateTaskMonitor(msg)
	case taskMonitorTickMsg:
		return m, m.updateTaskMonitorTick(msg)
	case accountSwitchMsg:
		return m, m.updateAccountSwitch(msg)
	case accountRemoveMsg:
		return m, m.updateAccountRemove(msg)
	case accountModelsLoadedMsg:
		return m, m.updateAccountModelsLoaded(msg)
	case sessionsLoadedMsg:
		return m, m.updateSessionsLoaded(msg)
	case sessionSwitchedMsg:
		return m, m.updateSessionSwitched(msg)
	case chatCompactMsg:
		return m, m.updateChatCompact(msg)
	case marketplaceListMsg:
		return m, m.updateMarketplaceList(msg)
	case marketplacePreviewMsg:
		return m, m.updateMarketplacePreview(msg)
	case extensionResultMsg:
		return m, m.updateExtension(msg)
	case marketplaceInstallMsg:
		return m, m.updateMarketplaceInstall(msg)
	case projectStatusMsg:
		return m, m.updateProjectStatus(msg)
	case projectDetailsMsg:
		return m, m.handleProjectDetailsMsg(msg)
	case editorFinishedMsg:
		return m, m.updateEditorFinished(msg)
	case settingsLoadedMsg:
		m.updateSettingsLoaded(msg)
		return m, nil
	case settingsSavedMsg:
		m.updateSettingsSaved(msg)
		return m, nil
	case ptyOutputMsg:
		return m, m.updatePTYOutput(msg)
	}
	return m, nil
}

func (m *Model) updateWindowSize(msg tea.WindowSizeMsg) tea.Cmd {
	m.cancelPaneDrag()
	atBottom := m.chatAtBottom()
	m.width, m.height = maxInt(msg.Width, 1), maxInt(msg.Height, 1)
	if atBottom {
		m.chatScroll = m.chatScrollMax()
	} else {
		m.clampChatScroll()
	}
	m.resizeShells()
	return nil
}

func (m *Model) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	stroke := msg.String()
	if stroke != "ctrl+c" && stroke != "ctrl+d" {
		m.exitArmedKey = ""
		m.exitArmedAt = time.Time{}
	}
	if m.paneDrag != nil {
		if stroke == "esc" || stroke == "escape" {
			m.cancelPaneDrag()
		}
		return nil
	}
	if stroke == "ctrl+v" || stroke == "ctrl+shift+v" || stroke == "shift+insert" {
		return m.requestClipboardPaste(false)
	}
	if stroke == "ctrl+c" || stroke == "ctrl+d" {
		if m.mode == ModeTerminal && m.viewerTabIsShell() {
			if err := m.ptySendKey(msg); err != nil {
				m.ptyErr = err.Error()
			}
			return nil
		}
		return m.handleExitInterrupt(stroke)
	}
	if m.settingsOpen {
		return m.settingsKey(stroke, msg.Text)
	}
	if m.accountsOpen {
		return m.accountKey(stroke, msg.Text)
	}
	if m.sessionsOpen {
		return m.handleSessionsKey(stroke)
	}
	if m.marketplaceOpen {
		return m.marketplaceKey(stroke, msg.Text)
	}
	if m.paletteOpen {
		return m.paletteKey(stroke, msg.Text)
	}
	if m.projectOpen {
		return m.handleProjectKey(stroke, msg.Text)
	}
	if m.helpOpen {
		switch stroke {
		case "escape", "esc", "?":
			m.helpOpen = false
			m.status = "help closed"
		case "j", "down":
			m.helpScroll++
		case "k", "up":
			m.helpScroll--
		case "ctrl+d", "pgdown":
			m.helpScroll += maxInt(m.helpInnerRows-1, 1)
		case "ctrl+u", "pgup":
			m.helpScroll -= maxInt(m.helpInnerRows-1, 1)
		case "g", "home":
			m.helpScroll = 0
		case "G", "end":
			m.helpScroll = m.helpLineCount
		}
		m.clampHelpScroll()
		return nil
	}
	if m.mode == ModeTerminal {
		switch msg.Keystroke() {
		case "esc", "escape", "ctrl+[", "ctrl+]":
			m.leaveTerminalInput()
			return nil
		case "ctrl+w":
			m.stopShellInput()
			m.prefix = true
			return nil
		case "alt+[":
			m.switchViewerTab(-1)
			return nil
		case "alt+]":
			m.switchViewerTab(1)
			return nil
		}
		if msg.Text == "\x1b" || msg.Text == "\x1d" || msg.Code == 0x1d || msg.Code == tea.KeyEscape {
			m.leaveTerminalInput()
			return nil
		}
		if m.viewerTabIsShell() {
			if err := m.ptySendKey(msg); err != nil {
				m.ptyErr = err.Error()
			}
		}
		return nil
	}
	if m.mode == ModeInline {
		return m.inlineEditorKey(msg)
	}
	if m.chatTabRenaming {
		return m.handleChatTabRenameKey(stroke, msg.Text)
	}
	if m.chatModelPicker.open {
		return m.handleChatModelPickerKey(stroke, msg.Text)
	}
	if m.chatEditing && m.focus == focusChat {
		return m.chatInputKeyPress(msg)
	}
	return m.handleNormalKey(msg.String(), msg.Text)
}

func (m *Model) updatePaste(msg tea.PasteMsg) tea.Cmd {
	return m.applyClipboardText(msg.Content)
}

func (m *Model) updateTerminalInput(msg terminalInputMsg) tea.Cmd {
	if msg.err != nil {
		m.status = msg.err.Error()
	}
	return nil
}

func (m *Model) updateReviewResult(msg reviewResultMsg) tea.Cmd {
	if msg.id != m.reviewID {
		return nil
	}
	m.reviewCancel = nil
	if msg.err != nil {
		if !chatCanceled(msg.err) {
			m.status = "review failed: " + msg.err.Error()
		}
		return nil
	}
	m.closeWalkthrough()
	m.viewerHighlights = nil
	m.viewPath, m.viewer, m.viewErr = "review", msg.text, ""
	m.viewerOffset = 0
	m.focus = focusViewer
	m.status = "review loaded"
	return nil
}

func (m *Model) updateEditorFinished(msg editorFinishedMsg) tea.Cmd {
	m.focus = msg.focus
	if msg.err != nil {
		m.status = "editor failed: " + msg.err.Error()
		return nil
	}
	m.files = m.listFiles(m.dir)
	if len(m.files) == 0 {
		m.selected, m.fileOffset = 0, 0
	} else {
		m.selected = minInt(m.selected, len(m.files)-1)
		m.ensureFileVisible()
	}
	if m.viewPath == msg.path {
		content, err := m.readFile(msg.path)
		if err != nil {
			m.viewErr, m.viewer = err.Error(), ""
			m.status = "editor returned; unable to refresh file"
			return nil
		}
		m.viewer, m.viewErr, m.viewerOffset = content, "", 0
	}
	m.status = "editor returned; workspace refreshed"
	if msg.review && m.reviewLoader != nil {
		return m.startReview()
	}
	return nil
}

func (m *Model) updatePTYOutput(msg ptyOutputMsg) tea.Cmd {
	if msg.err != nil {
		if msg.err == context.Canceled || msg.err == os.ErrClosed {
			return nil
		}
		if msg.err.Error() != "EOF" && (msg.session == nil || msg.session == m.pty || msg.session == m.activeShell()) {
			m.ptyErr = msg.err.Error()
		}
		return nil
	}
	session := msg.session
	if session == nil {
		session = m.pty
	}
	if session != nil && m.ownsShell(session) {
		return readPTY(session)
	}
	return nil
}

func readPTYMessage(p *ptySession) tea.Msg {
	msg := p.read(context.Background())
	msg.session = p
	return msg
}

func (m *Model) startTerminal() error {
	var p *ptySession
	var err error
	if len(m.terminalArgv) > 0 {
		p, err = startPTYArgv(m.terminalDir, m.terminalWidth(), m.terminalHeight(), m.terminalArgv)
	} else {
		p, err = startPTY(m.root, m.terminalWidth(), m.terminalHeight(), m.shell)
	}
	if err != nil {
		return err
	}
	m.pty = p
	m.status = "terminal running (provider not connected)"
	return nil
}

func (m *Model) ptySendKey(msg tea.KeyPressMsg) error {
	shell := m.activeShell()
	if shell == nil {
		return fmt.Errorf("terminal is unavailable")
	}
	key := uv.KeyPressEvent(uv.Key{Text: msg.Text, Code: msg.Code, Mod: uv.KeyMod(msg.Mod)})
	return shell.sendKey(key)
}

// Close stops the embedded child and is safe to call more than once.
func (m *Model) Close() error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	p := m.pty
	extra := append([]*ptySession(nil), m.extraShells...)
	chatCancel := m.chatCancel
	reviewCancel := m.reviewCancel
	inlineCancel := m.inlineCancel
	taskCancel := m.taskCancel
	extensionCancel := m.extensionCancel
	marketplaceCancel := m.marketplaceCancel
	projectCancel := m.projectCancel
	accountCancel := m.accountCancel
	m.mu.Unlock()
	if chatCancel != nil {
		chatCancel()
	}
	if reviewCancel != nil {
		reviewCancel()
	}
	if inlineCancel != nil {
		inlineCancel()
	}
	if taskCancel != nil {
		taskCancel()
	}
	if extensionCancel != nil {
		extensionCancel()
	}
	if marketplaceCancel != nil {
		marketplaceCancel()
	}
	if projectCancel != nil {
		projectCancel()
	}
	if accountCancel != nil {
		accountCancel()
	}
	err := p.close()
	for _, shell := range extra {
		err = errors.Join(err, shell.close())
	}
	if m.fs != nil {
		err = errors.Join(err, m.fs.Close())
	}
	return err
}

func (m *Model) handleNormalKey(stroke, text string) tea.Cmd {
	if m.prefix {
		return m.handlePrefixKey(stroke)
	}
	if m.handleChatFoldKey(stroke) {
		return nil
	}
	if cmd, handled := m.handleNormalAction(stroke); handled {
		return cmd
	}
	if m.handleNormalNavigation(stroke) {
		return nil
	}
	if text != "" && m.focus == focusFiles {
		m.status = "press Enter to open a file"
	}
	return nil
}

func (m *Model) handlePrefixKey(stroke string) tea.Cmd {
	m.prefix = false
	switch stroke {
	case "h", "left", "k", "up":
		m.moveFocus(-1)
	case "j", "down", "l", "right":
		m.moveFocus(1)
	case "v", "s":
		m.status = "use :layout to choose a workspace preset"
	case "q":
		m.status = "close view requested; terminal remains running"
	}
	return nil
}

func (m *Model) handleExitInterrupt(stroke string) tea.Cmd {
	if stroke == "ctrl+c" {
		if m.accountLoading || m.accountForm.modelsLoading {
			m.cancelAccountOp()
			m.exitArmedKey = "ctrl+c"
			m.exitArmedAt = time.Now()
			m.status = "account operation canceled — press Ctrl+C again to exit"
			return nil
		}
		if m.chatRunning {
			m.exitArmedKey = ""
			m.exitArmedAt = time.Time{}
			m.cancelChat()
			m.status = "chat canceled"
			if m.viewPath == "review" && m.reviewLoader != nil {
				return m.startReview()
			}
			return nil
		}
		if m.chatMention.open {
			m.chatMention = chatMentionState{}
			m.status = "mention popup closed"
			return nil
		}
		if m.chatInput != "" && !m.accountsOpen && !m.settingsOpen && !m.marketplaceOpen && !m.projectOpen && !m.paletteOpen && !m.sessionsOpen {
			m.chatInput = ""
			m.chatMention = chatMentionState{}
			m.exitArmedKey = "ctrl+c"
			m.exitArmedAt = time.Now()
			m.status = "input cleared — press Ctrl+C again to exit"
			return nil
		}
	}
	if m.exitArmedKey == stroke && !m.exitArmedAt.IsZero() && time.Since(m.exitArmedAt) <= 2*time.Second {
		m.cancelAccountOp()
		return tea.Quit
	}
	m.exitArmedKey = stroke
	m.exitArmedAt = time.Now()
	label := "Ctrl+C"
	if stroke == "ctrl+d" {
		label = "Ctrl+D"
	}
	m.status = fmt.Sprintf("press %s again to exit (or :q)", label)
	return nil
}

func (m *Model) handleNormalAction(stroke string) (tea.Cmd, bool) {
	switch stroke {
	case "ctrl+w":
		m.prefix = true
	case "ctrl+c", "ctrl+d":
		return m.handleExitInterrupt(stroke), true
	case "q":
		return tea.Quit, true
	case "c":
		m.focus, m.chatEditing = focusChat, true
		return nil, true
	case "i":
		if m.focus == focusViewer && m.viewerTabIsShell() {
			m.beginShellInput()
			return nil, true
		}
		if m.focus == focusChat {
			m.chatEditing = true
		}
		return nil, true
	case "r":
		return m.startReview(), true
	case "R":
		if m.viewPath == "tasks" {
			return m.startTaskMonitor(), true
		}
		if m.focus == focusChat {
			m.startChatTabRename()
			return nil, true
		}
	case "y", "Y", "ctrl+y":
		if m.focus == focusChat {
			return m.copyChatSelectionOrLastReply(), true
		}
		return nil, false
	case "x":
		if m.focus == focusViewer && m.viewerTabIsFile() && len(m.viewerFlowSteps) > 0 {
			m.viewerExplanationOpen = !m.viewerExplanationOpen
			return nil, true
		}
		return nil, false
	case "n":
		if m.focus == focusViewer && m.viewerTabIsFile() && len(m.viewerFlowSteps) > 0 {
			m.stepViewerFlow(1)
			return nil, true
		}
		return nil, false
	case "p":
		if m.focus == focusViewer && m.viewerTabIsFile() && len(m.viewerFlowSteps) > 0 {
			m.stepViewerFlow(-1)
			return nil, true
		}
		return nil, false
	case "m", "M":
		if m.focus == focusChat {
			return m.openChatModelPicker(), true
		}
		return nil, false
	case "N", "ctrl+t":
		if m.focus == focusChat || stroke == "ctrl+t" {
			return m.openNewChatTab(""), true
		}
		return nil, false
	case "alt+left", "{":
		if m.focus == focusChat {
			return m.switchChatTab(-1), true
		}
		return nil, false
	case "alt+right", "}":
		if m.focus == focusChat {
			return m.switchChatTab(1), true
		}
		return nil, false
	case "e":
		return m.startEditor(), true
	case "E":
		return m.startInlineEditor(), true
	case ":", "ctrl+p":
		m.paletteOpen = true
		m.paletteInput = ""
		m.paletteSelected = -1
		m.paletteCompletion = paletteCompletionState{}
		m.paletteError, m.paletteHelp = "", ""
		m.focus = focusViewer
		m.status = "command palette"
	case "?":
		m.openHelp()
	case "[", "alt+[":
		if m.viewerTabCount() > 1 {
			m.focus = focusViewer
			m.switchViewerTab(-1)
			return nil, true
		}
		return nil, false
	case "]", "alt+]":
		if m.viewerTabCount() > 1 {
			m.focus = focusViewer
			m.switchViewerTab(1)
			return nil, true
		}
		return nil, false
	case "t":
		m.openShellTab()
	case "T":
		return m.addShellTab(), true
	case "escape", "esc":
		m.clearChatSelection()
		m.mode = ModeNormal
	case "enter", "return":
		if m.focus == focusFiles {
			m.openSelected()
		} else if m.focus == focusViewer && m.viewerTabIsShell() {
			m.beginShellInput()
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *Model) handleNormalNavigation(stroke string) bool {
	if m.scrollExplanation(stroke) {
		return true
	}
	switch stroke {
	case "up", "k":
		if m.focus == focusChat {
			m.chatScroll--
			m.clampChatScroll()
		} else if m.focus == focusFiles && len(m.files) > 0 {
			m.selected = (m.selected + len(m.files) - 1) % len(m.files)
			m.ensureFileVisible()
		} else if m.focus == focusViewer && m.viewerTabIsFile() && m.viewerOffset > 0 {
			m.viewerOffset--
		} else if m.focus == focusViewer && m.viewerTabIsExec() && m.terminalScroll > 0 {
			m.terminalScroll--
		}
	case "down", "j":
		if m.focus == focusChat {
			m.chatScroll++
			m.clampChatScroll()
		} else if m.focus == focusFiles && len(m.files) > 0 {
			m.selected = (m.selected + 1) % len(m.files)
			m.ensureFileVisible()
		} else if m.focus == focusViewer && m.viewerTabIsFile() {
			m.viewerOffset++
			m.clampViewerOffset()
		} else if m.focus == focusViewer && m.viewerTabIsExec() {
			m.terminalScroll++
		}
	case "pageup":
		if m.focus == focusChat {
			m.chatScroll -= m.chatViewport()
			m.clampChatScroll()
		} else if m.focus == focusViewer {
			m.viewerOffset -= maxInt(m.viewerViewport(), 1)
			m.clampViewerOffset()
		}
	case "pagedown":
		if m.focus == focusChat {
			m.chatScroll += m.chatViewport()
			m.clampChatScroll()
		} else if m.focus == focusViewer {
			m.viewerOffset += maxInt(m.viewerViewport(), 1)
			m.clampViewerOffset()
		}
	case "right", "l":
		if m.focusVisible(focusChat) {
			m.focus = focusChat
		} else {
			m.status = "chat hidden in this layout"
		}
	case "left", "h":
		if m.focusVisible(focusFiles) {
			m.focus = focusFiles
		} else {
			m.status = "files hidden in this layout"
		}
	case "space":
		m.focus = focusFiles
	default:
		return false
	}
	return true
}

func (m *Model) openSelected() {
	if len(m.files) == 0 || m.selected < 0 || m.selected >= len(m.files) {
		return
	}
	entry := m.files[m.selected]
	if entry.Dir {
		if entry.Path == ".." {
			m.dir = filepath.ToSlash(filepath.Dir(filepath.FromSlash(m.dir)))
			if m.dir == "." {
				m.dir = ""
			}
		} else {
			m.dir = filepath.ToSlash(filepath.Join(m.dir, entry.Path))
		}
		m.files = m.listFiles(m.dir)
		m.selected = 0
		m.fileOffset = 0
		m.status = "directory: " + displayDir(m.dir)
		return
	}
	path := filepath.ToSlash(filepath.Join(m.dir, entry.Path))
	content, err := m.readFile(path)
	if err != nil {
		m.viewErr, m.viewer = err.Error(), ""
		m.status = "unable to read file"
		return
	}
	m.closeWalkthrough()
	m.viewerHighlights = nil
	m.viewPath, m.viewer, m.viewErr = path, content, ""
	m.viewerOffset = 0
	m.focus = focusViewer
	m.status = "read-only file viewer"
}

func (m *Model) fileViewport() int {
	return maxInt(m.mainPaneHeight()-3, 1)
}

func (m *Model) ensureFileVisible() {
	viewport := m.fileViewport()
	if m.selected < m.fileOffset {
		m.fileOffset = m.selected
	}
	if m.selected >= m.fileOffset+viewport {
		m.fileOffset = m.selected - viewport + 1
	}
	maxOffset := maxInt(len(m.files)-viewport, 0)
	if m.fileOffset > maxOffset {
		m.fileOffset = maxOffset
	}
}

func (m *Model) viewerViewport() int {
	height := m.mainPaneHeight() - 3
	if len(m.viewerFlowSteps) > 0 && !m.viewerExplanationOpen {
		height -= len(m.walkthroughPreviewLines()) + 4
	}
	return max(1, height)
}

func (m *Model) clampViewerOffset() {
	lines := strings.Count(m.viewer, "\n") + 1
	maxOffset := maxInt(lines-m.viewerViewport(), 0)
	if m.viewerOffset > maxOffset {
		m.viewerOffset = maxOffset
	}
	if m.viewerOffset < 0 {
		m.viewerOffset = 0
	}
}

func (m *Model) View() tea.View {
	v := tea.NewView(m.render())
	v.AltScreen = true
	v.ReportFocus = true
	if p := m.colors(); p.enabled() {
		v.BackgroundColor = p.chrome
		v.ForegroundColor = p.ink
	}
	if m.mode == ModeNormal || m.mode == ModeTerminal {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m *Model) render() string {
	if request := m.pendingExecutionApproval(); request != nil {
		return m.padRender(m.renderExecutionApproval(request))
	}
	return m.padRender(m.renderScreen())
}

func (m *Model) padRender(view string) string {
	if m.height <= 0 || m.width <= 0 {
		return view
	}
	lines := strings.Split(view, "\n")
	blank := truncatePad("", m.width)
	for len(lines) < m.height {
		lines = append(lines, blank)
	}
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	for i, line := range lines {
		if ansi.StringWidth(line) != m.width {
			lines[i] = truncatePad(line, m.width)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderScreen() string {
	p := m.colors()
	statusText := sanitizeDisplay(m.status)
	if m.inline != nil && m.inline.dirty {
		statusText += " *"
	}
	if err := m.pty.inputError(); err != nil {
		statusText = sanitizeDisplay(err.Error())
	}
	if m.ptyErr != "" {
		statusText += "  error: " + sanitizeDisplay(m.ptyErr)
	}
	left := p.brand("  "+m.brandMark()+" BIFROST  ") + p.quietText("/  "+displayDir(m.dir))
	if m.accountLabel != "" {
		if p.enabled() {
			left += lipgloss.NewStyle().Foreground(p.cyan).Background(lipgloss.Color("#213449")).Padding(0, 1).Render(m.providerIcon(m.connection.Provider) + " " + sanitizeDisplay(m.accountLabel))
		} else {
			left += "  [" + sanitizeDisplay(m.accountLabel) + "]"
		}
	}
	right := p.accentText(" "+m.mode.String()+" ") + p.mutedText(" "+statusText+" ")
	status := ""
	if ansi.StringWidth(right) >= m.width {
		status = truncatePad(right, m.width)
	} else {
		leftWidth := maxInt(m.width-ansi.StringWidth(right)-1, 1)
		header := truncatePad(left, leftWidth) + strings.Repeat(" ", maxInt(m.width-leftWidth-ansi.StringWidth(right), 1)) + right
		status = truncatePad(header, m.width)
	}
	if p.enabled() {
		status = surfaceText(status, p.ink, p.chrome)
	}
	available := maxInt(m.height-1-boolInt(m.footerVisible()), 1)
	if m.settingsOpen {
		body := status + "\n" + m.renderSettingsPanel(m.width, available)
		if m.footerVisible() {
			body += "\n" + m.renderFooter()
		}
		return body
	}
	if m.marketplaceOpen || m.paletteOpen || m.projectOpen || m.sessionsOpen {
		var panel string
		if m.marketplaceOpen {
			panel = m.renderMarketplacePanel(m.width, available)
		} else if m.projectOpen {
			panel = m.renderProjectPanel(m.width, available)
		} else if m.sessionsOpen {
			panel = renderPanelTheme(m.theme, "CONVERSATION HISTORY  ·  SESSIONS", m.renderSessionsView(), m.width, true, available)
		} else {
			panel = m.renderPalettePanel(m.width, available)
		}
		body := status + "\n" + panel
		if m.footerVisible() {
			body += "\n" + m.renderFooter()
		}
		return body
	}
	termHeight := m.terminalPaneHeight()
	mainHeight := maxInt(available-termHeight, 1)
	widths := m.paneWidths(m.width)
	var main string
	if m.width < 60 {
		// At narrow widths a single pane stays readable. The status line and
		// help text identify the available views; focus keys switch the pane.
		if m.focus == focusChat {
			main = m.renderChatPanel(m.width, mainHeight, true)
		} else {
			main = renderPanelTheme(m.theme, strings.ToUpper(m.focus.String()), m.focusContent(), m.width, true, mainHeight)
		}
	} else if m.activeLayout().name == LayoutFocus && !m.layoutOverlayVisible() {
		main = m.renderChatPanel(m.width, mainHeight, m.focus == focusChat)
	} else if m.activeLayout().name == LayoutFocus {
		main = renderPanelTheme(m.theme, m.layoutOverlayTitle(), m.layoutOverlayContent(), m.width, true, mainHeight)
	} else {
		centerTitle := m.viewerPaneTitle()
		parts := make([]string, 0, 3)
		order := []int{0, 1, 2}
		if m.activeLayout().name == LayoutConversation {
			order = []int{0, 2, 1}
		}
		for _, index := range order {
			switch index {
			case 0:
				if widths[0] > 0 {
					parts = append(parts, renderPanelTheme(m.theme, m.leftPaneTitle(), m.renderFiles(), widths[0], m.focus == focusFiles, mainHeight))
				}
			case 1:
				if widths[1] > 0 {
					parts = append(parts, renderPanelTheme(m.theme, centerTitle, m.viewerPaneBody(), widths[1], m.focus == focusViewer, mainHeight))
				}
			case 2:
				if widths[2] > 0 {
					parts = append(parts, m.renderChatPanel(widths[2], mainHeight, m.focus == focusChat))
				}
			}
		}
		main = m.joinWorkspacePanels(parts...)
	}
	body := status + "\n" + main
	if m.footerVisible() {
		body += "\n" + m.renderFooter()
	}
	if m.helpOpen {
		body = m.overlayHelpModal(body)
	}
	return body
}

func (m *Model) renderFiles() string {
	p := m.colors()
	if len(m.files) == 0 {
		return p.mutedText("(empty workspace)")
	}
	var b strings.Builder
	if m.dir == "" && m.hasLinkedProjectRepos() {
		if desc := strings.TrimSpace(m.projectStatus.ProjectDescription); desc != "" {
			b.WriteString(p.quietText(sanitizeDisplay(desc)))
			b.WriteByte('\n')
		}
	} else if dir := displayDir(m.dir); dir != "." {
		b.WriteString(sanitizeDisplay(dir))
		b.WriteByte('\n')
	}
	limit := minInt(len(m.files), m.fileOffset+m.fileViewport())
	for i, entry := range m.files[m.fileOffset:limit] {
		i += m.fileOffset
		prefix := "  "
		if i == m.selected {
			prefix = "› "
		}
		kind := ""
		if entry.Dir {
			kind = "/"
		}
		selected := i == m.selected
		repoEntry, isLinkedRepo := ProjectRepoEntry{}, false
		if m.dir == "" && entry.Dir {
			repoEntry, isLinkedRepo = m.linkedRepoForEntry(entry.Path)
		}

		if isLinkedRepo {
			branch := repoEntry.CurrentBranch
			if branch == "" {
				branch = repoEntry.DefaultBranch
			}
			plainGit := formatGitStatsPlain(repoEntry.GitAdded, repoEntry.GitModified, repoEntry.GitDeleted)
			baseLabel := prefix + "◈ " + sanitizeDisplay(entry.Path) + "/"
			if selected {
				line := baseLabel + " " + plainGit
				if branch != "" && len(line)+len(branch)+3 <= 28 {
					line += " · " + sanitizeDisplay(branch)
				}
				if p.enabled() {
					line = lipgloss.NewStyle().Foreground(p.ink).Background(lipgloss.Color("#3a2f68")).Bold(true).Render(line)
				} else {
					line = "▶ " + sanitizeDisplay(entry.Path) + "/ " + plainGit
				}
				b.WriteString(line)
			} else {
				line := p.violetText(baseLabel) + " " + p.formatGitStatsColored(repoEntry.GitAdded, repoEntry.GitModified, repoEntry.GitDeleted, false)
				if branch != "" && len(baseLabel)+len(plainGit)+len(branch)+4 <= 28 {
					line += p.quietText(" " + sanitizeDisplay(branch))
				}
				b.WriteString(line)
			}
			b.WriteByte('\n')
			continue
		}

		line := prefix + sanitizeDisplay(entry.Path) + kind
		if selected {
			if p.enabled() {
				line = lipgloss.NewStyle().Foreground(p.ink).Background(lipgloss.Color("#3a2f68")).Bold(true).Render(line)
			} else {
				line = "▶ " + sanitizeDisplay(entry.Path) + kind
			}
		} else if entry.Dir {
			line = p.cyanText(line)
		} else {
			line = p.mutedText(line)
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if m.fileOffset > 0 {
		b.WriteString(p.quietText("↑ more entries\n"))
	}
	if limit < len(m.files) {
		b.WriteString(p.quietText(fmt.Sprintf("… %d more entries", len(m.files)-limit)))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func displayDir(dir string) string {
	if dir == "" {
		return "."
	}
	return filepath.ToSlash(dir)
}

func (m *Model) renderViewer() string {
	if m.accountsOpen {
		return m.renderAccounts()
	}
	if m.marketplaceOpen {
		return m.renderMarketplace()
	}
	if m.paletteOpen {
		return m.renderPalette()
	}
	if m.mode == ModeInline {
		return m.renderInlineEditor()
	}
	if m.viewPath == "tasks" {
		return m.renderTasks()
	}
	if m.viewerExplanationOpen && len(m.viewerFlowSteps) > 0 {
		return m.renderWalkthroughExplanation()
	}
	if m.viewErr != "" {
		return m.colors().dangerText(m.viewErr)
	}
	if m.viewPath == "" {
		return "◆ WELCOME TO BIFROST\n\n" +
			"Select a file to inspect it here.\n" +
			"Chat, review, and terminal activity\n" +
			"stay visible beside your workspace.\n\n" +
			m.colors().mutedText("Ctrl-W h/j/k/l  focus panes\n:c  chat   :  commands   ?  help")
	}
	allLines := strings.Split(m.viewer, "\n")
	start := minInt(m.viewerOffset, maxInt(len(allLines)-1, 0))
	end := minInt(start+m.viewerViewport(), len(allLines))
	p := m.colors()
	isDiff := m.viewPath == "review" || strings.HasPrefix(m.viewPath, "diff") || strings.HasSuffix(strings.ToLower(m.viewPath), ".diff") || strings.HasSuffix(strings.ToLower(m.viewPath), ".patch")
	isGo := strings.HasSuffix(strings.ToLower(m.viewPath), ".go")

	var formatted []string
	if start > 0 {
		formatted = append(formatted, "↑ previous lines")
	}
	for i := start; i < end; i++ {
		lineNum := i + 1
		rawLine := sanitizeDisplay(allLines[i])
		highlighted, label := m.viewerLineHighlight(lineNum)
		if isDiff && p.enabled() {
			switch {
			case strings.HasPrefix(rawLine, "+") && !strings.HasPrefix(rawLine, "+++"):
				rawLine = p.okText(rawLine)
			case strings.HasPrefix(rawLine, "-") && !strings.HasPrefix(rawLine, "---"):
				rawLine = p.dangerText(rawLine)
			case strings.HasPrefix(rawLine, "@@"):
				rawLine = p.cyanText(rawLine)
			}
		} else if isGo {
			rawLine = colorizeGo(rawLine, p)
		}
		if highlighted {
			prefix := "▶ "
			if p.enabled() {
				prefix = p.cyanText("▶ ")
				rawLine = prefix + p.accentText(fmt.Sprintf("%4d │ ", lineNum)) + rawLine
			} else {
				rawLine = fmt.Sprintf(">> %4d | %s", lineNum, rawLine)
			}
			if label != "" {
				rawLine += "  " + p.badge(" ✦ "+sanitizeDisplay(label)+" ")
			}
		} else if len(m.viewerHighlights) > 0 {
			if p.enabled() {
				rawLine = p.quietText(fmt.Sprintf("  %4d │ ", lineNum)) + rawLine
			} else {
				rawLine = fmt.Sprintf("   %4d | %s", lineNum, rawLine)
			}
		}
		formatted = append(formatted, rawLine)
	}
	if end < len(allLines) {
		formatted = append(formatted, "… more lines below")
	}
	return m.viewerHeading() + "\n" + strings.Join(formatted, "\n")
}

func (m *Model) renderTerminal() string {
	return m.renderActiveTerminalTab()
}

func (m *Model) renderPTYTerminal() string {
	if m.ptyErr != "" {
		return m.colors().dangerText("terminal unavailable: " + m.ptyErr)
	}
	if m.pty == nil {
		return "starting terminal..."
	}
	text := m.pty.snapshot()
	if text == "" {
		return m.colors().mutedText("Terminal ready. Press t, then type. Ctrl-] returns to Normal.")
	}
	return text
}

func (m *Model) focusContent() string {
	switch m.focus {
	case focusFiles:
		return m.renderFiles()
	case focusViewer:
		return m.viewerPaneBody()
	case focusChat:
		return m.renderChat()
	default:
		return m.renderTerminal()
	}
}

func (m *Model) terminalPaneHeight() int { return 0 }

func (m *Model) terminalWidth() int {
	width := m.width
	if m.width >= 60 {
		if pane := m.paneWidths(m.width)[1]; pane > 0 {
			width = pane
		}
	}
	return maxInt(width-2, 1)
}
func (m *Model) terminalHeight() int { return maxInt(m.mainPaneHeight()-3, 1) }
func (m *Model) mainPaneHeight() int {
	return maxInt(maxInt(m.height-1-boolInt(m.footerVisible()), 1)-m.terminalPaneHeight(), 1)
}

func (m *Model) paneWidths(width int) [3]int {
	layout := m.activeLayout()
	if layout.name == LayoutFocus {
		return [3]int{0, 0, maxInt(width, 1)}
	}
	available := maxInt(width-2, 3)
	var result [3]int
	used := 0
	for i, ratio := range layout.proportions {
		result[i] = maxInt(available*ratio/100, 1)
		used += result[i]
	}
	for used < available {
		result[2]++
		used++
	}
	for used > available {
		for i := 2; i >= 0 && used > available; i-- {
			if result[i] > 1 {
				result[i]--
				used--
			}
		}
	}
	return result
}

func (m *Model) layoutOverlayVisible() bool {
	return m.accountsOpen || m.marketplaceOpen || m.paletteOpen || m.mode == ModeInline || m.viewPath == "tasks"
}

func (m *Model) layoutOverlayTitle() string {
	switch {
	case m.accountsOpen:
		return "ACCOUNTS"
	case m.marketplaceOpen:
		return "MARKETPLACE"
	case m.paletteOpen:
		return "COMMAND PALETTE"
	case m.mode == ModeInline:
		return "EDITOR"
	case m.viewPath == "tasks":
		return "TASKS"
	default:
		return "CHAT"
	}
}

func (m *Model) layoutOverlayContent() string {
	switch {
	case m.accountsOpen:
		return m.renderAccounts()
	case m.marketplaceOpen:
		return m.renderMarketplace()
	case m.paletteOpen:
		return m.renderPalette()
	case m.mode == ModeInline:
		return m.renderInlineEditor()
	case m.viewPath == "tasks":
		return m.renderTasks()
	default:
		return m.renderChat()
	}
}

func layoutWidths(width int) (int, int) {
	if width >= 120 {
		return 22, 46
	}
	if width >= 100 {
		return 20, 38
	}
	if width >= 80 {
		return 18, 32
	}
	return maxInt(width/4, 8), maxInt(width/4, 8)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (m *Model) footerVisible() bool { return m.height >= 8 }

func (m *Model) renderFooter() string {
	p := m.colors()
	status := sanitizeDisplay(m.status)
	if max := maxInt(m.width/4, 10); ansi.StringWidth(status) > max {
		status = ansi.Truncate(status, max, "…")
	}
	rightText := " " + m.mode.String() + " · " + status
	if m.width >= 96 {
		rightText += " · ^W panes · : commands"
	}
	right := p.accentText(rightText + " ")
	available := maxInt(m.width-ansi.StringWidth(right)-1, 1)
	connection := m.renderConnectionBar(available)
	footer := truncatePad(connection+" "+right, m.width)
	if p.enabled() {
		footer = surfaceText(footer, p.ink, p.chrome)
	}
	return footer
}

func colorizeGo(content string, p uiPalette) string {
	if !p.enabled() || content == "" {
		return content
	}
	src := []byte(content)
	var s scanner.Scanner
	file := token.NewFileSet().AddFile("viewer.go", -1, len(src))
	s.Init(file, src, nil, scanner.ScanComments)
	var out strings.Builder
	last := 0
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		start := file.Offset(pos)
		if start < last || start > len(src) {
			continue
		}
		out.Write(src[last:start])
		if tok == token.SEMICOLON && lit == "\n" {
			// scanner reports implicit semicolons for line endings. The newline
			// is already present in src (or absent at EOF), so emitting lit
			// would duplicate real line endings and invent a final one.
			last = start
			continue
		}
		styled := lit
		switch {
		case tok == token.COMMENT:
			styled = p.quietText(lit)
		case tok == token.STRING || tok == token.CHAR:
			styled = p.warnText(lit)
		case tok.IsKeyword():
			styled = p.accentText(lit)
		case tok >= token.INT && tok <= token.IMAG:
			styled = p.violetText(lit)
		case tok == token.IDENT && len(lit) > 0 && lit[0] >= 'A' && lit[0] <= 'Z':
			styled = p.cyanText(lit)
		}
		out.WriteString(styled)
		last = start + len(lit)
	}
	if last < len(src) {
		out.Write(src[last:])
	}
	return out.String()
}

func panel(title, content string, width int, focused bool, heights ...int) string {
	return renderPanelTheme("midnight", title, content, width, focused, heights...)
}

func panelTrusted(title, content string, width int, focused bool, heights ...int) string {
	// The terminal emulator is the producer of this ANSI content. Keep its
	// styles while still applying cell-aware clipping and padding below.
	return renderPanelTheme("midnight", title, content, width, focused, heights...)
}

func renderPanel(title, content string, width int, focused bool, heights ...int) string {
	return renderPanelTheme("midnight", title, content, width, focused, heights...)
}

func renderPanelTheme(theme, title, content string, width int, focused bool, heights ...int) string {
	// Content has already been sanitized at each producer boundary. Keeping
	// ANSI here lets the small amount of semantic color in lists and status
	// text survive into the panel while truncatePad remains cell-aware.
	width = maxInt(width, 1)
	if width < 3 {
		return truncatePad(title, width)
	}
	inner := width - 2
	lines := strings.Split(content, "\n")
	panelHeight := 0
	if len(heights) > 0 {
		panelHeight = maxInt(heights[0], 3)
	}
	innerHeight := len(lines)
	if panelHeight > 0 {
		innerHeight = maxInt(panelHeight-2, 1)
		if len(lines) > innerHeight {
			lines = lines[:innerHeight]
		}
		for len(lines) < innerHeight {
			lines = append(lines, "")
		}
	}
	p := paletteFor(theme)
	for i := range lines {
		lines[i] = truncatePad(lines[i], inner)
	}
	label := title
	if focused {
		label = "● " + label
	}
	label = " " + label + " "
	if ansi.StringWidth(label) > inner {
		label = truncatePad(label, inner)
	}
	result := make([]string, 0, len(lines)+2)
	surface := p.surface
	if theme == "light" {
		if strings.Contains(title, "TERMINAL") {
			surface = p.recessed
		}
	} else if p.enabled() {
		if strings.Contains(title, "TERMINAL") {
			surface = p.recessed
		} else if strings.Contains(title, "ACTIVITY") || strings.Contains(title, "ACCOUNTS") || strings.Contains(title, "PROJECT") {
			surface = lipgloss.Color("#181c30")
		} else if strings.Contains(title, "VIEWER") || strings.Contains(title, "EDITOR") {
			surface = lipgloss.Color("#131726")
		}
	}
	border := lipgloss.Color("#30384e")
	topLeft, topRight, bottomLeft, bottomRight, vertical, horizontal := "╭", "╮", "╰", "╯", "│", "─"
	if focused {
		topLeft, topRight, bottomLeft, bottomRight, vertical, horizontal = "┏", "┓", "┗", "┛", "┃", "━"
	}
	if p.enabled() {
		border = p.border
		if focused {
			border = p.cyan
			label = lipgloss.NewStyle().Foreground(p.cyan).Bold(true).Render(label)
		} else {
			label = p.mutedText(label)
		}
	}
	borderLine := func(s string) string {
		if !p.enabled() {
			return s
		}
		return surfaceText(lipgloss.NewStyle().Foreground(border).Render(s), border, surface)
	}
	labelWidth := ansi.StringWidth(label)
	result = append(result, borderLine(topLeft+label+strings.Repeat(horizontal, maxInt(inner-labelWidth, 0))+topRight))
	for _, line := range lines {
		if p.enabled() {
			line = surfaceText(line, p.ink, surface)
		}
		left, right := vertical, vertical
		if p.enabled() {
			left = surfaceText(lipgloss.NewStyle().Foreground(border).Render(left), border, surface)
			right = surfaceText(lipgloss.NewStyle().Foreground(border).Render(right), border, surface)
		}
		result = append(result, left+line+right)
	}
	result = append(result, borderLine(bottomLeft+strings.Repeat(horizontal, inner)+bottomRight))
	return strings.Join(result, "\n")
}

func hjoin(parts ...string) string {
	return hjoinWithSeparators(nil, parts...)
}

func hjoinWithSeparators(separators []string, parts ...string) string {
	if len(parts) == 0 {
		return ""
	}
	lines := make([][]string, len(parts))
	maxLines := 0
	widths := make([]int, len(parts))
	for i, part := range parts {
		lines[i] = strings.Split(part, "\n")
		for _, line := range lines[i] {
			widths[i] = maxInt(widths[i], ansi.StringWidth(line))
		}
		if len(lines[i]) > maxLines {
			maxLines = len(lines[i])
		}
	}
	var b strings.Builder
	for row := 0; row < maxLines; row++ {
		if row > 0 {
			b.WriteByte('\n')
		}
		for i := range lines {
			if i > 0 {
				if i-1 < len(separators) {
					b.WriteString(separators[i-1])
				} else {
					b.WriteByte(' ')
				}
			}
			if row < len(lines[i]) {
				b.WriteString(truncatePad(lines[i][row], widths[i]))
			} else {
				b.WriteString(strings.Repeat(" ", widths[i]))
			}
		}
	}
	return b.String()
}

func truncatePad(s string, width int) string {
	if width < 1 {
		return ""
	}
	if ansi.StringWidth(s) > width {
		s = ansi.Truncate(s, width, "…")
	}
	return s + strings.Repeat(" ", maxInt(width-ansi.StringWidth(s), 0))
}

func sanitizeDisplay(s string) string {
	s = ansi.Strip(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteRune(r)
		case r == '\t':
			b.WriteString("    ")
		case r == '\r':
			b.WriteRune('�')
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f):
			b.WriteRune('�')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

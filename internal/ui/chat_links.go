package ui

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type chatSelectionState struct {
	dragging bool
	active   bool
	startRow int // transcript row
	startCol int // cell column within chatReadingWidth
	endRow   int // transcript row
	endCol   int // cell column within chatReadingWidth
}

type chatLinkTarget struct {
	URL     string // http/https URL if external
	RelPath string // relative workspace file/dir path if local
	IsDir   bool
	LineNum int // 1-based line number if specified (e.g. path:42)
}

var (
	markdownLinkTokenRe = regexp.MustCompile(`(\([^()\s]+\)|https?://[^\s<>)"']+|@[A-Za-z0-9_./:-]+|[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)+(?::\d+)?/?|[A-Za-z0-9_.-]+\.(?:go|md|json|yaml|yml|toml|ts|tsx|js|jsx|py|rs|sh|sql|css|html|txt)(?::\d+)?)`)
)

func (m *Model) clearChatSelection() {
	m.chatSelection = chatSelectionState{}
}

func (m *Model) normalizedChatSelection() (r1, c1, r2, c2 int, ok bool) {
	s := m.chatSelection
	if !s.active && !s.dragging {
		return 0, 0, 0, 0, false
	}
	if s.startRow == s.endRow && s.startCol == s.endCol {
		return 0, 0, 0, 0, false
	}
	r1, c1, r2, c2 = s.startRow, s.startCol, s.endRow, s.endCol
	if r1 > r2 || (r1 == r2 && c1 > c2) {
		r1, c1, r2, c2 = r2, c2, r1, c1
	}
	return r1, c1, r2, c2, true
}

func (m *Model) selectedChatText() string {
	r1, c1, r2, c2, ok := m.normalizedChatSelection()
	if !ok {
		return ""
	}
	lines := m.chatTranscriptLines()
	if len(lines) == 0 || r1 >= len(lines) {
		return ""
	}
	marginW := len(m.chatMargin())
	r1 = maxInt(r1, 0)
	r2 = minInt(r2, len(lines)-1)
	var out []string
	for r := r1; r <= r2; r++ {
		plain := ansi.Strip(lines[r])
		if ansi.StringWidth(plain) > marginW {
			plain = ansi.Cut(plain, marginW, ansi.StringWidth(plain))
		} else {
			plain = ""
		}
		lineW := ansi.StringWidth(plain)
		start := 0
		end := lineW
		if r == r1 {
			start = minInt(maxInt(c1, 0), lineW)
		}
		if r == r2 {
			end = minInt(maxInt(c2, 0), lineW)
		}
		if end > start {
			out = append(out, strings.TrimRight(ansi.Cut(plain, start, end), " "))
		} else if r > r1 && r < r2 {
			out = append(out, "")
		}
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// copyChatSelectionOrLastReply copies either the active mouse selection or the
// most recent assistant/user message to the system and OSC 52 clipboards.
func (m *Model) copyChatSelectionOrLastReply() tea.Cmd {
	if sel := m.selectedChatText(); sel != "" {
		m.status = fmt.Sprintf("copied selection (%d chars) to clipboard", len([]rune(sel)))
		return m.writeClipboardText(sel)
	}
	for i := len(m.chatLines) - 1; i >= 0; i-- {
		line := m.chatLines[i]
		if (line.kind == "assistant" || line.kind == "user") && strings.TrimSpace(line.text) != "" {
			text := strings.TrimSpace(line.text)
			m.status = fmt.Sprintf("copied %s message (%d chars) to clipboard", line.kind, len([]rune(text)))
			return m.writeClipboardText(text)
		}
	}
	m.status = "nothing in chat to copy yet"
	return nil
}

func (m *Model) applyChatSelectionHighlight(rowIdx int, line string) string {
	r1, c1, r2, c2, ok := m.normalizedChatSelection()
	if !ok || rowIdx < r1 || rowIdx > r2 {
		return line
	}
	p := m.colors()
	if !p.enabled() {
		return line
	}
	marginW := len(m.chatMargin())
	totalW := ansi.StringWidth(line)
	if totalW <= marginW {
		return line
	}
	contentW := totalW - marginW
	start := 0
	end := contentW
	if rowIdx == r1 {
		start = minInt(maxInt(c1, 0), contentW)
	}
	if rowIdx == r2 {
		end = minInt(maxInt(c2, 0), contentW)
	}
	if end <= start {
		return line
	}
	absStart := marginW + start
	absEnd := marginW + end
	left := ansi.Cut(line, 0, absStart)
	midPlain := ansi.Strip(ansi.Cut(line, absStart, absEnd))
	right := ansi.Cut(line, absEnd, totalW)
	hi := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#ffffff")).
		Background(lipgloss.Color("#3b4d7a")).
		Render(midPlain)
	return left + hi + right
}

func (m *Model) chatMousePosition(mouse tea.Mouse) (transcriptRow int, col int, inTranscript bool) {
	x, width, visible := m.chatPaneBounds()
	if !visible || mouse.X < x || mouse.X >= x+width || mouse.Y < 1 || mouse.Y >= 1+m.mainPaneHeight() {
		return 0, 0, false
	}
	innerHeight := maxInt(m.mainPaneHeight()-2, 1)
	topRows := 0
	if m.chatTopBarVisible(innerHeight) {
		topRows = len(m.renderChatTopBar(m.chatPaneInnerWidth()))
	}
	contentRow := mouse.Y - 2
	row := contentRow - topRows
	if row < 0 || row >= m.chatViewport() {
		return 0, 0, false
	}
	startX := x + 1 + len(m.chatMargin())
	relCol := maxInt(mouse.X-startX, 0)
	return row + m.chatScroll, relCol, true
}

func (m *Model) handleChatMouseMotion(mouse tea.Mouse) {
	if !m.chatSelection.dragging {
		return
	}
	tRow, col, ok := m.chatMousePosition(mouse)
	if !ok {
		return
	}
	m.chatSelection.endRow = tRow
	m.chatSelection.endCol = col
	if m.chatSelection.startRow != m.chatSelection.endRow || m.chatSelection.startCol != m.chatSelection.endCol {
		m.chatSelection.active = true
	}
}

func (m *Model) handleChatMouseRelease(mouse tea.Mouse) tea.Cmd {
	if !m.chatSelection.dragging {
		return nil
	}
	m.chatSelection.dragging = false
	if tRow, col, ok := m.chatMousePosition(mouse); ok {
		m.chatSelection.endRow = tRow
		m.chatSelection.endCol = col
	}
	if sel := m.selectedChatText(); sel != "" {
		m.chatSelection.active = true
		m.status = fmt.Sprintf("copied %d selected chars (y to copy again · Esc clear)", len([]rune(sel)))
		return m.writeClipboardText(sel)
	}
	// Single click without drag: already handled on mouse click
	m.chatSelection.active = false
	return nil
}

func (m *Model) activateChatClickAt(transcriptRow, col int) tea.Cmd {
	lines := m.chatTranscriptLines()
	if transcriptRow < 0 || transcriptRow >= len(lines) {
		return nil
	}
	// First check if this row is a fold header and the click was near the fold mark
	for _, fold := range m.chatFolds {
		if fold.row == transcriptRow {
			// Still allow clicking a link on the header if there is one at col, otherwise toggle fold
			if target, ok := m.findLinkTargetInRow(lines[transcriptRow], col); ok {
				return m.openChatLinkTarget(target)
			}
			m.toggleChatFold(fold.line)
			return nil
		}
	}
	if target, ok := m.findLinkTargetInRow(lines[transcriptRow], col); ok {
		return m.openChatLinkTarget(target)
	}
	return nil
}

func (m *Model) findLinkTargetInRow(renderedRow string, col int) (chatLinkTarget, bool) {
	plain := ansi.Strip(renderedRow)
	marginW := len(m.chatMargin())
	if ansi.StringWidth(plain) <= marginW {
		return chatLinkTarget{}, false
	}
	content := ansi.Cut(plain, marginW, ansi.StringWidth(plain))
	matches := markdownLinkTokenRe.FindAllStringIndex(content, -1)
	for _, idx := range matches {
		token := content[idx[0]:idx[1]]
		startCol := ansi.StringWidth(content[:idx[0]])
		endCol := startCol + ansi.StringWidth(token)
		if col >= startCol-1 && col <= endCol {
			if target, ok := m.resolveTokenAsLinkTarget(token); ok {
				return target, true
			}
		}
	}
	// Also check if the word under cursor is a known workspace file/directory or linked repo name!
	word := extractPathTokenAtCol(content, col)
	if word != "" {
		if target, ok := m.resolveTokenAsLinkTarget(word); ok {
			return target, true
		}
	}
	return chatLinkTarget{}, false
}

func extractPathTokenAtCol(content string, col int) string {
	// Convert cell column to byte range around whitespace/punctuation
	runes := []rune(content)
	curCol := 0
	runeIdx := -1
	for i, r := range runes {
		w := ansi.StringWidth(string(r))
		if col >= curCol && col < curCol+maxInt(w, 1) {
			runeIdx = i
			break
		}
		curCol += w
	}
	if runeIdx < 0 {
		return ""
	}
	isTokenRune := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			strings.ContainsRune("_./:-@", r)
	}
	if !isTokenRune(runes[runeIdx]) {
		return ""
	}
	start := runeIdx
	for start > 0 && isTokenRune(runes[start-1]) {
		start--
	}
	end := runeIdx + 1
	for end < len(runes) && isTokenRune(runes[end]) {
		end++
	}
	return string(runes[start:end])
}

func (m *Model) resolveTokenAsLinkTarget(raw string) (chatLinkTarget, bool) {
	token := strings.TrimSpace(raw)
	token = strings.TrimPrefix(token, "(")
	token = strings.TrimSuffix(token, ")")
	token = strings.TrimRight(token, ".,;:!?\"'`]")
	token = strings.TrimLeft(token, "\"'`[")
	if token == "" {
		return chatLinkTarget{}, false
	}

	// 1. External HTTP / HTTPS URL
	if strings.HasPrefix(token, "https://") || strings.HasPrefix(token, "http://") {
		if u, err := url.Parse(token); err == nil && u.Host != "" {
			return chatLinkTarget{URL: token}, true
		}
	}

	// 2. Local file:// URL or path
	token = strings.TrimPrefix(token, "file://")
	token = strings.TrimPrefix(token, "@repo:")
	token = strings.TrimPrefix(token, "@")
	token = strings.TrimPrefix(token, "./")

	// Extract optional :line or #Lline suffix
	lineNum := 0
	if hashIdx := strings.Index(token, "#L"); hashIdx > 0 {
		numPart := token[hashIdx+2:]
		if dash := strings.IndexByte(numPart, '-'); dash >= 0 {
			numPart = numPart[:dash]
		}
		if n, err := strconv.Atoi(numPart); err == nil && n > 0 {
			lineNum = n
		}
		token = token[:hashIdx]
	} else if colonIdx := strings.LastIndexByte(token, ':'); colonIdx > 0 {
		tail := token[colonIdx+1:]
		// Handle startLine:endLine:filepath format as well as filepath:line
		if firstColon := strings.IndexByte(token, ':'); firstColon != colonIdx {
			if n1, err1 := strconv.Atoi(token[:firstColon]); err1 == nil {
				if _, err2 := strconv.Atoi(token[firstColon+1 : colonIdx]); err2 == nil && tail != "" {
					lineNum = n1
					token = tail
				}
			}
		} else if n, err := strconv.Atoi(tail); err == nil && n > 0 {
			lineNum = n
			token = token[:colonIdx]
		} else if parts := strings.SplitN(tail, ":", 2); len(parts) == 2 {
			if n, err := strconv.Atoi(parts[0]); err == nil && n > 0 {
				lineNum = n
				token = token[:colonIdx]
			}
		}
	}

	// Strip workspace root prefix if absolute path inside root
	if m.root != "" && strings.HasPrefix(token, "/") {
		cleanRoot := filepath.ToSlash(filepath.Clean(m.root))
		cleanTok := filepath.ToSlash(filepath.Clean(token))
		if strings.HasPrefix(cleanTok, cleanRoot+"/") {
			token = strings.TrimPrefix(cleanTok, cleanRoot+"/")
		} else if cleanTok == cleanRoot {
			return chatLinkTarget{RelPath: "", IsDir: true}, true
		}
	}

	hadTrailingSlash := strings.HasSuffix(token, "/")
	cleanRel := path.Clean(strings.TrimPrefix(filepath.ToSlash(token), "/"))
	if cleanRel == "." || cleanRel == "" || strings.HasPrefix(cleanRel, "..") {
		return chatLinkTarget{}, false
	}

	m.ensureChatMentionIndex()
	// Check indexed workspace files & directories
	for _, f := range m.chatMention.files {
		if f.Path == cleanRel {
			return chatLinkTarget{RelPath: cleanRel, IsDir: f.Dir, LineNum: lineNum}, true
		}
	}
	// Check linked or discovered project repos
	for _, repo := range m.chatMention.repos {
		if repo.Alias == cleanRel || repo.RelPath == cleanRel {
			rel := repo.RelPath
			if rel == "" {
				rel = repo.Alias
			}
			return chatLinkTarget{RelPath: rel, IsDir: true}, true
		}
	}
	// Direct filesystem check for deeper paths not in top 500 index
	if m.fs != nil {
		if hadTrailingSlash {
			if entries, err := m.fs.List(context.Background(), cleanRel); err == nil && len(entries) > 0 {
				return chatLinkTarget{RelPath: cleanRel, IsDir: true}, true
			}
		}
		if _, err := m.fs.Read(context.Background(), cleanRel); err == nil {
			return chatLinkTarget{RelPath: cleanRel, IsDir: false, LineNum: lineNum}, true
		}
		if entries, err := m.fs.List(context.Background(), cleanRel); err == nil && len(entries) > 0 {
			return chatLinkTarget{RelPath: cleanRel, IsDir: true}, true
		}
	}
	return chatLinkTarget{}, false
}

func (m *Model) openChatLinkTarget(target chatLinkTarget) tea.Cmd {
	if target.URL != "" {
		urlStr := target.URL
		m.status = "opening link: " + urlStr
		return func() tea.Msg {
			_ = openExternalURL(urlStr)
			return nil
		}
	}
	if target.IsDir {
		m.dir = target.RelPath
		m.files = m.listFiles(m.dir)
		m.selected = 0
		m.fileOffset = 0
		if m.activeLayout().name == LayoutFocus {
			m.layout, _ = layoutFor(string(LayoutStudio))
		}
		m.focus = focusFiles
		m.status = "opened directory: " + displayDir(m.dir)
		return nil
	}
	content, err := m.readFile(target.RelPath)
	if err != nil {
		m.status = "unable to open " + target.RelPath + ": " + err.Error()
		return nil
	}
	m.closeWalkthrough()
	m.viewerHighlights = nil
	m.viewPath = target.RelPath
	m.viewer = content
	m.viewErr = ""
	if target.LineNum > 1 {
		m.viewerOffset = maxInt(target.LineNum-3, 0)
		m.clampViewerOffset()
	} else {
		m.viewerOffset = 0
	}
	// Also sync the file tree to the parent directory and highlight the file
	parentDir := path.Dir(target.RelPath)
	if parentDir == "." {
		parentDir = ""
	}
	m.dir = parentDir
	m.files = m.listFiles(m.dir)
	baseName := path.Base(target.RelPath)
	for i, f := range m.files {
		if f.Path == baseName {
			m.selected = i
			m.ensureFileVisible()
			break
		}
	}
	if m.activeLayout().name == LayoutFocus {
		m.layout, _ = layoutFor(string(LayoutStudio))
	}
	m.focus = focusViewer
	if target.LineNum > 0 {
		m.status = fmt.Sprintf("opened %s:%d in viewer", target.RelPath, target.LineNum)
	} else {
		m.status = "opened " + target.RelPath + " in viewer"
	}
	return nil
}

func openExternalURL(rawURL string) error {
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"open", rawURL}}
	case "windows":
		candidates = [][]string{{"rundll32", "url.dll,FileProtocolHandler", rawURL}}
	default:
		candidates = [][]string{
			{"xdg-open", rawURL},
			{"gio", "open", rawURL},
		}
	}
	for _, argv := range candidates {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, argv[1:]...)
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return fmt.Errorf("no browser launcher found")
}

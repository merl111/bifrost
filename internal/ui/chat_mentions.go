package ui

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

const (
	maxMentionPopoverRows    = 6
	maxIndexedWorkspacePaths = 500
	maxMentionFileBytes      = 24 << 10
	maxMentionTotalBytes     = 64 << 10
	maxMentionDirEntries     = 80
	maxMentionDiffBytes      = 24 << 10
	maxMentionTerminalBytes  = 12 << 10
)

type chatMentionKind string

const (
	mentionKindRepo     chatMentionKind = "repo"
	mentionKindFile     chatMentionKind = "file"
	mentionKindFolder   chatMentionKind = "folder"
	mentionKindDiff     chatMentionKind = "diff"
	mentionKindTerminal chatMentionKind = "terminal"
)

type chatMentionCandidate struct {
	Kind   chatMentionKind
	Token  string // Inserted token without leading '@', e.g. "repo:laura-backend", "cmd/bifrost/main.go", "diff", "terminal"
	Label  string // Display label in popup
	Detail string // Secondary badge/detail in popup
	Score  int
}

type chatMentionState struct {
	open        bool
	triggerByte int
	query       string
	candidates  []chatMentionCandidate
	selected    int
	dismissedAt int // triggerByte that user explicitly dismissed with Esc (-1 when none)

	indexRoot  string
	indexed    bool
	files      []FileEntry
	repos      []ProjectRepoEntry
}

// activeChatMentionToken finds a trailing `@query` token at the end of input.
// A valid `@` trigger is either at index 0 or preceded by whitespace/punctuation
// (` `, `\n`, `\t`, `(`, `[`, `{`, `"`, `'`). Token characters cannot contain spaces.
func activeChatMentionToken(input string) (triggerByte int, query string, ok bool) {
	if input == "" {
		return -1, "", false
	}
	lastAt := strings.LastIndexByte(input, '@')
	if lastAt < 0 {
		return -1, "", false
	}
	if lastAt > 0 {
		prevRune, _ := lastRuneBefore(input[:lastAt])
		if !unicode.IsSpace(prevRune) && !strings.ContainsRune("([{\"'", prevRune) {
			return -1, "", false
		}
	}
	tail := input[lastAt+1:]
	for _, r := range tail {
		if unicode.IsSpace(r) {
			return -1, "", false
		}
	}
	return lastAt, tail, true
}

func lastRuneBefore(s string) (rune, int) {
	for i := len(s) - 1; i >= 0; i-- {
		if (s[i] & 0xc0) != 0x80 {
			r := rune(s[i])
			for _, decoded := range s[i:] {
				r = decoded
				break
			}
			return r, len(s) - i
		}
	}
	return 0, 0
}

func (m *Model) invalidateChatMentionIndex() {
	m.chatMention.indexed = false
	m.chatMention.files = nil
	m.chatMention.repos = nil
}

func (m *Model) ensureChatMentionIndex() {
	if m.chatMention.indexed && m.chatMention.indexRoot == m.root {
		return
	}
	m.chatMention.indexRoot = m.root
	m.chatMention.indexed = true
	m.chatMention.files = m.collectWorkspaceFiles(maxIndexedWorkspacePaths)

	repos := m.projectStatus.Repos
	if len(repos) == 0 && m.root != "" && !m.remoteFS {
		if st, err := scanLocalWorkspaceRepos(m.root, m.projectStatus); err == nil {
			repos = st.Repos
			if len(m.projectStatus.Repos) == 0 {
				m.projectStatus = st
			}
		}
	}
	m.chatMention.repos = append([]ProjectRepoEntry(nil), repos...)
}

func (m *Model) collectWorkspaceFiles(limit int) []FileEntry {
	if m.fs == nil || limit <= 0 {
		return nil
	}
	type queueItem struct {
		rel   string
		depth int
	}
	queue := []queueItem{{rel: "", depth: 0}}
	var result []FileEntry
	maxDepth := 4
	if m.remoteFS {
		maxDepth = 2
	}
	ctx := context.Background()
	for len(queue) > 0 && len(result) < limit {
		curr := queue[0]
		queue = queue[1:]
		entries, err := m.fs.List(ctx, curr.rel)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.Error != "" || entry.Path == ".." || entry.Path == "." {
				continue
			}
			name := path.Base(entry.Path)
			if strings.HasPrefix(name, ".") || shouldSkipMentionDir(name) {
				continue
			}
			fullRel := entry.Path
			if curr.rel != "" && !strings.HasPrefix(entry.Path, curr.rel+"/") {
				fullRel = path.Join(curr.rel, entry.Path)
			}
			item := FileEntry{Path: fullRel, Dir: entry.Dir}
			result = append(result, item)
			if len(result) >= limit {
				break
			}
			if entry.Dir && curr.depth+1 < maxDepth {
				queue = append(queue, queueItem{rel: fullRel, depth: curr.depth + 1})
			}
		}
	}
	return result
}

func shouldSkipMentionDir(name string) bool {
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "__pycache__", "venv", ".git", ".bifrost":
		return true
	default:
		return false
	}
}

func (m *Model) syncChatMentionState() {
	if !m.chatEditing {
		m.chatMention.open = false
		return
	}
	triggerByte, query, ok := activeChatMentionToken(m.chatInput)
	if !ok {
		m.chatMention.open = false
		m.chatMention.dismissedAt = -1
		return
	}
	if m.chatMention.dismissedAt == triggerByte && m.chatMention.query == query {
		m.chatMention.open = false
		return
	}
	m.chatMention.dismissedAt = -1
	m.ensureChatMentionIndex()
	candidates := m.buildChatMentionCandidates(query)
	m.chatMention.triggerByte = triggerByte
	m.chatMention.query = query
	m.chatMention.candidates = candidates
	if len(candidates) == 0 {
		m.chatMention.open = false
		m.chatMention.selected = 0
		return
	}
	m.chatMention.open = true
	if m.chatMention.selected >= len(candidates) {
		m.chatMention.selected = 0
	}
	if m.chatMention.selected < 0 {
		m.chatMention.selected = 0
	}
}

func (m *Model) buildChatMentionCandidates(query string) []chatMentionCandidate {
	q := strings.ToLower(strings.TrimSpace(query))
	var filterKind chatMentionKind
	searchQuery := q
	switch {
	case strings.HasPrefix(q, "repo:"):
		filterKind = mentionKindRepo
		searchQuery = strings.TrimPrefix(q, "repo:")
	case strings.HasPrefix(q, "file:"):
		filterKind = mentionKindFile
		searchQuery = strings.TrimPrefix(q, "file:")
	case strings.HasPrefix(q, "dir:") || strings.HasPrefix(q, "folder:"):
		filterKind = mentionKindFolder
		if strings.HasPrefix(q, "dir:") {
			searchQuery = strings.TrimPrefix(q, "dir:")
		} else {
			searchQuery = strings.TrimPrefix(q, "folder:")
		}
	}

	var all []chatMentionCandidate

	// 1. Repositories (@repo:<alias>)
	if filterKind == "" || filterKind == mentionKindRepo {
		repos := m.projectStatus.Repos
		if len(repos) == 0 {
			repos = m.chatMention.repos
		}
		for _, r := range repos {
			if r.Alias == "" {
				continue
			}
			token := "repo:" + r.Alias
			detail := "repository"
			if r.GitHubSlug != "" {
				detail = "github · " + r.GitHubSlug
			} else if r.RelPath != "" && r.RelPath != "." {
				detail = "repo · ./" + r.RelPath
			}
			if r.Linked {
				detail += " [linked]"
			}
			score, matched := scoreMentionCandidate(searchQuery, r.Alias, token, r.GitHubSlug, r.RelPath)
			if !matched {
				continue
			}
			if filterKind == mentionKindRepo || strings.HasPrefix(q, "repo") {
				score += 80
			} else if r.Linked {
				score += 45
			} else if searchQuery == "" {
				score += 35
			}
			all = append(all, chatMentionCandidate{
				Kind:   mentionKindRepo,
				Token:  token,
				Label:  "@" + token,
				Detail: detail,
				Score:  score,
			})
		}
	}

	// 2. Built-in workspace context targets (@diff, @terminal)
	if filterKind == "" {
		if score, ok := scoreMentionCandidate(searchQuery, "diff", "review", "git", "changes"); ok {
			if searchQuery == "" {
				score += 35
			}
			all = append(all, chatMentionCandidate{
				Kind:   mentionKindDiff,
				Token:  "diff",
				Label:  "@diff",
				Detail: "git status & working tree diff",
				Score:  score,
			})
		}
		if score, ok := scoreMentionCandidate(searchQuery, "terminal", "pty", "shell", "output"); ok {
			if searchQuery == "" {
				score += 30
			}
			all = append(all, chatMentionCandidate{
				Kind:   mentionKindTerminal,
				Token:  "terminal",
				Label:  "@terminal",
				Detail: "recent embedded terminal PTY output",
				Score:  score,
			})
		}
	}

	// 3. Currently open viewer file gets a boost
	activeFile := strings.TrimSpace(m.viewPath)
	if activeFile != "" && activeFile != "review" && activeFile != "tasks" {
		// Handled naturally below with a score boost.
	}

	// 4. Workspace files & directories
	for _, entry := range m.chatMention.files {
		kind := mentionKindFile
		token := entry.Path
		detail := "file"
		if entry.Dir {
			kind = mentionKindFolder
			if !strings.HasSuffix(token, "/") {
				token += "/"
			}
			detail = "folder"
		}
		if filterKind != "" && filterKind != kind {
			continue
		}
		base := path.Base(entry.Path)
		score, ok := scoreMentionCandidate(searchQuery, base, entry.Path)
		if !ok {
			continue
		}
		if entry.Path == activeFile {
			score += 60
			detail = "current viewer file"
		}
		// Prefer shorter/shallower paths when scores are close.
		depthPenalty := strings.Count(entry.Path, "/") * 2
		score -= depthPenalty
		all = append(all, chatMentionCandidate{
			Kind:   kind,
			Token:  token,
			Label:  "@" + token,
			Detail: detail,
			Score:  score,
		})
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		if all[i].Kind != all[j].Kind {
			return all[i].Kind < all[j].Kind
		}
		return all[i].Token < all[j].Token
	})
	if len(all) > 24 {
		all = all[:24]
	}
	return all
}

// scoreMentionCandidate performs fast fuzzy + substring + prefix scoring.
func scoreMentionCandidate(query string, fields ...string) (int, bool) {
	if query == "" {
		return 100, true
	}
	best := -1
	for idx, raw := range fields {
		target := strings.ToLower(strings.TrimSpace(raw))
		if target == "" {
			continue
		}
		weight := 0
		if idx == 0 {
			weight = 25 // Primary name/basename match bonus
		}
		if target == query {
			score := 400 + weight
			if score > best {
				best = score
			}
			continue
		}
		if strings.HasPrefix(target, query) {
			score := 300 + weight - minInt(len(target)-len(query), 40)
			if score > best {
				best = score
			}
			continue
		}
		if pos := strings.Index(target, query); pos >= 0 {
			score := 220 + weight - minInt(pos*3, 45)
			if score > best {
				best = score
			}
			continue
		}
		if fScore, ok := fuzzySubsequenceScore(query, target); ok {
			score := 100 + weight + fScore
			if score > best {
				best = score
			}
		}
	}
	return best, best >= 0
}

func fuzzySubsequenceScore(needle, haystack string) (int, bool) {
	nRunes := []rune(needle)
	hRunes := []rune(haystack)
	if len(nRunes) == 0 {
		return 0, true
	}
	if len(nRunes) > len(hRunes) {
		return 0, false
	}
	ni := 0
	score := 0
	lastMatch := -2
	for hi, hr := range hRunes {
		if hr == nRunes[ni] {
			if hi == 0 || hRunes[hi-1] == '/' || hRunes[hi-1] == '-' || hRunes[hi-1] == '_' || hRunes[hi-1] == '.' {
				score += 12 // Word/segment boundary bonus
			}
			if lastMatch == hi-1 {
				score += 10 // Consecutive character bonus
			}
			lastMatch = hi
			ni++
			if ni == len(nRunes) {
				// Penalize wide spread slightly
				spreadPenalty := minInt(hi-len(nRunes), 30)
				return score - spreadPenalty, true
			}
		}
	}
	return 0, false
}

func (m *Model) applySelectedChatMention() bool {
	if !m.chatMention.open || len(m.chatMention.candidates) == 0 {
		return false
	}
	idx := m.chatMention.selected
	if idx < 0 || idx >= len(m.chatMention.candidates) {
		idx = 0
	}
	cand := m.chatMention.candidates[idx]
	trigger := m.chatMention.triggerByte
	if trigger < 0 || trigger >= len(m.chatInput) || m.chatInput[trigger] != '@' {
		m.chatMention.open = false
		return false
	}
	atBottom := m.chatAtBottom()
	replacement := "@" + cand.Token + " "
	nextInput := m.chatInput[:trigger] + replacement
	if len([]rune(nextInput)) <= maxChatPrompt {
		m.chatInput = nextInput
	}
	m.chatMention.open = false
	m.chatMention.dismissedAt = -1
	m.chatInputChanged(atBottom)
	m.status = "attached context " + "@" + cand.Token
	return true
}

func (m *Model) renderChatMentionPopover(width, maxAllowedRows int) []string {
	if !m.chatMention.open || len(m.chatMention.candidates) == 0 || width < 18 || maxAllowedRows < 3 {
		return nil
	}
	p := m.colors()
	visibleCount := minInt(len(m.chatMention.candidates), minInt(maxMentionPopoverRows, maxAllowedRows-2))
	if visibleCount < 1 {
		return nil
	}

	selected := m.chatMention.selected
	start := 0
	if selected >= visibleCount {
		start = selected - visibleCount + 1
	}
	end := minInt(start+visibleCount, len(m.chatMention.candidates))

	border := p.cyanText
	title := " @ Context "
	if m.chatMention.query != "" {
		title = " @" + sanitizeDisplay(m.chatMention.query) + " "
	}
	hint := " ↑/↓ · Tab/Enter insert · Esc "
	innerW := width - 2
	if ansi.StringWidth(title)+ansi.StringWidth(hint) > innerW {
		hint = ""
	}
	if ansi.StringWidth(title) > innerW {
		title = ansi.Truncate(title, innerW, "…")
	}
	topDash := maxInt(innerW-ansi.StringWidth(title)-ansi.StringWidth(hint), 0)
	top := border("╭") + p.accentText(title) + border(strings.Repeat("─", topDash)) + p.quietText(hint) + border("╮")
	if p.enabled() {
		top = surfaceText(top, p.ink, p.elevated)
	}
	rows := []string{top}

	for i := start; i < end; i++ {
		cand := m.chatMention.candidates[i]
		badge := mentionKindBadge(cand.Kind)
		prefix := "  "
		if i == selected {
			prefix = "› "
		}
		rowInner := maxInt(width-4, 8)
		leftPart := prefix + badge + " " + sanitizeDisplay(cand.Label)
		rightPart := sanitizeDisplay(cand.Detail)

		var line string
		leftW := ansi.StringWidth(leftPart)
		rightW := ansi.StringWidth(rightPart)
		if leftW+2+rightW <= rowInner {
			line = leftPart + strings.Repeat(" ", rowInner-leftW-rightW) + p.quietText(rightPart)
		} else if leftW < rowInner-6 && rightW > 0 {
			availRight := maxInt(rowInner-leftW-2, 4)
			truncRight := ansi.Truncate(rightPart, availRight, "…")
			line = leftPart + strings.Repeat(" ", maxInt(rowInner-leftW-ansi.StringWidth(truncRight), 1)) + p.quietText(truncRight)
		} else {
			line = ansi.Truncate(leftPart, rowInner, "…")
		}

		if i == selected {
			line = p.cyanText(ansi.Strip(truncatePad(line, rowInner)))
		} else {
			line = truncatePad(line, rowInner)
		}
		row := border("│ ") + line + border(" │")
		if p.enabled() {
			row = surfaceText(row, p.ink, p.elevated)
		}
		rows = append(rows, row)
	}

	bottom := border("╰" + strings.Repeat("─", innerW) + "╯")
	if p.enabled() {
		bottom = surfaceText(bottom, p.ink, p.elevated)
	}
	rows = append(rows, bottom)
	return rows
}

func mentionKindBadge(kind chatMentionKind) string {
	switch kind {
	case mentionKindRepo:
		return "[repo]"
	case mentionKindFolder:
		return "[dir] "
	case mentionKindDiff:
		return "[git] "
	case mentionKindTerminal:
		return "[pty] "
	default:
		return "[file]"
	}
}

// expandChatPromptMentions extracts `@...` tokens from the prompt and attaches
// bounded context blocks (files, directories, project repositories, git diff,
// or terminal output) so the model receives the referenced artifacts directly.
func (m *Model) expandChatPromptMentions(prompt string) (expanded string, summary []string) {
	tokens := extractMentionTokensFromPrompt(prompt)
	if len(tokens) == 0 {
		return prompt, nil
	}
	m.ensureChatMentionIndex()

	var blocks []string
	totalBytes := 0
	seen := make(map[string]bool)

	for _, rawToken := range tokens {
		token := strings.TrimRight(rawToken, ".,;:!?)]}\"'")
		if token == "" || seen[token] {
			continue
		}
		seen[token] = true

		block, label, ok := m.resolveChatMentionContext(token)
		if !ok || block == "" {
			continue
		}
		if totalBytes+len(block) > maxMentionTotalBytes {
			remaining := maxMentionTotalBytes - totalBytes
			if remaining <= 256 {
				break
			}
			block = block[:remaining] + "\n... [context truncated]"
		}
		blocks = append(blocks, block)
		summary = append(summary, "@"+label)
		totalBytes += len(block)
	}

	if len(blocks) == 0 {
		return prompt, nil
	}
	return prompt + "\n\n<attached_context>\n" + strings.Join(blocks, "\n\n") + "\n</attached_context>", summary
}

func extractMentionTokensFromPrompt(prompt string) []string {
	var tokens []string
	fields := strings.Fields(prompt)
	for _, f := range fields {
		f = strings.TrimLeft(f, "([{\"'")
		if strings.HasPrefix(f, "@") && len(f) > 1 {
			tokens = append(tokens, f[1:])
		}
	}
	return tokens
}

func (m *Model) resolveChatMentionContext(token string) (block string, label string, ok bool) {
	lower := strings.ToLower(token)

	// 1. @diff / @review
	if lower == "diff" || lower == "review" {
		if m.reviewLoader != nil {
			if diff, err := m.reviewLoader(context.Background()); err == nil && strings.TrimSpace(diff) != "" {
				text := truncateBytes(strings.TrimSpace(diff), maxMentionDiffBytes)
				return fmt.Sprintf("<context kind=\"git_review\" ref=\"@%s\">\n%s\n</context>", token, text), token, true
			}
		}
		if m.viewPath == "review" && strings.TrimSpace(m.viewer) != "" {
			text := truncateBytes(strings.TrimSpace(m.viewer), maxMentionDiffBytes)
			return fmt.Sprintf("<context kind=\"git_review\" ref=\"@%s\">\n%s\n</context>", token, text), token, true
		}
		return "", "", false
	}

	// 2. @terminal / @pty
	if lower == "terminal" || lower == "pty" {
		if m.pty != nil {
			snap := strings.TrimSpace(ansi.Strip(m.pty.snapshot()))
			if snap != "" {
				text := truncateBytes(snap, maxMentionTerminalBytes)
				return fmt.Sprintf("<context kind=\"terminal\" ref=\"@%s\">\n%s\n</context>", token, text), token, true
			}
		}
		return "", "", false
	}

	// 3. @repo:<alias> or direct repository alias match
	repoAlias := ""
	if strings.HasPrefix(lower, "repo:") {
		repoAlias = token[len("repo:"):]
	}
	repos := m.projectStatus.Repos
	if len(repos) == 0 {
		repos = m.chatMention.repos
	}
	for _, r := range repos {
		if (repoAlias != "" && strings.EqualFold(r.Alias, repoAlias)) || (repoAlias == "" && strings.EqualFold(r.Alias, token)) {
			return m.formatRepoMentionContext(r), "repo:" + r.Alias, true
		}
	}

	// 4. Workspace file or directory path
	cleanToken := strings.TrimSuffix(strings.TrimPrefix(token, "./"), "/")
	if cleanToken == "" || m.fs == nil {
		return "", "", false
	}
	if content, err := m.fs.Read(context.Background(), cleanToken); err == nil {
		text := truncateBytes(content, maxMentionFileBytes)
		return fmt.Sprintf("<context kind=\"file\" path=%q>\n%s\n</context>", cleanToken, text), cleanToken, true
	}
	if entries, err := m.fs.List(context.Background(), cleanToken); err == nil && len(entries) > 0 {
		var names []string
		for _, e := range entries {
			if e.Path == ".." || e.Error != "" {
				continue
			}
			name := e.Path
			if e.Dir && !strings.HasSuffix(name, "/") {
				name += "/"
			}
			names = append(names, name)
			if len(names) >= maxMentionDirEntries {
				break
			}
		}
		if len(names) > 0 {
			return fmt.Sprintf("<context kind=\"directory\" path=%q>\n%s\n</context>", cleanToken+"/", strings.Join(names, "\n")), cleanToken + "/", true
		}
	}
	return "", "", false
}

func (m *Model) formatRepoMentionContext(r ProjectRepoEntry) string {
	var b strings.Builder
	repoPath := r.Path
	if repoPath == "" && r.RelPath != "" {
		repoPath = filepath.Join(m.root, filepath.FromSlash(r.RelPath))
	}
	b.WriteString(fmt.Sprintf("<context kind=\"repository\" alias=%q path=%q", r.Alias, repoPath))
	if r.GitHubSlug != "" {
		b.WriteString(fmt.Sprintf(" github=%q", r.GitHubSlug))
	}
	if r.Remote != "" {
		b.WriteString(fmt.Sprintf(" remote=%q", r.Remote))
	}
	if r.DefaultBranch != "" {
		b.WriteString(fmt.Sprintf(" branch=%q", r.DefaultBranch))
	}
	b.WriteString(">\n")

	relDir := r.RelPath
	if relDir == "" && r.Path != "" && m.root != "" {
		if rel, err := filepath.Rel(m.root, r.Path); err == nil {
			relDir = filepath.ToSlash(rel)
		}
	}
	if m.fs != nil && relDir != "" && !strings.HasPrefix(relDir, "..") {
		if entries, err := m.fs.List(context.Background(), relDir); err == nil && len(entries) > 0 {
			b.WriteString("Top-level entries:\n")
			count := 0
			for _, e := range entries {
				if e.Path == ".." || e.Error != "" {
					continue
				}
				name := e.Path
				if e.Dir && !strings.HasSuffix(name, "/") {
					name += "/"
				}
				b.WriteString("- " + name + "\n")
				count++
				if count >= 40 {
					break
				}
			}
		}
	}
	b.WriteString("</context>")
	return b.String()
}

func truncateBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	for cut > 0 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "\n... [truncated]"
}

func highlightComposerMentions(line string, p uiPalette) string {
	if !p.enabled() || line == "" {
		return line
	}
	runes := []rune(line)
	var b strings.Builder
	i := 0
	for i < len(runes) {
		if runes[i] == '@' && (i == 0 || unicode.IsSpace(runes[i-1]) || runes[i-1] == '(' || runes[i-1] == '[' || runes[i-1] == '"' || runes[i-1] == '\'') {
			end := i + 1
			for end < len(runes) && !unicode.IsSpace(runes[end]) {
				end++
			}
			b.WriteString(p.cyanText(string(runes[i:end])))
			i = end
			continue
		}
		start := i
		i++
		for i < len(runes) {
			if runes[i] == '@' && (unicode.IsSpace(runes[i-1]) || runes[i-1] == '(' || runes[i-1] == '[' || runes[i-1] == '"' || runes[i-1] == '\'') {
				break
			}
			i++
		}
		b.WriteString(colorStyle(p.ink).Render(string(runes[start:i])))
	}
	return b.String()
}

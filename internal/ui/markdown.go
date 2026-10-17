package ui

import (
	"fmt"
	"html"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// renderChatMarkdown converts one provider message into terminal rows. The
// parser is deliberately local to a render: streamed messages can be parsed
// concurrently without sharing parser state, and an unfinished fence is still
// represented by Goldmark as a fenced code block containing all available text.
func renderChatMarkdown(source string, width int, p uiPalette) []string {
	width = maxInt(width, 1)
	clean := sanitizeDisplay(source)
	if clean == "" {
		return nil
	}

	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	doc := md.Parser().Parse(text.NewReader([]byte(clean)))
	r := markdownRenderer{source: []byte(clean), width: width, palette: p}
	r.renderChildren(doc, "")
	for len(r.lines) > 0 && r.lines[len(r.lines)-1] == "" {
		r.lines = r.lines[:len(r.lines)-1]
	}
	return r.lines
}

type markdownRenderer struct {
	source  []byte
	width   int
	palette uiPalette
	lines   []string
}

type markdownTableRow struct {
	cells  []string
	header bool
}

func (r *markdownRenderer) renderChildren(parent ast.Node, prefix string) {
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		if child.Kind() == ast.KindLinkReferenceDefinition {
			continue
		}
		block := r.renderBlock(child, prefix)
		r.appendBlock(block)
	}
}

func (r *markdownRenderer) appendBlock(block []string) {
	if len(block) == 0 {
		return
	}
	for len(block) > 0 && block[0] == "" {
		block = block[1:]
	}
	for len(block) > 0 && block[len(block)-1] == "" {
		block = block[:len(block)-1]
	}
	if len(block) == 0 {
		return
	}
	if len(r.lines) > 0 && r.lines[len(r.lines)-1] != "" {
		r.lines = append(r.lines, "")
	}
	r.lines = append(r.lines, block...)
}

func (r *markdownRenderer) renderBlock(node ast.Node, prefix string) []string {
	switch n := node.(type) {
	case *ast.Paragraph:
		return r.wrapInlineBlock(n, prefix, false)
	case *ast.TextBlock:
		return r.wrapInlineBlock(n, prefix, false)
	case *ast.Heading:
		text := r.inlineChildren(n)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return r.wrapStyled(prefix, r.palette.accentText(strings.TrimSpace(text)), false)
	case *ast.Blockquote:
		return r.renderQuote(n, prefix)
	case *ast.List:
		return r.renderList(n, prefix)
	case *ast.FencedCodeBlock:
		return r.renderCode(n.Lines(), cleanLiteralMarkdownText(string(n.Language(r.source))), prefix)
	case *ast.CodeBlock:
		return r.renderCode(n.Lines(), "code", prefix)
	case *ast.ThematicBreak:
		return r.wrapStyled(prefix, r.palette.quietText("────"), true)
	case *extast.Table:
		return r.renderTable(n, prefix)
	case *ast.HTMLBlock:
		return r.renderRawLines(n.Lines(), prefix)
	default:
		if node.Lines() != nil && node.Lines().Len() > 0 {
			return r.renderRawLines(node.Lines(), prefix)
		}
		return nil
	}
}

func (r *markdownRenderer) wrapInlineBlock(node ast.Node, prefix string, preserve bool) []string {
	text := r.inlineChildren(node)
	if text == "" {
		return []string{prefix}
	}
	return r.wrapStyled(prefix, text, preserve)
}

func (r *markdownRenderer) renderQuote(node *ast.Blockquote, prefix string) []string {
	rail := r.palette.quietText("│ ")
	var out []string
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		part := r.renderBlock(child, prefix+rail)
		if len(out) > 0 && len(part) > 0 && out[len(out)-1] != "" {
			out = append(out, "")
		}
		out = append(out, part...)
	}
	return out
}

func (r *markdownRenderer) renderList(list *ast.List, prefix string) []string {
	var out []string
	index := list.Start
	if index == 0 && !list.IsOrdered() {
		index = 1
	}
	itemNumber := 0
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := "• "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", index+itemNumber)
		}
		itemNumber++
		itemPrefix := prefix + r.palette.accentText(marker)
		continuation := prefix + strings.Repeat(" ", ansi.StringWidth(marker))
		first := true
		for block := item.FirstChild(); block != nil; block = block.NextSibling() {
			switch n := block.(type) {
			case *ast.Paragraph, *ast.TextBlock:
				body := r.inlineChildren(n)
				if first {
					out = append(out, r.wrapListItem(itemPrefix, continuation, body)...)
					first = false
				} else {
					out = append(out, r.wrapStyled(continuation, body, false)...)
				}
			case *ast.List:
				if len(out) > 0 && out[len(out)-1] == "" {
					out = out[:len(out)-1]
				}
				out = append(out, r.renderList(n, continuation)...)
			default:
				out = append(out, r.renderBlock(block, continuation)...)
			}
		}
	}
	return out
}

func (r *markdownRenderer) renderCode(lines *text.Segments, language, prefix string) []string {
	if language == "" {
		language = "code"
	}
	header := prefix + r.palette.violetText("┄ "+language)
	out := r.wrapStyled("", header, false)
	rail := prefix + r.palette.quietText("│ ")
	available := maxInt(r.width-ansi.StringWidth(rail), 1)
	rawLines := make([]string, 0, lines.Len())
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		line := cleanLiteralMarkdownText(string(segment.Value(r.source)))
		line = strings.TrimSuffix(line, "\n")
		rawLines = append(rawLines, line)
	}
	styledLines := rawLines
	if strings.EqualFold(language, "go") || strings.EqualFold(language, "golang") {
		styledLines = strings.Split(colorizeGo(strings.Join(rawLines, "\n"), r.palette), "\n")
	}
	for _, line := range styledLines {
		styled := line
		if strings.EqualFold(language, "diff") || strings.EqualFold(language, "patch") {
			styled = r.styleDiff(line)
		}
		wrapped := ansi.Hardwrap(styled, available, true)
		parts := strings.Split(wrapped, "\n")
		if len(parts) == 0 {
			parts = []string{""}
		}
		for _, part := range parts {
			out = append(out, boundedLine(rail+part, r.width))
		}
	}
	if len(out) == 1 {
		// Keep an empty block recognizable and retain the same left rail as a
		// non-empty code panel.
		out = append(out, boundedLine(rail, r.width))
	}
	return out
}

func (r *markdownRenderer) styleDiff(line string) string {
	if !r.palette.enabled() || line == "" {
		return line
	}
	switch line[0] {
	case '+':
		return r.palette.cyanText(line)
	case '-':
		return r.palette.dangerText(line)
	case '@':
		return r.palette.violetText(line)
	default:
		return line
	}
}

func (r *markdownRenderer) renderTable(table *extast.Table, prefix string) []string {
	rows := make([]markdownTableRow, 0, table.ChildCount())
	columns := 0
	for row := table.FirstChild(); row != nil; row = row.NextSibling() {
		if row.Kind() != extast.KindTableHeader && row.Kind() != extast.KindTableRow {
			continue
		}
		cells := make([]string, 0, row.ChildCount())
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			if cell.Kind() == extast.KindTableCell {
				cells = append(cells, r.inlineChildren(cell))
			}
		}
		if len(cells) == 0 {
			continue
		}
		if len(cells) > columns {
			columns = len(cells)
		}
		rows = append(rows, markdownTableRow{cells: cells, header: row.Kind() == extast.KindTableHeader})
	}
	if len(rows) == 0 || columns == 0 {
		return nil
	}
	for i := range rows {
		for len(rows[i].cells) < columns {
			rows[i].cells = append(rows[i].cells, "")
		}
	}
	available := maxInt(r.width-ansi.StringWidth(prefix), 1)
	// Each column gets two padding cells, and each separator gets one space
	// on either side. If the viewport is too narrow for a real grid, stack
	// cells under a single rail so content remains readable.
	gridOverhead := 4 + 3*(columns-1)
	if available < gridOverhead+columns {
		return r.renderNarrowTable(rows, prefix)
	}
	widths := make([]int, columns)
	for col := range widths {
		for _, row := range rows {
			if w := ansi.StringWidth(row.cells[col]); w > widths[col] {
				widths[col] = w
			}
		}
		widths[col] = maxInt(widths[col], 1)
	}
	budget := available - gridOverhead
	if total := sumInts(widths); total > budget {
		for i := range widths {
			widths[i] = 1
		}
		for remaining := budget - columns; remaining > 0; remaining-- {
			best := 0
			for i := 1; i < columns; i++ {
				if widths[i] < widths[best] || (widths[i] == widths[best] && widths[i] < maxTableCellWidth(rows, i)) {
					best = i
				}
			}
			widths[best]++
		}
	}

	var out []string
	for rowIndex, row := range rows {
		cellLines := make([][]string, columns)
		lineCount := 1
		for col := range row.cells {
			cellLines[col] = splitTableCell(row.cells[col], widths[col])
			if len(cellLines[col]) > lineCount {
				lineCount = len(cellLines[col])
			}
		}
		for lineIndex := 0; lineIndex < lineCount; lineIndex++ {
			var body strings.Builder
			body.WriteString("│")
			for col := range widths {
				cell := ""
				if lineIndex < len(cellLines[col]) {
					cell = cellLines[col][lineIndex]
				}
				body.WriteString(" ")
				body.WriteString(cell)
				body.WriteString(strings.Repeat(" ", maxInt(widths[col]-ansi.StringWidth(cell), 0)))
				body.WriteString(" │")
			}
			text := body.String()
			if row.header {
				text = r.palette.accentText(text)
			}
			out = append(out, boundedLine(prefix+text, r.width))
		}
		if rowIndex == 0 && row.header {
			var separator strings.Builder
			separator.WriteString("├")
			for col, width := range widths {
				if col > 0 {
					separator.WriteString("┼")
				}
				separator.WriteString(strings.Repeat("─", width+2))
			}
			separator.WriteString("┤")
			out = append(out, boundedLine(prefix+r.palette.quietText(separator.String()), r.width))
		}
	}
	return out
}

func sumInts(values []int) int {
	total := 0
	for _, value := range values {
		total += value
	}
	return total
}

func maxTableCellWidth(rows []markdownTableRow, column int) int {
	width := 0
	for _, row := range rows {
		if column < len(row.cells) {
			width = maxInt(width, ansi.StringWidth(row.cells[column]))
		}
	}
	return width
}

func splitTableCell(cell string, width int) []string {
	wrapped := ansi.Wrap(cell, maxInt(width, 1), " \t")
	parts := strings.Split(wrapped, "\n")
	if len(parts) == 0 {
		return []string{""}
	}
	for i := range parts {
		parts[i] = boundedLine(parts[i], maxInt(width, 1))
	}
	return parts
}

func (r *markdownRenderer) renderNarrowTable(rows []markdownTableRow, prefix string) []string {
	var out []string
	for _, row := range rows {
		for _, cell := range row.cells {
			linePrefix := prefix + r.palette.quietText("│ ")
			if row.header {
				cell = r.palette.accentText(cell)
			}
			out = append(out, r.wrapStyled(linePrefix, cell, false)...)
		}
	}
	return out
}

func (r *markdownRenderer) renderRawLines(lines *text.Segments, prefix string) []string {
	var out []string
	for i := 0; i < lines.Len(); i++ {
		segment := lines.At(i)
		line := cleanMarkdownText(string(segment.Value(r.source)))
		line = strings.TrimSuffix(line, "\n")
		out = append(out, r.wrapStyled(prefix, line, true)...)
	}
	return out
}

func (r *markdownRenderer) wrapStyled(prefix, body string, preserveSpace bool) []string {
	if r.width <= ansi.StringWidth(prefix) {
		prefix = ansi.Truncate(prefix, maxInt(r.width, 1), "")
	}
	available := maxInt(r.width-ansi.StringWidth(prefix), 1)
	parts := strings.Split(body, "\n")
	var out []string
	for _, part := range parts {
		wrapped := ansi.Wrap(part, available, " \t")
		if preserveSpace {
			wrapped = ansi.Hardwrap(part, available, true)
		}
		wrappedParts := strings.Split(wrapped, "\n")
		if len(wrappedParts) == 0 {
			wrappedParts = []string{""}
		}
		for _, wrappedPart := range wrappedParts {
			out = append(out, boundedLine(prefix+wrappedPart, r.width))
		}
	}
	return out
}

func (r *markdownRenderer) wrapListItem(firstPrefix, continuation, body string) []string {
	available := maxInt(r.width-ansi.StringWidth(firstPrefix), 1)
	parts := strings.Split(body, "\n")
	var out []string
	firstLine := true
	for _, part := range parts {
		wrapped := ansi.Wrap(part, available, " \t")
		for _, wrappedPart := range strings.Split(wrapped, "\n") {
			prefix := continuation
			if firstLine {
				prefix = firstPrefix
				firstLine = false
			}
			out = append(out, boundedLine(prefix+wrappedPart, r.width))
		}
	}
	if len(out) == 0 {
		return []string{firstPrefix}
	}
	return out
}

func (r *markdownRenderer) inlineChildren(parent ast.Node) string {
	var b strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		b.WriteString(r.inline(child))
	}
	return b.String()
}

func (r *markdownRenderer) inlineLiteralChildren(parent ast.Node) string {
	var b strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch n := child.(type) {
		case *ast.Text:
			b.WriteString(cleanLiteralMarkdownText(string(n.Value(r.source))))
		case *ast.String:
			b.WriteString(cleanLiteralMarkdownText(string(n.Value)))
		default:
			if child.HasChildren() {
				b.WriteString(r.inlineLiteralChildren(child))
			} else {
				b.WriteString(cleanLiteralMarkdownText(string(child.Text(r.source))))
			}
		}
	}
	// CommonMark treats line endings inside code spans as spaces. Keeping the
	// span on the paragraph line also prevents streamed partial input from
	// unexpectedly changing the surrounding layout.
	return strings.ReplaceAll(b.String(), "\n", " ")
}

func (r *markdownRenderer) inline(node ast.Node) string {
	switch n := node.(type) {
	case *ast.Text:
		text := cleanMarkdownText(string(n.Value(r.source)))
		if n.HardLineBreak() {
			text += "\n"
		} else if n.SoftLineBreak() {
			text += " "
		}
		return text
	case *ast.String:
		return cleanMarkdownText(string(n.Value))
	case *ast.CodeSpan:
		return r.palette.warnText(r.inlineLiteralChildren(n))
	case *ast.Emphasis:
		body := r.inlineChildren(n)
		if n.Level > 1 {
			return r.palette.title(body)
		}
		if r.palette.enabled() {
			return ansi.NewStyle().Italic(true).Styled(body)
		}
		return body
	case *extast.Strikethrough:
		body := r.inlineChildren(n)
		if r.palette.enabled() {
			return ansi.NewStyle().Strikethrough(true).Styled(r.palette.quietText(body))
		}
		return body
	case *extast.TaskCheckBox:
		if n.IsChecked {
			return r.palette.cyanText("☑ ")
		}
		return r.palette.cyanText("☐ ")
	case *ast.Link:
		return r.inlineLink(r.inlineChildren(n), n.Destination)
	case *ast.Image:
		return r.inlineLink("["+r.inlineChildren(n)+"]", n.Destination)
	case *ast.AutoLink:
		return r.inlineLink(cleanMarkdownText(string(n.Label(r.source))), n.URL(r.source))
	case *ast.RawHTML:
		return cleanMarkdownText(string(n.Text(r.source)))
	default:
		if node.HasChildren() {
			return r.inlineChildren(node)
		}
		return cleanMarkdownText(string(node.Text(r.source)))
	}
}

func (r *markdownRenderer) inlineLink(label string, destination []byte) string {
	// Children were already decoded and sanitized. Decoding them again would
	// change a literal &amp; and remove generated inline formatting.
	dest := cleanMarkdownText(string(destination))
	if dest == "" {
		return label
	}
	if label == dest {
		return r.palette.cyanText(label)
	}
	return label + " " + r.palette.cyanText("("+dest+")")
}

func cleanMarkdownText(s string) string {
	// Goldmark may materialize entities as String nodes. Decode first, then
	// sanitize again so an encoded control sequence can never become terminal
	// output after the initial source sanitization.
	return sanitizeDisplay(html.UnescapeString(string(util.UnescapePunctuations([]byte(s)))))
}

func cleanLiteralMarkdownText(s string) string {
	// Code is literal Markdown content: entities and backslashes are part of
	// the program or patch and must survive rendering. Sanitization still runs
	// before the bytes reach the terminal.
	return sanitizeDisplay(s)
}

func boundedLine(line string, width int) string {
	width = maxInt(width, 1)
	if ansi.StringWidth(line) > width {
		line = ansi.Truncate(line, width, "…")
	}
	return line
}

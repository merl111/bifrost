package ui

// ViewerHighlights resolves the same symbol and line selection used by the TUI.
// The returned slice owns its storage and can be rendered by any frontend.
func ViewerHighlights(content string, p ViewerCommandPayload) []ViewerHighlightRange {
	start, end := p.StartLine, p.EndLine
	if start <= 0 {
		start = p.Line
	}
	if start <= 0 && p.Symbol != "" {
		start, end = findSymbolLineRange(content, p.Symbol)
	}
	var result []ViewerHighlightRange
	if start > 0 {
		result = append(result, ViewerHighlightRange{StartLine: start, EndLine: max(start, end), Label: valueOr(p.Note, p.Symbol)})
	}
	return append(result, p.Highlights...)
}

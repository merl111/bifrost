package ui

import (
	"github.com/yuin/goldmark/ast"
	"strings"
)

// MarkdownBlockText renders structured GFM blocks using the same table/list
// formatting as the TUI, without terminal styling.
func MarkdownBlockText(source []byte, node ast.Node, width int) string {
	r := markdownRenderer{source: source, width: max(1, width), palette: uiPalette{}}
	return strings.Join(r.renderBlock(node, ""), "\n")
}

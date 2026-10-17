package main

import (
	"bifrost/internal/ui"
	cui "cui.local/cui"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"strconv"
	"strings"
)

// Parse Markdown using Bifrost's existing parser. CUI owns text layout,
// selection and native rendering; the source is retained for copying.
func messageSpans(markdown string) []cui.ChatSpan {
	source := []byte(markdown)
	document := goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser().Parse(text.NewReader(source))
	spans := []cui.ChatSpan{}
	link := ""
	appendSpan := func(value string, style int) {
		if value == "" {
			return
		}
		if len(spans) > 0 && spans[len(spans)-1].Style == style && spans[len(spans)-1].Link == link {
			spans[len(spans)-1].Text += value
			return
		}
		spans = append(spans, cui.ChatSpan{Text: value, Style: style, Link: link})
	}
	strong := 0
	var listCounters []int
	ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		style := cui.ChatBody
		if strong > 0 {
			style = cui.ChatStrong
		}
		switch n := node.(type) {
		case *ast.Heading, *ast.Emphasis:
			if entering {
				strong++
			} else {
				strong--
				if node.Kind() == ast.KindHeading {
					appendSpan("\n\n", cui.ChatBody)
				}
			}
		case *ast.Text:
			if entering {
				value := n.Segment.Value(source)
				if !n.IsRaw() {
					value = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(value)))
				}
				appendSpan(string(value), style)
				if n.SoftLineBreak() {
					appendSpan(" ", style)
				}
				if n.HardLineBreak() {
					appendSpan("\n", style)
				}
			}
		case *ast.String:
			if entering {
				appendSpan(string(n.Value), style)
			}
		case *ast.CodeSpan:
			if entering {
				appendSpan(string(n.Text(source)), cui.ChatCode)
				return ast.WalkSkipChildren, nil
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			if entering {
				appendSpan("\n", cui.ChatBody)
				for i := 0; i < node.Lines().Len(); i++ {
					line := node.Lines().At(i)
					appendSpan(string(line.Value(source)), cui.ChatCode)
				}
				appendSpan("\n", cui.ChatBody)
				return ast.WalkSkipChildren, nil
			}
		case *ast.List:
			if entering {
				if len(listCounters) > 0 {
					appendSpan("\n", cui.ChatBody)
				}
				listCounters = append(listCounters, n.Start)
			} else {
				listCounters = listCounters[:len(listCounters)-1]
				appendSpan("\n", cui.ChatBody)
			}
		case *ast.ListItem:
			if entering {
				marker := "•  "
				if list, ok := n.Parent().(*ast.List); ok && list.IsOrdered() {
					marker = strconv.Itoa(listCounters[len(listCounters)-1]) + ".  "
					listCounters[len(listCounters)-1]++
				}
				appendSpan(strings.Repeat("  ", max(0, len(listCounters)-1))+marker, cui.ChatBody)
			} else {
				appendSpan("\n", cui.ChatBody)
			}
		case *ast.Paragraph:
			if !entering {
				appendSpan("\n\n", cui.ChatBody)
			}
		case *ast.Blockquote:
			if entering {
				appendSpan("│  ", cui.ChatMuted)
			}
		case *ast.Link:
			if entering {
				link = string(n.Destination)
			} else {
				appendSpan(" ("+string(n.Destination)+")", cui.ChatMuted)
				link = ""
			}
		case *ast.Image:
			if entering {
				link = string(n.Destination)
				appendSpan("Image: "+string(n.Text(source)), cui.ChatMention)
				link = ""
				return ast.WalkSkipChildren, nil
			}
		case *extast.Table:
			if entering {
				appendSpan(ui.MarkdownBlockText(source, node, 90)+"\n\n", cui.ChatCode)
				return ast.WalkSkipChildren, nil
			}
		case *extast.TaskCheckBox:
			if entering {
				mark := "☐ "
				if n.IsChecked {
					mark = "☑ "
				}
				appendSpan(mark, cui.ChatBody)
			}
		case *extast.Strikethrough:
			if entering {
				appendSpan("~~", cui.ChatMuted)
			} else {
				appendSpan("~~", cui.ChatMuted)
			}
		case *ast.AutoLink:
			if entering {
				link = string(n.URL(source))
				appendSpan(string(n.Label(source)), style)
				link = ""
			}
		case *ast.ThematicBreak:
			if entering {
				appendSpan("\n────────────\n", cui.ChatMuted)
			}
		case *ast.RawHTML:
			if entering {
				appendSpan(string(n.Text(source)), cui.ChatMuted)
			}
		case *ast.HTMLBlock:
			if entering {
				for i := 0; i < n.Lines().Len(); i++ {
					line := n.Lines().At(i)
					appendSpan(string(line.Value(source)), cui.ChatMuted)
				}
				if n.HasClosure() {
					appendSpan(string(n.ClosureLine.Value(source)), cui.ChatMuted)
				}
			}
		}
		return ast.WalkContinue, nil
	})
	// The ABI has a 128-span limit. Preserve all content for unusually complex
	// messages by rendering their Markdown verbatim rather than dropping text.
	if len(spans) > 128 {
		return []cui.ChatSpan{{Text: markdown, Style: cui.ChatBody}}
	}
	if len(spans) > 0 {
		spans[len(spans)-1].Text = strings.TrimRight(spans[len(spans)-1].Text, "\n")
	}
	return spans
}

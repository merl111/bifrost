package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderChatMarkdownVisibleSemantics(t *testing.T) {
	source := "# Heading\n\nA **bold** word, *quiet* text, `x < y`, and [docs](https://example.test/docs).\n\n- first\n  - nested\n1. one\n2. two\n\n> quoted"
	got := renderChatMarkdown(source, 80, paletteFor("plain"))
	if len(got) == 0 {
		t.Fatal("renderer returned no rows")
	}
	visible := strings.Join(got, "\n")
	for _, want := range []string{"Heading", "A bold word", "x < y", "docs (https://example.test/docs)", "• first", "  • nested", "1. one", "2. two", "│ quoted"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("visible output missing %q:\n%s", want, visible)
		}
	}
	for _, marker := range []string{"# ", "**", "[docs]", "```"} {
		if strings.Contains(visible, marker) {
			t.Fatalf("markdown syntax %q leaked into output:\n%s", marker, visible)
		}
	}
	if !strings.Contains(visible, "\n\n") {
		t.Fatalf("paragraphs are not separated:\n%s", visible)
	}
}

func TestRenderChatMarkdownCodePanelsAndFence(t *testing.T) {
	source := "```go\nfunc main() {\n\tprintln(\"ok\")\n}\n```\n\n```diff\n+added\n-removed\n```"
	got := renderChatMarkdown(source, 24, paletteFor("midnight"))
	if len(got) == 0 {
		t.Fatal("renderer returned no rows")
	}
	seenGo, seenDiff := false, false
	for _, line := range got {
		if ansi.StringWidth(line) > 24 {
			t.Fatalf("row exceeds width: %d: %q", ansi.StringWidth(line), line)
		}
		plain := ansi.Strip(line)
		seenGo = seenGo || strings.Contains(plain, "┄ go")
		seenDiff = seenDiff || strings.Contains(plain, "┄ diff")
	}
	if !seenGo || !seenDiff {
		t.Fatalf("missing code language headers: %#v", got)
	}
	joined := ansi.Strip(strings.Join(got, "\n"))
	for _, want := range []string{"│ func main()", "println(\"ok\")", "│ +added", "│ -removed"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("code content missing %q:\n%s", want, joined)
		}
	}
}

func TestRenderChatMarkdownKeepsCodeLiteral(t *testing.T) {
	source := "`&lt;tag&gt;` and `\\*literal\\*`\n\n```text\n&lt;tag&gt;\\n\n```"
	got := ansi.Strip(strings.Join(renderChatMarkdown(source, 80, paletteFor("plain")), "\n"))
	for _, want := range []string{"&lt;tag&gt;", "\\*literal\\*", "│ &lt;tag&gt;\\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("literal code content %q missing from:\n%s", want, got)
		}
	}
}

func TestRenderChatMarkdownEscapesDecodeOnce(t *testing.T) {
	source := `Escaped \*stars\* and [literal &amp;amp;](https://example.test).`
	got := ansi.Strip(strings.Join(renderChatMarkdown(source, 100, paletteFor("plain")), "\n"))
	if !strings.Contains(got, "Escaped *stars*") || !strings.Contains(got, "literal &amp;") {
		t.Fatalf("incorrect escaped text: %s", got)
	}
	for _, theme := range []string{"plain", "midnight"} {
		got = strings.Join(renderChatMarkdown("&#27;[2J **safe** &#x07;", 80, paletteFor(theme)), "\n")
		if strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\a") {
			t.Fatalf("encoded terminal controls leaked: %q", got)
		}
	}
}

func TestRenderChatMarkdownTablesEntitiesAndHostileText(t *testing.T) {
	source := "| Name | Value |\n| --- | ---: |\n| &lt;one&gt; | 2 |\n\nunsafe \x1b[31mred\x1b[0m &amp; done\u0001"
	got := renderChatMarkdown(source, 18, paletteFor("plain"))
	if len(got) == 0 {
		t.Fatal("renderer returned no rows")
	}
	joined := strings.Join(got, "\n")
	for _, want := range []string{"<one>", "2", "unsafe", "& done"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("decoded content missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "\x1b") || strings.Contains(joined, "\x01") {
		t.Fatalf("hostile control sequence leaked: %q", joined)
	}
	for _, line := range got {
		if ansi.StringWidth(line) > 18 {
			t.Fatalf("row exceeds narrow width: %d: %q", ansi.StringWidth(line), line)
		}
	}
	unequal := renderChatMarkdown("| Key | Description |\n| --- | --- |\n| a | a longer value |", 32, paletteFor("plain"))
	var tableWidth int
	for _, line := range unequal {
		plain := ansi.Strip(line)
		if !strings.Contains(plain, "│") {
			continue
		}
		if tableWidth == 0 {
			tableWidth = ansi.StringWidth(plain)
		}
		if gotWidth := ansi.StringWidth(plain); gotWidth != tableWidth {
			t.Fatalf("unequal table cells were not aligned: %d != %d in %q", gotWidth, tableWidth, plain)
		}
	}
	if !strings.Contains(strings.Join(unequal, "\n"), "┼") {
		t.Fatalf("unequal table has no aligned separator: %#v", unequal)
	}
}

func TestRenderChatMarkdownUnicodeAndUnfinishedFence(t *testing.T) {
	source := "Before 👩🏽‍💻 family 👨‍👩‍👧‍👦 flags 🇦🇹 heart ❤️ lab 🧪 界\n\n```text\n  literal **markers** 👩🏽‍💻\n  end"
	got := renderChatMarkdown(source, 10, paletteFor("plain"))
	if len(got) == 0 {
		t.Fatal("renderer returned no rows")
	}
	joined := strings.Join(got, "\n")
	var codeContent strings.Builder
	for _, line := range got {
		plain := ansi.Strip(line)
		if strings.HasPrefix(plain, "│ ") {
			codeContent.WriteString(strings.TrimPrefix(plain, "│ "))
		}
	}
	if !strings.Contains(codeContent.String(), "**markers**") {
		t.Fatalf("unfinished code content was parsed as markdown:\n%s", joined)
	}
	for _, want := range []string{"👩🏽‍💻", "👨‍👩‍👧‍👦", "🇦🇹", "❤️", "🧪"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("grapheme %q was lost:\n%s", want, joined)
		}
	}
	for _, line := range got {
		if ansi.StringWidth(line) > 10 {
			t.Fatalf("unicode row exceeds width: %d: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderChatMarkdownEdgeWidthsAndNestedLists(t *testing.T) {
	source := "> - outer " + strings.Repeat("x", 80) + "\n>   0. zero\n>     - deep\n\n" + strings.Repeat("界", 40)
	got := renderChatMarkdown(source, 12, paletteFor("plain"))
	if len(got) == 0 {
		t.Fatal("renderer returned no rows")
	}
	joined := ansi.Strip(strings.Join(got, "\n"))
	if !strings.Contains(joined, "0. zero") || !strings.Contains(joined, "• deep") {
		t.Fatalf("nested or zero-start list content was lost:\n%s", joined)
	}
	for _, line := range got {
		if ansi.StringWidth(line) > 12 {
			t.Fatalf("nested narrow row exceeds width: %d: %q", ansi.StringWidth(line), line)
		}
	}

	long := renderChatMarkdown(strings.Repeat("x", 1000), 7, paletteFor("plain"))
	if len(long) < 100 {
		t.Fatalf("long unbroken token was not wrapped: %d rows", len(long))
	}
	for _, line := range long {
		if ansi.StringWidth(line) > 7 {
			t.Fatalf("unbroken token row exceeds width: %d: %q", ansi.StringWidth(line), line)
		}
	}
}

func TestRenderChatMarkdownInlineCodeLineEnding(t *testing.T) {
	got := ansi.Strip(strings.Join(renderChatMarkdown("before `first\nsecond` after", 80, paletteFor("plain")), "\n"))
	if !strings.Contains(got, "before first second after") {
		t.Fatalf("inline code newline did not become a space: %q", got)
	}
}

func TestRenderChatMarkdownMalformedStreamingInput(t *testing.T) {
	cases := []string{
		"`", "**", "[label](", "```", "```go\nfunc main() {", ">", "- [", "\\",
		"\x1b[38;5;196m" + strings.Repeat("x", 200),
	}
	for _, source := range cases {
		for width := 1; width <= 20; width++ {
			got := renderChatMarkdown(source, width, paletteFor("midnight"))
			for _, line := range got {
				if ansi.StringWidth(line) > width {
					t.Fatalf("malformed source row exceeds width %d: %q -> %q", width, source, line)
				}
			}
		}
	}
}

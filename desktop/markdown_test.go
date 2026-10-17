package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	cui "cui.local/cui"
)

func TestMarkdownRetainsContentAndDistinguishesCode(t *testing.T) {
	input := "# Heading\n\n**Important** &amp; \\*literal\\*. Use `a < b`.\n\n3. First\n4. Second\n   - Nested\n\n```go\nfmt.Println(\"🌉\")\n```\n\n[Guide](https://example.com)\n\n<div>Keep this source</div>"
	spans := messageSpans(input)
	var rendered, code, strong strings.Builder
	for _, span := range spans {
		rendered.WriteString(span.Text)
		if span.Style == cui.ChatCode {
			code.WriteString(span.Text)
		}
		if span.Style == cui.ChatStrong {
			strong.WriteString(span.Text)
		}
	}
	for _, expected := range []string{"Heading", "Important & *literal*", "3.  First", "4.  Second", "  •  Nested", "Guide (https://example.com)", "<div>Keep this source</div>"} {
		if !strings.Contains(rendered.String(), expected) {
			t.Errorf("lost %q in %q", expected, rendered.String())
		}
	}
	if !strings.Contains(code.String(), "a < b") || !strings.Contains(code.String(), "fmt.Println(\"🌉\")") || strings.Contains(rendered.String(), "```go") {
		t.Fatalf("code formatting: %+v", spans)
	}
	if strong.String() != "HeadingImportant" {
		t.Fatalf("heading/emphasis styles: %q", strong.String())
	}
}

func TestComplexMarkdownFallsBackWithoutDroppingText(t *testing.T) {
	input := strings.Repeat("**strong** plain `code` ", 100)
	spans := messageSpans(input)
	if len(spans) != 1 || spans[0].Text != input {
		t.Fatal("ABI fallback must preserve the original message")
	}
}

func TestActivityKeepsNewestDetailsWithinBound(t *testing.T) {
	log := boundedLog(strings.Repeat("🌉", 16000), "Latest tool completed")
	if !utf8.ValidString(log) || !strings.HasSuffix(log, "Latest tool completed") || len([]rune(log)) > 16040 {
		t.Fatal("activity lost newest details or exceeded its display bound")
	}
}

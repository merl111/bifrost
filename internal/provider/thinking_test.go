package provider

import (
	"strings"
	"testing"
)

func TestResponsesThinkingSummaryUsesPublicSummaryEntries(t *testing.T) {
	output := []responsesOutput{
		{Type: "reasoning", Summary: []responsesSummary{{Type: "summary_text", Text: "Checked the requested files."}}},
		{Type: "reasoning", Summary: []responsesSummary{{Type: "encrypted_content", Text: "must stay hidden"}}},
	}
	got := responsesThinkingSummary(output)
	if got != "Checked the requested files." {
		t.Fatalf("summary = %q", got)
	}
	resp, err := responsesResult(responsesWire{Status: "completed", Output: output})
	if err != nil || resp.ThinkingSummary != got {
		t.Fatalf("response summary = %q, err=%v", resp.ThinkingSummary, err)
	}
}

func TestAnthropicThinkingSummaryKeepsTextAndExcludesRedactedBlocks(t *testing.T) {
	got := anthropicThinkingSummary([]anthropicContent{
		{Type: "thinking", Thinking: "public summary"},
		{Type: "thinking", Thinking: "signed public summary", Signature: "sig"},
		{Type: "redacted_thinking", Thinking: "redacted"},
	})
	if got != "public summary\nsigned public summary" {
		t.Fatalf("summary = %q", got)
	}
	if got := boundedThinkingSummary(strings.Repeat("界", maxThinkingSummaryBytes)); len([]byte(got)) > maxThinkingSummaryBytes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded summary bytes=%d", len([]byte(got)))
	}
}

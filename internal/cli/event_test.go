package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"bifrost/internal/agentbackend"
)

func TestExternalToolEventDetailsUseSafeIDAndBoundedData(t *testing.T) {
	ev := agentbackend.Event{Kind: "tool", Tool: "read_file", Status: "finished", Text: "done", Data: json.RawMessage(`{"call_id":"c-1","result":"ok"}`)}
	if got := externalToolEventID(ev.Data); got != "c-1" {
		t.Fatalf("event ID = %q", got)
	}
	if got := externalToolDetail(ev); !strings.Contains(got, "result") {
		t.Fatalf("event detail = %q", got)
	}
	ev.Data = json.RawMessage(`{"id":"c-2","thinking":"opaque"}`)
	if got := externalToolDetail(ev); got != "done" {
		t.Fatalf("opaque detail = %q", got)
	}
}

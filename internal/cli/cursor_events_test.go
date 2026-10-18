package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/cursorclient"
	"testing"
)

func TestCursorStreamDoesNotReplayWholeTurn(t *testing.T) {
	var events []agent.Event
	forward := cursorEventForwarder{emit: func(ev agent.Event) { events = append(events, ev) }}
	forward.event(cursorclient.Event{Kind: "delta", Text: "I will inspect the code."})
	forward.event(cursorclient.Event{Kind: "tool", ID: "read-1", Text: "Read main.go"})
	forward.event(cursorclient.Event{Kind: "delta", Text: "Here is the explanation."})
	forward.finish("I will inspect the code.\n\nHere is the explanation.")
	if len(events) != 3 || events[2].Kind != "delta" {
		t.Fatalf("replayed aggregate: %+v", events)
	}
	events = nil
	forward = cursorEventForwarder{emit: func(ev agent.Event) { events = append(events, ev) }}
	forward.finish("Unstreamed response")
	if len(events) != 1 || events[0].Kind != "final" {
		t.Fatal("lost unstreamed response")
	}
}

package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/cursorclient"
)

// Cursor returns the aggregate text of all steps. Replaying that as a final
// replaces the last streamed message in both frontends and duplicates earlier
// steps, so only emit a final when no text was streamed to the UI.
type cursorEventForwarder struct {
	emit     func(agent.Event)
	streamed bool
}

func (f *cursorEventForwarder) event(ev cursorclient.Event) {
	if f.emit == nil {
		return
	}
	if ev.Kind == "delta" && ev.Text != "" {
		f.streamed = true
	}
	f.emit(agent.Event{Kind: ev.Kind, Text: ev.Text, ID: ev.ID, Detail: ev.Detail})
}

func (f *cursorEventForwarder) finish(text string) {
	if f.emit != nil && !f.streamed && text != "" {
		f.emit(agent.Event{Kind: "final", Text: text})
	}
}

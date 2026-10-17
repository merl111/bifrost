package main

import (
	"context"
	cui "cui.local/cui"
	"fmt"
	"strings"
)

const activityLink = "bifrost:toggle-activity"

// Activity is grouped only across adjacent events. User prompts and assistant
// replies retain their chronological position and every source event keeps its ID.
func (t *transcript) native() []cui.ChatMessage {
	items := make([]cui.ChatMessage, 0, len(t.messages))
	for i := 0; i < len(t.messages); {
		m := t.messages[i]
		if m.Kind != "" {
			end := t.activityEnd(i)
			items = append(items, t.activityMessage(i, end))
			i = end
			continue
		}
		color := violet
		var flags uint32
		body := m.Text
		if m.Collapsed {
			body = "▸ " + compactText(body, 110)
		}
		spans := messageSpans(safeText(body))
		if m.Author == "You" {
			flags = cui.ChatOutgoing
			color = cyan
			// The document renderer hides outgoing sender headers, so keep an explicit
			// label inside the bubble as well as the accessible author field.
			spans = append([]cui.ChatSpan{{Text: "You\n", Style: cui.ChatMuted}}, spans[:min(len(spans), 127)]...)
		}
		items = append(items, cui.ChatMessage{ID: m.ID, Author: m.Author, Flags: flags, AuthorColor: themeColor(color), AvatarColor: themeColor(color), SelectedOption: -1, Spans: spans})
		i++
	}
	return items
}

func (t *transcript) activityEnd(start int) int {
	end := start
	for end < len(t.messages) && t.messages[end].Kind != "" {
		end++
	}
	return end
}

func (t *transcript) activityBody(start, end int) string {
	var out strings.Builder
	for _, m := range t.messages[start:end] {
		if out.Len() > 0 {
			out.WriteString("\n\n")
		}
		out.WriteString(activityKind(m.Kind) + "\n" + m.Raw)
		if m.Detail != "" {
			out.WriteString("\n" + m.Detail)
		}
	}
	return out.String()
}

func activityKind(kind string) string {
	switch kind {
	case "thinking", "reasoning":
		return "Reasoning"
	case "tool":
		return "Tool"
	case "status":
		return "Status"
	case "decision":
		return "Decision"
	case "summary":
		return "Summary"
	}
	return kind
}

func (t *transcript) activityMessage(start, end int) cui.ChatMessage {
	m := t.messages[start]
	tools, reasoning := 0, 0
	for _, e := range t.messages[start:end] {
		switch e.Kind {
		case "tool":
			tools++
		case "thinking", "reasoning":
			reasoning++
		}
	}
	parts := []string{}
	if tools > 0 {
		parts = append(parts, activityCount(tools, "tool event"))
	}
	if reasoning > 0 {
		parts = append(parts, activityCount(reasoning, "reasoning update"))
	}
	if other := end - start - tools - reasoning; other > 0 {
		parts = append(parts, activityCount(other, "update"))
	}
	arrow := "▸"
	if !m.Collapsed {
		arrow = "▾"
	}
	spans := []cui.ChatSpan{{Text: arrow + " Activity · " + strings.Join(parts, " · "), Style: cui.ChatMuted, Link: activityLink}}
	if !m.Collapsed {
		spans = append(spans, cui.ChatSpan{Text: "\n\n" + safeText(t.activityBody(start, end)), Style: cui.ChatMuted})
	}
	return cui.ChatMessage{ID: m.ID, Author: "Activity", Flags: cui.ChatContinued, SelectedOption: -1, Spans: spans}
}

func (g *desktop) handleTimelineAction(e cui.ChatEvent) {
	if e.Action == cui.ChatLink && e.Text != activityLink {
		g.openChatLink(e.Text)
		return
	}
	for i := range g.transcript.messages {
		m := &g.transcript.messages[i]
		if m.ID != e.ID {
			continue
		}
		switch e.Action {
		case cui.ChatThread, cui.ChatLink:
			if e.Action == cui.ChatLink && m.Kind == "" {
				return
			}
			m.Collapsed = !m.Collapsed
			g.dirty = true
		case cui.ChatCopy, cui.ChatMore:
			body := m.Raw
			if m.Detail != "" {
				body += "\n\n" + m.Detail
			}
			if m.Kind != "" {
				body = g.transcript.activityBody(i, g.transcript.activityEnd(i))
			}
			if e.Action == cui.ChatCopy {
				g.window.Clipboard(body)
			} else {
				g.loadSource(m.Author, func(context.Context) (string, error) { return body, nil })
			}
		}
		return
	}
}

func activityCount(count int, label string) string {
	if count != 1 {
		label += "s"
	}
	return fmt.Sprintf("%d %s", count, label)
}

func (t *transcript) latestActivity() int {
	for i := len(t.messages) - 1; i >= 0; i-- {
		if t.messages[i].Kind == "" {
			continue
		}
		for i > 0 && t.messages[i-1].Kind != "" {
			i--
		}
		return i
	}
	return -1
}

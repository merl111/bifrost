package main

import (
	"context"
	"strings"
	"time"

	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type mentionView struct {
	list           cui.Widget
	candidates     []ui.MentionCandidate
	query          string
	prefix         string
	trigger, caret int
	request        uint64
	due            time.Time
	open           bool
}

func (g *desktop) buildMentions(parent cui.Widget) {
	g.mentions.list = parent.List(nil)
	g.mentions.list.MinSize(0, 100)
	g.mentions.list.SetVisible(false)
	g.mentions.list.OnAction(func(w cui.Widget) { g.acceptMention(w.Selected()) })
	g.composer.OnAction(func(cui.Widget) { g.queueMentions() })
}

func (g *desktop) dismissMentions() {
	m := &g.mentions
	m.request++
	m.open = false
	m.candidates = nil
	m.due = time.Time{}
	m.list.SetVisible(false)
}

func (g *desktop) replacePrompt(text string) {
	g.dismissMentions()
	g.composer.SetText(text)
}

func (g *desktop) mentionContextMatches() bool {
	m := &g.mentions
	text := g.composer.Text()
	start, end, ok := g.composer.TextRange()
	return ok && int(start) == m.caret && start == end && strings.HasPrefix(text, m.prefix)
}
func (g *desktop) queueMentions() {
	m := &g.mentions
	start, _, ok := g.composer.TextRange()
	text := g.composer.Text()
	if !ok {
		start = uint(len(text))
	}
	if int(start) > len(text) {
		return
	}
	prefix := text[:start]
	trigger, query, active := ui.ActiveMention(prefix)
	m.request++
	m.open = false
	m.list.SetVisible(false)
	m.due = time.Time{}
	if !active {
		return
	}
	m.trigger, m.caret, m.query, m.prefix = trigger, int(start), query, prefix
	m.due = time.Now().Add(180 * time.Millisecond)
}
func (g *desktop) updateMentions() {
	m := &g.mentions
	if (m.open || !m.due.IsZero()) && !g.mentionContextMatches() {
		g.dismissMentions()
		return
	}
	if m.due.IsZero() || time.Now().Before(m.due) {
		return
	}
	m.due = time.Time{}
	request, query := m.request, m.query
	g.work(func(ctx context.Context) func() {
		choices := g.ws.MentionContext(ctx, nil).Candidates(query)
		if len(choices) > 6 {
			choices = choices[:6]
		}
		return func() {
			if request != m.request || !g.mentionContextMatches() {
				return
			}
			m.candidates = choices
			var names []string
			for _, c := range choices {
				names = append(names, c.Label+" · "+c.Detail)
			}
			m.list.Items(names)
			m.list.SetSelected(0)
			m.open = len(choices) > 0
			m.list.SetVisible(m.open)
		}
	})
}
func (g *desktop) acceptMention(index int) bool {
	m := &g.mentions
	if !m.open || index < 0 || index >= len(m.candidates) {
		return false
	}
	text := g.composer.Text()
	if !g.mentionContextMatches() {
		g.dismissMentions()
		return false
	}
	next := text[:m.trigger] + "@" + m.candidates[index].Token + " " + text[m.caret:]
	cursor := m.trigger + len(m.candidates[index].Token) + 2
	g.composer.SetText(next)
	g.composer.SetTextRange(uint(cursor), uint(cursor))
	g.dismissMentions()
	g.composer.Focus()
	return true
}
func (g *desktop) mentionKey(key int) bool {
	m := &g.mentions
	if !m.open {
		return false
	}
	switch key {
	case cui.KeyTab, cui.KeyEnter:
		return g.acceptMention(m.list.Selected())
	case cui.KeyUp, cui.KeyDown:
		d := 1
		if key == cui.KeyUp {
			d = -1
		}
		m.list.SetSelected((m.list.Selected() + d + len(m.candidates)) % len(m.candidates))
		return true
	case cui.KeyEscape:
		g.dismissMentions()
		return true
	}
	return false
}

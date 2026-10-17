package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

// ReviewLoader supplies a read-only status/diff snapshot for the viewer.
type ReviewLoader func(context.Context) (string, error)

type reviewResultMsg struct {
	id   uint64
	text string
	err  error
}

func startReview(ctx context.Context, loader ReviewLoader, id uint64) tea.Cmd {
	return func() tea.Msg {
		text, err := loader(ctx)
		return reviewResultMsg{id: id, text: text, err: err}
	}
}

func (m *Model) startReview() tea.Cmd {
	if m.reviewLoader == nil {
		m.status = "review loader not connected"
		return nil
	}
	if !m.focusVisible(focusViewer) {
		if err := m.setLayout(string(LayoutReview)); err != nil {
			m.status = "review layout failed: " + sanitizeDisplay(err.Error())
			return nil
		}
	}
	if m.reviewCancel != nil {
		m.reviewCancel()
	}
	m.reviewID++
	id := m.reviewID
	ctx, cancel := context.WithCancel(context.Background())
	m.reviewCancel = cancel
	m.status = "loading review"
	return startReview(ctx, m.reviewLoader, id)
}

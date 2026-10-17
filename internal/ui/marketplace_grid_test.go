package ui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestMarketplaceCardsReflowAndNavigateSpatially(t *testing.T) {
	for _, size := range []struct{ width, columns int }{{36, 1}, {80, 2}, {120, 3}, {160, 4}} {
		t.Run(fmt.Sprint(size.width), func(t *testing.T) {
			m := New(t.TempDir())
			m.width, m.height = size.width, 36
			m.marketplaceOpen = true
			for i := 0; i < 9; i++ {
				m.marketplaceEntries = append(m.marketplaceEntries, MarketplaceEntry{ID: fmt.Sprint(i), Name: fmt.Sprintf("Card %d", i), Description: "Useful tools for your project ✨"})
			}
			rows := strings.Split(ansi.Strip(m.render()), "\n")
			if len(rows) != 36 {
				t.Fatalf("height=%d", len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) != size.width {
					t.Fatalf("bad width: %q", row)
				}
			}
			first := m.marketplaceView.resultHits[0]
			for col := 0; col < size.columns; col++ {
				hit := m.marketplaceView.resultHits[col]
				if hit.y != first.y || !strings.Contains(rows[hit.y+1], fmt.Sprintf("Card %d", col)) {
					t.Fatalf("cards not on same row: %+v", hit)
				}
			}
			m.marketplaceKey("down", "")
			if m.marketplaceSelected != size.columns {
				t.Fatalf("down selected %d, want %d", m.marketplaceSelected, size.columns)
			}
			m.marketplaceKey("right", "")
			m.marketplaceKey("up", "")
			if m.marketplaceSelected != 1 {
				t.Fatalf("spatial navigation selected %d", m.marketplaceSelected)
			}
			m.marketplaceSelected = 8
			m.marketplaceKey("down", "")
			if m.marketplaceSelected != 8 {
				t.Fatal("down wrapped past last card")
			}
		})
	}
}

func TestMarketplaceCardClickModalIsolationAndReturn(t *testing.T) {
	previewed := ""
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{Preview: func(_ context.Context, id string) (string, error) { previewed = id; return "Skill details", nil }}))
	m.width, m.height = 120, 36
	m.marketplaceOpen = true
	for i := 0; i < 30; i++ {
		m.marketplaceEntries = append(m.marketplaceEntries, MarketplaceEntry{ID: fmt.Sprint(i), Name: fmt.Sprintf("Tool %02d", i), Description: "Useful tools"})
	}
	m.marketplaceQuery = "tool"
	m.marketplaceSelected = 20
	m.render()
	offset := m.marketplaceView.resultOffset
	var hit marketplaceHit
	for _, h := range m.marketplaceView.resultHits {
		if h.index == 20 {
			hit = h
		}
	}
	cmd := m.marketplaceMouse(tea.Mouse{X: hit.x + 2, Y: hit.y + 1, Button: tea.MouseLeft}, false)
	if cmd == nil {
		t.Fatal("card click did not open preview")
	}
	m.Update(cmd())
	if previewed != "20" {
		t.Fatalf("wrong skill %q", previewed)
	}
	view := ansi.Strip(m.render())
	if !strings.Contains(view, "SKILL DETAILS") || !strings.Contains(view, "Discover skills") {
		t.Fatal("modal did not retain background grid")
	}
	m.marketplaceMouse(tea.Mouse{X: 3, Y: 5, Button: tea.MouseLeft}, false)
	if m.marketplaceSearching {
		t.Fatal("background search consumed click through modal")
	}
	close := m.marketplaceView.closeHit
	m.marketplaceMouse(tea.Mouse{X: close.x, Y: close.y, Button: tea.MouseLeft}, false)
	if m.marketplacePreviewID != "" || !m.marketplaceOpen || m.marketplaceQuery != "tool" || m.marketplaceSelected != 20 || m.marketplaceView.resultOffset != offset {
		t.Fatal("closing modal lost grid state")
	}
}

func TestMarketplaceCloseLoadingModalCancelsAndIgnoresLatePreview(t *testing.T) {
	var previewContext context.Context
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{Preview: func(ctx context.Context, _ string) (string, error) { previewContext = ctx; return "Late preview", nil }}))
	m.marketplaceOpen = true
	m.marketplaceEntries = []MarketplaceEntry{{ID: "one", Name: "One"}}
	cmd := m.startMarketplacePreview()
	m.marketplaceKey("esc", "")
	m.Update(cmd())
	if previewContext.Err() != context.Canceled || m.marketplacePreviewID != "" || !m.marketplaceOpen || m.marketplaceLoading {
		t.Fatal("closing modal did not cancel safely and preserve grid")
	}
}

func TestMarketplaceLiveSearchAcceptsVimLettersAndEnterDoesNotReload(t *testing.T) {
	m := New(t.TempDir())
	m.marketplaceOpen, m.marketplaceSearching = true, true
	m.marketplaceEntries = []MarketplaceEntry{{ID: "project-kit", Name: "Project kit"}, {ID: "other", Name: "Other"}}
	for _, char := range "project kit" {
		m.marketplaceKey(string(char), string(char))
	}
	if m.marketplaceQuery != "project kit" || !m.marketplaceSearching || len(m.marketplaceVisibleEntries()) != 1 {
		t.Fatalf("search consumed navigation letters: %q", m.marketplaceQuery)
	}
	if cmd := m.marketplaceKey("enter", ""); cmd != nil || m.marketplaceSearching || m.marketplaceLoading {
		t.Fatal("Enter reloaded the catalog")
	}
}

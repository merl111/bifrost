package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestMarketplaceSelectionPreviewsBeforeInstall(t *testing.T) {
	installed := ""
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) {
			return []MarketplaceEntry{{ID: "one", Name: "One", Description: "first", Source: "org/repo@main/path"}}, nil
		},
		Preview: func(context.Context, string) (string, error) { return "reviewed content", nil },
		Install: func(_ context.Context, id, scope string) (string, error) {
			installed = id + ":" + scope
			return "One", nil
		},
	}))
	cmd := m.startMarketplace()
	_, _ = m.Update(cmd())
	if !m.marketplaceOpen || len(m.marketplaceEntries) != 1 {
		t.Fatalf("marketplace did not load: open=%v entries=%d", m.marketplaceOpen, len(m.marketplaceEntries))
	}
	cmd = m.marketplaceKey("enter", "")
	if cmd == nil || installed != "" {
		t.Fatalf("selection installed immediately: cmd=%v installed=%q", cmd != nil, installed)
	}
	_, _ = m.Update(cmd())
	if m.marketplacePreviewID != "one" || !strings.Contains(m.renderMarketplace(), "reviewed content") {
		t.Fatalf("preview missing: id=%q view=%q", m.marketplacePreviewID, m.renderMarketplace())
	}
	cmd = m.marketplaceKey("i", "")
	if cmd == nil {
		t.Fatal("project install command missing")
	}
	_, _ = m.Update(cmd())
	if installed != "one:project" || !strings.Contains(m.status, "installed") {
		t.Fatalf("install state=%q status=%q", installed, m.status)
	}
	if !strings.Contains(m.renderMarketplace(), "Installed project") {
		t.Fatalf("install state missing from preview: %q", m.renderMarketplace())
	}
}

func TestMarketplaceStaleResponsesAndErrorsAreIgnoredOrShown(t *testing.T) {
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) { return nil, errors.New("offline") },
	}))
	_ = m.startMarketplace()
	// A response from an older operation cannot replace a newer request.
	m.marketplaceID++
	_, _ = m.Update(marketplaceListMsg{id: m.marketplaceID - 1, entries: []MarketplaceEntry{{ID: "old"}}})
	if len(m.marketplaceEntries) != 0 {
		t.Fatal("stale marketplace response was applied")
	}
	_, _ = m.Update(marketplaceListMsg{id: m.marketplaceID, err: errors.New("offline")})
	if !strings.Contains(m.renderMarketplace(), "offline") {
		t.Fatalf("marketplace error not shown: %q", m.renderMarketplace())
	}
}

func TestMarketplaceStalePreviewCannotReplaceCurrentPreview(t *testing.T) {
	m := New(t.TempDir())
	m.marketplaceOpen = true
	m.marketplaceID = 7
	m.marketplacePreviewID = "current"
	m.marketplaceLoading = true
	_, _ = m.Update(marketplacePreviewMsg{id: 7, entry: MarketplaceEntry{ID: "older"}, text: "stale"})
	if !m.marketplaceLoading || m.marketplacePreview != "" {
		t.Fatalf("stale preview changed state: loading=%v preview=%q", m.marketplaceLoading, m.marketplacePreview)
	}
}

func TestMarketplaceOverlayFitsFocusLayout(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) {
			return []MarketplaceEntry{{ID: "x", Name: "Skill", Source: "repo/ref/path"}}, nil
		},
	}))
	m.width, m.height = 80, 24
	cmd := m.startMarketplace()
	_, _ = m.Update(cmd())
	view := strings.Split(m.render(), "\n")
	if len(view) != 24 {
		t.Fatalf("rows=%d", len(view))
	}
	for i, row := range view {
		if width := ansi.StringWidth(row); width > 80 {
			t.Fatalf("row %d width %d", i, width)
		}
	}
}

func TestMarketplaceSearchFiltersLocallyAndSelectionUsesFilteredRows(t *testing.T) {
	previewed := ""
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) {
			return []MarketplaceEntry{
				{ID: "alpha", Name: "Alpha", Description: "terminal helpers", Source: "one/repo"},
				{ID: "beta", Name: "Beta", Description: "review helpers", Source: "two/repo"},
			}, nil
		},
		Preview: func(_ context.Context, id string) (string, error) {
			previewed = id
			return "preview", nil
		},
	}))
	cmd := m.startMarketplace()
	_, _ = m.Update(cmd())
	m.marketplaceSearching = false
	m.marketplaceQuery = "review"
	m.marketplaceSelected = 0
	if got := m.marketplaceVisibleEntries(); len(got) != 1 || got[0].ID != "beta" {
		t.Fatalf("filtered entries=%+v", got)
	}
	cmd = m.marketplaceKey("enter", "")
	_, _ = m.Update(cmd())
	if previewed != "beta" {
		t.Fatalf("preview selected %q, want beta", previewed)
	}
}

func TestMarketplaceSearchCancelAndNarrowPanelGeometry(t *testing.T) {
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) {
			return []MarketplaceEntry{{ID: "one", Name: "A useful skill", Description: "A compact description", Source: "org/repo"}}, nil
		},
	}))
	m.marketplaceOpen = true
	m.marketplaceSearching = true
	m.marketplaceQuery = "use"
	m.marketplaceKey("escape", "")
	if m.marketplaceSearching || !m.marketplaceOpen {
		t.Fatalf("search cancel state: searching=%v open=%v", m.marketplaceSearching, m.marketplaceOpen)
	}
	m.marketplaceQuery = ""
	view := strings.Split(m.renderMarketplacePanel(36, 12), "\n")
	if len(view) != 12 {
		t.Fatalf("narrow panel rows=%d", len(view))
	}
	for i, row := range view {
		if got := ansi.StringWidth(row); got != 36 {
			t.Fatalf("narrow panel row %d width=%d, want 36", i, got)
		}
	}
}

func TestMarketplaceViewportKeepsSelectedResultVisible(t *testing.T) {
	m := New(t.TempDir())
	m.marketplaceOpen = true
	for i := 0; i < 100; i++ {
		m.marketplaceEntries = append(m.marketplaceEntries, MarketplaceEntry{
			ID: fmt.Sprintf("id-%03d", i), Name: fmt.Sprintf("Skill %03d", i), Description: "short description", Source: "org/repo",
		})
	}
	m.marketplaceSelected = 99
	view := ansi.Strip(m.renderMarketplacePanel(80, 14))
	if !strings.Contains(view, "Skill 099") {
		t.Fatalf("selected result is offscreen: %q", view)
	}
	if m.marketplaceView.resultOffset == 0 {
		t.Fatal("result viewport did not scroll")
	}
	if rows := strings.Count(view, "\n") + 1; rows != 14 {
		t.Fatalf("rows=%d, want 14", rows)
	}
}

func TestMarketplacePreviewUsesFixedColumnsAndFailedPreviewCannotInstall(t *testing.T) {
	installed := false
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		List: func(context.Context, string) ([]MarketplaceEntry, error) {
			return []MarketplaceEntry{{ID: "one", Name: "One", Description: "details", Source: strings.Repeat("very-long-source/", 20)}}, nil
		},
		Preview: func(context.Context, string) (string, error) { return "", errors.New("preview unavailable") },
		Install: func(context.Context, string, string) (string, error) { installed = true; return "one", nil },
	}))
	cmd := m.startMarketplace()
	_, _ = m.Update(cmd())
	cmd = m.marketplaceKey("enter", "")
	_, _ = m.Update(cmd())
	if m.marketplacePreviewID != "" || installed {
		t.Fatalf("failed preview state: id=%q installed=%v", m.marketplacePreviewID, installed)
	}
	if cmd := m.marketplaceKey("i", ""); cmd != nil || installed {
		t.Fatalf("failed preview allowed install: cmd=%v installed=%v", cmd != nil, installed)
	}

	m.marketplaceError = ""
	m.marketplacePreviewID = "one"
	m.marketplaceInstallScope = "preview-ready"
	m.marketplacePreview = strings.Repeat("preview line ", 200)
	view := ansi.Strip(m.renderMarketplacePanel(120, 18))
	for i, row := range strings.Split(view, "\n") {
		if got := ansi.StringWidth(row); got != 120 {
			t.Fatalf("preview row %d width=%d, want 120", i, got)
		}
	}
	if cmd := m.marketplaceMouse(tea.Mouse{X: 119, Y: 17, Button: tea.MouseLeft}, false); cmd != nil || installed {
		t.Fatalf("outside install button triggered action: cmd=%v installed=%v", cmd != nil, installed)
	}
}

func TestMarketplaceMouseInstallUsesRenderedButtonHitbox(t *testing.T) {
	installed := ""
	m := New(t.TempDir(), WithMarketplace(MarketplaceCallbacks{
		Install: func(context.Context, string, string) (string, error) {
			installed = "ok"
			return "One", nil
		},
	}))
	m.marketplaceOpen = true
	m.marketplaceEntries = []MarketplaceEntry{{ID: "one", Name: "One", Source: "org/repo"}}
	m.marketplacePreviewID = "one"
	m.marketplaceInstallScope = "preview-ready"
	m.marketplacePreview = "# Preview"
	m.marketplaceLoading = false
	_ = m.renderMarketplacePanel(120, 18)
	hit := m.marketplaceView.projectHit
	if hit.width == 0 || hit.y <= 0 {
		t.Fatalf("install hitbox was not rendered: %+v", hit)
	}
	cmd := m.marketplaceMouse(tea.Mouse{X: hit.x + hit.width/2, Y: hit.y, Button: tea.MouseLeft}, false)
	if cmd == nil {
		t.Fatal("project install button did not return a command")
	}
	_, _ = m.Update(cmd())
	if installed != "ok" {
		t.Fatalf("button click did not install: %q", installed)
	}
}

func TestMarketplaceSearchClearRestoresCatalogAndMouseControlsSelection(t *testing.T) {
	m := New(t.TempDir())
	m.marketplaceOpen = true
	m.marketplaceEntries = []MarketplaceEntry{{ID: "alpha", Name: "Alpha"}, {ID: "beta", Name: "Beta"}}
	m.marketplaceQuery = "beta"
	m.marketplaceSearching = true
	if len(m.marketplaceVisibleEntries()) != 1 {
		t.Fatal("search did not filter catalog")
	}
	m.marketplaceKey("ctrl+u", "")
	if len(m.marketplaceVisibleEntries()) != 2 {
		t.Fatal("clearing search did not restore catalog")
	}
	m.marketplaceSearching = false
	_ = m.renderMarketplacePanel(80, 24)
	m.marketplaceMouse(tea.Mouse{Button: tea.MouseWheelDown}, true)
	if m.marketplaceSelected != 1 {
		t.Fatalf("wheel selection=%d, want 1", m.marketplaceSelected)
	}
	m.marketplaceMouse(tea.Mouse{Button: tea.MouseLeft, X: m.marketplaceView.searchHit.x + 1, Y: m.marketplaceView.searchHit.y + 1}, false)
	if !m.marketplaceSearching {
		t.Fatal("search field click did not focus search")
	}
}

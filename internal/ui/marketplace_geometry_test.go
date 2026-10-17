package ui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestMarketplaceVisibleRowsMatchMouseTargets(t *testing.T) {
	for _, theme := range []string{"midnight", "plain", "light"} {
		m := New(t.TempDir(), WithTheme(theme))
		m.width, m.height = 80, 24
		m.marketplaceOpen, m.marketplaceSearching = true, true
		for i := 0; i < 30; i++ {
			m.marketplaceEntries = append(m.marketplaceEntries, MarketplaceEntry{ID: fmt.Sprint(i), Name: fmt.Sprintf("Skill %02d", i), Description: "Useful project tooling", Source: "org/repo"})
		}
		m.marketplaceSelected = 20
		rows := strings.Split(ansi.Strip(m.render()), "\n")
		if !strings.Contains(strings.Join(rows, "\n"), "Enter preview") {
			t.Fatal("marketplace footer clipped")
		}
		found := false
		for y, row := range rows {
			if strings.Contains(row, "Skill 20") {
				m.marketplaceSelected = 0
				m.Update(tea.MouseClickMsg{X: 8, Y: y, Button: tea.MouseLeft})
				if m.marketplaceSelected != 20 {
					t.Fatalf("%s clicked visible row %d, got entry %d", theme, y, m.marketplaceSelected)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("selected skill not visible")
		}
	}
}

func TestMarketplaceInstallButtonsRemainVisibleOnNarrowScreens(t *testing.T) {
	for _, width := range []int{36, 80, 120} {
		m := New(t.TempDir())
		m.width, m.height = width, 24
		m.marketplaceOpen = true
		m.marketplacePreviewID = "skill"
		m.marketplacePreview = "# Preview\n\nUseful skill."
		m.marketplaceInstallScope = "preview-ready"
		rows := strings.Split(ansi.Strip(m.render()), "\n")
		for _, button := range []struct {
			label string
			hit   marketplaceHit
		}{{"[ i  Install to project ]", m.marketplaceView.projectHit}, {"[ g  Install globally ]", m.marketplaceView.globalHit}} {
			if button.hit.y >= len(rows) || !strings.Contains(rows[button.hit.y], button.label) {
				t.Fatalf("%d: button/hitbox mismatch for %s at %+v", width, button.label, button.hit)
			}
			if button.hit.x+button.hit.width > width {
				t.Fatalf("%d: button extends beyond screen", width)
			}
		}
	}
}

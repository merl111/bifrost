package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLayoutPresetsFitRepresentativeDimensions(t *testing.T) {
	for _, name := range []string{"studio", "conversation", "review", "focus"} {
		t.Run(name, func(t *testing.T) {
			m := New(t.TempDir(), WithLayout(name))
			for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 36}} {
				m.width, m.height = size[0], size[1]
				rows := strings.Split(m.render(), "\n")
				if len(rows) != size[1] {
					t.Fatalf("%s at %dx%d produced %d rows", name, size[0], size[1], len(rows))
				}
				for i, row := range rows {
					if got := ansi.StringWidth(row); got != size[0] {
						t.Fatalf("%s at %dx%d row %d width %d", name, size[0], size[1], i, got)
					}
				}
			}
		})
	}
}

func TestConversationLayoutCentersChatAndFocusesInReadingOrder(t *testing.T) {
	m := New(t.TempDir(), WithLayout("conversation"))
	if got := m.visibleFocuses(); len(got) < 3 || got[0] != focusFiles || got[1] != focusChat || got[2] != focusViewer {
		t.Fatalf("conversation focus order=%v", got)
	}
	widths := m.paneWidths(120)
	if widths[2] <= widths[1] {
		t.Fatalf("conversation chat width=%v, want wider than inspector", widths)
	}
}

func TestLayoutSettingsCallbackPersistsResizeAndTerminal(t *testing.T) {
	var saved []LayoutSettings
	m := New(t.TempDir(), WithLayoutSettingsChange(func(settings LayoutSettings) error {
		saved = append(saved, settings)
		return nil
	}))
	if err := m.setLayout("conversation"); err != nil {
		t.Fatal(err)
	}
	if err := m.resizePane("chat", 60); err != nil {
		t.Fatal(err)
	}
	if err := m.setTerminalVisible(false); err != nil {
		t.Fatal(err)
	}
	if len(saved) != 3 {
		t.Fatalf("callback calls=%d, want 3", len(saved))
	}
	got := saved[len(saved)-1]
	if got.Name != "conversation" || got.Chat != 60 || got.TerminalVisible {
		t.Fatalf("saved settings=%+v", got)
	}
}

func TestLayoutSettingsCallbackFailureRollsBack(t *testing.T) {
	m := New(t.TempDir(), WithLayoutSettingsChange(func(LayoutSettings) error {
		return errors.New("config is read-only")
	}))
	old := m.layout
	if err := m.setLayout("focus"); err == nil {
		t.Fatal("expected layout persistence failure")
	}
	if m.layout != old || m.Layout() != "studio" {
		t.Fatalf("layout changed after failed persistence: %+v", m.layout)
	}
	if err := m.resizePane("chat", 60); err == nil {
		t.Fatal("expected resize persistence failure")
	}
	if m.layout != old {
		t.Fatalf("resize changed after failed persistence: %+v", m.layout)
	}
}

func TestChatConnectionEventUpdatesFooterWithoutTranscript(t *testing.T) {
	m := New(t.TempDir())
	info := ConnectionInfo{Provider: "Claude", Account: "route", Model: "sonnet", Location: "workspace", Connected: true}
	m.appendChatEvent(ChatEvent{Kind: "connection", Text: "credential detail must stay out", Connection: &info})
	if len(m.chatLines) != 0 {
		t.Fatalf("connection event entered transcript: %+v", m.chatLines)
	}
	if !strings.Contains(m.renderConnectionBar(160), "Claude") || !strings.Contains(m.renderConnectionBar(160), "sonnet") {
		t.Fatalf("connection footer did not update: %q", m.renderConnectionBar(160))
	}
}

func TestLayoutCommandPersistsAndFocusSkipsHiddenPanes(t *testing.T) {
	var saved string
	m := New(t.TempDir(), WithLayoutChange(func(name string) error {
		saved = name
		return nil
	}))
	m.paletteInput = "layout focus"
	m.paletteOpen = true
	m.paletteKey("enter", "")
	if m.Layout() != "focus" || saved != "focus" || m.focus != focusChat {
		t.Fatalf("layout=%q saved=%q focus=%v", m.Layout(), saved, m.focus)
	}
	m.prefix = true
	m.handlePrefixKey("h")
	if m.focus != focusChat {
		t.Fatalf("hidden focus changed to %v", m.focus)
	}
	m.paletteInput = "resize chat 60"
	m.paletteOpen = true
	m.paletteKey("enter", "")
	if m.layout.proportions[2] != 60 {
		t.Fatalf("chat proportion=%v", m.layout.proportions)
	}
}

func TestFocusLayoutOverlaysUtilityViews(t *testing.T) {
	m := New(t.TempDir(), WithLayout("focus"), WithAccounts([]AccountChoice{{ID: "a", Label: "Account", Active: true}}, nil))
	m.width, m.height = 80, 24
	m.openAccounts()
	view := m.render()
	if !strings.Contains(view, "ACCOUNTS") || !strings.Contains(view, "Account") {
		t.Fatalf("account overlay missing from focus layout: %q", view)
	}
	m.accountsOpen = false
	m.viewPath = "tasks"
	view = m.render()
	if !strings.Contains(view, "TASKS") {
		t.Fatalf("task overlay missing from focus layout: %q", view)
	}
}

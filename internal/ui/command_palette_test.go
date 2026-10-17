package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"bifrost/internal/domain"
	"bifrost/internal/tasks"
	"github.com/charmbracelet/x/ansi"
)

func suggestionTexts(items []paletteSuggestion) []string {
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = item.text
	}
	return texts
}

func TestPaletteSuggestionsCommandNamesAndArguments(t *testing.T) {
	if got := suggestionTexts(paletteSuggestions("help lay")); !reflect.DeepEqual(got, []string{"help layout"}) {
		t.Fatalf("help completion=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("accounts --h")); !reflect.DeepEqual(got, []string{"accounts --help"}) {
		t.Fatalf("help flag completion=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("lay")); !reflect.DeepEqual(got, []string{"layout "}) {
		t.Fatalf("layout completion=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("wri")); !reflect.DeepEqual(got, []string{"write"}) {
		t.Fatalf("alias completion=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("layout f")); !reflect.DeepEqual(got, []string{"layout focus"}) {
		t.Fatalf("layout arguments=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("terminal o")); !reflect.DeepEqual(got, []string{"terminal on", "terminal off"}) {
		t.Fatalf("terminal arguments=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("resize ch")); !reflect.DeepEqual(got, []string{"resize chat"}) {
		t.Fatalf("resize pane=%v", got)
	}
	if got := suggestionTexts(paletteSuggestions("resize chat ")); !reflect.DeepEqual(got, []string{"resize chat 10", "resize chat 20", "resize chat 30", "resize chat 40", "resize chat 50", "resize chat 60", "resize chat 70", "resize chat 80"}) {
		t.Fatalf("resize percentage=%v", got)
	}
}

func TestPaletteUsageIsActionable(t *testing.T) {
	if command, ok := paletteCommandFor("TASK"); !ok || command.name != "tasks" {
		t.Fatalf("case-insensitive command lookup=%+v, %v", command, ok)
	}
	command, ok := paletteCommandFor("resize")
	if !ok {
		t.Fatal("resize command missing from registry")
	}
	if got := paletteUsage(command); got != "usage: :resize PANE N" {
		t.Fatalf("usage=%q", got)
	}
	if got := paletteUnknownUsage("wat"); got != `unknown command "wat"; type :help for commands` {
		t.Fatalf("unknown usage=%q", got)
	}
}

func TestPaletteDispatchValidatesShapeAndKeepsTaskIDCase(t *testing.T) {
	const id = "task_abcdabcdabcdabcdabcdabcdabcdabcd"
	var canceled domain.ID
	m := New(t.TempDir(), WithTaskMonitor(TaskMonitorCallbacks{
		Refresh: func(context.Context) ([]tasks.Summary, error) { return nil, nil },
		Cancel: func(_ context.Context, got domain.ID) error {
			canceled = got
			return nil
		},
	}))
	if cmd := m.executePaletteCommand("tasks cancel " + id); cmd == nil {
		t.Fatal("successful task cancellation should schedule a refresh")
	}
	if string(canceled) != id {
		t.Fatalf("task ID=%q, want %q", canceled, id)
	}

	if cmd := m.executePaletteCommand("resize chat"); cmd != nil {
		t.Fatal("invalid resize unexpectedly returned a command")
	}
	if m.status != "usage: :resize PANE N" {
		t.Fatalf("invalid resize status=%q", m.status)
	}
	if cmd := m.executePaletteCommand("terminal maybe"); cmd != nil {
		t.Fatal("invalid terminal unexpectedly returned a command")
	}
	if m.status != "usage: :terminal [on|off|new|next|prev]" {
		t.Fatalf("invalid terminal status=%q", m.status)
	}
}

func TestPaletteInvalidInputStaysOpenWithSpecificUsage(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = "resize chat nope"
	m.paletteKey("enter", "")
	if !m.paletteOpen {
		t.Fatal("invalid command closed the palette")
	}
	if m.paletteInput != "resize chat nope" {
		t.Fatalf("invalid input changed to %q", m.paletteInput)
	}
	if !strings.Contains(m.paletteError, "files/viewer/chat 10-80%") || !strings.Contains(m.paletteError, "terminal 10-60%") {
		t.Fatalf("specific resize usage missing: %q", m.paletteError)
	}
	if !strings.Contains(ansi.Strip(m.renderPalettePanel(80, 12)), "10-60%") {
		t.Fatal("palette did not render the full usage error")
	}
}

func TestPaletteHelpCommandAndFlag(t *testing.T) {
	m := New(t.TempDir())
	if cmd := m.executePaletteCommand("help layout"); cmd != nil {
		t.Fatal("help command unexpectedly scheduled work")
	}
	if m.helpOpen || !strings.Contains(m.paletteHelp, ":layout NAME") {
		t.Fatalf("command help status=%q paletteHelp=%q", m.status, m.paletteHelp)
	}
	if cmd := m.executePaletteCommand("layout --help"); cmd != nil {
		t.Fatal("help flag unexpectedly scheduled work")
	}
	if !strings.Contains(m.status, "choose studio") {
		t.Fatalf("help flag status=%q", m.status)
	}
}

func TestPaletteTabCyclesStableCandidatesAndEnterRunsCurrent(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = "terminal "
	m.paletteKey("tab", "")
	if m.paletteInput != "terminal on" {
		t.Fatalf("first completion=%q", m.paletteInput)
	}
	m.paletteKey("tab", "")
	if m.paletteInput != "terminal off" {
		t.Fatalf("second completion=%q", m.paletteInput)
	}
	m.paletteKey("enter", "")
	if m.paletteOpen || m.layout.terminalVisible {
		t.Fatalf("completed command did not execute: open=%v visible=%v", m.paletteOpen, m.layout.terminalVisible)
	}

	m.paletteOpen = true
	m.paletteInput = "terminal "
	m.paletteKey("shift+tab", "")
	if m.paletteInput != "terminal prev" {
		t.Fatalf("reverse completion=%q", m.paletteInput)
	}
}

func TestPaletteCompletionResetsOnManualInputAndTaskIDsAreNotFabricated(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = "lay"
	m.paletteKey("tab", "")
	if !m.paletteCompletion.active {
		t.Fatal("completion state did not activate")
	}
	m.paletteKey("x", "x")
	if m.paletteCompletion.active || m.paletteInput != "layout x" {
		t.Fatalf("manual input did not reset completion: %+v input=%q", m.paletteCompletion, m.paletteInput)
	}
	for _, suggestion := range m.paletteSuggestions("tasks cancel ") {
		if strings.Contains(suggestion.text, "ID") {
			t.Fatalf("fabricated task ID suggestion=%q", suggestion.text)
		}
	}
}

func TestPaletteViewportKeepsSelectedSuggestionVisible(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = ""
	for i := 0; i < len(paletteCommands)-1; i++ {
		m.paletteMoveSelection(true)
	}
	selected := paletteCommandNames()[m.paletteSelected].text
	view := ansi.Strip(m.renderPalettePanel(72, 9))
	if !strings.Contains(view, selected) {
		t.Fatalf("selected suggestion %q missing from viewport: %q", selected, view)
	}
	if rows := strings.Count(view, "\n") + 1; rows > 9 {
		t.Fatalf("palette exceeded panel height: %d", rows)
	}
}

func TestPaletteTargetHelpStaysInPalette(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = "layout --help"
	m.paletteKey("enter", "")
	if !m.paletteOpen || !strings.Contains(m.paletteHelp, "example: :layout conversation") {
		t.Fatalf("target help did not stay visible: open=%v help=%q", m.paletteOpen, m.paletteHelp)
	}
	if !strings.Contains(ansi.Strip(m.renderPalettePanel(80, 12)), "choose studio") {
		t.Fatal("target help body missing from palette")
	}
}

func TestPaletteUniqueCompletionsDescendIntoArguments(t *testing.T) {
	m := New(t.TempDir())
	m.paletteOpen = true
	m.paletteInput = "lay"
	m.paletteKey("tab", "")
	if m.paletteInput != "layout " {
		t.Fatalf("command completion=%q", m.paletteInput)
	}
	m.paletteKey("tab", "")
	if m.paletteInput != "layout studio" {
		t.Fatalf("argument completion=%q", m.paletteInput)
	}

	m.paletteInput = "resize ch"
	m.paletteCompletion = paletteCompletionState{}
	m.paletteKey("tab", "")
	if m.paletteInput != "resize chat " {
		t.Fatalf("resize pane completion=%q", m.paletteInput)
	}
	m.paletteKey("tab", "")
	if m.paletteInput != "resize chat 10" {
		t.Fatalf("resize value completion=%q", m.paletteInput)
	}
}

func TestPaletteTaskCompletionUsesRealIDs(t *testing.T) {
	m := New(t.TempDir())
	m.taskSummaries = []tasks.Summary{{Task: tasks.Task{ID: domain.ID("task_ABC123")}}}
	items := m.paletteSuggestions("tasks cancel ")
	if len(items) != 1 || items[0].text != "tasks cancel task_ABC123" {
		t.Fatalf("task completions=%v", suggestionTexts(items))
	}
}

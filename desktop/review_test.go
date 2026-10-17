package main

import (
	"errors"
	"strings"
	"testing"

	"bifrost/internal/cli"
	"bifrost/internal/ui"
)

func TestStreamSurvivesInterleavedActivity(t *testing.T) {
	var tr transcript
	for _, e := range []ui.ChatEvent{
		{Kind: "user", Text: "Explain"}, {Kind: "delta", Text: "First "},
		{Kind: "status", Text: "Usage updated"}, {Kind: "delta", Text: "answer"},
		{Kind: "thinking", ID: "summary", Text: "Summary"}, {Kind: "final", Text: "First answer."},
	} {
		tr.apply(e)
	}
	if len(tr.messages) != 4 || tr.messages[1].Raw != "First answer." || tr.messages[2].Kind != "status" || tr.messages[3].Kind != "thinking" {
		t.Fatalf("stream duplicated or activity overwritten: %+v", tr.messages)
	}
	tr.apply(ui.ChatEvent{Kind: "user", Text: "Again"})
	tr.apply(ui.ChatEvent{Kind: "delta", Text: "Before tool"})
	tr.apply(ui.ChatEvent{Kind: "tool", ID: "exec", Text: "exec"})
	tr.apply(ui.ChatEvent{Kind: "delta", Text: "After tool"})
	tr.apply(ui.ChatEvent{Kind: "final"})
	if tr.messages[5].Raw != "Before tool" || tr.messages[7].Raw != "After tool" {
		t.Fatalf("tool boundary or empty final lost content: %+v", tr.messages)
	}
}

func TestStreamAnchorEvictionDoesNotOverwriteActivity(t *testing.T) {
	var tr transcript
	tr.apply(ui.ChatEvent{Kind: "delta", Text: "start"})
	for range maxVisibleMessages {
		tr.apply(ui.ChatEvent{Kind: "status", Text: "activity"})
	}
	tr.apply(ui.ChatEvent{Kind: "delta", Text: "continued"})
	if last := tr.messages[len(tr.messages)-1]; last.Kind != "" || last.Raw != "continued" {
		t.Fatalf("evicted stream reused activity: %+v", last)
	}
}

func TestSettingsSaveValidatesAllPendingBeforePersistence(t *testing.T) {
	original := ui.SettingsDocument{Data: "original", Revision: "revision"}
	pending := map[string]string{"a": "valid", "b": "invalid"}
	edit := func(doc ui.SettingsDocument, key, value string) (ui.SettingsDocument, error) {
		if value == "invalid" {
			return doc, errors.New("invalid value")
		}
		doc.Data += ";" + key + "=" + value
		return doc, nil
	}
	if _, err := stageSettingsEdits(original, pending, edit); err == nil || !strings.Contains(err.Error(), "b:") {
		t.Fatalf("missing field validation: %v", err)
	}
	if original.Data != "original" || len(pending) != 2 {
		t.Fatal("failed validation consumed edits")
	}
	pending["b"] = "valid"
	saved, err := stageSettingsEdits(original, pending, edit)
	if err != nil || saved.Data != "original;a=valid;b=valid" || saved.Revision != "revision" {
		t.Fatalf("save lost values or revision: %+v %v", saved, err)
	}
}

func TestTabsShareRuntimeButKeepSessionIdentity(t *testing.T) {
	a := &chatTab{snapshot: cli.DesktopSnapshot{Sessions: []ui.SessionSummary{{ID: "a", Active: true}}, History: []ui.ChatEvent{{Kind: "user", Text: "A"}}}}
	b := &chatTab{snapshot: cli.DesktopSnapshot{Sessions: []ui.SessionSummary{{ID: "b", Active: true}}, History: []ui.ChatEvent{{Kind: "user", Text: "B"}}}}
	g := desktop{chats: chatTabs{items: []*chatTab{a, b}}}
	g.syncRuntimeSnapshot(cli.DesktopSnapshot{Connection: ui.ConnectionInfo{Connected: true, Account: "beta", Model: "model-b"}, Accounts: []ui.AccountChoice{{ID: "beta", Active: true}}, Sessions: []ui.SessionSummary{{ID: "a"}, {ID: "b", Active: true}, {ID: "c"}}, WriteEnabled: true})
	for i, tab := range g.chats.items {
		if tab.snapshot.Connection.Account != "beta" || !tab.snapshot.Accounts[0].Active || !tab.snapshot.WriteEnabled {
			t.Fatal("stale account advertised")
		}
		if snapshotSession(tab.snapshot) != []string{"a", "b"}[i] || tab.snapshot.History[0].Text != []string{"A", "B"}[i] || len(tab.snapshot.Sessions) != 3 {
			t.Fatal("session identity or history changed")
		}
	}
}

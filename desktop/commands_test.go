package main

import (
	"bifrost/internal/ui"
	"net/url"
	"strings"
	"testing"
)

func TestEveryTUICommandHasDesktopHandler(t *testing.T) {
	var g desktop
	handlers := g.commandHandlers()
	for _, spec := range ui.Commands() {
		if handlers[spec.Name] == nil {
			t.Errorf("no desktop handler for %s", spec.Name)
		}
		for _, alias := range spec.Aliases {
			resolved, ok := ui.Command(alias)
			if !ok || resolved.Name != spec.Name {
				t.Errorf("alias %s diverges", alias)
			}
		}
	}
}
func TestNativeMarkdownGFMAndLinks(t *testing.T) {
	spans := messageSpans("| Name | Value |\n| --- | --- |\n| One | Two |\n\n- [x] Complete\n\n[File](workspace.go#L2) and https://example.com\n")
	text := ""
	links := map[string]bool{}
	for _, s := range spans {
		text += s.Text
		links[s.Link] = true
	}
	if !strings.Contains(text, "One") || !strings.Contains(text, "Two") || !strings.Contains(text, "☑") || !links["workspace.go#L2"] || !links["https://example.com"] {
		t.Fatalf("GFM: %q, links: %v", text, links)
	}
}
func TestToolUpdatesKeepIdentityAndDetails(t *testing.T) {
	var tr transcript
	tr.apply(ui.ChatEvent{Kind: "user", Text: "run"})
	tr.apply(ui.ChatEvent{Kind: "tool", ID: "one", Text: "exec started", Detail: "command"})
	tr.apply(ui.ChatEvent{Kind: "tool", ID: "one", Text: "exec complete", Detail: "output"})
	if len(tr.messages) != 2 || tr.messages[1].Detail != "output" || !tr.messages[1].Collapsed {
		t.Fatalf("%+v", tr.messages)
	}
	tr.apply(ui.ChatEvent{Kind: "delta", Text: "done"})
	tr.apply(ui.ChatEvent{Kind: "final", Text: "done!"})
	if len(tr.messages) != 3 || tr.messages[2].Raw != "done!" {
		t.Fatalf("%+v", tr.messages)
	}
}

func TestSourceLinkLocations(t *testing.T) {
	for _, tc := range []struct {
		raw, path string
		line      int
	}{
		{"workspace.go:2", "workspace.go", 2},
		{"internal/ui/model.go:42", "internal/ui/model.go", 42},
		{"internal/ui/model.go#L8-L12", "internal/ui/model.go", 8},
		{"file:///tmp/source%20file.go#L3", "/tmp/source file.go", 3},
	} {
		u, err := url.Parse(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		path, line, err := sourceLink(u, tc.raw)
		if err != nil || path != tc.path || line != tc.line {
			t.Errorf("%s: %q:%d %v", tc.raw, path, line, err)
		}
	}
	for _, raw := range []string{"javascript:alert(1)", "file://other-host/tmp/source.go"} {
		u, _ := url.Parse(raw)
		if _, _, err := sourceLink(u, raw); err == nil {
			t.Errorf("accepted unsupported URL %s", raw)
		}
	}
}

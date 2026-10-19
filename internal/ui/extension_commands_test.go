package ui

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestExtensionCommandsFromOfflineChat(t *testing.T) {
	for _, test := range []struct {
		input string
		want  ExtensionRequest
	}{
		{`/skill install "./my skill" --scope global`, ExtensionRequest{Kind: "skill-local", Source: "./my skill", Scope: "global"}},
		{`:skill install-git https://example.test/repo.git main "skills/my skill"`, ExtensionRequest{Kind: "skill-git", Source: "https://example.test/repo.git", Ref: "main", Directory: "skills/my skill", Scope: "project"}},
		{`/mcp config './my servers.json'`, ExtensionRequest{Kind: "mcp-config", Source: "./my servers.json", Scope: "project"}},
		{`/mcp config --scope global`, ExtensionRequest{Kind: "mcp-config", Scope: "global"}},
	} {
		t.Run(test.input, func(t *testing.T) {
			var got ExtensionRequest
			m := New(t.TempDir(), WithExtensions(func(_ context.Context, r ExtensionRequest) (string, error) { got = r; return "Installed fixture", nil }))
			m.chatInput = test.input
			cmd := m.submitChat()
			if cmd == nil {
				t.Fatalf("no command: %s", m.status)
			}
			if m.chatInput != "" || len(m.chatHistory) != 0 {
				t.Fatal("command was sent to model or not cleared")
			}
			m.Update(cmd())
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %+v, want %+v", got, test.want)
			}
			if !strings.Contains(m.status, "Installed fixture") {
				t.Fatalf("lost result: %q", m.status)
			}
		})
	}
}

func TestMalformedExtensionCommandsHaveNoSideEffects(t *testing.T) {
	for _, input := range []string{`/skill install "./bad`, `/skill install ./skill --scope invalid`, `/skill install-git repo main`, `/skill install ./skill --oops`, `/mcp config ""`, `/mcp config file extra`, `/mcp config --scope invalid`} {
		t.Run(input, func(t *testing.T) {
			m := New(t.TempDir(), WithExtensions(func(context.Context, ExtensionRequest) (string, error) { t.Fatal("callback called"); return "", nil }))
			m.chatInput = input
			if cmd := m.submitChat(); cmd != nil {
				t.Fatal("invalid command scheduled")
			}
			if m.chatInput != input || m.paletteError == "" {
				t.Fatalf("input/error lost: %q / %q", m.chatInput, m.paletteError)
			}
		})
	}
}
func TestCommandParsingPreservesLiteralArguments(t *testing.T) {
	fields, err := CommandFields(`/skill install './$(echo nope)'`)
	if err != nil || fields[2] != "./$(echo nope)" {
		t.Fatalf("%v %v", fields, err)
	}
	fields, err = CommandFields(`/explain what's happening`)
	if err != nil || fields[1] != "what's" {
		t.Fatalf("%v %v", fields, err)
	}
	if IsChatCommand("/home/me/code") || !IsChatCommand("/skill install ./foo") {
		t.Fatal("slash command classification")
	}
	request, custom, err := ParseExtensionCommand("skill", []string{"install", "catalog/one"})
	if custom || err != nil {
		t.Fatalf("catalog ID classified as local: %+v", request)
	}
}

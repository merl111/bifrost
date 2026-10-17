package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bifrost/internal/domain"
	"bifrost/internal/tasks"
	"github.com/charmbracelet/x/ansi"
)

// TestRenderRepresentativeDimensions protects the terminal geometry contract
// around screens that carry the most user supplied text. It intentionally
// checks a ceiling rather than exact copy so visual polish can evolve without
// turning layout tests into brittle snapshots.
func TestRenderRepresentativeDimensions(t *testing.T) {
	root := t.TempDir()
	longName := strings.Repeat("界", 40) + "-a-very-long-file-name.txt"
	if err := os.WriteFile(filepath.Join(root, longName), []byte(strings.Repeat("世界 wide content ", 80)), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(root, WithAccounts([]AccountChoice{
		{ID: "personal", Label: "Personal", Active: true},
		{ID: "production", Label: "Production", Detail: "violet workspace"},
	}, nil))
	m.viewer = strings.Repeat("界 long viewer line with ANSI-safe content\n", 120)
	m.viewPath = longName
	m.appendChatEvent(ChatEvent{Kind: "assistant", Text: strings.Repeat("assistant response 世界 ", 120)})

	taskID, err := domain.ParseID("task_0123456789abcdef0123456789abcdef")
	if err != nil {
		t.Fatal(err)
	}
	for _, size := range [][2]int{{60, 20}, {80, 24}, {120, 36}} {
		m.width, m.height, m.viewPath = size[0], size[1], longName
		for _, screen := range []string{"chat", "tasks", "accounts"} {
			m.helpOpen, m.paletteOpen, m.accountsOpen = false, false, false
			switch screen {
			case "chat":
				m.focus = focusChat
			case "tasks":
				m.viewPath = "tasks"
				m.taskSummaries = []tasks.Summary{{Task: tasks.Task{ID: taskID, Status: tasks.StatusActive, Model: "studio-model", Profile: "production", Result: tasks.Result{Steps: 8, ToolCalls: 3, Tokens: tasks.TokenUsage{TotalTokens: 2048}}}, Elapsed: 3 * time.Second}}
				m.taskEvents = []tasks.Event{{Sequence: 1, TaskID: taskID, Type: "task.active", Message: "streaming wide unicode 世界"}}
			case "accounts":
				m.viewPath = ""
				m.openAccounts()
			}
			view := strings.Split(m.render(), "\n")
			if len(view) > size[1] {
				t.Fatalf("%s at %dx%d produced %d rows", screen, size[0], size[1], len(view))
			}
			for row, line := range view {
				if got := ansi.StringWidth(line); got > size[0] {
					t.Fatalf("%s at %dx%d row %d width %d: %q", screen, size[0], size[1], row, got, line)
				}
			}
		}
	}
}

// TestWriteRenderSnapshots is an opt-in deterministic fixture generator. The
// generated ANSI files are useful for visual review and are intentionally kept
// out of normal test runs because they are an artifact of the checked-in UI.
func TestWriteRenderSnapshots(t *testing.T) {
	if os.Getenv("UPDATE_UI_SNAPSHOTS") != "1" {
		t.Skip("set UPDATE_UI_SNAPSHOTS=1 to refresh render fixtures")
	}
	root := t.TempDir()
	for _, name := range []string{"main.go", "README.md", "internal", "世界.txt"} {
		path := filepath.Join(root, name)
		if name == "internal" {
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("package studio\n\nfunc main() {\n\tprintln(\"ready\")\n}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := New(root, WithIcons(IconsNerd), WithAccountLabel("Production"), WithConnectionInfo(ConnectionInfo{Provider: "Claude", Account: "Production", Model: "claude-sonnet", Location: "workspace", Connected: true}), WithAccounts([]AccountChoice{
		{ID: "personal", Label: "Personal", Detail: "local models"},
		{ID: "production", Label: "Production", Detail: "OpenAI Responses · studio-model", Active: true},
		{ID: "review", Label: "Review", Detail: "read-only account"},
	}, nil))
	m.width, m.height, m.viewPath = 120, 36, "main.go"
	m.viewer = `package studio

import (
	"context"
	"fmt"
)

func buildWorkspace(ctx context.Context, root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("workspace root is empty")
	}
	return root, ctx.Err()
}
`
	m.appendChatEvent(ChatEvent{Kind: "user", Text: "Can you make the chat easier to read and review?"})
	m.appendChatEvent(ChatEvent{Kind: "tool", ID: "fixture-read", Text: "read_file · internal/ui/chat.go", Detail: "Read internal/ui/chat.go\n\nFound message rendering, streaming, and keyboard handlers."})
	m.appendChatEvent(ChatEvent{Kind: "thinking", ID: "fixture-thinking", Text: "Thinking summary", Detail: "Separate Markdown rendering from pane layout. Check narrow widths, preserve source text, and test emoji sequences."})
	m.appendChatEvent(ChatEvent{Kind: "delta", Text: "## A calmer conversation ✨\n\nThe chat now renders **Markdown**, gives code its own space, and keeps your reading position while replies stream.\n\n- **Readable typography** — headings, lists, and inline `code`\n- **Proper brand marks** — OpenAI and Claude with a Nerd Font\n- **Emoji support** — Unicode stays intact 🧪\n\n```diff\n- raw Markdown inside a box\n+ formatted conversation with room to breathe\n```\n\nTry `:layout focus` for a distraction-free view."})
	m.appendChatEvent(ChatEvent{Kind: "tool", ID: "fixture-tests", Text: "go test ./internal/ui · passed ✓", Detail: "args: go test ./internal/ui\n\nresult:\nok  bifrost/internal/ui\nexit code: 0"})
	m.chatEditing = true
	m.chatInput = "Looks good. Show me the OpenAI account next."

	outputs := map[string]string{"chat-120x36.ansi": m.render()}
	for _, layout := range []string{"studio", "conversation", "review", "focus"} {
		m.layout, _ = layoutFor(layout)
		m.normalizeFocus()
		m.focus = focusChat
		m.width, m.height = 120, 36
		outputs[layout+"-120x36.ansi"] = m.render()
	}
	m.width, m.height = 80, 24
	outputs["chat-80x24.ansi"] = m.render()

	m.width, m.height = 160, 48
	m.layout, _ = layoutFor("conversation")
	m.normalizeFocus()
	m.focus = focusChat
	outputs["conversation-160x48.ansi"] = m.render()
	m.layout, _ = layoutFor("focus")
	m.normalizeFocus()
	m.focus = focusChat
	outputs["focus-160x48.ansi"] = m.render()
	m.chatEditing = false
	m.toggleChatFold(2)
	m.width, m.height = 120, 36
	outputs["folds-expanded-120x36.ansi"] = m.render()
	m.toggleChatFold(2)

	m.width, m.height, m.viewPath = 80, 24, "tasks"
	m.taskSummaries = []tasks.Summary{{Task: tasks.Task{ID: "task_0123456789abcdef0123456789abcdef", Status: tasks.StatusActive, Model: "studio-model", Profile: "production", Result: tasks.Result{Steps: 5, ToolCalls: 2, Tokens: tasks.TokenUsage{TotalTokens: 1024}}}, Elapsed: 2 * time.Second}}
	m.taskEvents = []tasks.Event{{Sequence: 1, Type: "task.active", Message: "streaming response"}, {Sequence: 2, Type: "tool.complete", Message: "workspace review"}}
	outputs["tasks-80x24.ansi"] = m.render()

	m.width, m.height = 120, 36
	m.openAccounts()
	outputs["accounts-120x36.ansi"] = m.render()
	m.width, m.height = 80, 24
	outputs["accounts-80x24.ansi"] = m.render()

	dir := filepath.Join("testdata", "snapshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range outputs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

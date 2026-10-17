package main

import (
	"fmt"
	"strings"

	"bifrost/internal/cli"
	cui "cui.local/cui"
)

type chatTab struct {
	title, draft, activity string
	snapshot               cli.DesktopSnapshot
	transcript             transcript
}
type chatTabs struct {
	root      cui.Widget
	items     []*chatTab
	active    int
	selectBox cui.Widget
}

func snapshotSession(s cli.DesktopSnapshot) string {
	for _, v := range s.Sessions {
		if v.Active {
			return v.ID
		}
	}
	return ""
}
func (g *desktop) activeChat() *chatTab { return g.chats.items[g.chats.active] }
func (g *desktop) buildChatTabs() {
	g.chats.items = []*chatTab{{}}
	row := g.content.Box(cui.Horizontal, 6)
	g.chats.root = row
	g.chats.selectBox = row.Select([]string{"Main chat"})
	g.chats.selectBox.Expand(true)
	g.chats.selectBox.OnAction(func(w cui.Widget) { g.selectChat(w.Selected()) })
	iconButton(row, cui.SymbolPlus, "New chat tab", func() { g.addChat("") })
	iconButton(row, cui.SymbolEdit, "Rename chat tab", func() { g.openCommand("chat rename ") })
	iconButton(row, cui.SymbolClose, "Close chat tab", g.closeChat)
}
func (g *desktop) rememberChat() {
	t := g.activeChat()
	t.draft = g.composer.Text()
	t.transcript = g.transcript
	t.activity = g.activityLog
}
func (g *desktop) refreshChatTabs() {
	var titles []string
	for i, t := range g.chats.items {
		title := t.title
		if title == "" {
			for _, s := range t.snapshot.Sessions {
				if s.Active {
					title = s.Title
					break
				}
			}
		}
		if title == "(empty conversation)" {
			title = "New conversation"
		}
		if title == "" {
			title = fmt.Sprintf("Chat %d", i+1)
		}
		titles = append(titles, shortText(title, 32))
	}
	g.chats.selectBox.Items(titles)
	g.chats.selectBox.SetSelected(g.chats.active)
}
func (g *desktop) selectChat(index int) {
	if g.busy && g.turnCancel == nil {
		g.refreshChatTabs()
		return
	}
	if index < 0 || index >= len(g.chats.items) || index == g.chats.active {
		return
	}
	g.rememberChat()
	g.chats.active = index
	t := g.activeChat()
	g.replacePrompt(t.draft)
	g.displaySnapshot(t.snapshot)
	g.transcript = t.transcript
	g.activityLog = t.activity
	g.dirty = true
	g.refreshChatTabs()
}
func (g *desktop) addChat(name string) {
	if g.busy && g.turnCancel == nil {
		return
	}
	if len(g.chats.items) >= 32 {
		g.status.SetText("Close a chat before opening more (32-tab limit)")
		return
	}
	g.rememberChat()
	s := g.activeChat().snapshot
	s.History = nil
	s.Sessions = nil
	g.chats.items = append(g.chats.items, &chatTab{title: strings.TrimSpace(name), snapshot: s})
	g.selectChat(len(g.chats.items) - 1)
}
func (g *desktop) closeChat() {
	if g.busy {
		g.showError(fmt.Errorf("stop the current operation before closing a chat"))
		return
	}
	if len(g.chats.items) == 1 {
		g.newSession()
		return
	}
	if strings.TrimSpace(g.composer.Text()) != "" {
		tab := g.activeChat()
		g.window.Alert("Close chat with an unsent message?", "The sent conversation is saved. Closing this tab discards its unsent message.", "Discard draft & close", func(result int, _ string) {
			if result == cui.DialogAccepted && g.activeChat() == tab && !g.busy {
				g.removeChat()
			}
		})
		return
	}
	g.removeChat()
}

func (g *desktop) removeChat() {
	i := g.chats.active
	g.chats.items = append(g.chats.items[:i], g.chats.items[i+1:]...)
	g.chats.active = -1
	// The removed tab has already retained its history in the session store.
	target := min(i, len(g.chats.items)-1)
	g.chats.active = target
	t := g.activeChat()
	g.replacePrompt(t.draft)
	g.displaySnapshot(t.snapshot)
	g.transcript = t.transcript
	g.activityLog = t.activity
	g.refreshChatTabs()
}
func (g *desktop) commandChat(a []string) error {
	if len(a) == 0 {
		g.composer.Focus()
		return nil
	}
	sub := strings.ToLower(a[0])
	switch sub {
	case "new", "open", "add":
		g.addChat(strings.Join(a[1:], " "))
	case "rename", "name", "title":
		if len(a) < 2 {
			return fmt.Errorf("enter a name after chat rename")
		}
		g.activeChat().title = strings.Join(a[1:], " ")
		g.refreshChatTabs()
	case "next", "prev", "previous":
		if len(a) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		delta := 1
		if sub != "next" {
			delta = -1
		}
		g.selectChat((g.chats.active + delta + len(g.chats.items)) % len(g.chats.items))
	case "close":
		if len(a) != 1 {
			return fmt.Errorf("unexpected arguments")
		}
		g.closeChat()
	default:
		return fmt.Errorf("unknown chat action")
	}
	return nil
}

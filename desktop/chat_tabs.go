package main

import (
	"context"
	"fmt"
	"strings"

	"bifrost/internal/cli"
	"bifrost/internal/ui"
	cui "cui.local/cui"
)

type chatTab struct {
	title, draft, activity string
	snapshot               cli.DesktopSnapshot
	transcript             transcript
	cancel                 context.CancelFunc
	unread                 bool
	status                 string
	viewer                 *ui.ChatEvent
	runID                  uint64
	scroll                 float64
}
type chatTabs struct {
	root            cui.Widget
	items           []*chatTab
	active          int
	buttons         []cui.Widget
	previous, next  cui.Widget
	capacity, start int
	restoreScroll   bool
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
func (g *desktop) buildChatTabs(parent cui.Widget) {
	g.chats.items = []*chatTab{{}}
	host := parent.Stack()
	host.Expand(false)
	host.StackLayer(cui.LayerFill, 0, 0, 0).MinSize(0, 54)
	row := host.StackLayer(cui.LayerFill, 0, 0, 0).Box(cui.Horizontal, 6)
	style(row, rail, foreground, 0, false, 8)
	g.chats.root = row
	g.chats.capacity = 4
	g.chats.previous = button(row, "‹", func() { g.cycleChat(-1) })
	for i := 0; i < 32; i++ {
		w := button(row, "", func() { g.selectChat(i) })
		w.SetVisible(false)
		g.chats.buttons = append(g.chats.buttons, w)
	}
	g.chats.next = button(row, "›", func() { g.cycleChat(1) })
	spacer := row.Box(cui.Horizontal, 0)
	spacer.Expand(true)
	iconButton(row, cui.SymbolPlus, "New conversation · Ctrl+N", func() { g.addChat("") })
	iconButton(row, cui.SymbolEdit, "Rename tab · :chat rename NAME", func() { g.openCommand("chat rename ") })
	iconButton(row, cui.SymbolClose, "Close selected tab · Ctrl+W", g.closeChat)
}

func (g *desktop) cycleChat(delta int) {
	g.selectChat((g.chats.active + delta + len(g.chats.items)) % len(g.chats.items))
}

func chatTitle(t *chatTab, index int) string {
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
		title = fmt.Sprintf("Chat %d", index+1)
	}
	return title
}

func (g *desktop) rememberChat() {
	t := g.activeChat()
	t.draft = g.composer.Text()
	t.transcript = g.transcript
	t.activity = g.activityLog
	if !g.chats.restoreScroll {
		t.scroll = g.timeline.ScrollOffset()
	}
}
func (g *desktop) refreshChatTabs() {
	capacity := max(1, g.chats.capacity)
	if g.chats.active < g.chats.start {
		g.chats.start = g.chats.active
	}
	if g.chats.active >= g.chats.start+capacity {
		g.chats.start = g.chats.active - capacity + 1
	}
	g.chats.start = max(0, min(g.chats.start, len(g.chats.items)-capacity))
	overflow := len(g.chats.items) > capacity
	g.chats.previous.SetVisible(overflow)
	g.chats.next.SetVisible(overflow)
	for i, w := range g.chats.buttons {
		visible := i < len(g.chats.items) && i >= g.chats.start && i < g.chats.start+capacity
		w.SetVisible(visible)
		if !visible {
			continue
		}
		t := g.chats.items[i]
		title := chatTitle(t, i)
		mark, state := "", ""
		if t.cancel != nil {
			mark, state = "◌  ", " · Running"
		} else if t.unread {
			mark, state = "●  ", " · Unread result"
		}
		w.SetText(mark + shortText(title, 20))
		w.Tooltip(title + state)
		w.Accessibility(title+state, "Switch conversation")
		bg, fg := rail, muted
		if i == g.chats.active {
			bg, fg = accentSoft, foreground
		} else if t.unread {
			fg = cyan
		}
		style(w, bg, fg, 7, i == g.chats.active, 8)
	}
}

func (g *desktop) resizeChatTabs() {
	width, _, ok := g.chats.root.AllocatedSize()
	if !ok {
		return
	}
	capacity := max(1, (width-int(190*g.textScale))/int(200*g.textScale))
	if capacity != g.chats.capacity {
		g.chats.capacity = capacity
		g.refreshChatTabs()
	}
}

func (g *desktop) selectChat(index int) {
	if g.operationBusy {
		g.refreshChatTabs()
		return
	}
	if index < 0 || index >= len(g.chats.items) || index == g.chats.active {
		return
	}
	g.rememberChat()
	g.chats.active = index
	g.chats.restoreScroll = true
	g.restoreChat()
}

func (g *desktop) restoreChat() {
	g.chats.restoreScroll = true
	t := g.activeChat()
	g.replacePrompt(t.draft)
	g.displaySnapshot(t.snapshot)
	g.transcript = t.transcript
	g.activityLog = t.activity
	t.unread = false
	g.refreshBusy()
	if t.viewer != nil {
		g.receiveViewer(*t.viewer)
		t.viewer = nil
	}
	g.dirty = true
	g.refreshChatTabs()
}
func (g *desktop) addChat(name string) {
	if g.operationBusy {
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
	if g.operationBusy || g.activeChat().cancel != nil {
		g.showError(fmt.Errorf("stop the current operation before closing a chat"))
		return
	}

	if strings.TrimSpace(g.composer.Text()) != "" {
		tab := g.activeChat()
		g.window.Alert("Close chat with an unsent message?", "The sent conversation is saved. Closing this tab discards its unsent message.", "Discard draft & close", func(result int, _ string) {
			if result == cui.DialogAccepted && g.activeChat() == tab && !g.operationBusy && tab.cancel == nil {
				g.removeChat()
			}
		})
		return
	}
	g.removeChat()
}

func (g *desktop) removeChat() {
	i := g.chats.active
	if len(g.chats.items) == 1 {
		s := g.activeChat().snapshot
		s.History, s.Sessions = nil, nil
		g.chats.items = append(g.chats.items, &chatTab{snapshot: s})
	}
	g.chats.items = append(g.chats.items[:i], g.chats.items[i+1:]...)
	g.chats.active = -1
	// The removed tab has already retained its history in the session store.
	target := min(i, len(g.chats.items)-1)
	g.chats.active = target
	g.chats.restoreScroll = true
	g.restoreChat()
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

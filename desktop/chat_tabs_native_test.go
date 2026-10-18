package main

import (
	"fmt"
	"strings"

	"bifrost/internal/ui"
)

func (p *nativeParity) chatTabsParity(g *desktop) (bool, error) {
	fail := func(text string) (bool, error) { return false, fmt.Errorf("tabs phase %d: %s", p.step, text) }
	switch p.step {
	case 180:
		g.setPrompt("")
		for range 8 {
			g.addChat("A very long conversation title with Unicode λ")
		}
		p.tabCount = len(g.chats.items)
		g.sidebarVisible, g.inspectorVisible = false, false
		g.sidebar.SetVisible(false)
		g.inspector.SetVisible(false)
		g.window.SetSize(640, 600)
	case 181:
		g.window.SetSize(640, 600)
		if w, _, _ := g.window.Size(); w > 640 {
			return false, nil
		}
		if g.chats.start == 0 || g.chats.capacity >= len(g.chats.items) {
			return fail("overflow did not keep selected tab visible")
		}
		p.capture("desktop-tabs-narrow")
		if err := injectKeys("Control+Tab"); err != nil {
			return fail(err.Error())
		}
	case 182:
		if g.chats.active != 0 {
			return fail("Ctrl+Tab did not wrap")
		}
		if err := injectKeys("Control+Shift+Tab"); err != nil {
			return fail(err.Error())
		}
	case 183:
		if g.chats.active != p.tabCount-1 {
			return fail("Ctrl+Shift+Tab did not switch backwards")
		}
		g.setPrompt("Retain this draft")
		if err := injectKeys("Control+n"); err != nil {
			return fail(err.Error())
		}
	case 184:
		if len(g.chats.items) != p.tabCount+1 || g.composer.Text() != "" {
			return fail("Ctrl+N did not open an empty tab")
		}
		if err := injectKeys("Control+w"); err != nil {
			return fail(err.Error())
		}
	case 185:
		if len(g.chats.items) != p.tabCount || g.composer.Text() != "Retain this draft" {
			return fail("Ctrl+W did not restore adjacent draft")
		}
		g.transcript = transcript{}
		for i := range 30 {
			g.transcript.apply(ui.ChatEvent{Kind: "user", Text: fmt.Sprintf("Message %d: %s", i, strings.Repeat("readable history ", 10))})
		}
		g.dirty = true
	case 186:
		g.timeline.Scroll(120)
		g.selectChat(0)
	case 187:
		g.selectChat(p.tabCount - 1)
	case 188:
		if offset := g.timeline.ScrollOffset(); offset < 119 || offset > 121 {
			return fail(fmt.Sprintf("scroll position was lost: %f", offset))
		}
		return true, nil
	}
	p.step++
	return false, nil
}

func (p *nativeParity) parallelChatParity(g *desktop) (bool, error) {
	fail := func(text string) (bool, error) { return false, fmt.Errorf("parallel tabs phase %d: %s", p.step, text) }
	switch p.step {
	case 108:
		if !g.busy {
			return false, nil
		}
		g.chats.buttons[1].Activate()
		if g.composer.Text() != "other draft" {
			return fail("parallel draft overwritten")
		}
		g.cancelTurn()
		if g.chats.items[0].cancel == nil {
			return fail("Stop in idle tab cancelled background turn")
		}
		g.send.Activate()
	case 109:
		if p.requests.Load() < p.parallelRequests+2 {
			return false, nil
		}
		if g.chats.items[0].cancel == nil || g.chats.items[1].cancel == nil {
			return fail("two requests did not run concurrently")
		}
		g.resume(snapshotSession(g.chats.items[0].snapshot))
		if g.chats.active != 0 || len(g.chats.items) != 2 {
			return fail("reopening running session duplicated its tab")
		}
		g.selectChat(1)
		p.capture("desktop-parallel-tabs")
		g.addChat("Notes")
		g.closeChat()
		if len(g.chats.items) != 2 || g.chats.active != 1 {
			return fail("closing idle tab affected active turns")
		}
		g.cancelTurn()
	case 110:
		if g.chats.items[1].cancel != nil {
			return false, nil
		}
		if g.chats.items[0].cancel == nil || !g.busy {
			return fail("stopping one tab stopped the other")
		}
		g.chats.items[0].cancel() // Finish the other turn while its tab stays in the background.
	case 111:
		if g.busy {
			return false, nil
		}
		if !g.chats.items[0].unread {
			return fail("background completion did not mark unread")
		}
		g.selectChat(0)
		if g.chats.items[0].unread {
			return fail("reading tab did not clear badge")
		}
		g.selectChat(1)
		first, second := snapshotSession(g.chats.items[0].snapshot), snapshotSession(g.chats.items[1].snapshot)
		if first == "" || second == "" || first == second {
			return fail("chat tabs reused session identity")
		}
		g.setPrompt(":not-a-command")
		g.sendPrompt()
		if g.composer.Text() != ":not-a-command" {
			return fail("invalid command erased input")
		}
		g.setPrompt(":terminal new")
		g.sendPrompt()

	}
	p.step++
	return false, nil
}

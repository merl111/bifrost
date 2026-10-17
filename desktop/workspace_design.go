package main

import (
	cui "cui.local/cui"
	"strings"
)

// Navigation has a quiet resting state; filled surfaces identify selection.
func navigationButton(parent cui.Widget, title string, symbol int, action func()) cui.Widget {
	w := button(parent, title, action)
	w.SetSymbol(symbol)
	style(w, 0, muted, 6, false, 8)
	w.Tooltip(title)
	w.Accessibility(title, "Workspace navigation")
	return w
}

func sectionCard(parent cui.Widget, title string) cui.Widget {
	card := parent.Box(cui.Vertical, 10)
	style(card, surface, foreground, 10, true, 18)
	if title != "" {
		label(card, title, 12, foreground).Font("", 12, 600)
	}
	return card
}

func compactText(text string, limit int) string {
	// Attached context is model input, not a useful conversation/task title.
	if i := strings.Index(text, "<attached_context>"); i >= 0 {
		text = text[:i]
	}
	return shortText(strings.Join(strings.Fields(text), " "), limit)
}

func (g *desktop) updateWorkbenchLayout(width int) {
	w := g.workbench
	if w == nil {
		return
	}
	compact := width < 1000
	if compact != w.compact {
		w.compact = compact
		w.brand.SetVisible(!compact)
		w.navTitle.SetVisible(!compact)
		w.back.IconOnly(compact)
		for _, b := range w.navigation {
			b.IconOnly(compact)
		}
		if compact {
			w.rail.MinSize(48, 0)
		} else {
			w.rail.MinSize(186, 0)
		}
	}
	if available, _, ok := w.reading.AllocatedSize(); ok {
		inset := max(0, (available-1360)/2)
		if inset != w.inset {
			w.inset = inset
			w.left.MinSize(inset, 0)
			w.right.MinSize(inset, 0)
		}
	}
}

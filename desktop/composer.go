package main

import (
	"math"
	"strings"

	cui "cui.local/cui"
)

type composerMetrics struct {
	text                string
	width, windowHeight int
	scale               float64
	rows, height        int
}

func (g *desktop) updateComposer() {
	width, _, ok := g.composer.AllocatedSize()
	if !ok || width <= 24 {
		return
	}
	text := g.composer.Text()
	limit := g.ws.Appearance.ComposerMaxRows
	if limit <= 0 {
		limit = 8
	}
	_, windowHeight, _ := g.window.Size()
	old := g.composerMetrics
	if old.text == text && old.width == width && old.scale == g.textScale && old.rows == limit && old.windowHeight == windowHeight {
		return
	}
	rows := 0
	lineHeight := 20 * g.textScale
	for _, line := range strings.Split(text, "\n") {
		measured, h, valid := cui.MeasureText(line, "", 11*g.textScale, 400)
		if valid {
			lineHeight = max(lineHeight, h)
			rows += max(1, int(math.Ceil(measured/float64(width-24))))
		} else {
			rows++
		}
		if rows >= limit {
			break
		}
	}
	height := max(60, min(int(float64(min(max(3, rows), limit))*lineHeight)+20, max(82, windowHeight/3)))
	g.composer.TextareaHeight(height)
	g.composerMetrics = composerMetrics{text, width, windowHeight, g.textScale, limit, height}
	g.send.SetEnabled(!g.busy && (g.connected || strings.HasPrefix(strings.TrimSpace(text), ":")))
}

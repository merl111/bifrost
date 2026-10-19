package main

import (
	"bifrost/internal/ui"
	"strings"
	"testing"
)

func TestContextMeter(t *testing.T) {
	for _, tc := range []struct {
		used, max int
		label     string
	}{{40000, 200000, "Context ~20%"}, {0, 0, "Context —"}, {240000, 200000, "Context ~120%"}} {
		label, tip := contextLabel(ui.ConnectionInfo{ContextTokens: tc.used, ContextMaxTokens: tc.max})
		if label != tc.label || !strings.Contains(tip, "History") {
			t.Fatalf("%s %s", label, tip)
		}
	}
}

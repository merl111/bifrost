package config

import "errors"

func (u UISettings) ValidateLayout() error {
	switch u.Layout {
	case "", "studio", "conversation", "review", "focus":
	default:
		return errors.New("layout must be studio, conversation, review, or focus")
	}
	p := u.PaneProportions
	if p != [3]int{} && p != [3]int{0, 0, 100} && (p[0] < 10 || p[1] < 10 || p[2] < 10 || p[0] > 80 || p[1] > 80 || p[2] > 80 || p[0]+p[1]+p[2] != 100) {
		return errors.New("pane_proportions must sum to 100 and leave at least 10 percent per pane")
	}
	if u.TerminalPercent != 0 && (u.TerminalPercent < 10 || u.TerminalPercent > 60) {
		return errors.New("terminal_percent must be 10..60")
	}
	return nil
}

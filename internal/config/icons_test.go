package config

import "testing"

func TestSettingsIconModes(t *testing.T) {
	for _, mode := range []string{"", "unicode", "nerd", "ascii"} {
		cfg := DefaultSettings()
		cfg.UI.Icons = mode
		if err := cfg.Validate(); err != nil {
			t.Errorf("icons mode %q rejected: %v", mode, err)
		}
	}
	for _, mode := range []string{"emoji", "Nerd Font", "symbols"} {
		cfg := DefaultSettings()
		cfg.UI.Icons = mode
		if err := cfg.Validate(); err == nil {
			t.Errorf("icons mode %q accepted", mode)
		}
	}
}

package config

import "testing"

func TestLayoutConfiguration(t *testing.T) {
	for _, layout := range []string{"", "studio", "conversation", "review", "focus"} {
		cfg := DefaultSettings()
		cfg.UI.Layout = layout
		if err := cfg.Validate(); err != nil {
			t.Fatalf("%s: %v", layout, err)
		}
	}
	for _, settings := range []UISettings{
		{Layout: "unknown"}, {PaneProportions: [3]int{20, 20, 20}}, {PaneProportions: [3]int{0, 50, 50}}, {TerminalPercent: 90},
	} {
		if err := settings.ValidateLayout(); err == nil {
			t.Fatalf("accepted %#v", settings)
		}
	}
	if err := (UISettings{Layout: "focus", PaneProportions: [3]int{0, 0, 100}}).ValidateLayout(); err != nil {
		t.Fatal(err)
	}
}

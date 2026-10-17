package config

import "testing"

func decisionProfiles() map[string]ProviderSettings {
	return map[string]ProviderSettings{
		"native-fast":     {Kind: "responses", Model: "fast"},
		"native-careful":  {Kind: "chat-completions", Model: "careful"},
		"external-review": {Kind: "claude", Model: "auto", Account: "review"},
	}
}

func validDecisionSettings() DecisionSettings {
	return DecisionSettings{
		Enabled:       true,
		Kind:          "openrouter",
		BaseURL:       "http://127.0.0.1:43123/api/alpha/decisions",
		Model:         "~typesafe/jev-latest",
		APIKeyEnv:     "OPENROUTER_API_KEY",
		MinConfidence: 0.8,
		Candidates: map[string]string{
			"native-fast":     "short, routine implementation work",
			"native-careful":  "complex reasoning and risky changes",
			"external-review": "vendor CLI review of a broad change",
		},
	}
}

func TestDecisionSettingsValidateAcceptsNativeAndExternalCandidates(t *testing.T) {
	if err := validDecisionSettings().Validate(decisionProfiles()); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionSettingsValidateAllowsZeroForRuntimeDefault(t *testing.T) {
	d := validDecisionSettings()
	d.MinConfidence = 0 // routeAgent applies the documented 0.8 default.
	if err := d.Validate(decisionProfiles()); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionSettingsValidateRejectsUnsafeOrIncompletePolicy(t *testing.T) {
	tests := []struct {
		name string
		edit func(*DecisionSettings)
	}{
		{"kind", func(d *DecisionSettings) { d.Kind = "unknown" }},
		{"model", func(d *DecisionSettings) { d.Model = " " }},
		{"api key environment", func(d *DecisionSettings) { d.APIKeyEnv = "OPENROUTER-KEY" }},
		{"confidence", func(d *DecisionSettings) { d.MinConfidence = 1.01 }},
		{"unknown candidate", func(d *DecisionSettings) { d.Candidates["missing"] = "not configured" }},
		{"too few candidates", func(d *DecisionSettings) {
			delete(d.Candidates, "native-careful")
			delete(d.Candidates, "external-review")
		}},
		{"public http endpoint", func(d *DecisionSettings) { d.BaseURL = "http://decision.example/v1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := validDecisionSettings()
			tt.edit(&d)
			if err := d.Validate(decisionProfiles()); err == nil {
				t.Fatal("accepted invalid decision policy")
			}
		})
	}
}

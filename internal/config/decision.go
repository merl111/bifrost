package config

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// DecisionSettings is an opt-in, bounded routing policy, separate from the
// generative provider. Candidate descriptions explain cost and capability.
type DecisionSettings struct {
	Enabled       bool              `json:"enabled"`
	Kind          string            `json:"kind,omitempty"`
	BaseURL       string            `json:"base_url,omitempty"`
	Model         string            `json:"model,omitempty"`
	APIKeyEnv     string            `json:"api_key_env,omitempty"`
	MinConfidence float64           `json:"min_confidence,omitempty"`
	Candidates    map[string]string `json:"candidates,omitempty"`
}

func (d DecisionSettings) Validate(profiles map[string]ProviderSettings) error {
	if !d.Enabled {
		return nil
	}
	if d.Kind != "typesafe" && d.Kind != "openrouter" {
		return errors.New("decision kind must be typesafe or openrouter")
	}
	if strings.TrimSpace(d.Model) == "" || len(d.Model) > 256 {
		return errors.New("decision model is required (up to 256 bytes)")
	}
	if !envName.MatchString(d.APIKeyEnv) {
		return errors.New("decision api_key_env must name an environment variable")
	}
	if math.IsNaN(d.MinConfidence) || math.IsInf(d.MinConfidence, 0) || d.MinConfidence < 0 || d.MinConfidence > 1 {
		return errors.New("decision min_confidence must be between 0 and 1")
	}
	if len(d.Candidates) < 2 || len(d.Candidates) > 16 {
		return errors.New("decision needs 2..16 candidate profiles")
	}
	for name, description := range d.Candidates {
		if _, ok := profiles[name]; !ok {
			return fmt.Errorf("decision references unknown profile %q", name)
		}
		if strings.TrimSpace(description) == "" || len(description) > 1024 {
			return errors.New("decision candidate descriptions must contain 1..1024 bytes")
		}
	}
	return (ProviderSettings{Kind: "chat-completions", Model: d.Model, BaseURL: d.BaseURL}).Validate()
}

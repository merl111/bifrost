package main

import (
	"encoding/json"
	"fmt"

	"bifrost/internal/ui"
)

// Only fields accepted by the shared settings callback belong in the editor.
// Identity, repository rows and discovery metadata remain outside this form.
var projectDefaultFields = map[string]bool{
	"environment": true, "provider": true, "allow_write": true, "allow_exec": true,
	"browser": true, "skills": true, "mcp_config": true, "decision_enabled": true,
	"decision_kind": true, "decision_model": true, "decision_api_key_env": true,
	"decision_min_confidence": true, "decision_candidates": true,
	"github_token_env": true, "notion_token_env": true, "notion_page": true,
}

func projectDefaultsJSON(status ui.ProjectStatus) string {
	data, _ := json.Marshal(status)
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(data, &fields)
	for key := range fields {
		if !projectDefaultFields[key] {
			delete(fields, key)
		}
	}
	data, _ = json.MarshalIndent(fields, "", "  ")
	return string(data)
}

func parseProjectDefaults(text string, current ui.ProjectStatus) (ui.ProjectStatus, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &fields); err != nil {
		return current, err
	}
	if fields == nil {
		return current, fmt.Errorf("enter a JSON object of project defaults")
	}
	for key := range fields {
		if !projectDefaultFields[key] {
			return current, fmt.Errorf("unknown project setting %q", key)
		}
	}
	// Omitted settings retain their current values; the shared service validates
	// provider/profile references and the configuration before persisting it.
	next := current
	err := json.Unmarshal([]byte(text), &next)
	return next, err
}

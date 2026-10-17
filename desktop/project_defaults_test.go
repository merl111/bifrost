package main

import (
	"strings"
	"testing"

	"bifrost/internal/ui"
)

func TestProjectDefaultsPreserveIdentityAndOmittedValues(t *testing.T) {
	current := ui.ProjectStatus{ProjectName: "workspace", ProjectID: "project-id", Root: "/private/root", Provider: "work", Skills: []string{"review"}, Repos: []ui.ProjectRepoEntry{{Alias: "api"}, {Alias: "web"}}}
	text := projectDefaultsJSON(current)
	if strings.Contains(text, "/private/root") || strings.Contains(text, "project-id") || strings.Contains(text, "repos") {
		t.Fatalf("discovery metadata exposed in defaults: %s", text)
	}
	next, err := parseProjectDefaults(`{"allow_write":true}`, current)
	if err != nil || !next.AllowWrite || next.ProjectName != current.ProjectName || len(next.Repos) != 2 || next.Provider != "work" || len(next.Skills) != 1 {
		t.Fatalf("partial edit lost unrelated project data: %+v %v", next, err)
	}
	for _, bad := range []string{`{"project_name":"other"}`, `{"allow_write":"yes"}`, `null`, `[]`, `{"api_key":"secret"}`} {
		if _, err := parseProjectDefaults(bad, current); err == nil {
			t.Fatalf("accepted invalid defaults %q", bad)
		}
	}
}

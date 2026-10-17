package config

import (
	"strings"
	"testing"
)

func TestProjectConfigStrictVersionAndResolution(t *testing.T) {
	manifest, err := LoadProject(strings.NewReader(`{"schema_version":1,"name":"storefront","repositories":[{"alias":"web"},{"alias":"api"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	local, err := LoadLocal(strings.NewReader(`{"schema_version":1,"project_ref":"storefront","environments":{"local":{"kind":"local","repositories":{"web":"/work/web","api":"/work/api"}}}}`))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(manifest, local)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved.Roots["local"]["api"]; got != "/work/api" {
		t.Fatalf("root = %q", got)
	}
	for _, raw := range []string{
		`{"schema_version":2,"name":"x","repositories":[{"alias":"web"}]}`,
		`{"schema_version":1,"name":"x","repositories":[{"alias":"web"}],"unexpected":true}`,
	} {
		if _, err := LoadProject(strings.NewReader(raw)); err == nil {
			t.Errorf("LoadProject(%s) unexpectedly succeeded", raw)
		}
	}
	badLocal := local
	badLocal.Environments["local"] = Environment{Kind: "local", Repositories: map[string]string{"missing": "/work/missing"}}
	if _, err := Resolve(manifest, badLocal); err == nil {
		t.Fatal("unknown repository root unexpectedly resolved")
	}
}

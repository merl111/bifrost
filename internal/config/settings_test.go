package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsAtomicSaveStrictLoadAndProfiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	cfg := DefaultSettings()
	cfg.Providers["work.local"] = ProviderSettings{Kind: "responses", Model: "m", APIKeyEnv: "MY_API_KEY"}
	cfg.ActiveProvider = "work.local"
	cfg.Projects["demo"] = ProjectDefaults{Provider: "work.local", Skills: []string{"coding"}}
	if err := SaveSettings(p, cfg, false); err != nil {
		t.Fatal(err)
	}
	if err := SaveSettings(p, cfg, false); err == nil {
		t.Fatal("overwrote config without opt-in")
	}
	got, err := LoadSettings(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForProject("demo").Provider != "work.local" || got.ForProject("demo").AllowExec {
		t.Fatal("profile permissions")
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "secret") {
		t.Fatal("unexpected credential")
	}
	info, _ := os.Stat(p)
	if info.Mode().Perm()&0077 != 0 {
		t.Fatal("configuration is not private")
	}
	os.WriteFile(p, append(b[:len(b)-2], []byte(",\"api_key\":\"secret\"}\n")...), 0600)
	if _, err := LoadSettings(p); err == nil {
		t.Fatal("unknown fields accepted")
	}
}
func TestSettingsRejectInvalidProviderAndGuest(t *testing.T) {
	for _, url := range []string{"https://user:secret@example.com/v1", "https://example.com/?key=x", "file:///tmp/key", "http://public.example/v1"} {
		p := ProviderSettings{Kind: "responses", Model: "m", BaseURL: url}
		if p.Validate() == nil {
			t.Fatal(url)
		}
	}
	cfg := DefaultSettings()
	cfg.SSH["vm"] = SSHSettings{Target: "-oProxyCommand=x", Root: "/root", StateDir: "/state"}
	if cfg.Validate() == nil {
		t.Fatal("SSH option injection")
	}
}

func TestSettingsForProjectDoesNotAliasDefaultSkills(t *testing.T) {
	cfg := DefaultSettings()
	cfg.Defaults.Skills = []string{"one", "two"}
	defaults := cfg.ForProject("")
	defaults.Skills[0] = "changed"
	if cfg.Defaults.Skills[0] != "one" {
		t.Fatal("project defaults share the settings skill backing array")
	}
}

func TestSettingsRejectsSSHValuesRuntimeWouldReject(t *testing.T) {
	base := DefaultSettings()
	base.SSH["guest"] = SSHSettings{Target: "user@host", Root: "/workspace", StateDir: "/state", IdentityFile: "relative-key"}
	if base.Validate() == nil {
		t.Fatal("accepted relative identity file")
	}
	base.SSH["guest"] = SSHSettings{Target: "user@\thost", Root: "/workspace", StateDir: "/state"}
	if base.Validate() == nil {
		t.Fatal("accepted tab in SSH target")
	}
}

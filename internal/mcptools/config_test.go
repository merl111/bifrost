package mcptools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bifrost/internal/mcpclient"
)

func TestConfigCRUDStoresHeaderReferencesOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	server := ServerConfig{Name: "demo", URL: "https://example.test/mcp", Headers: []mcpclient.HeaderRef{{Name: "Authorization", Env: "BIFROST_TOKEN"}}}
	if err := AddServer(path, server); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || !strings.Contains(string(data), "BIFROST_TOKEN") {
		t.Fatalf("config contains unexpected credentials: %s", data)
	}
	if mode := mustMode(t, path).Perm(); mode != 0600 {
		t.Fatalf("mode=%#o", mode)
	}
	servers, err := ConfigServers(path)
	if err != nil || len(servers) != 1 || servers[0].Headers[0].Env != "BIFROST_TOKEN" {
		t.Fatalf("servers=%+v err=%v", servers, err)
	}
	if err := AddServer(path, ServerConfig{Name: "demo", Command: []string{"fixture"}}); err != nil {
		t.Fatal(err)
	}
	servers, err = ConfigServers(path)
	if err != nil || len(servers) != 1 || len(servers[0].Command) != 1 {
		t.Fatalf("replacement=%+v err=%v", servers, err)
	}
	if err := RemoveServer(path, "demo"); err != nil {
		t.Fatal(err)
	}
	servers, err = ConfigServers(path)
	if err != nil || len(servers) != 0 {
		t.Fatalf("removed=%+v err=%v", servers, err)
	}
}

func TestAddServerRejectsHeaderLiteralAndInvalidReference(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	for _, ref := range []mcpclient.HeaderRef{{Name: "Authorization", Env: ""}, {Name: "X\nBad", Env: "TOKEN"}} {
		if err := AddServer(path, ServerConfig{Name: "demo", URL: "https://example.test", Headers: []mcpclient.HeaderRef{ref}}); err == nil {
			t.Fatalf("accepted ref %+v", ref)
		}
	}
}

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

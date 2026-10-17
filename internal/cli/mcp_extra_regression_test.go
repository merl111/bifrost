package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMCPConfigUsesExplicitMCPConfigFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	var out bytes.Buffer
	if err := runMCPConfig(options{configPath: filepath.Join(dir, "settings.json")}, []string{"add", "--mcp-config", path, "--name", "fixture", "--url", "https://example.test/mcp"}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("application settings path was touched: %v", err)
	}
	if err := runMCPConfig(options{configPath: filepath.Join(dir, "settings.json")}, []string{"list", "--mcp-config", path}, &out); err != nil {
		t.Fatal(err)
	}
	var servers []map[string]any
	if err := json.Unmarshal(out.Bytes(), &servers); err != nil || len(servers) != 1 || servers[0]["name"] != "fixture" {
		t.Fatalf("list output=%q err=%v", out.String(), err)
	}
	if err := runMCPConfig(options{}, []string{"list", "--config", path}, &out); err == nil {
		t.Fatal("accepted application --config as MCP config path")
	}
}

func TestMCPConfigCommandThroughCLIRun(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	var out, errOut bytes.Buffer
	args := []string{"--state-dir", dir, "mcp", "config", "add", "--mcp-config", path, "--name", "fixture", "--url", "https://example.test/mcp"}
	if err := Run(context.Background(), args, &out, &errOut); err != nil {
		t.Fatalf("add: %v (%s)", err, errOut.String())
	}
	out.Reset()
	if err := Run(context.Background(), []string{"--state-dir", dir, "mcp", "config", "list", "--mcp-config", path}, &out, &errOut); err != nil {
		t.Fatalf("list: %v (%s)", err, errOut.String())
	}
	if !strings.Contains(out.String(), "fixture") {
		t.Fatalf("list output=%q", out.String())
	}
}

func TestMCPConfigNeverStoresHeaderSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.json")
	var out bytes.Buffer
	if err := runMCPConfig(options{}, []string{"add", "--mcp-config", path, "--name", "fixture", "--url", "https://example.test/mcp", "--header", "Authorization=TOKEN_ENV"}, &out); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "TOKEN_ENV") == false || strings.Contains(string(b), "secret-value") {
		t.Fatalf("unexpected header config: %s", b)
	}
}

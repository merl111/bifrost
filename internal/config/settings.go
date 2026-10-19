package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"bifrost/internal/atomicfile"
	"bifrost/internal/domain"
	"bifrost/internal/execpolicy"
	"bifrost/internal/filelock"
	"bifrost/internal/hooks"
	"net/url"
)

const maxSettingsBytes = 1 << 20

// Settings contains user-approved machine-local defaults, never literal API keys.
// Project files are not automatically promoted to trusted execution settings.
type Settings struct {
	Subagents      SubagentSettings            `json:"subagents"`
	Execution      execpolicy.Settings         `json:"execution"`
	Hooks          []hooks.Hook                `json:"hooks,omitempty"`
	SchemaVersion  int                         `json:"schema_version"`
	ActiveProvider string                      `json:"active_provider,omitempty"`
	Providers      map[string]ProviderSettings `json:"providers,omitempty"`
	Defaults       RunDefaults                 `json:"defaults"`
	Projects       map[string]ProjectDefaults  `json:"projects,omitempty"`
	SSH            map[string]SSHSettings      `json:"ssh,omitempty"`
	UI             UISettings                  `json:"ui"`
	Decision       DecisionSettings            `json:"decision,omitempty"`
	Marketplace    MarketplaceSettings         `json:"marketplace,omitempty"`
	Connectors     ConnectorSettings           `json:"connectors,omitempty"`
}

type SubagentSettings struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (s SubagentSettings) Validate(providers map[string]ProviderSettings) error {
	if s.Provider != "" {
		if _, ok := providers[s.Provider]; !ok {
			return errors.New("subagents.provider must name a saved provider")
		}
	}
	if len(s.Model) > 256 || strings.ContainsAny(s.Model, "\x00\r\n") {
		return errors.New("invalid subagent model")
	}

	return nil
}

type ConnectorSettings struct {
	GitHubTokenEnv string `json:"github_token_env,omitempty"`
	NotionTokenEnv string `json:"notion_token_env,omitempty"`
}
type ProviderSettings struct {
	Kind       string `json:"kind"`
	BaseURL    string `json:"base_url,omitempty"`
	Model      string `json:"model"`
	APIKeyEnv  string `json:"api_key_env,omitempty"`
	Account    string `json:"account,omitempty"`
	Executable string `json:"executable,omitempty"`
}

type MarketplaceSettings struct {
	CatalogURL string `json:"catalog_url,omitempty"`
}
type RunDefaults struct {
	Project    string   `json:"project,omitempty"`
	Provider   string   `json:"provider,omitempty"`
	Skills     []string `json:"skills,omitempty"`
	MCPConfig  string   `json:"mcp_config,omitempty"`
	AllowWrite bool     `json:"allow_write"`
	AllowExec  bool     `json:"allow_exec"`
	Browser    bool     `json:"browser"`
	MaxSteps   int      `json:"max_steps"`
	MaxAgents  int      `json:"max_agents"`
}
type ProjectDefaults struct {
	Provider       string   `json:"provider,omitempty"`
	Skills         []string `json:"skills"`
	MCPConfig      string   `json:"mcp_config,omitempty"`
	Environment    string   `json:"environment,omitempty"`
	GitHubRepo     string   `json:"github_repo,omitempty"`
	NotionPage     string   `json:"notion_page,omitempty"`
	GitHubTokenEnv string   `json:"github_token_env,omitempty"`
	NotionTokenEnv string   `json:"notion_token_env,omitempty"`
}
type SSHSettings struct {
	Target       string `json:"target"`
	Port         int    `json:"port,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
	Executable   string `json:"executable,omitempty"`
	Root         string `json:"root"`
	StateDir     string `json:"state_dir"`
}
type UISettings struct {
	ComposerMaxRows int      `json:"composer_max_rows,omitempty"`
	NewlineKey      string   `json:"newline_key,omitempty"`
	Theme           string   `json:"theme"`
	Icons           string   `json:"icons,omitempty"`
	Editor          []string `json:"editor,omitempty"`
	Layout          string   `json:"layout,omitempty"`
	PaneProportions [3]int   `json:"pane_proportions,omitempty"`
	TerminalVisible *bool    `json:"terminal_visible,omitempty"`
	TerminalPercent int      `json:"terminal_percent,omitempty"`
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func DefaultSettings() Settings {
	return Settings{SchemaVersion: 1, Providers: map[string]ProviderSettings{}, Projects: map[string]ProjectDefaults{}, SSH: map[string]SSHSettings{}, Defaults: RunDefaults{MaxSteps: 8, MaxAgents: 3}, UI: UISettings{Theme: "midnight", Icons: "unicode", ComposerMaxRows: 8, NewlineKey: "shift+enter"}}
}
func SettingsPath() (string, error) {
	if path := os.Getenv("BIFROST_CONFIG"); path != "" {
		return filepath.Abs(path)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bifrost", "config.json"), nil
}
func LoadSettings(path string) (Settings, error) {
	f, err := os.Open(path)
	if err != nil {
		return Settings{}, err
	}
	defer f.Close()
	var cfg Settings
	if err := decodeStrict(io.LimitReader(f, (1<<20)+1), &cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}
func SaveSettings(path string, cfg Settings, replace bool) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lock, err := acquireSettingsLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return errors.New("config destination must be a regular file")
		}
		if !replace {
			return errors.New("config already exists; use config set or setup --force")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeSettings(path, cfg)
}

func acquireSettingsLock(path string) (*filelock.Lock, error) {
	return filelock.Acquire(path + ".lock")
}

func settingsFileRevision(path string) (string, error) {
	b, err := readSettingsFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return MissingRevision, nil
	}
	if err != nil {
		return "", err
	}
	return settingsRevision(b), nil
}

// readSettingsFile checks the path before opening it and bounds every read.
// The pre-open Lstat is important for FIFOs: opening one for reading can
// block indefinitely when no writer is connected.
func readSettingsFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config source must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("config source must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read configuration: %w", err)
	}
	if len(b) > maxSettingsBytes {
		return nil, errors.New("configuration exceeds 1 MiB")
	}
	return b, nil
}

func writeSettings(path string, cfg Settings) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("config exceeds 1 MiB")
	}
	return atomicfile.Write(path, append(b, '\n'))
}
func (s Settings) Validate() error {
	if err := s.Subagents.Validate(s.Providers); err != nil {
		return err
	}
	if err := s.Execution.Validate(); err != nil {
		return err
	}
	if s.Marketplace.CatalogURL != "" {
		if err := (ProviderSettings{Kind: "chat-completions", Model: "catalog", BaseURL: s.Marketplace.CatalogURL}).Validate(); err != nil {
			return fmt.Errorf("marketplace catalog: %w", err)
		}
	}
	if s.Connectors.GitHubTokenEnv != "" && !envName.MatchString(s.Connectors.GitHubTokenEnv) {
		return errors.New("github_token_env must name an environment variable, not contain a token")
	}
	if s.Connectors.NotionTokenEnv != "" && !envName.MatchString(s.Connectors.NotionTokenEnv) {
		return errors.New("notion_token_env must name an environment variable, not contain a token")
	}
	if err := s.Decision.Validate(s.Providers); err != nil {
		return err
	}
	if len(s.Hooks) > 32 {
		return errors.New("too many hooks")
	}
	if len(s.Hooks) > 0 {
		if _, err := hooks.New(hooks.Config{Trusted: true, Hooks: s.Hooks}); err != nil {
			return err
		}
	}
	if s.SchemaVersion != 1 {
		return fmt.Errorf("%w: settings schema %d", ErrUnsupportedVersion, s.SchemaVersion)
	}
	if len(s.Providers) > 32 || len(s.Projects) > 256 || len(s.SSH) > 64 {
		return errors.New("too many configured entries")
	}
	if s.Defaults.MaxAgents < 0 || s.Defaults.MaxAgents > 32 {
		return errors.New("max_agents must be 0..32")
	}
	if s.Defaults.MaxSteps < 1 || s.Defaults.MaxSteps > 100 {
		return errors.New("max_steps must be 1..100")
	}
	for name, p := range s.Providers {
		if err := domain.ValidateName(name); err != nil {
			return err
		}
		if err := p.Validate(); err != nil {
			return fmt.Errorf("provider %s: %w", name, err)
		}
	}
	for _, name := range []string{s.ActiveProvider, s.Defaults.Provider} {
		if name != "" {
			if _, ok := s.Providers[name]; !ok {
				return fmt.Errorf("unknown provider profile %q", name)
			}
		}
	}
	if len(s.Defaults.Skills) > 64 {
		return errors.New("too many default skills")
	}
	for name, p := range s.Projects {
		if err := domain.ValidateName(name); err != nil {
			return err
		}
		if p.Provider != "" {
			if _, ok := s.Providers[p.Provider]; !ok {
				return fmt.Errorf("project %s references unknown provider", name)
			}
		}
		if len(p.Skills) > 64 {
			return errors.New("too many project skills")
		}
		if p.GitHubTokenEnv != "" && !envName.MatchString(p.GitHubTokenEnv) {
			return errors.New("project github_token_env must name an environment variable")
		}
		if p.NotionTokenEnv != "" && !envName.MatchString(p.NotionTokenEnv) {
			return errors.New("project notion_token_env must name an environment variable")
		}
	}
	for name, ssh := range s.SSH {
		if err := domain.ValidateName(name); err != nil {
			return err
		}
		if ssh.Target == "" || strings.HasPrefix(ssh.Target, "-") || strings.ContainsAny(ssh.Target, "\t\r\n\x00 ") {
			return errors.New("invalid SSH target")
		}
		if ssh.Port < 0 || ssh.Port > 65535 {
			return errors.New("invalid SSH port")
		}
		if !strings.HasPrefix(ssh.Root, "/") || !strings.HasPrefix(ssh.StateDir, "/") {
			return errors.New("SSH root and state_dir must be absolute guest paths")
		}
		if ssh.IdentityFile != "" && !filepath.IsAbs(ssh.IdentityFile) {
			return errors.New("SSH identity_file must be an absolute host path")
		}
		if strings.ContainsAny(ssh.Root+ssh.StateDir+ssh.Executable, "\r\n\x00") {
			return errors.New("invalid SSH path or executable")
		}
	}
	if s.UI.Theme != "midnight" && s.UI.Theme != "light" && s.UI.Theme != "plain" {
		return errors.New("theme must be midnight, light, or plain")
	}
	if s.UI.Icons != "" && s.UI.Icons != "unicode" && s.UI.Icons != "nerd" && s.UI.Icons != "ascii" {
		return errors.New("icons must be unicode, nerd, or ascii")
	}
	if err := s.UI.ValidateLayout(); err != nil {
		return err
	}
	if s.UI.ComposerMaxRows < 0 || s.UI.ComposerMaxRows > 20 {
		return errors.New("composer_max_rows must be 0 (default), or 1..20")
	}
	switch s.UI.NewlineKey {
	case "", "shift+enter", "alt+enter", "ctrl+j":
	default:
		return errors.New("newline_key must be shift+enter, alt+enter, or ctrl+j")
	}
	return nil
}
func (p ProviderSettings) Validate() error {
	switch p.Kind {
	case "chat-completions", "responses", "anthropic", "gemini", "chatgpt", "cursor", "cursor-native", "claude", "openrouter":
	default:
		return fmt.Errorf("unsupported provider kind %q", p.Kind)
	}
	if strings.TrimSpace(p.Model) == "" || len(p.Model) > 256 {
		return errors.New("model is required (up to 256 bytes)")
	}
	if p.APIKeyEnv != "" && !envName.MatchString(p.APIKeyEnv) {
		return errors.New("api_key_env must name an environment variable, not contain a key")
	}
	if p.Account != "" {
		if err := domain.ValidateName(p.Account); err != nil {
			return fmt.Errorf("account: %w", err)
		}
	}
	if p.Kind == "chatgpt" || p.Kind == "cursor" || p.Kind == "cursor-native" || p.Kind == "claude" {
		if p.Account == "" {
			return errors.New("subscription provider requires an account; use bifrost account login")
		}
		if p.BaseURL != "" || p.APIKeyEnv != "" {
			return errors.New("subscription accounts cannot override API endpoint or credential environment")
		}
	}
	if p.Executable != "" {
		if p.Kind != "cursor" && p.Kind != "claude" {
			return errors.New("executable is only supported for cursor and claude")
		}
		if len(p.Executable) > 4096 || strings.ContainsAny(p.Executable, "\x00\r\n") {
			return errors.New("invalid agent executable")
		}
	}
	if p.BaseURL != "" {
		u, err := url.Parse(p.BaseURL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme == "http" && !loopbackHost(u.Hostname())) {
			return errors.New("provider base_url must be an HTTP(S) URL without credentials, query, or fragment")
		}
	}
	if len(p.BaseURL) > 4096 {
		return errors.New("provider URL too long")
	}
	return nil
}

func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// ForProject overlays per-project resource choices without widening execution permissions.
func (s Settings) ForProject(name string) RunDefaults {
	d := s.Defaults
	d.Skills = append([]string(nil), s.Defaults.Skills...)
	if p, ok := s.Projects[name]; ok {
		if p.Provider != "" {
			d.Provider = p.Provider
		}
		if p.Skills != nil {
			d.Skills = append([]string(nil), p.Skills...)
		}
		if p.MCPConfig != "" {
			d.MCPConfig = p.MCPConfig
		}
	}
	return d
}

package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bifrost/internal/config"
	"bifrost/internal/setup"
	"bifrost/internal/store"
)

func (o options) configFile() (string, error) {
	if o.configPath != "" {
		return filepath.Abs(o.configPath)
	}
	if o.stateDir != "" {
		return filepath.Join(o.stateDir, "config.json"), nil
	}
	return config.SettingsPath()
}
func (o options) settings() (config.Settings, error) {
	path, err := o.configFile()
	if err != nil {
		return config.Settings{}, err
	}
	cfg, err := config.LoadSettings(path)
	if errors.Is(err, os.ErrNotExist) {
		return config.DefaultSettings(), nil
	}
	return cfg, err
}
func interactive(out io.Writer) bool {
	f, ok := out.(*os.File)
	if !ok {
		return false
	}
	a, e := f.Stat()
	b, e2 := os.Stdin.Stat()
	return e == nil && e2 == nil && a.Mode()&os.ModeCharDevice != 0 && b.Mode()&os.ModeCharDevice != 0 && os.Getenv("TERM") != "dumb"
}
func firstRun(ctx context.Context, opts options, out io.Writer) (options, error) {
	path, err := opts.configFile()
	if err != nil {
		return opts, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) && interactive(out) && envFirst("BIFROST_MODEL", "OPENAI_MODEL") == "" {
		if err := runSetup(ctx, opts, nil, out, out); err != nil {
			return opts, err
		}
	}
	return applyProjectDefault(opts)
}
func applyProjectDefault(opts options) (options, error) {
	cfg, err := opts.settings()
	if err != nil {
		return opts, err
	}
	if opts.root == "" && opts.project == "" {
		opts.project = cfg.Defaults.Project
	}
	return opts, nil
}
func runSetup(ctx context.Context, opts options, args []string, out, stderr io.Writer) error {
	cfg, err := opts.settings()
	if err != nil {
		return err
	}
	path, err := opts.configFile()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	kind, model, base, keyEnv, project := "offline", "", "", "", opts.project
	force, noninteractive := false, false
	fs.StringVar(&kind, "provider", kind, "offline, responses, anthropic, gemini, chat-completions")
	fs.StringVar(&model, "model", "", "model ID")
	fs.StringVar(&base, "base-url", "", "API base URL")
	fs.StringVar(&keyEnv, "api-key-env", "", "environment variable containing credential")
	fs.StringVar(&project, "name", project, "project name")
	fs.BoolVar(&force, "force", false, "replace existing settings")
	fs.BoolVar(&noninteractive, "non-interactive", false, "write settings from explicit flags")
	fs.BoolVar(&cfg.Defaults.AllowWrite, "allow-write", cfg.Defaults.AllowWrite, "enable file edits")
	fs.BoolVar(&cfg.Defaults.AllowExec, "allow-exec", cfg.Defaults.AllowExec, "enable host commands")
	fs.BoolVar(&cfg.Defaults.Browser, "browser", cfg.Defaults.Browser, "enable browser tools")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("setup takes named flags")
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return err
	}
	var result setup.Result
	if !noninteractive && interactive(out) {
		result, err = setup.Run(ctx, cfg, root)
		if err != nil {
			return err
		}
	} else {
		if !noninteractive {
			return errors.New("setup needs a terminal; use --non-interactive with --provider, --model and --name")
		}
		if project == "" {
			project = filepath.Base(root)
		}
		if cfg.Providers == nil {
			cfg.Providers = map[string]config.ProviderSettings{}
		}
		if kind != "offline" {
			if keyEnv == "" {
				keyEnv = defaultAPIKeyEnv(kind)
			}
			cfg.Providers[kind] = config.ProviderSettings{Kind: kind, BaseURL: base, Model: model, APIKeyEnv: keyEnv}
			cfg.ActiveProvider = kind
			cfg.Defaults.Provider = kind
		} else {
			cfg.ActiveProvider = ""
			cfg.Defaults.Provider = ""
		}
		cfg.Defaults.Project = project
		result = setup.Result{Settings: cfg, Project: project, Root: root}
	}
	if err := result.Settings.Validate(); err != nil {
		return err
	}
	// Existing settings require an explicit replacement; validation precedes any project write.
	if _, err := os.Stat(path); err == nil && !force {
		return errors.New("configuration already exists; use setup --force or config set")
	}
	if _, err := absoluteDirectory(result.Root); err != nil {
		return err
	}
	reg, err := openRegistry(opts)
	if err != nil {
		return err
	}
	existing, findErr := reg.GetByName(result.Project)
	if findErr == nil {
		matched := false
		for _, repo := range existing.Repositories {
			if repo.Roots["local"] == result.Root {
				matched = true
			}
		}
		if !matched {
			reg.Close()
			return errors.New("existing project has a different root; choose another name")
		}
	} else if errors.Is(findErr, store.ErrProjectNotFound) {
		_, err = reg.Create(store.CreateProjectRequest{Name: result.Project, Repositories: []store.RepositoryInput{{Alias: "main", Roots: map[string]string{"local": result.Root}}}})
	} else {
		err = findErr
	}
	reg.Close()
	if err != nil {
		return err
	}
	if err := config.SaveSettings(path, result.Settings, force); err != nil {
		return err
	}
	fmt.Fprintf(out, "Configured Bifrost: %s\nProject: %s\nLaunch: bifrost tui\n", path, result.Project)
	if result.Settings.ActiveProvider != "" {
		p := result.Settings.Providers[result.Settings.ActiveProvider]
		if p.APIKeyEnv != "" && os.Getenv(p.APIKeyEnv) == "" {
			fmt.Fprintf(out, "Credential: set %s before using the provider (local endpoints may not require one).\n", p.APIKeyEnv)
		}
	}
	return nil
}
func defaultAPIKeyEnv(kind string) string {
	switch kind {
	case "responses":
		return "OPENAI_API_KEY"
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "chatgpt", "cursor", "cursor-native", "claude":
		return ""
	default:
		return "BIFROST_API_KEY"
	}
}
func runConfig(opts options, args []string, out io.Writer) error {
	path, err := opts.configFile()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "path" {
		fmt.Fprintln(out, path)
		return nil
	}
	cfg, err := opts.settings()
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "show" {
		return writeJSON(out, cfg)
	}
	switch args[0] {
	case "import":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("config import FILE [--apply]")
		}
		incoming, err := config.LoadSettings(args[1])
		if err != nil {
			return err
		}
		if len(args) == 3 {
			if args[2] != "--apply" {
				return errors.New("expected --apply")
			}
			return config.SaveSettings(path, incoming, true)
		}
		return writeJSON(out, map[string]any{"preview": incoming, "destination": path, "apply": "config import FILE --apply"})
	case "set":
		if len(args) != 3 {
			return errors.New("config set KEY VALUE")
		}
		if err := setConfigValue(&cfg, args[1], args[2]); err != nil {
			return err
		}
		return config.SaveSettings(path, cfg, true)
	default:
		return errors.New("usage: config show | path | set KEY VALUE | import FILE [--apply]")
	}
}
func setConfigValue(cfg *config.Settings, key, value string) error {
	return config.SetValue(cfg, key, value)
}

// decodeConfigValue is shared by the CLI's bounded JSON plan inputs. Config
// settings themselves use config.SetValue so they also receive validation and
// copy-before-commit semantics.
func decodeConfigValue(value string, target any) error {
	d := json.NewDecoder(strings.NewReader(value))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("expected one JSON value")
	}
	return nil
}

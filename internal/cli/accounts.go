package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bifrost/internal/accounts"
	"bifrost/internal/agentbackend"
	"bifrost/internal/config"
	"bifrost/internal/cursorclient"
	"bifrost/internal/domain"
	"bifrost/internal/filelock"
	"bifrost/internal/provider"
)

func accountStore(opts options) (*accounts.Store, error) {
	path, err := opts.configFile()
	if err != nil {
		return nil, err
	}
	return accounts.New(filepath.Join(filepath.Dir(path), "accounts"))
}

func accountDirectory(opts options, id string) (string, error) {
	if err := domain.ValidateName(id); err != nil {
		return "", err
	}
	path, err := opts.configFile()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(path), "accounts", "agents", id), nil
}

func runAccount(ctx context.Context, opts options, args []string, out, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: account login KIND NAME [--model MODEL] | list | use PROFILE | status PROFILE | logout PROFILE | models PROFILE")
	}
	if args[0] == "login" {
		return loginAccount(ctx, opts, args[1:], out, stderr)
	}
	settings, err := opts.settings()
	if err != nil {
		return err
	}
	store, err := accountStore(opts)
	if err != nil {
		return err
	}
	if args[0] == "list" && len(args) == 1 {
		items, err := store.List()
		if err != nil {
			return err
		}
		return writeJSON(out, items)
	}
	if len(args) != 2 {
		return errors.New("account command requires a provider profile name")
	}
	profile := args[1]
	p, ok := settings.Providers[profile]
	if !ok {
		return fmt.Errorf("unknown provider profile %q", profile)
	}
	if args[0] == "use" {
		if p.Account != "" {
			if _, err := store.GetByName(accounts.Kind(p.Kind), p.Account); err != nil {
				return err
			}
		}
		if err := selectProfile(opts, &settings, profile); err != nil {
			return err
		}
		_, err := fmt.Fprintf(out, "Active profile: %s\n", profile)
		return err
	}
	if p.Account == "" {
		return errors.New("profile does not reference a saved account")
	}
	a, err := store.GetByName(accounts.Kind(p.Kind), p.Account)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if a.Kind == accounts.KindCursor || a.Kind == accounts.KindClaude {
			cfg := authAgentConfig(a, p.Executable)
			return agentbackend.Status(ctx, cfg, os.Stdin, out, stderr)
		}
		return writeJSON(out, a)
	case "models":
		if a.Kind == accounts.KindCursorNative {
			access, err := cursorAccessToken(ctx, store, a)
			if err != nil {
				return err
			}
			models, err := (cursorclient.Client{AccessToken: access, TeamID: cursorTeam(a)}).Models(ctx)
			if err != nil {
				return err
			}
			return writeJSON(out, models)
		}
		if a.Kind != accounts.KindChatGPT {
			return errors.New("account models currently supports ChatGPT and native Cursor; use the vendor model catalog for other accounts")
		}
		client, err := provider.NewChatGPT(provider.ChatGPTConfig{Model: p.Model, TokenSource: chatGPTTokenSource(store, a)})
		if err != nil {
			return err
		}
		models, err := client.Models(ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, models)
	case "logout":
		if err := logoutAccount(ctx, store, a, p.Executable, out, stderr); err != nil {
			return err
		}
		fmt.Fprintf(out, "Signed out: %s. Profile retained; log in again before use.\n", profile)
		return nil
	default:
		return fmt.Errorf("unknown account command %q", args[0])
	}
}

func selectProfile(opts options, settings *config.Settings, name string) error {
	if _, ok := settings.Providers[name]; !ok {
		return errors.New("unknown provider profile")
	}
	settings.ActiveProvider = name
	settings.Defaults.Provider = name
	if opts.project != "" {
		p := settings.Projects[opts.project]
		p.Provider = name
		settings.Projects[opts.project] = p
	}
	path, err := opts.configFile()
	if err != nil {
		return err
	}
	return config.SaveSettings(path, *settings, true)
}

func loginAccount(ctx context.Context, opts options, args []string, out, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: account login chatgpt|cursor|cursor-native|claude|openrouter NAME [--model MODEL] [--api-key-env NAME] [--executable PATH]")
	}
	kind, name := accounts.Kind(args[0]), args[1]
	id, err := accounts.StableID(kind, name)
	if err != nil {
		return err
	}
	if err := domain.ValidateName(id); err != nil {
		return err
	}
	fs := flag.NewFlagSet("account login", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var model, keyEnv, executable string
	fs.StringVar(&model, "model", "", "model ID; ChatGPT discovers a default, CLI agents use auto")
	fs.StringVar(&keyEnv, "api-key-env", "OPENROUTER_API_KEY", "OpenRouter API key environment variable")
	fs.StringVar(&executable, "executable", "", "installed Cursor or Claude executable")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected login arguments")
	}
	settings, err := opts.settings()
	if err != nil {
		return err
	}
	store, err := accountStore(opts)
	if err != nil {
		return err
	}
	a, loadErr := store.GetByName(kind, name)
	if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return loadErr
	}
	if loadErr != nil {
		a = accounts.Account{ID: id, Name: name, Kind: kind}
	}
	if saved, ok := settings.Providers[id]; ok {
		if model == "" {
			model = saved.Model
		}
		if executable == "" {
			executable = saved.Executable
		}
	}
	checkModel := model
	if checkModel == "" {
		checkModel = "auto"
	}
	check := config.ProviderSettings{Kind: string(kind), Account: name, Model: checkModel, Executable: executable}
	if kind == accounts.KindOpenRouter {
		check.APIKeyEnv = keyEnv
	}
	if err := check.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	switch kind {
	case accounts.KindChatGPT:
		host, err := store.HostID()
		if err != nil {
			return err
		}
		oauth, err := accounts.NewOAuth(accounts.OAuthConfig{ClientID: a.ClientID, HostID: host, Name: name, AgentNameHint: "bifrost"})
		if err != nil {
			return err
		}
		signed, creds, err := oauth.SignIn(ctx, func(url string) error {
			if out != nil {
				_, _ = fmt.Fprintf(out, "Continue with ChatGPT in your browser:\n%s\nWaiting for sign-in…\n", url)
			}
			if os.Getenv("NO_OPEN_BROWSER") == "" && !strings.HasPrefix(url, "http://127.0.0.1:") {
				_ = openBrowserURL(url)
			}
			return nil
		})
		if err != nil {
			return err
		}
		signed.ID, signed.Name = id, name
		if err := store.Save(signed, creds); err != nil {
			return err
		}
		a, err = store.Get(id)
		if err != nil {
			return err
		}
		if model == "" {
			client, err := provider.NewChatGPT(provider.ChatGPTConfig{Model: "catalog", TokenSource: chatGPTTokenSource(store, a)})
			if err != nil {
				return err
			}
			models, err := client.Models(ctx)
			if err != nil {
				return fmt.Errorf("account saved; model discovery failed, repeat login with --model: %w", err)
			}
			if len(models) == 0 {
				return errors.New("account saved but no models available for plan usage")
			}
			model = models[0].Slug
		}
	case accounts.KindCursorNative:
		signed, creds, discovered, err := loginCursorNative(ctx, a, out)
		if err != nil {
			return err
		}
		signed.ID, signed.Name = id, name
		if model == "" {
			model = discovered
		}
		if model == "" {
			model = "auto"
		}
		if err := store.Save(signed, creds); err != nil {
			return err
		}
	case accounts.KindCursor, accounts.KindClaude:
		dir, err := accountDirectory(opts, id)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		lock, err := filelock.Acquire(dir + ".login.lock")
		if err != nil {
			return fmt.Errorf("account is busy: %w", err)
		}
		defer lock.Close()
		a.ConfigDir = dir
		cfg := authAgentConfig(a, executable)
		if err := agentbackend.Login(ctx, cfg, os.Stdin, out, stderr); err != nil {
			return err
		}
		// Vendor CLIs own identity verification; a fresh generation prevents
		// resuming a previous login's conversation under a replacement identity.
		a.Generation, err = accountGeneration()
		if err != nil {
			return err
		}
		if err := store.Save(a); err != nil {
			return err
		}
		if model == "" {
			model = "auto"
		}
	case accounts.KindOpenRouter:
		if model == "" {
			model = "openrouter/auto"
		}
		a.APIKeyEnv = keyEnv
		p := config.ProviderSettings{Kind: string(kind), Model: model, Account: name, APIKeyEnv: keyEnv}
		if err := p.Validate(); err != nil {
			return err
		}
		key := strings.TrimSpace(os.Getenv(keyEnv))
		if key == "" {
			if existing, loadErr := store.LoadCredentials(a.ID); loadErr == nil {
				key = strings.TrimSpace(existing.APIKey)
			}
		}
		if key == "" {
			return fmt.Errorf("set %s before connecting OpenRouter", keyEnv)
		}
		a.Generation, err = accountGeneration()
		if err != nil {
			return err
		}
		if err := store.Save(a, accounts.Credentials{APIKey: key}); err != nil {
			return err
		}
	}
	p := config.ProviderSettings{Kind: string(kind), Model: model, Account: name, Executable: executable}
	if kind == accounts.KindOpenRouter {
		p.APIKeyEnv = keyEnv
	}
	settings.Providers[id] = p
	if err := selectProfile(opts, &settings, id); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Connected %s as %s\nModel: %s\nLaunch: bifrost tui --profile %s\n", kind, name, model, id)
	return err
}

func accountGeneration() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func authAgentConfig(a accounts.Account, executable string) agentbackend.Config {
	if executable == "" {
		if a.Kind == accounts.KindCursor {
			executable = "cursor-agent"
		} else {
			executable = "claude"
		}
	}
	login, status := []string{executable, "login"}, []string{executable, "status"}
	if a.Kind == accounts.KindClaude {
		login = []string{executable, "auth", "login"}
		status = []string{executable, "auth", "status"}
	}
	return agentbackend.Config{Kind: agentbackend.Kind(a.Kind), Executable: executable, AccountDir: a.ConfigDir, Root: a.ConfigDir, LoginCommand: login, StatusCommand: status}
}

func logoutAccount(ctx context.Context, store *accounts.Store, a accounts.Account, executable string, out, stderr io.Writer) error {
	if a.Kind == accounts.KindChatGPT {
		oauth, err := accounts.NewOAuth(accounts.OAuthConfig{ClientID: a.ClientID, HostID: a.HostID})
		if err != nil {
			return err
		}
		return oauth.Logout(ctx, store, a.ID)
	}
	if a.Kind == accounts.KindCursor || a.Kind == accounts.KindClaude {
		lock, err := filelock.Acquire(a.ConfigDir + ".login.lock")
		if err != nil {
			return fmt.Errorf("account is busy: %w", err)
		}
		defer lock.Close()
		cfg := authAgentConfig(a, executable)
		cfg.LoginCommand = []string{cfg.Executable, "logout"}
		if a.Kind == accounts.KindClaude {
			cfg.LoginCommand = []string{cfg.Executable, "auth", "logout"}
		}
		if err := agentbackend.Login(ctx, cfg, os.Stdin, out, stderr); err != nil {
			return err
		}
	}
	return store.Remove(a.ID)
}

func openBrowserURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return errors.New("empty URL")
	}
	var candidates [][]string
	switch runtime.GOOS {
	case "darwin":
		candidates = [][]string{{"open", rawURL}}
	case "windows":
		candidates = [][]string{{"rundll32", "url.dll,FileProtocolHandler", rawURL}}
	default:
		candidates = [][]string{
			{"xdg-open", rawURL},
			{"gio", "open", rawURL},
			{"sensible-browser", rawURL},
			{"x-www-browser", rawURL},
		}
	}
	for _, argv := range candidates {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, argv[1:]...)
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err := cmd.Start(); err == nil {
			go func() { _ = cmd.Wait() }()
			return nil
		}
	}
	return errors.New("could not find a browser launcher (xdg-open/open)")
}

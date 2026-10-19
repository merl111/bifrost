package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"

	"bifrost/internal/accounts"
	"bifrost/internal/agent"
	"bifrost/internal/agentbackend"
	"bifrost/internal/config"
	"bifrost/internal/cursorclient"
	"bifrost/internal/domain"
	"bifrost/internal/provider"
	"bifrost/internal/session"
	"bifrost/internal/store"
	"bifrost/internal/ui"
)

// workspaceChat serializes turns and account switches around one conversation.
type workspaceChat struct {
	mu                  sync.Mutex
	ctx                 context.Context
	cancel              context.CancelFunc
	opts                options
	args                []string
	setup               agentSetup
	monitor             *taskMonitorState
	stderr              io.Writer
	store               *session.Store
	current             session.Session
	contextInstructions string
	cleanup             func()
	// Frontends without the TUI's interactive viewer must not advertise its
	// highlight and walkthrough tools as available to the model.
}

func newWorkspaceChat(ctx context.Context, opts options, args []string, setup agentSetup, monitor *taskMonitorState, stderr io.Writer) (*workspaceChat, error) {
	st, err := sessionStore(opts)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	c := &workspaceChat{ctx: ctx, cancel: cancel, opts: opts, args: args, setup: setup, monitor: monitor, stderr: stderr, store: st, cleanup: func() {}}
	if setup.cfg.Provider != nil || setup.cfg.RunExternal != nil {
		c.current, err = loadOrCreateSession(st, setup.sessionID, setup.cfg.Root, setup.fingerprint)
	} else if setup.sessionID != "" {
		err = errors.New("--session requires a configured provider")
	}
	if err != nil {
		cancel()
	}
	return c, err
}

func (c *workspaceChat) close() { c.cancel(); c.mu.Lock(); defer c.mu.Unlock(); c.cleanup() }

func (c *workspaceChat) history() []ui.ChatEvent {
	h := sessionChatHistory(c.current.Messages)
	if notice := resumeNotice(c.current); notice != "" {
		h = append(h, ui.ChatEvent{Kind: "status", Text: notice})
	}
	return h
}

func (c *workspaceChat) buildAccountChoices(settings config.Settings) []ui.AccountChoice {
	names := make([]string, 0, len(settings.Providers))
	for name := range settings.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	choices := make([]ui.AccountChoice, 0, len(names))
	for _, name := range names {
		p := settings.Providers[name]
		choices = append(choices, ui.AccountChoice{
			ID:       name,
			Label:    name,
			Detail:   p.Kind + " · " + p.Model,
			Provider: p.Kind,
			Model:    p.Model,
			Location: c.setup.cfg.Root,
			Active:   name == c.setup.cfg.ProfileName,
		})
	}
	return choices
}

func (c *workspaceChat) currentConnectionInfo(providerKind string) ui.ConnectionInfo {
	label := c.setup.cfg.ProfileName
	if label == "" {
		label = c.setup.cfg.ModelName
	}
	connected := c.setup.cfg.Provider != nil || c.setup.cfg.RunExternal != nil
	var usedTok, maxTok int
	if connected && strings.TrimSpace(c.setup.cfg.ModelName) != "" {
		usedTok = EstimateSessionTokens(c.current.Messages, valueOrString(c.contextInstructions, c.setup.cfg.Instructions), "")
		maxTok = provider.ModelContextWindowTokens(providerKind, c.setup.cfg.ModelName)
	}
	return ui.ConnectionInfo{
		Provider:         providerKind,
		Account:          label,
		Model:            c.setup.cfg.ModelName,
		Location:         c.setup.cfg.Root,
		Connected:        connected,
		ContextTokens:    usedTok,
		ContextMaxTokens: maxTok,
	}
}

func (c *workspaceChat) options() ([]ui.Option, error) {
	settings, err := c.opts.settings()
	if err != nil {
		return nil, err
	}
	choices := c.buildAccountChoices(settings)
	label := c.setup.cfg.ProfileName
	if label == "" {
		label = c.setup.cfg.ModelName
	}
	p := settings.Providers[c.setup.cfg.ProfileName]
	info := c.currentConnectionInfo(p.Kind)
	// The resolved connection includes explicit runtime overrides, which take
	// precedence over the saved profile metadata used by the account picker.
	return []ui.Option{
		ui.WithChatHistory(c.history()),
		ui.WithChatRunner(c.run),
		ui.WithAccountLabel(label),
		ui.WithAccounts(choices, c.switchAccount),
		ui.WithAccountModelSwitcher(c.switchAccountModel),
		ui.WithAccountManager(c.addAccount, c.removeAccount, c.discoverAccountModels),
		ui.WithSessions(ui.SessionCallbacks{
			List:     c.listSessions,
			Resume:   c.resumeSession,
			New:      c.newSession,
			Activate: c.activateSession,
			Compact:  c.compactCurrentSession,
		}),
		ui.WithConnectionInfo(info),
	}, nil
}

func (c *workspaceChat) run(ctx context.Context, prompt string, emit func(ui.ChatEvent)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.setup.cfg.Provider == nil && c.setup.cfg.RunExternal == nil {
		return errors.New("configure an account with :accounts (press 'a' to add) or bifrost account login")
	}
	if c.setup.daemonMode {
		_, args, err := daemonPrompt(append(append([]string(nil), c.args...), "__tui_prompt__"))
		if err != nil {
			return err
		}
		// Pin profile and model even when the active defaults change elsewhere.
		args = append(args[:len(args)-1], "--model", c.setup.cfg.ModelName)
		if c.setup.cfg.ProfileName != "" {
			args = append(args, "--profile", c.setup.cfg.ProfileName)
		}
		runner, closeClient, err := daemonChatRunner(c.ctx, c.opts, args, string(c.current.ID))
		if err != nil {
			return err
		}
		defer closeClient()
		runErr := runner(ctx, prompt, emit)
		current, loadErr := c.store.Load(c.current.ID)
		if loadErr == nil {
			c.current = current
		}
		return errors.Join(runErr, loadErr)
	}
	cfg, err := prepareWorkspaceTurn(ctx, c.opts, c.setup.cfg, emit)
	if err != nil {
		return err
	}
	c.contextInstructions = cfg.Instructions
	provKind := ""
	if s, err := c.opts.settings(); err == nil {
		provKind = s.Providers[cfg.ProfileName].Kind
	}
	maxTok := provider.ModelContextWindowTokens(provKind, cfg.ModelName)
	cfg.OnEvent = func(e agent.Event) {
		ev := toChatEvent(e, cfg.Root)
		if ev.Connection != nil {
			ev.Connection.ContextTokens = EstimateSessionTokens(c.current.Messages, cfg.Instructions, prompt)
			ev.Connection.ContextMaxTokens = maxTok
		}
		emit(ev)
	}
	current, _, err := runMonitoredSessionTurn(ctx, c.opts, c.store, c.current, cfg, prompt, c.monitor, c.setup.maxAgents)
	c.current = current
	usedTok := EstimateSessionTokens(c.current.Messages, cfg.Instructions, "")
	// Turn-wide usage includes repeated inputs; it is not context occupancy.
	emit(ui.ChatEvent{
		Kind: "connection",
		Connection: &ui.ConnectionInfo{
			Provider:         provKind,
			Account:          valueOrString(cfg.ProfileName, cfg.ModelName),
			Model:            cfg.ModelName,
			Location:         cfg.Root,
			Connected:        true,
			ContextTokens:    usedTok,
			ContextMaxTokens: maxTok,
		},
	})
	return err
}

func valueOrString(val, fallback string) string {
	if strings.TrimSpace(val) != "" {
		return val
	}
	return fallback
}

func toChatEvent(e agent.Event, root string) ui.ChatEvent {
	event := ui.ChatEvent{Kind: e.Kind, Text: e.Text, ID: e.ID, Detail: e.Detail}
	if e.Kind == "connection" {
		event.Connection = &ui.ConnectionInfo{
			Provider:         e.Provider,
			Account:          e.Profile,
			Model:            e.Model,
			Location:         root,
			Connected:        true,
			ContextMaxTokens: provider.ModelContextWindowTokens(e.Provider, e.Model),
		}
	}
	return event
}

func (c *workspaceChat) switchAccount(ctx context.Context, name string) ([]ui.ChatEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.switchAccountUnlocked(ctx, name)
}

func (c *workspaceChat) switchAccountModel(ctx context.Context, name, model string) ([]ui.AccountChoice, ui.AccountChoice, []ui.ChatEvent, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	c.mu.Lock()
	defer c.mu.Unlock()
	settings, err := c.opts.settings()
	if err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}
	profileID := strings.TrimSpace(name)
	model = strings.TrimSpace(model)
	if _, ok := settings.Providers[profileID]; !ok {
		return nil, ui.AccountChoice{}, nil, fmt.Errorf("unknown provider profile %q", profileID)
	}
	if model == "" {
		return nil, ui.AccountChoice{}, nil, errors.New("model ID is required")
	}
	events, swErr := c.switchAccountWithModelUnlocked(ctx, profileID, model)
	freshSettings, _ := c.opts.settings()
	choices := c.buildAccountChoices(freshSettings)
	var activeChoice ui.AccountChoice
	for _, ch := range choices {
		if ch.ID == profileID {
			activeChoice = ch
			break
		}
	}
	return choices, activeChoice, events, swErr
}

func (c *workspaceChat) switchAccountUnlocked(ctx context.Context, name string) ([]ui.ChatEvent, error) {
	return c.switchAccountWithModelUnlocked(ctx, name, "")
}

// Prepare the new runtime before changing saved defaults. Failed model setup
// must leave the active provider, conversation, and remembered model intact.
func (c *workspaceChat) switchAccountWithModelUnlocked(ctx context.Context, name, model string) ([]ui.ChatEvent, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	if c.setup.sshName != "" {
		return nil, errors.New("restart the SSH workspace with --profile to change accounts")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	args := accountSwitchArgs(c.args, name)
	if model != "" {
		args = append(args, "--model", model)
	}
	next, err := configureAgentSetup(ctx, c.opts, args, c.stderr, true)
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			next.cleanup()
		}
	}()
	if next.cfg.Root != c.setup.cfg.Root || next.sshName != c.setup.sshName {
		return nil, errors.New("account switch cannot change the workspace environment")
	}
	next.cfg.Backend = c.setup.cfg.Backend
	settings, err := c.opts.settings()
	if err != nil {
		return nil, err
	}
	current := c.current
	if current.ID == "" {
		current, err = loadOrCreateSession(c.store, "", next.cfg.Root, next.fingerprint)
	} else {
		unlock, lockErr := acquireSessionTurn(c.opts.statePath(), current.ID)
		if lockErr != nil {
			return nil, lockErr
		}
		defer unlock()
		current, err = c.store.Rebind(current.ID, next.fingerprint, current.Revision)
	}
	if err != nil {
		return nil, err
	}
	// The session switch has already committed. Persistence of a global
	// default is best-effort; failure cannot leave the live session mismatched.
	if model != "" {
		p := settings.Providers[name]
		p.Model = model
		settings.Providers[name] = p
	}
	persistErr := selectProfile(c.opts, &settings, name)
	c.cleanup()
	c.cleanup = next.cleanup
	c.setup, c.current, c.args = next, current, args
	keep = true
	history := c.history()
	if persistErr != nil {
		history = append(history, ui.ChatEvent{Kind: "status", Text: "Account switched for this conversation; saving the default failed: " + persistErr.Error()})
	}
	return history, nil
}

func (c *workspaceChat) discoverAccountModels(ctx context.Context, req ui.AccountAddRequest, doOAuth bool) ([]ui.AccountModelOption, string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	rawName := strings.TrimSpace(req.Name)
	if rawName == "" {
		rawName = kind
	}

	if s, err := c.opts.settings(); err == nil {
		if saved, ok := s.Providers[rawName]; ok {
			if kind == "" {
				kind = strings.ToLower(strings.TrimSpace(saved.Kind))
			}
			if strings.TrimSpace(req.APIKeyEnv) == "" {
				req.APIKeyEnv = saved.APIKeyEnv
			}
			if strings.TrimSpace(req.BaseURL) == "" {
				req.BaseURL = saved.BaseURL
			}
			if strings.TrimSpace(req.Executable) == "" {
				req.Executable = saved.Executable
			}
			if strings.TrimSpace(req.Account) == "" {
				req.Account = saved.Account
			}
			if strings.TrimSpace(req.Account) != "" && rawName == "" {
				rawName = saved.Account
			}
		}
	}

	if kind == "cursor" || kind == "claude" {
		acctStore, err := accountStore(c.opts)
		if err != nil {
			return nil, "", nil
		}
		var acct accounts.Account
		found := false
		stableID, _ := accounts.StableID(accounts.Kind(kind), rawName)
		for _, candidateID := range []string{rawName, stableID} {
			if candidateID == "" {
				continue
			}
			if a, getErr := acctStore.Get(candidateID); getErr == nil {
				acct = a
				found = true
				break
			}
		}
		if !found && strings.TrimSpace(req.Account) != "" {
			if a, getErr := acctStore.GetByName(accounts.Kind(kind), strings.TrimSpace(req.Account)); getErr == nil {
				acct = a
				found = true
			}
		}
		if !found {
			return nil, "", nil
		}
		cfg := authAgentConfig(acct, strings.TrimSpace(req.Executable))
		entries, mErr := agentbackend.Models(ctx, cfg)
		if mErr != nil {
			return nil, "", mErr
		}
		out := make([]ui.AccountModelOption, 0, len(entries))
		for _, e := range entries {
			out = append(out, ui.AccountModelOption{
				Slug:        e.Slug,
				DisplayName: e.DisplayName,
			})
		}
		defModel := strings.TrimSpace(req.Model)
		if len(out) > 0 && defModel == "" {
			defModel = out[0].Slug
		}
		return out, defModel, nil
	}

	if strings.TrimSpace(req.RawAPIKey) != "" {
		envName := strings.TrimSpace(req.APIKeyEnv)
		if envName == "" {
			envName = defaultEnvForKind(kind)
		}
		_ = os.Setenv(envName, strings.TrimSpace(req.RawAPIKey))
	}

	var tokenSource func(context.Context) (string, error)
	if kind == "cursor-native" {
		if doOAuth || strings.TrimSpace(req.RawAPIKey) != "" {
			if key := strings.TrimSpace(req.RawAPIKey); key != "" {
				_ = os.Setenv("CURSOR_API_KEY", key)
				defer os.Unsetenv("CURSOR_API_KEY")
			}
			if err := loginAccount(ctx, c.opts, []string{"cursor-native", rawName}, io.Discard, c.stderr); err != nil {
				return nil, "", err
			}
		}
		acctStore, err := accountStore(c.opts)
		if err != nil {
			return nil, "", err
		}
		stableID, _ := accounts.StableID(accounts.KindCursorNative, rawName)
		acct, err := acctStore.Get(stableID)
		if err != nil {
			return nil, "", nil
		}
		access, err := cursorAccessToken(ctx, acctStore, acct)
		if err != nil {
			return nil, "", err
		}
		models, err := (cursorclient.Client{AccessToken: access, TeamID: cursorTeam(acct)}).Models(ctx)
		if err != nil {
			return nil, "", err
		}
		out := make([]ui.AccountModelOption, 0, len(models))
		for _, model := range models {
			out = append(out, ui.AccountModelOption{Slug: model.ID, DisplayName: model.DisplayName})
		}
		defModel := strings.TrimSpace(req.Model)
		if defModel == "" && len(out) > 0 {
			defModel = out[0].Slug
		}
		if defModel == "" {
			defModel = "auto"
		}
		return out, defModel, nil
	}

	if kind == "chatgpt" && doOAuth {
		loginArgs := []string{"chatgpt", rawName}
		if err := loginAccount(ctx, c.opts, loginArgs, io.Discard, c.stderr); err != nil {
			return nil, "", err
		}
	}
	if kind == "chatgpt" {
		if acctStore, err := accountStore(c.opts); err == nil {
			stableID, _ := accounts.StableID(accounts.KindChatGPT, rawName)
			for _, candidateID := range []string{stableID, rawName} {
				if candidateID == "" {
					continue
				}
				if acct, err := acctStore.Get(candidateID); err == nil {
					if creds, err := acctStore.LoadCredentials(acct.ID); err == nil && (creds.AccessToken != "" || creds.RefreshToken != "") {
						tokenSource = chatGPTTokenSource(acctStore, acct)
						break
					}
				}
			}
		}
		if tokenSource == nil {
			return nil, "", nil
		}
	}

	apiKey := strings.TrimSpace(req.RawAPIKey)
	if apiKey == "" && strings.TrimSpace(req.APIKeyEnv) != "" {
		apiKey = strings.TrimSpace(os.Getenv(strings.TrimSpace(req.APIKeyEnv)))
	}

	catModels, err := provider.DiscoverCatalogModels(ctx, provider.Config{
		Kind:        provider.Kind(kind),
		BaseURL:     strings.TrimSpace(req.BaseURL),
		APIKey:      apiKey,
		TokenSource: tokenSource,
	})
	out := make([]ui.AccountModelOption, 0, len(catModels))
	for _, m := range catModels {
		out = append(out, ui.AccountModelOption{
			Slug:          m.Slug,
			DisplayName:   m.DisplayName,
			ContextLength: m.ContextLength,
		})
	}
	defModel := strings.TrimSpace(req.Model)
	if len(out) > 0 && defModel == "" {
		defModel = out[0].Slug
	}
	return out, defModel, err
}

func defaultEnvForKind(kind string) string {
	switch kind {
	case "anthropic":
		return "ANTHROPIC_API_KEY"
	case "gemini":
		return "GEMINI_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	default:
		return "OPENAI_API_KEY"
	}
}

func (c *workspaceChat) addAccount(ctx context.Context, req ui.AccountAddRequest) ([]ui.AccountChoice, ui.AccountChoice, []ui.ChatEvent, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	rawName := strings.TrimSpace(req.Name)
	if rawName == "" {
		return nil, ui.AccountChoice{}, nil, errors.New("profile name is required")
	}
	profileID := store.SanitizeAlias(rawName)
	if err := domain.ValidateName(profileID); err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}
	model := strings.TrimSpace(req.Model)

	if kind == "cursor-native" {
		if key := strings.TrimSpace(req.RawAPIKey); key != "" {
			_ = os.Setenv("CURSOR_API_KEY", key)
			defer os.Unsetenv("CURSOR_API_KEY")
		}
		stableID, _ := accounts.StableID(accounts.KindCursorNative, rawName)
		already := false
		if strings.TrimSpace(req.RawAPIKey) == "" {
			if acctStore, stErr := accountStore(c.opts); stErr == nil {
				if acct, loadErr := acctStore.Get(stableID); loadErr == nil {
					if creds, cErr := acctStore.LoadCredentials(acct.ID); cErr == nil && (creds.AccessToken != "" || creds.RefreshToken != "" || creds.APIKey != "") {
						already = true
					}
				}
			}
		}
		if !already {
			loginArgs := []string{"cursor-native", rawName}
			if model != "" && model != "auto" {
				loginArgs = append(loginArgs, "--model", model)
			}
			if err := loginAccount(ctx, c.opts, loginArgs, io.Discard, c.stderr); err != nil {
				return nil, ui.AccountChoice{}, nil, err
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		settings, sErr := c.opts.settings()
		if sErr != nil {
			return nil, ui.AccountChoice{}, nil, sErr
		}
		if already && model != "" && model != "auto" {
			if p, ok := settings.Providers[stableID]; ok {
				p.Model = model
				settings.Providers[stableID] = p
				if cfgPath, cfgErr := c.opts.configFile(); cfgErr == nil {
					_ = config.SaveSettings(cfgPath, settings, true)
				}
			}
		}
		events, swErr := c.switchAccountUnlocked(ctx, stableID)
		freshSettings, _ := c.opts.settings()
		choices := c.buildAccountChoices(freshSettings)
		var activeChoice ui.AccountChoice
		for _, ch := range choices {
			if ch.ID == stableID {
				activeChoice = ch
				break
			}
		}
		return choices, activeChoice, events, swErr
	}

	if strings.TrimSpace(req.RawAPIKey) != "" {
		envName := strings.TrimSpace(req.APIKeyEnv)
		if envName == "" {
			envName = defaultEnvForKind(kind)
		}
		_ = os.Setenv(envName, strings.TrimSpace(req.RawAPIKey))
		req.APIKeyEnv = envName
	}

	if kind == "chatgpt" {
		stableID, _ := accounts.StableID(accounts.KindChatGPT, rawName)
		alreadyLoggedIn := false
		if acctStore, stErr := accountStore(c.opts); stErr == nil {
			if acct, loadErr := acctStore.Get(stableID); loadErr == nil {
				if creds, cErr := acctStore.LoadCredentials(acct.ID); cErr == nil && (creds.AccessToken != "" || creds.RefreshToken != "") {
					alreadyLoggedIn = true
				}
			}
		}
		if !alreadyLoggedIn {
			loginArgs := []string{"chatgpt", rawName}
			if model != "" && model != "auto" {
				loginArgs = append(loginArgs, "--model", model)
			}
			if err := loginAccount(ctx, c.opts, loginArgs, io.Discard, c.stderr); err != nil {
				return nil, ui.AccountChoice{}, nil, err
			}
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		settings, sErr := c.opts.settings()
		if sErr != nil {
			return nil, ui.AccountChoice{}, nil, sErr
		}
		if alreadyLoggedIn && model != "" && model != "auto" {
			if p, ok := settings.Providers[stableID]; ok {
				p.Model = model
				settings.Providers[stableID] = p
				if cfgPath, cfgErr := c.opts.configFile(); cfgErr == nil {
					_ = config.SaveSettings(cfgPath, settings, true)
				}
			}
		}
		events, swErr := c.switchAccountUnlocked(ctx, stableID)
		freshSettings, _ := c.opts.settings()
		choices := c.buildAccountChoices(freshSettings)
		var activeChoice ui.AccountChoice
		for _, ch := range choices {
			if ch.ID == stableID {
				activeChoice = ch
				break
			}
		}
		return choices, activeChoice, events, swErr
	}

	if model == "" {
		if kind == "claude" || kind == "cursor" {
			model = "auto"
		} else {
			models, defMod, err := c.discoverAccountModels(ctx, req, false)
			if err != nil {
				return nil, ui.AccountChoice{}, nil, fmt.Errorf("failed to fetch models from API: %w", err)
			}
			if defMod != "" {
				model = defMod
			} else if len(models) > 0 {
				model = models[0].Slug
			} else {
				return nil, ui.AccountChoice{}, nil, errors.New("enter valid API credentials and select a model from the API first")
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	settings, err := c.opts.settings()
	if err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}
	if settings.Providers == nil {
		settings.Providers = make(map[string]config.ProviderSettings)
	}

	var provCfg config.ProviderSettings
	switch kind {
	case "claude", "cursor":
		acctKind := accounts.Kind(kind)
		stableID, sErr := accounts.StableID(acctKind, profileID)
		if sErr == nil {
			profileID = stableID
		}
		acctStore, stErr := accountStore(c.opts)
		if stErr != nil {
			return nil, ui.AccountChoice{}, nil, stErr
		}
		dir, dErr := accountDirectory(c.opts, profileID)
		if dErr != nil {
			return nil, ui.AccountChoice{}, nil, dErr
		}
		_ = os.MkdirAll(dir, 0o700)
		gen, _ := accountGeneration()
		acct := accounts.Account{
			ID:         profileID,
			Name:       rawName,
			Kind:       acctKind,
			ConfigDir:  dir,
			Generation: gen,
		}
		if err := acctStore.Save(acct); err != nil {
			return nil, ui.AccountChoice{}, nil, err
		}
		provCfg = config.ProviderSettings{
			Kind:       kind,
			Model:      model,
			Account:    rawName,
			Executable: strings.TrimSpace(req.Executable),
		}
	case "openrouter":
		acctKind := accounts.KindOpenRouter
		stableID, sErr := accounts.StableID(acctKind, profileID)
		if sErr == nil {
			profileID = stableID
		}
		keyEnv := strings.TrimSpace(req.APIKeyEnv)
		if keyEnv == "" {
			keyEnv = "OPENROUTER_API_KEY"
		}
		acctStore, stErr := accountStore(c.opts)
		if stErr != nil {
			return nil, ui.AccountChoice{}, nil, stErr
		}
		gen, _ := accountGeneration()
		acct := accounts.Account{
			ID:         profileID,
			Name:       rawName,
			Kind:       acctKind,
			APIKeyEnv:  keyEnv,
			Generation: gen,
		}
		if err := acctStore.Save(acct); err != nil {
			return nil, ui.AccountChoice{}, nil, err
		}
		key := strings.TrimSpace(req.RawAPIKey)
		if key == "" {
			key = strings.TrimSpace(os.Getenv(keyEnv))
		}
		if key != "" {
			if err := acctStore.UpdateCredentials(profileID, accounts.Credentials{APIKey: key}); err != nil {
				return nil, ui.AccountChoice{}, nil, err
			}
		}
		provCfg = config.ProviderSettings{
			Kind:      kind,
			Model:     model,
			Account:   rawName,
			APIKeyEnv: keyEnv,
		}
	case "anthropic", "responses", "gemini", "compatible":
		keyEnv := strings.TrimSpace(req.APIKeyEnv)
		if keyEnv == "" {
			keyEnv = defaultEnvForKind(kind)
		}
		provCfg = config.ProviderSettings{
			Kind:      kind,
			Model:     model,
			APIKeyEnv: keyEnv,
			BaseURL:   strings.TrimSpace(req.BaseURL),
		}
	default:
		return nil, ui.AccountChoice{}, nil, fmt.Errorf("unsupported provider kind %q", kind)
	}

	if err := provCfg.Validate(); err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}
	settings.Providers[profileID] = provCfg
	if settings.ActiveProvider == "" {
		settings.ActiveProvider = profileID
		settings.Defaults.Provider = profileID
	}
	cfgPath, err := c.opts.configFile()
	if err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}
	if err := config.SaveSettings(cfgPath, settings, true); err != nil {
		return nil, ui.AccountChoice{}, nil, err
	}

	events, swErr := c.switchAccountUnlocked(ctx, profileID)
	freshSettings, _ := c.opts.settings()
	choices := c.buildAccountChoices(freshSettings)
	var activeChoice ui.AccountChoice
	for _, ch := range choices {
		if ch.ID == profileID {
			activeChoice = ch
			break
		}
	}
	if swErr != nil {
		return choices, activeChoice, nil, fmt.Errorf("saved profile %q, but connection failed: %w", profileID, swErr)
	}
	return choices, activeChoice, events, nil
}

func (c *workspaceChat) removeAccount(ctx context.Context, profileID string) ([]ui.AccountChoice, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	settings, err := c.opts.settings()
	if err != nil {
		return nil, err
	}
	if _, ok := settings.Providers[profileID]; !ok {
		return nil, fmt.Errorf("profile %q not found", profileID)
	}
	delete(settings.Providers, profileID)
	if settings.Subagents.Provider == profileID {
		settings.Subagents = config.SubagentSettings{}
	}
	if settings.ActiveProvider == profileID {
		settings.ActiveProvider = ""
		for name := range settings.Providers {
			settings.ActiveProvider = name
			break
		}
		settings.Defaults.Provider = settings.ActiveProvider
	}
	cfgPath, err := c.opts.configFile()
	if err != nil {
		return nil, err
	}
	if err := config.SaveSettings(cfgPath, settings, true); err != nil {
		return nil, err
	}
	return c.buildAccountChoices(settings), nil
}

func (c *workspaceChat) listSessions(ctx context.Context) ([]ui.SessionSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil, errors.New("session store unavailable")
	}
	items, err := c.store.List(c.setup.cfg.Root)
	if err != nil {
		return nil, err
	}
	out := make([]ui.SessionSummary, 0, len(items))
	for _, item := range items {
		out = append(out, summarizeSession(item, item.ID == c.current.ID))
	}
	return out, nil
}

func (c *workspaceChat) activateSession(ctx context.Context, rawID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return nil
	}
	id, err := domain.ParseID(strings.TrimSpace(rawID))
	if err != nil {
		return err
	}
	loaded, err := c.store.Load(id)
	if err != nil {
		return err
	}
	c.current = loaded
	return nil
}

func (c *workspaceChat) compactCurrentSession(ctx context.Context) (ui.SessionSummary, []ui.ChatEvent, int, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil || c.current.ID == "" {
		return ui.SessionSummary{}, nil, 0, 0, errors.New("no active session to compact")
	}
	unlock, err := acquireSessionTurn(c.opts.statePath(), c.current.ID)
	if err != nil {
		return ui.SessionSummary{}, nil, 0, 0, err
	}
	defer unlock()
	fresh, err := c.store.Load(c.current.ID)
	if err != nil {
		return ui.SessionSummary{}, nil, 0, 0, err
	}
	if len(fresh.Messages) == 0 {
		return ui.SessionSummary{}, nil, 0, 0, errors.New("conversation has no messages to compact yet")
	}
	if fresh.Status != session.StatusCompleted {
		return ui.SessionSummary{}, nil, 0, 0, errors.New("resume and complete the interrupted conversation before summarizing")
	}
	summaryText := BuildConversationSummary(ctx, c.setup.cfg, fresh.Messages)
	if err := ctx.Err(); err != nil {
		return ui.SessionSummary{}, nil, 0, 0, err
	}
	compacted, err := compactReducedSession(ctx, c.store, fresh, summaryText)
	if err != nil {
		return ui.SessionSummary{}, nil, 0, 0, err
	}
	c.current = compacted
	provKind := ""
	if s, err := c.opts.settings(); err == nil {
		provKind = s.Providers[c.setup.cfg.ProfileName].Kind
	}
	usedTok := EstimateSessionTokens(c.current.Messages, valueOrString(c.contextInstructions, c.setup.cfg.Instructions), "")
	maxTok := provider.ModelContextWindowTokens(provKind, c.setup.cfg.ModelName)
	return summarizeSession(c.current, true), c.history(), usedTok, maxTok, nil
}

func (c *workspaceChat) resumeSession(ctx context.Context, rawID string) (ui.SessionSummary, []ui.ChatEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil {
		return ui.SessionSummary{}, nil, errors.New("session store unavailable")
	}
	id, err := domain.ParseID(strings.TrimSpace(rawID))
	if err != nil {
		return ui.SessionSummary{}, nil, fmt.Errorf("invalid session ID %q: %w", rawID, err)
	}
	loaded, err := c.store.Load(id)
	if err != nil {
		return ui.SessionSummary{}, nil, err
	}
	if c.setup.fingerprint != "" {
		if bindErr := c.store.ValidateBinding(loaded, c.setup.cfg.Root, c.setup.fingerprint); bindErr != nil {
			if errors.Is(bindErr, session.ErrBindingMismatch) && loaded.Root == c.current.Root {
				unlock, lockErr := acquireSessionTurn(c.opts.statePath(), loaded.ID)
				if lockErr != nil {
					return ui.SessionSummary{}, nil, lockErr
				}
				rebound, rebindErr := c.store.Rebind(loaded.ID, c.setup.fingerprint, loaded.Revision)
				_ = unlock()
				if rebindErr != nil {
					return ui.SessionSummary{}, nil, rebindErr
				}
				loaded = rebound
			} else {
				return ui.SessionSummary{}, nil, bindErr
			}
		}
	}
	c.current = loaded
	return summarizeSession(c.current, true), c.history(), nil
}

func (c *workspaceChat) newSession(ctx context.Context) (ui.SessionSummary, []ui.ChatEvent, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.store == nil || c.setup.fingerprint == "" {
		return ui.SessionSummary{}, nil, errors.New("connect an account first before creating a persistent session")
	}
	created, err := loadOrCreateSession(c.store, "", c.setup.cfg.Root, c.setup.fingerprint)
	if err != nil {
		return ui.SessionSummary{}, nil, err
	}
	c.current = created
	return summarizeSession(c.current, true), c.history(), nil
}

func summarizeSession(s session.Session, active bool) ui.SessionSummary {
	title := ""
	for _, m := range s.Messages {
		if m.Role == provider.RoleUser && strings.TrimSpace(m.Content) != "" {
			firstLine := strings.Split(strings.TrimSpace(m.Content), "\n")[0]
			if idx := strings.Index(firstLine, "<attached_context>"); idx > 0 {
				firstLine = strings.TrimSpace(firstLine[:idx])
			}
			runes := []rune(firstLine)
			if len(runes) > 64 {
				firstLine = string(runes[:63]) + "…"
			}
			title = firstLine
			break
		}
	}
	if title == "" {
		title = "(empty conversation)"
	}
	return ui.SessionSummary{
		ID:           string(s.ID),
		Title:        title,
		Status:       string(s.Status),
		MessageCount: len(s.Messages),
		Revision:     s.Revision,
		UpdatedAt:    s.UpdatedAt.Local().Format("2006-01-02 15:04"),
		Active:       active,
	}
}

func accountSwitchArgs(args []string, profile string) []string {
	remove := map[string]bool{"profile": true, "account": true, "provider": true, "model": true, "base-url": true, "api-key-env": true, "session": true}
	var result []string
	for i := 0; i < len(args); i++ {
		name, _, hasValue := strings.Cut(strings.TrimLeft(args[i], "-"), "=")
		if strings.HasPrefix(args[i], "-") && remove[name] {
			if !hasValue && i+1 < len(args) {
				i++
			}
			continue
		}
		result = append(result, args[i])
	}
	return append(result, "--profile", profile)
}

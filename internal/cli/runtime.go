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
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/browser"
	"bifrost/internal/browsertools"
	"bifrost/internal/config"
	"bifrost/internal/desktop"
	"bifrost/internal/desktoptools"
	"bifrost/internal/environment"
	"bifrost/internal/mcptools"
	"bifrost/internal/provider"
	"bifrost/internal/remotetools"
	"bifrost/internal/skill"
	"bifrost/internal/ui"
	"bifrost/internal/workspace"
)

func envFirst(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

type agentSetup struct {
	cfg            agent.Config
	rest           []string
	cleanup        func()
	sessionID      string
	fingerprint    string
	sshName        string
	maxAgents      int
	daemonMode     bool
	accountBinding string
	startupNotice  string
}

func configureAgent(ctx context.Context, opts options, args []string, stderr io.Writer, required bool) (agent.Config, []string, func(), error) {
	setup, err := configureAgentSetup(ctx, opts, args, stderr, required)
	return setup.cfg, setup.rest, setup.cleanup, err
}

func configureAgentSetup(ctx context.Context, opts options, args []string, stderr io.Writer, required bool) (result agentSetup, err error) {
	cleanup := func() {}
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	cfg := agent.Config{MaxSteps: 8}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	settings, err := opts.settings()
	if err != nil {
		return agentSetup{cleanup: cleanup}, err
	}
	defaults := settings.ForProject(opts.project)
	profile := defaults.Provider
	if profile == "" {
		profile = settings.ActiveProvider
	}
	var base, model, kind, keyEnv, skills, mcpConfig, sessionID, accountName string
	sshName := settings.Projects[opts.project].Environment
	var browserOptIn, desktopOptIn, daemonMode bool
	maxAgents := defaults.MaxAgents
	fs.IntVar(&maxAgents, "agents", maxAgents, "maximum concurrent sub-agents (0 disables)")
	fs.BoolVar(&daemonMode, "daemon", false, "run chat turns in background daemon")
	fs.StringVar(&profile, "profile", profile, "saved provider profile")
	fs.StringVar(&accountName, "account", "", "saved account alias for the selected provider")
	fs.StringVar(&kind, "provider", "", "provider API family")
	fs.StringVar(&base, "base-url", "", "API base URL")
	fs.StringVar(&model, "model", "", "model ID")
	fs.StringVar(&keyEnv, "api-key-env", "", "API credential environment variable")
	fs.BoolVar(&cfg.WriteEnabled, "allow-write", defaults.AllowWrite, "allow revision-checked file edits")
	fs.BoolVar(&cfg.ExecEnabled, "allow-exec", defaults.AllowExec, "allow commands with host permissions")
	fs.IntVar(&cfg.MaxSteps, "max-steps", defaults.MaxSteps, "maximum model steps, 1..100")
	fs.StringVar(&skills, "skill", strings.Join(defaults.Skills, ","), "comma-separated installed skills")
	fs.StringVar(&mcpConfig, "mcp-config", defaults.MCPConfig, "trusted MCP configuration")
	fs.BoolVar(&desktopOptIn, "desktop", false, "enable observation-based desktop tools")
	fs.BoolVar(&browserOptIn, "browser", defaults.Browser, "enable isolated browser tools")
	fs.StringVar(&sessionID, "session", "", "resume conversation")
	fs.StringVar(&sshName, "ssh", "", "configured SSH environment")
	if err := fs.Parse(args); err != nil {
		return agentSetup{cleanup: cleanup}, err
	}
	if maxAgents < 0 || maxAgents > 32 {
		return agentSetup{cleanup: cleanup}, errors.New("agents must be 0..32")
	}
	if sshName != "" && daemonMode {
		return agentSetup{cleanup: cleanup}, errors.New("daemon chat with SSH is not yet supported; use direct SSH chat")
	}
	if sshName != "" {
		explicit := false
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "agents" {
				explicit = true
			}
		})
		if explicit && maxAgents > 0 {
			return agentSetup{cleanup: cleanup}, errors.New("remote sub-agent isolation is not yet supported; use --agents 0")
		}
		maxAgents = 0
	}
	p := config.ProviderSettings{Kind: "chat-completions"}
	if profile != "" {
		var ok bool
		p, ok = settings.Providers[profile]
		if !ok {
			return agentSetup{cleanup: cleanup}, fmt.Errorf("unknown provider profile %q", profile)
		}
	}
	if accountName != "" {
		p.Account = accountName
	}
	if kind == "" {
		if p.Account == "" {
			kind = envFirst("BIFROST_PROVIDER")
		}
		if kind == "" {
			kind = p.Kind
		}
	}
	subscription := kind == "chatgpt" || kind == "cursor" || kind == "cursor-native" || kind == "claude"
	if base == "" && !subscription {
		if p.Account == "" {
			base = envFirst("BIFROST_BASE_URL", "OPENAI_BASE_URL")
		}
		if base == "" {
			base = p.BaseURL
		}
	}
	if model == "" {
		if p.Account == "" {
			model = envFirst("BIFROST_MODEL", "OPENAI_MODEL")
		}
		if model == "" {
			model = p.Model
		}
	}
	if keyEnv == "" {
		keyEnv = p.APIKeyEnv
	}
	if keyEnv == "" {
		keyEnv = defaultAPIKeyEnv(kind)
	}
	if cfg.MaxSteps < 1 || cfg.MaxSteps > 100 {
		return agentSetup{cfg: cfg, cleanup: cleanup}, errors.New("max-steps must be 1..100")
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return agentSetup{cfg: cfg, cleanup: cleanup}, err
	}
	cfg.Root = root
	cfg.StateDir, err = opts.workspaceStatePath(root)
	if err != nil {
		return agentSetup{cfg: cfg, cleanup: cleanup}, err
	}
	if sshName != "" {
		ssh, ok := settings.SSH[sshName]
		if !ok {
			return agentSetup{cleanup: cleanup}, fmt.Errorf("unknown SSH environment %q", sshName)
		}
		remote, dialErr := environment.DialSSH(ctx, environment.SSHConfig{Target: ssh.Target, Port: ssh.Port, IdentityFile: ssh.IdentityFile, RemoteExecutable: ssh.Executable, Root: ssh.Root, StateDir: ssh.StateDir, BrowserEnabled: browserOptIn, DesktopEnabled: desktopOptIn})
		if dialErr != nil {
			return agentSetup{cleanup: cleanup}, dialErr
		}
		cfg.Backend = remote
		cleanup = composeCleanup(cleanup, func() { _ = remote.Close() })
		cfg.Instructions += fmt.Sprintf("All file and command tools operate in SSH environment %s at guest root %s. Host paths are not guest paths.\n", sshName, remote.Identity().Root)
	}
	if model == "" && !required {
		explicitCapabilityFlag := false
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "skill", "mcp-config", "browser", "desktop", "session":
				explicitCapabilityFlag = true
			}
		})
		if explicitCapabilityFlag && (skills != "" || mcpConfig != "" || browserOptIn || desktopOptIn || sessionID != "") {
			return agentSetup{cfg: cfg, cleanup: cleanup}, errors.New("tui flags --skill, --mcp-config, --browser, and --session require a provider")
		}
		return agentSetup{cfg: cfg, rest: fs.Args(), cleanup: cleanup, sessionID: sessionID, fingerprint: runtimeFingerprint(kind, base, model, sshName, settings), sshName: sshName, maxAgents: maxAgents, daemonMode: daemonMode}, nil
	}
	if model == "" {
		return agentSetup{cfg: cfg, cleanup: cleanup}, errors.New("run requires BIFROST_MODEL or --model")
	}
	cfg.ModelName, cfg.ProfileName = model, profile
	p.Kind, p.BaseURL, p.Model, p.APIKeyEnv = kind, base, model, keyEnv
	binding, bindErr := bindRuntimeProvider(opts, &cfg, p)
	var startupNotice string
	if bindErr != nil {
		// The interactive workspace must still open when a saved account has
		// no key yet. Headless runs keep failing until the credential exists.
		if required || !errors.Is(bindErr, errMissingAccountCredential) {
			return agentSetup{cfg: cfg, cleanup: cleanup}, bindErr
		}
		cfg.Provider = nil
		cfg.RunExternal = nil
		startupNotice = bindErr.Error() + ". Workspace opened without that account; paste the key in :accounts."
	}
	if cfg.RunExternal != nil {
		explicitExternalIncompatible := false
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "ssh", "browser", "desktop", "mcp-config":
				explicitExternalIncompatible = true
			}
		})
		if explicitExternalIncompatible && (sshName != "" || browserOptIn || desktopOptIn || mcpConfig != "") {
			return agentSetup{cleanup: cleanup}, errors.New("external agents use their own local tools; Bifrost --ssh, --browser, --desktop and --mcp-config require a native provider")
		}
		sshName, browserOptIn, desktopOptIn, mcpConfig = "", false, false, ""
	}
	if err := activateSkills(opts, &cfg, skills); err != nil {
		return agentSetup{cfg: cfg, cleanup: cleanup}, err
	}
	if mcpConfig != "" {
		var extras []agent.Tool
		var mcpCleanup func()
		extras, mcpCleanup, err = mcptools.Open(ctx, mcpConfig, stderr)
		cleanup = composeCleanup(cleanup, mcpCleanup)
		cfg.ExtraTools = append(cfg.ExtraTools, extras...)
		if err != nil {
			cleanup()
			return agentSetup{cfg: cfg, cleanup: func() {}}, err
		}
	}
	if err := configureHooks(opts, &cfg); err != nil {
		return agentSetup{cleanup: cleanup}, err
	}
	if cfg.Backend != nil && (browserOptIn || desktopOptIn) {
		extras, closeRemote, openErr := remotetools.Open(ctx, cfg.Backend, browserOptIn, desktopOptIn)
		if openErr != nil {
			return agentSetup{cleanup: cleanup}, openErr
		}
		cfg.ExtraTools = append(cfg.ExtraTools, extras...)
		cleanup = composeCleanup(cleanup, closeRemote)
	}
	if desktopOptIn && cfg.Backend == nil {
		extras, closeDesktop, openErr := desktoptools.Open(ctx, desktop.Config{ArtifactDir: filepath.Join(cfg.StateDir, "desktop")})
		if openErr != nil {
			return agentSetup{cleanup: cleanup}, openErr
		}
		cfg.ExtraTools = append(cfg.ExtraTools, extras...)
		cleanup = composeCleanup(cleanup, closeDesktop)
	}
	if browserOptIn && cfg.Backend == nil {
		extras, browserCleanup := browsertools.Open(ctx, browser.Config{})
		cfg.ExtraTools = append(cfg.ExtraTools, extras...)
		cleanup = composeCleanup(cleanup, browserCleanup)
	}
	fingerprint := runtimeFingerprint(kind, base, model, sshName, settings)
	if binding != "" {
		fingerprint = providerFingerprint(fingerprint, binding)
	}
	return agentSetup{cfg: cfg, rest: fs.Args(), cleanup: cleanup, sessionID: sessionID, fingerprint: fingerprint, sshName: sshName, maxAgents: maxAgents, daemonMode: daemonMode, accountBinding: binding, startupNotice: startupNotice}, nil
}

func composeCleanup(cleanups ...func()) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				if cleanups[i] != nil {
					cleanups[i]()
				}
			}
		})
	}
}

func activateSkills(opts options, cfg *agent.Config, skills string) error {
	if strings.TrimSpace(skills) == "" {
		return nil
	}
	manager, err := skillManager(opts)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(skills, ",") {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		loaded, err := skill.LoadAvailable(manager, name)
		if err != nil {
			return err
		}
		cfg.Instructions += fmt.Sprintf("\nActive skill %s (revision %s):\n%s\n", loaded.Name, loaded.Digest, loaded.Body)
		if len(cfg.Instructions) > 256<<10 {
			return errors.New("active skills exceed instruction budget")
		}
	}
	cfg.ExtraTools = append(cfg.ExtraTools, agent.Tool{Definition: provider.ToolDefinition{Name: "read_skill_resource", Description: "Read a text resource from an explicitly activated skill snapshot.", Parameters: json.RawMessage(`{"type":"object","required":["skill","path"],"properties":{"skill":{"type":"string"},"path":{"type":"string"}}}`)}, Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		var args struct {
			Skill string `json:"skill"`
			Path  string `json:"path"`
		}
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", err
		}
		if !seen[args.Skill] {
			return "", errors.New("skill is not activated")
		}
		return skill.ReadAvailableResource(manager, args.Skill, args.Path)
	}})
	return nil
}

func runAgent(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	setup, err := configureAgentSetup(ctx, opts, args, stderr, true)
	if err != nil {
		return err
	}
	defer setup.cleanup()
	cfg := setup.cfg
	cfg.DisableStreaming = opts.json
	applyWorkspaceProjectContext(opts, &cfg)
	prompt := strings.Join(setup.rest, " ")
	if strings.TrimSpace(prompt) == "" {
		return errors.New("run requires a prompt")
	}
	// JSON remains one complete result; interactive output streams text separately from activity.
	if !opts.json {
		cfg.OnEvent = func(event agent.Event) {
			if event.Kind == "delta" {
				fmt.Fprint(stdout, event.Text)
			} else if event.Text != "" {
				fmt.Fprintln(stderr, event.Text)
			}
		}
	}
	st, err := sessionStore(opts)
	if err != nil {
		return err
	}
	current, err := loadOrCreateSession(st, setup.sessionID, cfg.Root, setup.fingerprint)
	if err != nil {
		return err
	}
	cfg = addResumeNotice(cfg, current, stderr)
	fmt.Fprintf(stderr, "session %s\n", current.ID)
	_, result, runErr := runMonitoredSessionTurn(ctx, opts, st, current, cfg, prompt, nil, setup.maxAgents)
	if runErr != nil {
		return runErr
	}
	if opts.json {
		return writeJSON(stdout, result)
	}
	fmt.Fprintln(stdout)
	return nil
}

func runTUI(ctx context.Context, opts options, args []string, stderr io.Writer) error {
	setup, err := configureAgentSetup(ctx, opts, args, stderr, false)
	if err != nil {
		return err
	}
	defer setup.cleanup()
	cfg := setup.cfg
	if len(setup.rest) == 1 && opts.root == "" && !strings.HasPrefix(setup.rest[0], "-") {
		if info, statErr := os.Stat(setup.rest[0]); statErr == nil && info.IsDir() {
			setup.cleanup()
			opts.root = setup.rest[0]
			setup, err = configureAgentSetup(ctx, opts, nil, stderr, false)
			if err != nil {
				return err
			}
			defer setup.cleanup()
			cfg = setup.cfg
		}
	}
	if len(setup.rest) > 0 {
		return errors.New("tui takes flags, not a prompt")
	}
	backend := cfg.Backend
	if backend == nil {
		var local environment.Backend
		var err error
		if setup.daemonMode {
			local, err = environment.OpenTransientLocal(environment.LocalConfig{Root: cfg.Root, StateDir: cfg.StateDir})
		} else {
			local, err = environment.OpenLocal(environment.LocalConfig{Root: cfg.Root, StateDir: cfg.StateDir})
		}
		if err != nil {
			return err
		}
		defer local.Close()
		backend = local
	}
	cfg.Backend = backend
	monitor := &taskMonitorState{}
	options := []ui.Option{monitor.option(opts), ui.WithReviewLoader(func(ctx context.Context) (string, error) { r, err := backend.Review(ctx); return r.Text(), err }), ui.WithDraftStateDir(filepath.Join(cfg.StateDir, "drafts")), ui.WithInlineEditor(
		func(ctx context.Context, path string) (ui.EditorDocument, error) {
			text, rev, err := backend.Read(ctx, path)
			return ui.EditorDocument{Path: path, Content: text, Revision: rev}, err
		},
		func(ctx context.Context, doc ui.EditorDocument) (ui.EditorDocument, error) {
			revision := workspace.Revision(doc.Revision)
			change, err := backend.Write(ctx, doc.Path, &revision, doc.Content)
			if err != nil {
				return doc, err
			}
			doc.Revision = change.After
			return doc, nil
		},
	)}
	settings, err := opts.settings()
	if err != nil {
		return err
	}
	options = append(options, ui.WithTheme(settings.UI.Theme), projectUIOption(opts, cfg.Root))
	presentation, err := workspaceUIOptions(opts, settings)
	if err != nil {
		return err
	}
	options = append(options, presentation...)
	if len(settings.UI.Editor) > 0 {
		options = append(options, ui.WithEditor(settings.UI.Editor))
	}

	if cfg.Backend != nil && cfg.Backend.Identity().Kind == "ssh" {
		remoteOpts, err := remoteUIOptions(backend, settings.SSH[setup.sshName])
		if err != nil {
			return err
		}
		options = append(options, remoteOpts...)
	}
	setup.cfg = cfg
	chat, err := newWorkspaceChat(ctx, opts, args, setup, monitor, stderr)
	if err != nil {
		return err
	}
	defer chat.close()
	chatOptions, err := chat.options()
	if err != nil {
		return err
	}
	options = append(options, chatOptions...)
	if notice := strings.TrimSpace(setup.startupNotice); notice != "" {
		options = append(options, ui.WithStatus(notice), ui.WithChatHistory([]ui.ChatEvent{{Kind: "status", Text: notice}}))
	}

	return ui.RunContext(ctx, cfg.Root, options...)
}

func sessionChatHistory(messages []provider.Message) []ui.ChatEvent {
	events := make([]ui.ChatEvent, 0, len(messages))
	toolArgs := make(map[string]string)
	for _, message := range messages {
		switch message.Role {
		case provider.RoleUser:
			events = append(events, ui.ChatEvent{Kind: "user", Text: message.Content})
		case provider.RoleAssistant:
			for _, call := range message.ToolCalls {
				name := strings.TrimSpace(call.Name)
				if name == "" {
					name = "tool"
				}
				args := agent.BoundedEventDetail("args: " + string(call.Arguments))
				if call.ID != "" {
					toolArgs[call.ID] = args
				}
				events = append(events, ui.ChatEvent{Kind: "tool", ID: call.ID, Text: name + " started", Detail: args})
			}
			if strings.TrimSpace(message.Content) != "" {
				events = append(events, ui.ChatEvent{Kind: "assistant", Text: message.Content})
			}
		case provider.RoleTool:
			name := strings.TrimSpace(message.ToolName)
			if name == "" {
				name = "tool"
			}
			result := agent.BoundedEventDetail("result: " + message.Content)
			if message.ToolCallID != "" {
				if args := toolArgs[message.ToolCallID]; args != "" {
					result = agent.BoundedEventDetail(args + "\n" + result)
				}
			}
			events = append(events, ui.ChatEvent{Kind: "tool", ID: message.ToolCallID, Text: name + " finished", Detail: result})
		}
	}
	return events
}

func skillManager(opts options) (*skill.Manager, error) {
	root, err := resolveRoot(opts)
	if err != nil {
		return nil, err
	}
	state, err := opts.workspaceStatePath(root)
	if err != nil {
		return nil, err
	}
	scope := filepath.Base(state)
	if opts.project != "" {
		registry, err := openRegistry(opts)
		if err != nil {
			return nil, err
		}
		defer registry.Close()
		project, err := registryProject(registry, opts.project)
		if err != nil {
			return nil, err
		}
		scope = string(project.ID)
	}
	manager, err := skill.New(opts.statePath(), scope)
	if err != nil {
		return nil, err
	}
	global, err := skill.New(opts.statePath(), "global")
	if err != nil {
		return nil, err
	}
	return manager.WithFallback(global), nil
}

func runSkill(ctx context.Context, opts options, args []string, stdout io.Writer) error {
	if len(args) > 0 && args[0] == "market" {
		return runSkillMarket(ctx, opts, args[1:], stdout)
	}
	if len(args) == 0 {
		return errors.New("usage: skill install DIRECTORY | list | show NAME | remove NAME")
	}
	m, err := skillManager(opts)
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("skill list takes no arguments")
		}
		v, err := m.List()
		if err != nil {
			return err
		}
		return writeJSON(stdout, skill.WithBundled(v))
	case "install-git":
		if len(args) != 4 {
			return errors.New("skill install-git requires REPOSITORY REF SUBDIRECTORY")
		}
		v, err := m.InstallGit(ctx, args[1], args[2], args[3])
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "install":
		if len(args) != 2 {
			return errors.New("skill install requires a local directory")
		}
		v, err := m.Install(args[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "show":
		if len(args) != 2 {
			return errors.New("skill show requires a name")
		}
		v, err := skill.LoadAvailable(m, args[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "remove":
		if len(args) != 2 {
			return errors.New("skill remove requires a name")
		}
		return m.Remove(args[1])
	default:
		return errors.New("unknown skill command")
	}
}

func runtimeFingerprint(kind, base, model, sshName string, settings config.Settings) string {
	if kind != "chat-completions" {
		base = kind + ":" + base
	}
	if sshName != "" {
		ssh := settings.SSH[sshName]
		base += "\x00ssh:" + ssh.Target + ":" + ssh.Root + ":" + ssh.StateDir
	}
	return providerFingerprint(base, model)
}

func compatibleKeyEnv(kind string) string {
	if kind == "chat-completions" || kind == "responses" {
		return "OPENAI_API_KEY"
	}
	return ""
}

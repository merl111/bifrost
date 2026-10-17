// Package cli implements the small, scriptable Bifrost command line surface.
package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bifrost/internal/browser"
	"bifrost/internal/domain"
	"bifrost/internal/mcpclient"
	"bifrost/internal/process"
	"bifrost/internal/store"
	"bifrost/internal/workspace"
)

const Version = "0.1.0"

// ExitError preserves a command's non-zero process status for cmd/bifrost.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("command exited with status %d", e.Code) }

func ExitCode(err error) int {
	var exitErr *ExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 && exitErr.Code < 256 {
		return exitErr.Code
	}
	return 1
}

type options struct {
	configPath  string
	stateDir    string
	root        string
	project     string
	repository  string
	environment string
	json        bool
	version     bool
}

// Run executes one CLI invocation. It writes normal output to stdout and
// diagnostics to stderr; callers own process exit handling.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	globalArgs, commandArgs, err := extractGlobalFlags(args)
	if err != nil {
		return err
	}
	opts, _, err := parseGlobal(globalArgs, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if opts.version {
		_, _ = fmt.Fprintf(stdout, "bifrost %s\n", Version)
		return nil
	}
	if len(commandArgs) > 0 && opts.root == "" && !strings.HasPrefix(commandArgs[0], "-") {
		if !isKnownTopCommand(commandArgs[0]) {
			if info, statErr := os.Stat(commandArgs[0]); statErr == nil && info.IsDir() {
				opts.root = commandArgs[0]
				commandArgs = commandArgs[1:]
			}
		}
	}
	if len(commandArgs) == 0 {
		if opts.root == "" {
			opts, err = firstRun(ctx, opts, stdout)
			if err != nil {
				return err
			}
		} else {
			opts, err = applyProjectDefault(opts)
			if err != nil {
				return err
			}
		}
		return runTUI(ctx, opts, nil, stderr)
	}
	if commandArgs[0] != "setup" && commandArgs[0] != "config" {
		opts, err = applyProjectDefault(opts)
		if err != nil {
			return err
		}
	}
	switch commandArgs[0] {
	case "account":
		return runAccount(ctx, opts, commandArgs[1:], stdout, stderr)
	case "tasks":
		return runTasks(ctx, opts, commandArgs[1:], stdout)
	case "daemon":
		return runDaemon(ctx, opts, commandArgs[1:], stdout, stderr)
	case "desktop":
		return runDesktop(ctx, commandArgs[1:], stdout)
	case "git":
		return runGit(ctx, opts, commandArgs[1:], stdout)
	case "github":
		return runGitHub(ctx, opts, commandArgs[1:], stdout)
	case "worker":
		return runWorker(ctx, opts, commandArgs[1:], stdout)
	case "remote":
		return runRemote(ctx, opts, commandArgs[1:], stdout)
	case "setup":
		return runSetup(ctx, opts, commandArgs[1:], stdout, stderr)
	case "config":
		return runConfig(opts, commandArgs[1:], stdout)
	case "help":
		printUsage(stdout)
		return nil
	case "version":
		_, _ = fmt.Fprintf(stdout, "bifrost %s\n", Version)
		return nil
	case "project":
		return runProject(opts, commandArgs[1:], stdout, stderr)
	case "file":
		return runFile(opts, commandArgs[1:], stdout, stderr)
	case "exec":
		return runExec(ctx, opts, commandArgs[1:], stdout, stderr)
	case "tui":
		return runTUI(ctx, opts, commandArgs[1:], stderr)
	case "skill":
		return runSkill(ctx, opts, commandArgs[1:], stdout)
	case "vm":
		return runVM(ctx, opts, commandArgs[1:], stdout)
	case "session":
		return runSession(opts, commandArgs[1:], stdout)
	case "review":
		return runReview(ctx, opts, commandArgs[1:], stdout)
	case "search":
		return runSearch(ctx, opts, commandArgs[1:], stdout)
	case "run":
		return runDaemonAgent(ctx, opts, commandArgs[1:], stdout, stderr)
	case "doctor":
		return runDoctor(opts, stdout)
	case "lsp":
		return runLSP(ctx, opts, commandArgs[1:], stdout)
	case "mcp":
		if handled, err := runMCPExtra(ctx, opts, commandArgs[1:], stdout, stderr); handled {
			return err
		}
		return runMCP(ctx, opts, commandArgs[1:], stdout, stderr)
	case "browser":
		return runBrowser(ctx, commandArgs[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q (use --help)", commandArgs[0])
	}
}

func isKnownTopCommand(cmd string) bool {
	switch cmd {
	case "account", "tasks", "daemon", "desktop", "git", "github", "worker", "remote",
		"setup", "config", "help", "version", "project", "file", "exec", "tui", "skill",
		"vm", "session", "review", "search", "run", "doctor", "lsp", "mcp", "browser":
		return true
	default:
		return false
	}
}

func extractGlobalFlags(args []string) ([]string, []string, error) {
	knownValue := map[string]bool{"--config": true, "--state-dir": true, "--root": true, "--project": true, "--repository": true, "--environment": true}
	knownBool := map[string]bool{"--json": true, "--version": true, "--help": true, "-h": true}
	var global, command []string
	var pendingRun []string
	seen := make(map[string]bool)
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			command = append(command, args[i:]...)
			break
		}
		name := a
		if equal := strings.IndexByte(a, '='); equal >= 0 {
			name = a[:equal]
		}
		if len(command) == 0 && runValueFlag(name) {
			pendingRun = append(pendingRun, a)
			if !strings.Contains(a, "=") && runValueFlagNeedsValue(name) {
				if i+1 >= len(args) {
					return nil, nil, fmt.Errorf("run flag %s requires a value", name)
				}
				pendingRun = append(pendingRun, args[i+1])
				i++
			}
			continue
		}
		if localValueFlag(command, name) {
			command = append(command, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				command = append(command, args[i+1])
				i++
			}
			continue
		}
		if knownBool[name] {
			if seen[name] {
				return nil, nil, fmt.Errorf("duplicate global flag %s", name)
			}
			seen[name] = true
			global = append(global, a)
			continue
		}
		if knownValue[name] {
			if seen[name] {
				return nil, nil, fmt.Errorf("duplicate global flag %s", name)
			}
			seen[name] = true
			global = append(global, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				if strings.HasPrefix(args[i+1], "-") {
					return nil, nil, fmt.Errorf("global flag %s requires a value", name)
				}
				global = append(global, args[i+1])
				i++
			} else if !strings.Contains(a, "=") {
				return nil, nil, fmt.Errorf("global flag %s requires a value", name)
			}
			continue
		}
		command = append(command, a)
		if (a == "run" || a == "tui") && len(pendingRun) > 0 {
			command = append(command, pendingRun...)
			pendingRun = nil
		}
	}
	if len(pendingRun) > 0 {
		command = append(pendingRun, command...)
	}
	return global, command, nil
}

func runValueFlag(name string) bool {
	switch name {
	case "--base-url", "--model", "--allow-write", "--allow-exec", "--max-steps", "--skill", "--mcp-config", "--session", "--browser", "--provider", "--profile", "--api-key-env", "--ssh", "--desktop", "--daemon", "--agents", "--account":
		return true
	default:
		return false
	}
}

func runValueFlagNeedsValue(name string) bool {
	return name == "--account" || name == "--agents" || name == "--base-url" || name == "--model" || name == "--max-steps" || name == "--skill" || name == "--mcp-config" || name == "--session" || name == "--provider" || name == "--profile" || name == "--api-key-env" || name == "--ssh"
}

func localValueFlag(command []string, name string) bool {
	if len(command) == 0 || strings.Contains(name, "=") {
		return false
	}
	cmd := command[0]
	sub := ""
	if len(command) > 1 && !strings.HasPrefix(command[1], "-") {
		sub = command[1]
	}
	switch cmd + ":" + sub {
	case "setup:":
		return name == "--provider" || name == "--model" || name == "--base-url" || name == "--api-key-env" || name == "--name"
	case "project:add":
		return name == "--root" || name == "--alias" || name == "--remote" || name == "--default-branch" || name == "--manifest"
	case "project:add-repo":
		return name == "--root" || name == "--alias" || name == "--remote" || name == "--default-branch"
	case "project:remove-repo", "project:remove":
		return name == "--alias"
	case "project:scan":
		return name == "--root" || name == "--name"
	case "file:edit":
		return name == "--expected" || name == "--old" || name == "--new"
	case "file:write":
		return name == "--expected" || name == "--content" || name == "--content-file"
	case "exec:":
		return name == "--cwd" || name == "--max-output-bytes"
	case "run:", "tui:":
		return runValueFlagNeedsValue(name)
	case "mcp:inspect", "mcp:call":
		return name == "--url" || name == "--protocol" || name == "--tool" || name == "--args"
	case "browser:probe":
		return name == "--url" || name == "--screenshot" || name == "--chrome-path" || name == "--timeout"
	default:
		return false
	}
}

func parseGlobal(args []string, stderr io.Writer) (options, []string, error) {
	opts := options{environment: "local"}
	fs := flag.NewFlagSet("bifrost", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { printUsage(stderr) }
	fs.StringVar(&opts.configPath, "config", "", "settings file")
	fs.StringVar(&opts.stateDir, "state-dir", "", "state directory (default: XDG state directory)")
	fs.StringVar(&opts.root, "root", "", "workspace root")
	fs.StringVar(&opts.project, "project", "", "registered project name or ID")
	fs.StringVar(&opts.repository, "repository", "", "repository alias when selecting a project root")
	fs.StringVar(&opts.environment, "environment", "local", "project environment name")
	fs.BoolVar(&opts.json, "json", false, "emit JSON where supported")
	version := fs.Bool("version", false, "print version")
	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	if *version {
		opts.version = true
	}
	return opts, fs.Args(), nil
}

func runProject(opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printProjectUsage(stdout)
		return nil
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("project list takes no positional arguments")
		}
		registry, err := openRegistry(opts)
		if err != nil {
			return err
		}
		defer registry.Close()
		projects, err := registry.List()
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, projects)
		}
		for _, project := range projects {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\trev %d\n", project.Name, project.ID, project.Revision)
		}
		return nil
	case "add":
		return runProjectAdd(opts, args[1:], stdout)
	case "add-repo":
		return runProjectAddRepo(opts, args[1:], stdout)
	case "remove-repo", "remove":
		return runProjectRemoveRepo(opts, args[1:], stdout)
	case "scan":
		return runProjectScan(opts, args[1:], stdout)
	default:
		return fmt.Errorf("unknown project command %q", args[0])
	}
}

func runProjectAddRepo(opts options, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost project add-repo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, alias, remote, branch := "", "", "", ""
	fs.StringVar(&root, "root", "", "repository root (required)")
	fs.StringVar(&alias, "alias", "", "repository alias (required)")
	fs.StringVar(&remote, "remote", "", "repository remote")
	fs.StringVar(&branch, "default-branch", "", "default branch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 1 || root == "" || alias == "" {
		return errors.New("usage: bifrost project add-repo NAME --alias ALIAS --root PATH")
	}
	root, err := absoluteDirectory(root)
	if err != nil {
		return fmt.Errorf("repository root: %w", err)
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return err
	}
	defer registry.Close()
	project, err := registryProject(registry, fs.Args()[0])
	if err != nil {
		return err
	}
	for _, repo := range project.Repositories {
		if repo.Alias == alias {
			return fmt.Errorf("repository alias %q already exists in project %q", alias, project.Name)
		}
	}
	repoID, err := domain.NewID(domain.KindRepo)
	if err != nil {
		return err
	}
	project.Repositories = append(project.Repositories, domain.Repository{ID: repoID, ProjectID: project.ID, Alias: alias, Remote: remote, DefaultBranch: branch, Roots: map[string]string{opts.environment: root}})
	if err := registry.Update(project); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(stdout, project)
	}
	_, _ = fmt.Fprintf(stdout, "added repository %s to %s\n", alias, project.Name)
	return nil
}

func runProjectAdd(opts options, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost project add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := ""
	alias, remote, branch, manifest := "local", "", "", ""
	fs.StringVar(&root, "root", opts.root, "repository root (required)")
	fs.StringVar(&alias, "alias", "local", "repository alias")
	fs.StringVar(&remote, "remote", "", "repository remote")
	fs.StringVar(&branch, "default-branch", "", "default branch")
	fs.StringVar(&manifest, "manifest", "", "portable project manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) != 1 {
		return errors.New("usage: bifrost project add NAME --root PATH")
	}
	if root == "" {
		return errors.New("project add requires --root")
	}
	root, err := absoluteDirectory(root)
	if err != nil {
		return fmt.Errorf("project root: %w", err)
	}
	if manifest != "" {
		manifest, err = filepath.Abs(manifest)
		if err != nil {
			return fmt.Errorf("manifest: %w", err)
		}
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return err
	}
	defer registry.Close()
	project, err := registry.Create(store.CreateProjectRequest{
		Name: pos[0], ManifestPath: manifest,
		Repositories: []store.RepositoryInput{{Alias: alias, Remote: remote, DefaultBranch: branch, Roots: map[string]string{opts.environment: root}}},
	})
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(stdout, project)
	}
	_, _ = fmt.Fprintf(stdout, "created %s (%s)\n", project.Name, project.ID)
	return nil
}

func runFile(opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printFileUsage(stdout)
		return nil
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return err
	}
	workspaceState, err := opts.workspaceStatePath(root)
	if err != nil {
		return err
	}
	w, err := workspace.Open(root, workspaceState)
	if err != nil {
		return err
	}
	defer w.Close()
	switch args[0] {
	case "read":
		if len(args) != 2 {
			return errors.New("usage: bifrost file read PATH")
		}
		text, revision, err := w.Read(args[1])
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, map[string]any{"path": args[1], "text": text, "revision": revision})
		}
		_, _ = io.WriteString(stdout, text)
		if !strings.HasSuffix(text, "\n") {
			_, _ = io.WriteString(stdout, "\n")
		}
		_, _ = fmt.Fprintf(stderr, "revision %s\n", revision)
		return nil
	case "patch":
		if len(args) != 2 {
			return errors.New("file patch PLAN.json")
		}
		f, err := os.Open(args[1])
		if err != nil {
			return err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
		if err != nil {
			return err
		}
		if len(b) > 16<<20 {
			return errors.New("patch plan exceeds 16 MiB")
		}
		var plan workspace.PatchPlan
		if err := decodeConfigValue(string(b), &plan); err != nil {
			return err
		}
		result, err := w.ApplyPatch(plan)
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "undo-patch":
		if len(args) != 2 {
			return errors.New("file undo-patch PATCH_ID")
		}
		result, err := w.UndoPatch(workspace.ChangeID(args[1]))
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "edit":
		return runFileEdit(opts, w, args[1:], stdout)
	case "write":
		return runFileWrite(opts, w, args[1:], stdout)
	case "undo":
		if len(args) != 2 {
			return errors.New("usage: bifrost file undo CHANGE_ID")
		}
		if err := w.Undo(workspace.ChangeID(args[1])); err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, map[string]any{"change_id": args[1], "status": "reverted"})
		}
		_, _ = fmt.Fprintf(stdout, "reverted %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("unknown file command %q", args[0])
	}
}

func runFileEdit(opts options, w *workspace.Workspace, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost file edit", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	expected, oldText, newText := "", "", ""
	fs.StringVar(&expected, "expected", "", "expected file revision (required)")
	fs.StringVar(&oldText, "old", "", "text to replace")
	fs.StringVar(&newText, "new", "", "replacement text")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hasNew := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "new" {
			hasNew = true
		}
	})
	if !hasNew || expected == "" || oldText == "" || len(fs.Args()) != 1 {
		return errors.New("usage: bifrost file edit PATH --expected REVISION --old TEXT --new TEXT")
	}
	change, err := w.Edit(fs.Args()[0], expected, oldText, newText)
	if err != nil {
		return err
	}
	return printChange(stdout, opts.json, change)
}

func runFileWrite(opts options, w *workspace.Workspace, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost file write", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	expected, content, contentFile := "", "", ""
	mustNotExist := false
	fs.StringVar(&expected, "expected", "", "expected file revision")
	fs.BoolVar(&mustNotExist, "must-not-exist", false, "require the destination to be absent")
	fs.StringVar(&content, "content", "", "file content")
	fs.StringVar(&contentFile, "content-file", "", "read file content from this path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hasExpected, hasContent, hasContentFile := false, false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "expected":
			hasExpected = true
		case "content":
			hasContent = true
		case "content-file":
			hasContentFile = true
		}
	})
	if len(fs.Args()) != 1 || (hasExpected == mustNotExist) || (hasContent == hasContentFile) {
		return errors.New("usage: bifrost file write PATH (--expected REVISION | --must-not-exist) (--content TEXT | --content-file PATH)")
	}
	if contentFile != "" {
		b, err := os.ReadFile(contentFile)
		if err != nil {
			return fmt.Errorf("read content file: %w", err)
		}
		content = string(b)
	}
	var revision *workspace.Revision
	if !mustNotExist {
		r := expected
		revision = &r
	}
	change, err := w.Write(fs.Args()[0], revision, content)
	if err != nil {
		return err
	}
	return printChange(stdout, opts.json, change)
}

func runExec(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("bifrost exec", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cwd, maxOutput := opts.root, 0
	var err error
	fs.StringVar(&cwd, "cwd", opts.root, "working directory")
	fs.IntVar(&maxOutput, "max-output-bytes", 0, "per-stream output limit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	hasCwd := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "cwd" {
			hasCwd = true
		}
	})
	if hasCwd && (opts.root != "" || opts.project != "") {
		return errors.New("--cwd cannot be combined with --root or --project")
	}
	argv := fs.Args()
	if len(argv) > 0 && argv[0] == "--" {
		argv = argv[1:]
	}
	if len(argv) == 0 {
		return errors.New("usage: bifrost exec [--cwd PATH] -- COMMAND [ARG ...]")
	}
	if !hasCwd && (opts.root != "" || opts.project != "") {
		cwd, err = resolveRoot(opts)
		if err != nil {
			return err
		}
	} else if cwd == "" {
		cwd, _ = os.Getwd()
	}
	cwd, err = absoluteDirectory(cwd)
	if err != nil {
		return fmt.Errorf("exec cwd: %w", err)
	}
	result, runErr := process.Run(ctx, process.Spec{Argv: argv, Dir: cwd, MaxOutputBytes: maxOutput})
	if opts.json {
		payload := map[string]any{"argv": result.Argv, "exit_code": result.ExitCode, "cancelled": result.Cancelled, "stdout": string(result.Stdout), "stderr": string(result.Stderr), "stdout_truncated": result.StdoutTruncated, "stderr_truncated": result.StderrTruncated}
		if err := writeJSON(stdout, payload); err != nil {
			return err
		}
	} else {
		_, _ = stdout.Write(result.Stdout)
		_, _ = stderr.Write(result.Stderr)
		if result.StdoutTruncated || result.StderrTruncated {
			_, _ = fmt.Fprintln(stderr, "command output truncated")
		}
	}
	if runErr != nil {
		return runErr
	}
	if result.ExitCode != 0 {
		return &ExitError{Code: result.ExitCode}
	}
	return nil
}

type commandCheck struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Note      string `json:"note,omitempty"`
}

func runDoctor(opts options, stdout io.Writer) error {
	checks := []commandCheck{
		findCommand("git", "git"),
		findCommand("rg", "rg"),
		findCommand("ssh", "ssh"),
		findCandidates("browser", []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "msedge"}),
		findCandidates("qemu", []string{"qemu-system-x86_64", "qemu-system-aarch64"}),
		findCommand("virtualbox", "VBoxManage"),
	}
	if opts.json {
		return writeJSON(stdout, map[string]any{"checks": checks, "note": "Executable availability only; this does not prove browser launch, VM connectivity, or guest readiness."})
	}
	for _, check := range checks {
		state := "missing"
		if check.Available {
			state = "available"
		}
		if check.Path != "" {
			_, _ = fmt.Fprintf(stdout, "%s: %s (%s)\n", check.Name, state, check.Path)
		} else {
			_, _ = fmt.Fprintf(stdout, "%s: %s\n", check.Name, state)
		}
	}
	_, _ = fmt.Fprintln(stdout, "note: executable availability only; no browser launch, VM connectivity, or guest readiness was checked")
	return nil
}

func findCommand(name, executable string) commandCheck {
	path, err := execLookPath(executable)
	return commandCheck{Name: name, Available: err == nil, Path: path}
}

func findCandidates(name string, candidates []string) commandCheck {
	for _, candidate := range candidates {
		if path, err := execLookPath(candidate); err == nil {
			return commandCheck{Name: name, Available: true, Path: path, Note: candidate}
		}
	}
	return commandCheck{Name: name, Available: false}
}

var execLookPath = func(file string) (string, error) {
	return exec.LookPath(file)
}

func runMCP(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: bifrost mcp inspect (--url URL | -- COMMAND [ARG ...])")
		_, _ = fmt.Fprintln(stdout, "       bifrost mcp call --tool NAME [--args JSON] (--url URL | -- COMMAND [ARG ...])")
		return nil
	}
	action := args[0]
	if action != "inspect" && action != "call" {
		return fmt.Errorf("unknown MCP command %q", action)
	}
	fs := flag.NewFlagSet("bifrost mcp "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpointURL, protocol, toolName, rawArgs := "", "", "", "{}"
	fs.StringVar(&endpointURL, "url", "", "MCP Streamable HTTP endpoint")
	fs.StringVar(&protocol, "protocol", "", "optional MCP protocol version")
	fs.StringVar(&toolName, "tool", "", "MCP tool name")
	fs.StringVar(&rawArgs, "args", "{}", "MCP tool arguments as JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	command := append([]string(nil), fs.Args()...)
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if endpointURL != "" && len(command) > 0 {
		return errors.New("MCP endpoint accepts either --url or an argv command, not both")
	}
	if endpointURL == "" && len(command) == 0 {
		return errors.New("MCP endpoint requires --url URL or -- COMMAND [ARG ...]")
	}
	if action == "call" && strings.TrimSpace(toolName) == "" {
		return errors.New("MCP call requires --tool NAME")
	}
	if !json.Valid([]byte(rawArgs)) {
		return errors.New("MCP --args must be valid JSON")
	}
	session, err := mcpclient.Dial(ctx, mcpclient.Endpoint{URL: endpointURL, Command: command, ProtocolVersion: protocol}, stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	if action == "inspect" {
		info := session.Inspect()
		tools, err := session.List(ctx)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(stdout, map[string]any{"server": info, "tools": tools})
		}
		_, _ = fmt.Fprintf(stdout, "%s %s (protocol %s)\n", info.Name, info.Version, info.ProtocolVersion)
		for _, tool := range tools {
			_, _ = fmt.Fprintf(stdout, "  %s", tool.Name)
			if tool.Description != "" {
				_, _ = fmt.Fprintf(stdout, " - %s", tool.Description)
			}
			_, _ = fmt.Fprintln(stdout)
		}
		return nil
	}
	result, err := session.Call(ctx, toolName, json.RawMessage(rawArgs))
	if writeErr := writeJSON(stdout, result); writeErr != nil {
		return writeErr
	}
	return err
}

func runBrowser(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: bifrost browser probe --url URL [--screenshot PATH] [--chrome-path PATH]")
		return nil
	}
	if args[0] != "probe" {
		return fmt.Errorf("unknown browser command %q", args[0])
	}
	fs := flag.NewFlagSet("bifrost browser probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	pageURL, screenshotPath, chromePath := "", "", ""
	timeout := 15 * time.Second
	fs.StringVar(&pageURL, "url", "", "URL to navigate")
	fs.StringVar(&screenshotPath, "screenshot", "", "write a PNG screenshot to this path")
	fs.StringVar(&chromePath, "chrome-path", "", "Chromium executable path")
	fs.DurationVar(&timeout, "timeout", timeout, "navigation and snapshot timeout")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || strings.TrimSpace(pageURL) == "" {
		return errors.New("browser probe requires --url URL")
	}
	parsed, err := url.Parse(pageURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("invalid browser URL %q", pageURL)
	}
	session, err := browser.Launch(ctx, browser.Config{ChromePath: chromePath, Timeout: timeout})
	if err != nil {
		return err
	}
	defer session.Close()
	if err := session.Navigate(pageURL); err != nil {
		return err
	}
	snapshot, err := session.Snapshot()
	if err != nil {
		return err
	}
	if screenshotPath != "" {
		png, err := session.Screenshot()
		if err != nil {
			return err
		}
		if err := os.WriteFile(screenshotPath, png, 0600); err != nil {
			return fmt.Errorf("write browser screenshot: %w", err)
		}
	}
	return writeJSON(stdout, snapshot)
}

func printChange(out io.Writer, asJSON bool, change workspace.Change) error {
	if asJSON {
		return writeJSON(out, change)
	}
	_, err := fmt.Fprintf(out, "change %s: %s -> %s\n", change.ID, change.Before, change.After)
	return err
}

func writeJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func (o options) statePath() string {
	if o.stateDir != "" {
		return o.stateDir
	}
	return defaultStateDir()
}

func (o options) workspaceStatePath(root string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("canonicalize workspace root: %w", err)
	}
	sum := sha256.Sum256([]byte(filepath.Clean(canonical)))
	return filepath.Join(o.statePath(), "workspace", hex.EncodeToString(sum[:])), nil
}

func openRegistry(opts options) (*store.Registry, error) {
	return store.OpenRegistry(filepath.Join(opts.statePath(), "registry"))
}

func registryProject(registry *store.Registry, value string) (domain.Project, error) {
	if id, err := domain.ParseID(value); err == nil {
		return registry.Get(id)
	}
	return registry.GetByName(value)
}

func resolveRoot(opts options) (string, error) {
	if opts.root != "" && opts.project != "" {
		return "", errors.New("--root and --project cannot be used together")
	}
	if opts.root != "" {
		return absoluteDirectory(opts.root)
	}
	if opts.project == "" {
		root, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
		return root, nil
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return "", err
	}
	defer registry.Close()
	project, err := registryProject(registry, opts.project)
	if err != nil {
		return "", err
	}
	if len(project.Repositories) == 0 {
		return "", fmt.Errorf("project %q has no repositories", project.Name)
	}
	alias := opts.repository
	if alias == "" {
		alias = project.Repositories[0].Alias
	}
	for _, repo := range project.Repositories {
		if repo.Alias != alias {
			continue
		}
		root := repo.Roots[opts.environment]
		if root == "" {
			return "", fmt.Errorf("project %q repository %q has no root for environment %q", project.Name, alias, opts.environment)
		}
		return absoluteDirectory(root)
	}
	return "", fmt.Errorf("project %q has no repository %q", project.Name, alias)
}

func absoluteDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.Clean(abs), nil
}

func defaultStateDir() string {
	if value := os.Getenv("XDG_STATE_HOME"); value != "" {
		return filepath.Join(value, "bifrost")
	}
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if runtime.GOOS == "windows" {
			if config, configErr := os.UserConfigDir(); configErr == nil && config != "" {
				return filepath.Join(config, "bifrost")
			}
		}
		return filepath.Join(home, ".local", "state", "bifrost")
	}
	return filepath.Join(".", ".bifrost-state")
}

func printUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Bifrost local coding workspace")
	_, _ = fmt.Fprintln(out, "Usage: bifrost [global flags] <command>")
	_, _ = fmt.Fprintln(out, "\nCommands:")
	fmt.Fprintln(out, "  setup                          First-run onboarding and config")
	fmt.Fprintln(out, "  account login/list/use/status/logout/models  Connect and switch accounts")
	fmt.Fprintln(out, "  config show/set/import/path    Manage saved profiles and defaults")
	fmt.Fprintln(out, "  tui [agent flags]              Chat, review, files and terminal")
	fmt.Fprintln(out, "  session list/show/export/import/compact  Manage conversations")
	fmt.Fprintln(out, "  vm qemu/virtualbox ...          Lifecycle, snapshots, screenshot and input")
	fmt.Fprintln(out, "  review                         Read staged/unstaged Git changes")
	fmt.Fprintln(out, "  search QUERY                   Literal rooted text search")
	fmt.Fprintln(out, "  skill install/list/show/remove Manage project-scoped skill snapshots")
	fmt.Fprintln(out, "  skill install-git REPO REF PATH Install an explicit Git skill snapshot")
	_, _ = fmt.Fprintln(out, "  project add NAME --root PATH   Register a project repository")
	_, _ = fmt.Fprintln(out, "  project add-repo NAME ...       Add another repository root")
	_, _ = fmt.Fprintln(out, "  project remove-repo NAME ALIAS  Remove a repository root")
	_, _ = fmt.Fprintln(out, "  project scan [--root PATH]      Scan directory for GitHub repositories")
	_, _ = fmt.Fprintln(out, "  project list                   List registered projects")
	_, _ = fmt.Fprintln(out, "  file read PATH                 Read a rooted text file")
	_, _ = fmt.Fprintln(out, "  file edit PATH ...             Apply one exact revision-checked edit")
	_, _ = fmt.Fprintln(out, "  file write PATH ...            Write with an explicit precondition")
	_, _ = fmt.Fprintln(out, "  file undo CHANGE_ID            Undo when the current revision still matches")
	_, _ = fmt.Fprintln(out, "  exec -- COMMAND [ARG ...]      Run an explicit argv command")
	_, _ = fmt.Fprintln(out, "  run PROMPT                     Run the streaming model/tool loop")
	_, _ = fmt.Fprintln(out, "  mcp inspect ...                Inspect an MCP server and list tools")
	_, _ = fmt.Fprintln(out, "  mcp call ...                   Call one MCP tool with JSON arguments")
	_, _ = fmt.Fprintln(out, "  browser probe --url URL        Navigate and print a browser snapshot")
	fmt.Fprintln(out, "  tasks list/tree/status/events/cancel  Monitor and cancel sub-agents")
	fmt.Fprintln(out, "  daemon start/jobs/attach/cancel/stop  Manage background jobs")
	fmt.Fprintln(out, "  git status/diff/stage/unstage/commit/worktree  Review repositories")
	fmt.Fprintln(out, "  github auth-status/link/repo/pr/checks  GitHub integration")
	fmt.Fprintln(out, "  remote NAME inspect/read/list/exec/review  Guest workspace")
	fmt.Fprintln(out, "  desktop capabilities/capture   Inspect display or capture an observation")
	fmt.Fprintln(out, "  lsp check/hover/definition -- SERVER...  Explicit language server")
	fmt.Fprintln(out, "  mcp config/resources/prompts/serve  Configure MCP and expose native tools")
	fmt.Fprintln(out, "  file patch PLAN.json | file undo-patch ID  Recoverable multi-file edits")
	_, _ = fmt.Fprintln(out, "  doctor                         Check local executable availability")
	_, _ = fmt.Fprintln(out, "\nAgent flags: --profile, --provider, --model, --allow-write, --allow-exec, --agents N, --browser, --desktop, --ssh NAME, --daemon, --session ID")
	fmt.Fprintln(out, "Global flags: --config, --root, --project, --repository, --environment, --state-dir, --json, --version")
}

func printProjectUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage: bifrost project add NAME --root PATH | project add-repo NAME --alias ALIAS --root PATH | project remove-repo NAME ALIAS | project scan [--root PATH] [--add] | project list")
}

func printFileUsage(out io.Writer) {
	_, _ = fmt.Fprintln(out, "Usage: bifrost file read PATH | file edit PATH --expected REV --old TEXT --new TEXT | file write PATH (--expected REV | --must-not-exist) (--content TEXT | --content-file PATH) | file undo CHANGE_ID")
}

// reorderFlags permits the conventional "PATH --flag value" form while
// retaining the standard flag package's validation and help behavior.
func reorderFlags(args []string) []string {
	var flags, positional []string
	boolean := map[string]bool{"--must-not-exist": true, "--add": true}
	value := map[string]bool{
		"--root": true, "--alias": true, "--remote": true, "--default-branch": true, "--manifest": true, "--name": true,
		"--expected": true, "--old": true, "--new": true, "--content": true, "--content-file": true,
		"--cwd": true, "--max-output-bytes": true,
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i:]...)
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			flags = append(flags, a)
			if !boolean[a] && value[a] && !strings.Contains(a, "=") && i+1 < len(args) {
				flags = append(flags, args[i+1])
				i++
			} else if !boolean[a] && !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}

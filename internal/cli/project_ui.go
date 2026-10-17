package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"bifrost/internal/config"
	"bifrost/internal/domain"
	"bifrost/internal/github"
	"bifrost/internal/notion"
	"bifrost/internal/store"
	"bifrost/internal/ui"
)

func projectUIOption(opts options, root string) ui.Option {
	env := opts.environment
	if env == "" {
		env = "local"
	}
	initialStatus, _ := scanWorkspaceProject(opts, root, env)
	callbacks := workspaceProjectCallbacks(opts, root)
	return func(m *ui.Model) {
		ui.WithInitialProjectStatus(initialStatus)(m)
		ui.WithProject(callbacks)(m)
	}
}

func workspaceProjectCallbacks(opts options, root string) ui.ProjectCallbacks {
	env := opts.environment
	if env == "" {
		env = "local"
	}
	curOpts := opts
	var mu sync.Mutex
	return ui.ProjectCallbacks{
		Scan: func(ctx context.Context) (ui.ProjectStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			return scanWorkspaceProject(curOpts, root, env)
		},
		AddRepo: func(ctx context.Context, alias, repoPath, remote, branch string) (ui.ProjectStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			st, err := addWorkspaceProjectRepo(curOpts, root, env, alias, repoPath, remote, branch)
			if err == nil && st.ProjectName != "" {
				curOpts.project = st.ProjectName
			}
			return st, err
		},
		AddAll: func(ctx context.Context) (ui.ProjectStatus, int, error) {
			mu.Lock()
			defer mu.Unlock()
			st, added, err := addAllWorkspaceProjectRepos(curOpts, root, env)
			if err == nil && st.ProjectName != "" {
				curOpts.project = st.ProjectName
			}
			return st, added, err
		},
		RemoveRepo: func(ctx context.Context, alias string) (ui.ProjectStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			return removeWorkspaceProjectRepo(curOpts, root, env, alias)
		},
		UpdateMeta: func(ctx context.Context, name, description string) (ui.ProjectStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			st, err := updateWorkspaceProjectMeta(curOpts, root, env, name, description)
			if err == nil && st.ProjectName != "" {
				curOpts.project = st.ProjectName
			}
			return st, err
		},
		SaveSettings: func(ctx context.Context, next ui.ProjectStatus) (ui.ProjectStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			st, err := saveWorkspaceProjectSettings(curOpts, root, env, next)
			if err == nil && st.ProjectName != "" {
				curOpts.project = st.ProjectName
			}
			return st, err
		},
		FetchDetails: func(ctx context.Context) (ui.ProjectDetails, error) {
			mu.Lock()
			defer mu.Unlock()
			return fetchWorkspaceProjectDetails(ctx, curOpts, root, env)
		},
	}
}

func resolveOrMatchWorkspaceProject(registry *store.Registry, opts options, root, env string) (domain.Project, bool, error) {
	if opts.project != "" {
		p, err := registryProject(registry, opts.project)
		if errors.Is(err, store.ErrProjectNotFound) {
			return domain.Project{}, false, nil
		}
		if err != nil {
			return domain.Project{}, false, err
		}
		return p, true, nil
	}
	projects, err := registry.List()
	if err != nil {
		return domain.Project{}, false, err
	}
	cleanRoot := filepath.Clean(root)
	// First prefer a project whose name matches the directory or that contains a repo under cleanRoot.
	baseAlias := store.SanitizeAlias(filepath.Base(cleanRoot))
	for _, p := range projects {
		for _, r := range p.Repositories {
			rRoot := filepath.Clean(r.Roots[env])
			if rRoot == cleanRoot || strings.HasPrefix(rRoot, cleanRoot+string(filepath.Separator)) {
				return p, true, nil
			}
		}
	}
	for _, p := range projects {
		if p.Name == baseAlias {
			return p, true, nil
		}
	}
	return domain.Project{}, false, nil
}

func ensureWorkspaceProject(registry *store.Registry, opts options, root, env string) (domain.Project, error) {
	if p, ok, err := resolveOrMatchWorkspaceProject(registry, opts, root, env); err != nil {
		return domain.Project{}, err
	} else if ok {
		return p, nil
	}
	baseName := opts.project
	if baseName == "" {
		baseName = store.SanitizeAlias(filepath.Base(root))
	}
	name := baseName
	for seq := 2; ; seq++ {
		p, err := registry.Create(store.CreateProjectRequest{Name: name})
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, store.ErrDuplicateProject) {
			return domain.Project{}, err
		}
		name = fmt.Sprintf("%s-%d", baseName, seq)
	}
}

func scanWorkspaceProject(opts options, root, env string) (ui.ProjectStatus, error) {
	discovered, err := store.ScanGitRepositories(root)
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	defer registry.Close()

	project, hasProject, err := resolveOrMatchWorkspaceProject(registry, opts, root, env)
	if err != nil && !errors.Is(err, store.ErrProjectNotFound) {
		return ui.ProjectStatus{}, err
	}
	projName := project.Name
	if projName == "" {
		if opts.project != "" {
			projName = opts.project
		} else {
			projName = store.SanitizeAlias(filepath.Base(root))
		}
	}

	linkedByAlias := make(map[string]domain.Repository)
	linkedByPath := make(map[string]domain.Repository)
	if hasProject {
		for _, r := range project.Repositories {
			linkedByAlias[r.Alias] = r
			if p := r.Roots[env]; p != "" {
				linkedByPath[filepath.Clean(p)] = r
			}
		}
	}

	seenLinkedAliases := make(map[string]bool)
	entries := make([]ui.ProjectRepoEntry, 0, len(discovered)+len(project.Repositories))
	for _, d := range discovered {
		entry := ui.ProjectRepoEntry{
			Alias:         d.Alias,
			Path:          d.Path,
			RelPath:       d.RelPath,
			Remote:        d.Remote,
			GitHubSlug:    d.GitHubSlug,
			DefaultBranch: d.DefaultBranch,
			CurrentBranch: d.CurrentBranch,
			GitAdded:      d.GitAdded,
			GitModified:   d.GitModified,
			GitDeleted:    d.GitDeleted,
			Discovered:    true,
		}
		if linked, ok := linkedByPath[filepath.Clean(d.Path)]; ok {
			entry.Linked = true
			entry.Alias = linked.Alias
			if linked.Remote != "" {
				entry.Remote = linked.Remote
				if o, r, _, isGH := store.ParseGitHubRemote(linked.Remote); isGH {
					entry.GitHubSlug = o + "/" + r
				}
			}
			if linked.DefaultBranch != "" {
				entry.DefaultBranch = linked.DefaultBranch
			}
			seenLinkedAliases[linked.Alias] = true
		} else if linked, ok := linkedByAlias[d.Alias]; ok {
			entry.Linked = true
			if linked.Remote != "" {
				entry.Remote = linked.Remote
				if o, r, _, isGH := store.ParseGitHubRemote(linked.Remote); isGH {
					entry.GitHubSlug = o + "/" + r
				}
			}
			if linked.DefaultBranch != "" {
				entry.DefaultBranch = linked.DefaultBranch
			}
			seenLinkedAliases[linked.Alias] = true
		}
		entries = append(entries, entry)
	}
	if hasProject {
		for _, r := range project.Repositories {
			if seenLinkedAliases[r.Alias] {
				continue
			}
			rPath := r.Roots[env]
			var rel string
			if rPath != "" {
				if rp, relErr := filepath.Rel(root, rPath); relErr == nil {
					rel = filepath.ToSlash(rp)
				} else {
					rel = rPath
				}
			}
			var slug string
			if o, repoName, _, isGH := store.ParseGitHubRemote(r.Remote); isGH {
				slug = o + "/" + repoName
			}
			var added, modified, deleted int
			if rPath != "" {
				added, modified, deleted = store.InspectGitWorktreeCounts(rPath)
			}
			entries = append(entries, ui.ProjectRepoEntry{
				Alias:         r.Alias,
				Path:          rPath,
				RelPath:       rel,
				Remote:        r.Remote,
				GitHubSlug:    slug,
				DefaultBranch: r.DefaultBranch,
				CurrentBranch: r.DefaultBranch,
				GitAdded:      added,
				GitModified:   modified,
				GitDeleted:    deleted,
				Linked:        true,
			})
		}
	}
	st := ui.ProjectStatus{
		ProjectName:        projName,
		ProjectDescription: project.Description,
		ProjectID:          string(project.ID),
		Root:               root,
		Environment:        env,
		Repos:              entries,
	}
	if _, statErr := os.Stat(filepath.Join(root, "AGENTS.md")); statErr == nil {
		st.AgentsMDDetected = true
	} else if _, statErr := os.Stat(filepath.Join(root, "CLAUDE.md")); statErr == nil {
		st.AgentsMDDetected = true
	}
	for _, s := range DiscoverWorkspaceSkills(opts, root) {
		st.AutoDetectedSkills = append(st.AutoDetectedSkills, s.Name)
	}
	if settings, sErr := opts.settings(); sErr == nil {
		st.AllowWrite = settings.Defaults.AllowWrite
		st.AllowExec = settings.Defaults.AllowExec
		st.Browser = settings.Defaults.Browser
		st.Provider = settings.Defaults.Provider
		st.Skills = append([]string(nil), settings.Defaults.Skills...)
		st.MCPConfig = settings.Defaults.MCPConfig
		st.GitHubTokenEnv = settings.Connectors.GitHubTokenEnv
		st.NotionTokenEnv = settings.Connectors.NotionTokenEnv
		st.DecisionEnabled = settings.Decision.Enabled
		st.DecisionKind = settings.Decision.Kind
		st.DecisionModel = settings.Decision.Model
		st.DecisionAPIKeyEnv = settings.Decision.APIKeyEnv
		st.DecisionMinConfidence = settings.Decision.MinConfidence
		if len(settings.Decision.Candidates) > 0 {
			st.DecisionCandidates = make(map[string]string, len(settings.Decision.Candidates))
			for k, v := range settings.Decision.Candidates {
				st.DecisionCandidates[k] = v
			}
		}
		if projCfg, ok := settings.Projects[projName]; ok {
			if projCfg.Provider != "" {
				st.Provider = projCfg.Provider
			}
			if projCfg.Environment != "" {
				st.Environment = projCfg.Environment
			}
			if len(projCfg.Skills) > 0 {
				st.Skills = append([]string(nil), projCfg.Skills...)
			}
			if projCfg.MCPConfig != "" {
				st.MCPConfig = projCfg.MCPConfig
			}
			if projCfg.GitHubTokenEnv != "" {
				st.GitHubTokenEnv = projCfg.GitHubTokenEnv
			}
			if projCfg.NotionTokenEnv != "" {
				st.NotionTokenEnv = projCfg.NotionTokenEnv
			}
			if projCfg.NotionPage != "" {
				st.NotionPage = projCfg.NotionPage
			}
		}
		for provName := range settings.Providers {
			st.AvailableProviders = append(st.AvailableProviders, provName)
		}
		sort.Strings(st.AvailableProviders)
	}
	return st, nil
}

func saveWorkspaceProjectSettings(opts options, root, env string, next ui.ProjectStatus) (ui.ProjectStatus, error) {
	if _, err := updateWorkspaceProjectMeta(opts, root, env, next.ProjectName, next.ProjectDescription); err != nil {
		return ui.ProjectStatus{}, err
	}
	if strings.TrimSpace(next.ProjectName) != "" {
		opts.project = strings.TrimSpace(next.ProjectName)
	}
	settings, err := opts.settings()
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	settings.Defaults.AllowWrite = next.AllowWrite
	settings.Defaults.AllowExec = next.AllowExec
	settings.Defaults.Browser = next.Browser
	projName := opts.project
	if projName == "" {
		projName = store.SanitizeAlias(filepath.Base(root))
	}
	if settings.Projects == nil {
		settings.Projects = make(map[string]config.ProjectDefaults)
	}
	pd := settings.Projects[projName]
	pd.Provider = strings.TrimSpace(next.Provider)
	pd.Environment = strings.TrimSpace(next.Environment)
	if pd.Environment == "local" {
		pd.Environment = ""
	}
	pd.Skills = append([]string(nil), next.Skills...)
	pd.MCPConfig = strings.TrimSpace(next.MCPConfig)
	pd.GitHubTokenEnv = strings.TrimSpace(next.GitHubTokenEnv)
	pd.NotionTokenEnv = strings.TrimSpace(next.NotionTokenEnv)
	pd.NotionPage = strings.TrimSpace(next.NotionPage)
	settings.Projects[projName] = pd
	if pd.GitHubTokenEnv != "" {
		settings.Connectors.GitHubTokenEnv = pd.GitHubTokenEnv
	}
	if pd.NotionTokenEnv != "" {
		settings.Connectors.NotionTokenEnv = pd.NotionTokenEnv
	}

	settings.Decision.Enabled = next.DecisionEnabled
	if next.DecisionKind != "" {
		settings.Decision.Kind = next.DecisionKind
	} else if settings.Decision.Kind == "" {
		settings.Decision.Kind = "openrouter"
	}
	if strings.TrimSpace(next.DecisionModel) != "" {
		settings.Decision.Model = strings.TrimSpace(next.DecisionModel)
	} else if settings.Decision.Model == "" {
		settings.Decision.Model = "~typesafe/jev-latest"
	}
	if strings.TrimSpace(next.DecisionAPIKeyEnv) != "" {
		settings.Decision.APIKeyEnv = strings.TrimSpace(next.DecisionAPIKeyEnv)
	} else if settings.Decision.APIKeyEnv == "" {
		settings.Decision.APIKeyEnv = "OPENROUTER_API_KEY"
	}
	if next.DecisionMinConfidence > 0 {
		settings.Decision.MinConfidence = next.DecisionMinConfidence
	}
	if len(next.DecisionCandidates) > 0 {
		cands := make(map[string]string)
		for k, v := range next.DecisionCandidates {
			if _, ok := settings.Providers[k]; ok {
				cands[k] = v
			}
		}
		if len(cands) > 0 {
			settings.Decision.Candidates = cands
		}
	}
	if settings.Decision.Enabled && len(settings.Decision.Candidates) < 2 {
		if len(settings.Providers) >= 2 {
			cands := make(map[string]string)
			names := make([]string, 0, len(settings.Providers))
			for k := range settings.Providers {
				names = append(names, k)
			}
			sort.Strings(names)
			for i, k := range names {
				if i == 0 {
					cands[k] = "fast routine coding and inspection"
				} else {
					cands[k] = "complex reasoning, architecture, and high-risk work"
				}
			}
			settings.Decision.Candidates = cands
		} else {
			settings.Decision.Enabled = false
			return ui.ProjectStatus{}, errors.New("decision engine requires at least 2 configured accounts/profiles (:accounts to add)")
		}
	}

	cfgPath, err := opts.configFile()
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	if err := config.SaveSettings(cfgPath, settings, true); err != nil {
		return ui.ProjectStatus{}, err
	}
	return scanWorkspaceProject(opts, root, env)
}

func updateWorkspaceProjectMeta(opts options, root, env, name, description string) (ui.ProjectStatus, error) {
	registry, err := openRegistry(opts)
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	project, err := ensureWorkspaceProject(registry, opts, root, env)
	if err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	name = strings.TrimSpace(name)
	if name != "" && name != project.Name {
		if err := domain.ValidateName(name); err != nil {
			sanitized := store.SanitizeAlias(name)
			if err2 := domain.ValidateName(sanitized); err2 != nil {
				_ = registry.Close()
				return ui.ProjectStatus{}, err
			}
			name = sanitized
		}
		project.Name = name
	}
	project.Description = strings.TrimSpace(description)
	if err := registry.Update(project); err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	_ = registry.Close()
	opts.project = project.Name
	return scanWorkspaceProject(opts, root, env)
}

func addWorkspaceProjectRepo(opts options, root, env, alias, repoPath, remote, branch string) (ui.ProjectStatus, error) {
	registry, err := openRegistry(opts)
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	project, err := ensureWorkspaceProject(registry, opts, root, env)
	if err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	if repoPath == "" {
		candidate := filepath.Join(root, alias)
		if dir, dirErr := absoluteDirectory(candidate); dirErr == nil {
			repoPath = dir
		} else {
			repoPath = root
		}
	} else if dir, dirErr := absoluteDirectory(repoPath); dirErr == nil {
		repoPath = dir
	}
	if alias == "" {
		alias = store.SanitizeAlias(filepath.Base(repoPath))
	}
	if err := domain.ValidateName(alias); err != nil {
		alias = store.SanitizeAlias(alias)
	}
	if _, _, canonical, isGH := store.ParseGitHubRemote(remote); isGH {
		remote = canonical
	}
	if branch == "" {
		branch = "main"
	}

	updated := false
	for i := range project.Repositories {
		if project.Repositories[i].Alias == alias {
			if project.Repositories[i].Roots == nil {
				project.Repositories[i].Roots = make(map[string]string)
			}
			project.Repositories[i].Roots[env] = repoPath
			if remote != "" {
				project.Repositories[i].Remote = remote
			}
			if branch != "" {
				project.Repositories[i].DefaultBranch = branch
			}
			updated = true
			break
		}
	}
	if !updated {
		repoID, idErr := domain.NewID(domain.KindRepo)
		if idErr != nil {
			_ = registry.Close()
			return ui.ProjectStatus{}, idErr
		}
		project.Repositories = append(project.Repositories, domain.Repository{
			ID:            repoID,
			ProjectID:     project.ID,
			Alias:         alias,
			Remote:        remote,
			DefaultBranch: branch,
			Roots:         map[string]string{env: repoPath},
		})
	}
	if err := registry.Update(project); err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	_ = registry.Close()
	opts.project = project.Name
	return scanWorkspaceProject(opts, root, env)
}

func addAllWorkspaceProjectRepos(opts options, root, env string) (ui.ProjectStatus, int, error) {
	discovered, err := store.ScanGitRepositories(root)
	if err != nil {
		return ui.ProjectStatus{}, 0, err
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return ui.ProjectStatus{}, 0, err
	}
	project, err := ensureWorkspaceProject(registry, opts, root, env)
	if err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, 0, err
	}

	existingAlias := make(map[string]int, len(project.Repositories))
	existingPath := make(map[string]int, len(project.Repositories))
	for i, r := range project.Repositories {
		existingAlias[r.Alias] = i
		if p := r.Roots[env]; p != "" {
			existingPath[filepath.Clean(p)] = i
		}
	}

	added := 0
	for _, d := range discovered {
		remote := d.Remote
		if _, _, canonical, isGH := store.ParseGitHubRemote(remote); isGH {
			remote = canonical
		}
		branch := d.DefaultBranch
		if branch == "" {
			branch = "main"
		}
		if idx, ok := existingPath[filepath.Clean(d.Path)]; ok {
			if remote != "" && project.Repositories[idx].Remote == "" {
				project.Repositories[idx].Remote = remote
			}
			continue
		}
		if idx, ok := existingAlias[d.Alias]; ok {
			if project.Repositories[idx].Roots == nil {
				project.Repositories[idx].Roots = make(map[string]string)
			}
			project.Repositories[idx].Roots[env] = d.Path
			if remote != "" {
				project.Repositories[idx].Remote = remote
			}
			continue
		}
		repoID, idErr := domain.NewID(domain.KindRepo)
		if idErr != nil {
			_ = registry.Close()
			return ui.ProjectStatus{}, 0, idErr
		}
		project.Repositories = append(project.Repositories, domain.Repository{
			ID:            repoID,
			ProjectID:     project.ID,
			Alias:         d.Alias,
			Remote:        remote,
			DefaultBranch: branch,
			Roots:         map[string]string{env: d.Path},
		})
		added++
	}
	if err := registry.Update(project); err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, 0, err
	}
	_ = registry.Close()
	opts.project = project.Name
	st, err := scanWorkspaceProject(opts, root, env)
	return st, added, err
}

func removeWorkspaceProjectRepo(opts options, root, env, alias string) (ui.ProjectStatus, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ui.ProjectStatus{}, errors.New("repository alias is required")
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return ui.ProjectStatus{}, err
	}
	project, ok, err := resolveOrMatchWorkspaceProject(registry, opts, root, env)
	if err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	if !ok {
		_ = registry.Close()
		return ui.ProjectStatus{}, fmt.Errorf("no registered project found for %s", root)
	}
	found := false
	next := make([]domain.Repository, 0, len(project.Repositories))
	for _, r := range project.Repositories {
		if r.Alias == alias {
			found = true
			continue
		}
		next = append(next, r)
	}
	if !found {
		_ = registry.Close()
		return ui.ProjectStatus{}, fmt.Errorf("repository %q not found in project %q", alias, project.Name)
	}
	project.Repositories = next
	if err := registry.Update(project); err != nil {
		_ = registry.Close()
		return ui.ProjectStatus{}, err
	}
	_ = registry.Close()
	opts.project = project.Name
	return scanWorkspaceProject(opts, root, env)
}

func runProjectScan(opts options, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost project scan", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	scanRoot := opts.root
	projectName := opts.project
	addDiscovered := false
	fs.StringVar(&scanRoot, "root", opts.root, "directory to scan for Git/GitHub repositories")
	fs.StringVar(&projectName, "name", opts.project, "project name when adding repositories")
	fs.BoolVar(&addDiscovered, "add", false, "register all discovered repositories into the project")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: bifrost project scan [NAME] [--root PATH] [--add]")
	}
	if fs.NArg() == 1 && projectName == "" {
		projectName = fs.Arg(0)
	}
	if scanRoot == "" {
		resolved, err := resolveRoot(opts)
		if err != nil {
			return err
		}
		scanRoot = resolved
	}
	absRoot, err := absoluteDirectory(scanRoot)
	if err != nil {
		return fmt.Errorf("scan root: %w", err)
	}
	if projectName != "" {
		opts.project = projectName
	}
	env := opts.environment
	if env == "" {
		env = "local"
	}
	var st ui.ProjectStatus
	if addDiscovered {
		st, _, err = addAllWorkspaceProjectRepos(opts, absRoot, env)
	} else {
		st, err = scanWorkspaceProject(opts, absRoot, env)
	}
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(stdout, st)
	}
	for _, repo := range st.Repos {
		state := "discovered"
		if repo.Linked {
			state = "linked"
		}
		remote := repo.GitHubSlug
		if remote == "" {
			remote = repo.Remote
		}
		if remote == "" {
			remote = "-"
		}
		_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", state, repo.Alias, repo.RelPath, remote, repo.DefaultBranch)
	}
	return nil
}

func runProjectRemoveRepo(opts options, args []string, stdout io.Writer) error {
	args = reorderFlags(args)
	fs := flag.NewFlagSet("bifrost project remove-repo", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	alias := ""
	fs.StringVar(&alias, "alias", "", "repository alias to remove")
	if err := fs.Parse(args); err != nil {
		return err
	}
	pos := fs.Args()
	var projectName string
	switch {
	case len(pos) == 2 && alias == "":
		projectName, alias = pos[0], pos[1]
	case len(pos) == 1 && alias != "":
		projectName = pos[0]
	case len(pos) == 1 && opts.project != "" && alias == "":
		projectName, alias = opts.project, pos[0]
	default:
		return errors.New("usage: bifrost project remove-repo NAME ALIAS (or --alias ALIAS)")
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return err
	}
	defer registry.Close()
	project, err := registryProject(registry, projectName)
	if err != nil {
		return err
	}
	found := false
	next := make([]domain.Repository, 0, len(project.Repositories))
	for _, r := range project.Repositories {
		if r.Alias == alias {
			found = true
			continue
		}
		next = append(next, r)
	}
	if !found {
		return fmt.Errorf("repository alias %q not found in project %q", alias, project.Name)
	}
	project.Repositories = next
	if err := registry.Update(project); err != nil {
		return err
	}
	if opts.json {
		return writeJSON(stdout, project)
	}
	_, _ = fmt.Fprintf(stdout, "removed repository %s from %s\n", alias, project.Name)
	return nil
}

func fetchWorkspaceProjectDetails(ctx context.Context, opts options, root, env string) (ui.ProjectDetails, error) {
	st, err := scanWorkspaceProject(opts, root, env)
	if err != nil {
		return ui.ProjectDetails{}, err
	}
	var out ui.ProjectDetails
	out.GitHubTokenEnv = st.GitHubTokenEnv
	out.NotionTokenEnv = st.NotionTokenEnv
	out.NotionPage = st.NotionPage

	// 1. Read local README fallback first
	for _, candidate := range []string{"README.md", "README.txt", "README", "docs/README.md"} {
		full := filepath.Join(root, candidate)
		if body, ok := readBoundedUTF8File(full, 64<<10); ok {
			out.ReadmeTitle = candidate
			out.ReadmeContent = body
			break
		}
	}

	// 2. GitHub Connector
	ghClient, ghErr := github.FromEnvOrGHCLI(st.GitHubTokenEnv)
	if ghErr == nil && ghClient != nil {
		if auth, authErr := ghClient.AuthStatus(ctx); authErr == nil && auth.Authenticated {
			out.GitHubConnected = true
			out.GitHubUser = auth.Login
		}
		seenSlugs := make(map[string]bool)
		for _, repo := range st.Repos {
			slug := strings.TrimSpace(repo.GitHubSlug)
			if slug == "" && repo.Remote != "" {
				if o, r, _, ok := store.ParseGitHubRemote(repo.Remote); ok {
					slug = o + "/" + r
				}
			}
			if slug == "" || seenSlugs[slug] {
				continue
			}
			seenSlugs[slug] = true
			owner, name, slugErr := github.ParseSlug(slug)
			if slugErr != nil {
				continue
			}
			info := ui.ProjectGitHubRepoInfo{
				Alias:         repo.Alias,
				Slug:          slug,
				DefaultBranch: repo.DefaultBranch,
			}
			if ghRepo, err := ghClient.InspectRepository(ctx, owner, name); err == nil {
				info.Description = ghRepo.Description
				info.HTMLURL = ghRepo.HTMLURL
				info.Language = ghRepo.Language
				info.DefaultBranch = ghRepo.DefaultBranch
				info.Stars = ghRepo.StargazersCount
				info.Forks = ghRepo.ForksCount
				info.OpenIssues = ghRepo.OpenIssuesCount
				info.Private = ghRepo.Private
				info.Archived = ghRepo.Archived
				info.Topics = ghRepo.Topics
				info.UpdatedAt = ghRepo.UpdatedAt
			} else {
				info.Error = err.Error()
			}
			if readme, err := ghClient.GetReadme(ctx, owner, name); err == nil && strings.TrimSpace(readme) != "" {
				info.Readme = readme
				if out.ReadmeContent == "" {
					out.ReadmeTitle = slug + " / README.md"
					out.ReadmeContent = readme
				}
			}
			out.GitHubRepos = append(out.GitHubRepos, info)

			if prs, err := ghClient.ListPullRequests(ctx, owner, name, "open", 1, 20); err == nil {
				for _, pr := range prs {
					out.OpenPRs = append(out.OpenPRs, ui.ProjectGitHubPR{
						Number:    pr.Number,
						Title:     pr.Title,
						State:     pr.State,
						Draft:     pr.Draft,
						Author:    pr.User.Login,
						Head:      pr.Head.Ref,
						Base:      pr.Base.Ref,
						URL:       pr.HTMLURL,
						UpdatedAt: pr.UpdatedAt,
						RepoSlug:  slug,
					})
				}
			}
			if issues, err := ghClient.ListIssues(ctx, owner, name, "open", 1, 25); err == nil {
				for _, iss := range issues {
					var labels []string
					for _, l := range iss.Labels {
						if l.Name != "" {
							labels = append(labels, l.Name)
						}
					}
					out.OpenIssues = append(out.OpenIssues, ui.ProjectGitHubIssue{
						Number:    iss.Number,
						Title:     iss.Title,
						State:     iss.State,
						Author:    iss.User.Login,
						Comments:  iss.Comments,
						Labels:    labels,
						URL:       iss.HTMLURL,
						UpdatedAt: iss.UpdatedAt,
						RepoSlug:  slug,
					})
				}
			}
		}
	} else if ghErr != nil {
		out.GitHubError = ghErr.Error()
	}

	// 3. Notion Connector
	notionClient, nErr := notion.FromEnvName(st.NotionTokenEnv)
	if nErr == nil && notionClient != nil && notionClient.HasToken() {
		if auth, authErr := notionClient.AuthStatus(ctx); authErr == nil && auth.Authenticated {
			out.NotionConnected = true
			out.NotionBotName = auth.BotName
			out.NotionWorkspace = auth.WorkspaceName
		} else if authErr != nil {
			out.NotionError = authErr.Error()
		}
		query := strings.TrimSpace(st.NotionPage)
		if query == "" {
			query = st.ProjectName
		}
		if pages, sErr := notionClient.Search(ctx, query, 12); sErr == nil {
			for _, pg := range pages {
				out.NotionPages = append(out.NotionPages, ui.ProjectNotionItem{
					ID:             pg.ID,
					Object:         pg.Object,
					Title:          pg.Title,
					URL:            pg.URL,
					LastEditedTime: pg.LastEditedTime,
				})
			}
		}
		if st.NotionPage != "" {
			if body, pErr := notionClient.ReadPageText(ctx, st.NotionPage); pErr == nil && body != "" {
				out.NotionPageBody = body
			}
		}
	}

	return out, nil
}

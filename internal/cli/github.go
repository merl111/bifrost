package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	gh "bifrost/internal/github"
	"bifrost/internal/gitops"
)

// runGitHub implements only explicit GitHub inspection/linking/PR commands.
// It never pushes, creates remotes, or mutates a remote unless `pr create` is
// explicitly requested.
func runGitHub(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("github requires auth-status, link, repo, pr, or checks")
	}
	switch args[0] {
	case "auth-status":
		if len(args) != 1 {
			return errors.New("github auth-status takes no arguments")
		}
		client, err := gh.FromEnv()
		if err != nil {
			return err
		}
		status, err := client.AuthStatus(ctx)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, status)
		}
		if !status.Authenticated {
			_, err = fmt.Fprintln(out, "not authenticated")
			return err
		}
		_, err = fmt.Fprintf(out, "authenticated as %s\n", status.Login)
		return err
	case "link":
		return runGitHubLink(opts, args[1:], out)
	case "repo":
		return runGitHubRepo(ctx, opts, args[1:], out)
	case "pr":
		return runGitHubPR(ctx, opts, args[1:], out)
	case "checks":
		return runGitHubChecks(ctx, opts, args[1:], out)
	default:
		return fmt.Errorf("unknown github command %q", args[0])
	}
}

// runGit dispatches bounded local Git operations. Every mutating operation
// receives explicit paths or options; no subcommand exposes reset, force, or
// push.
func runGit(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("git requires status, diff, stage, unstage, commit, or worktree")
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return err
	}
	g, err := gitops.Open(root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("git status takes no arguments")
		}
		result, err := g.Status(ctx)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		for _, e := range result.Entries {
			if _, err := fmt.Fprintf(out, "%s %s\n", e.XY, e.Path); err != nil {
				return err
			}
		}
		return nil
	case "diff":
		fs := flag.NewFlagSet("git diff", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		cached := fs.Bool("cached", false, "show staged changes")
		stat := fs.Bool("stat", false, "show diffstat")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		paths, err := gitArgsPaths(fs.Args())
		if err != nil {
			return err
		}
		result, err := g.Diff(ctx, gitops.DiffOptions{Cached: *cached, Stat: *stat, Paths: paths})
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		_, err = io.WriteString(out, result.Text)
		return err
	case "stage", "unstage":
		paths, err := gitArgsPaths(args[1:])
		if err != nil {
			return err
		}
		var result gitops.CommandResult
		if args[0] == "stage" {
			result, err = g.Stage(ctx, paths)
		} else {
			result, err = g.Unstage(ctx, paths)
		}
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		return writeGitCommandOutput(out, result)
	case "commit":
		fs := flag.NewFlagSet("git commit", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		message := fs.String("message", "", "commit message")
		signoff := fs.Bool("signoff", false, "add signoff")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return errors.New("git commit requires --message and no paths")
		}
		result, err := g.Commit(ctx, gitops.CommitOptions{Message: *message, Signoff: *signoff})
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		return writeGitCommandOutput(out, result)
	case "worktree":
		return runGitWorktree(ctx, g, opts, args[1:], out)
	default:
		return fmt.Errorf("unknown git command %q", args[0])
	}
}

func runGitWorktree(ctx context.Context, g *gitops.Git, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("git worktree requires list, create, or remove")
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return errors.New("git worktree list takes no arguments")
		}
		items, err := g.WorktreeList(ctx)
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, items)
		}
		for _, item := range items {
			if _, err := fmt.Fprintf(out, "%s %s\n", item.Path, item.Branch); err != nil {
				return err
			}
		}
		return nil
	case "create":
		if len(args) != 3 {
			return errors.New("git worktree create requires PATH REF")
		}
		result, err := g.WorktreeCreate(ctx, args[1], args[2])
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		return writeGitCommandOutput(out, result)
	case "remove":
		if len(args) != 2 {
			return errors.New("git worktree remove requires PATH")
		}
		result, err := g.WorktreeRemove(ctx, args[1])
		if err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, result)
		}
		return writeGitCommandOutput(out, result)
	default:
		return fmt.Errorf("unknown git worktree command %q", args[0])
	}
}

func gitArgsPaths(args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, gitops.ErrInvalidPath
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return nil, gitops.ErrInvalidPath
		}
	}
	return append([]string(nil), args...), nil
}
func writeGitCommandOutput(out io.Writer, result gitops.CommandResult) error {
	if len(result.Stdout) > 0 {
		if _, err := out.Write(result.Stdout); err != nil {
			return err
		}
	}
	if len(result.Stdout) > 0 && !strings.HasSuffix(string(result.Stdout), "\n") {
		_, _ = io.WriteString(out, "\n")
	}
	return nil
}

func runGitHubLink(opts options, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("github link requires OWNER/REPO")
	}
	owner, repo, err := gh.ParseSlug(args[0])
	if err != nil {
		return err
	}
	if opts.project == "" {
		return errors.New("github link requires --project to select a registry project")
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return err
	}
	defer registry.Close()
	project, err := registryProject(registry, opts.project)
	if err != nil {
		return err
	}
	alias := opts.repository
	if alias == "" && len(project.Repositories) == 1 {
		alias = project.Repositories[0].Alias
	}
	if alias == "" {
		return errors.New("github link requires --repository when the project has multiple repositories")
	}
	for i := range project.Repositories {
		if project.Repositories[i].Alias != alias {
			continue
		}
		project.Repositories[i].Remote = githubRemoteURL(owner, repo)
		if err := registry.Update(project); err != nil {
			return err
		}
		if opts.json {
			return writeJSON(out, project.Repositories[i])
		}
		_, err := fmt.Fprintf(out, "linked %s to %s\n", alias, project.Repositories[i].Remote)
		return err
	}
	return fmt.Errorf("project %q has no repository %q", project.Name, alias)
}

func githubRemoteURL(owner, repo string) string {
	host := strings.TrimSpace(os.Getenv("GITHUB_HOST"))
	if host == "" {
		host = "github.com"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")
	return "https://" + host + "/" + owner + "/" + repo + ".git"
}

func githubClient() (*gh.Client, error) { return gh.FromEnv() }

func runGitHubRepo(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) != 2 || args[0] != "inspect" {
		return errors.New("github repo inspect requires OWNER/REPO")
	}
	owner, repo, err := gh.ParseSlug(args[1])
	if err != nil {
		return err
	}
	client, err := githubClient()
	if err != nil {
		return err
	}
	result, err := client.InspectRepository(ctx, owner, repo)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, result)
	}
	_, err = fmt.Fprintf(out, "%s default=%s private=%t\n", result.FullName, result.DefaultBranch, result.Private)
	return err
}

func runGitHubPR(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("github pr requires list or create")
	}
	switch args[0] {
	case "list":
		return runGitHubPRList(ctx, opts, args[1:], out)
	case "create":
		return runGitHubPRCreate(ctx, opts, args[1:], out)
	default:
		return fmt.Errorf("unknown github pr command %q", args[0])
	}
}

func runGitHubPRList(ctx context.Context, opts options, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("github pr list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	state := fs.String("state", "open", "pull request state")
	page := fs.Int("page", 1, "page number")
	perPage := fs.Int("per-page", 30, "results per page")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errors.New("github pr list requires OWNER/REPO")
	}
	owner, repo, err := gh.ParseSlug(fs.Arg(0))
	if err != nil {
		return err
	}
	client, err := githubClient()
	if err != nil {
		return err
	}
	prs, err := client.ListPullRequests(ctx, owner, repo, *state, *page, *perPage)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, prs)
	}
	for _, pr := range prs {
		if _, err := fmt.Fprintf(out, "#%d %s draft=%t %s\n", pr.Number, pr.Title, pr.Draft, pr.HTMLURL); err != nil {
			return err
		}
	}
	return nil
}

func runGitHubPRCreate(ctx context.Context, opts options, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("github pr create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	title := fs.String("title", "", "pull request title")
	body := fs.String("body", "", "pull request body")
	head := fs.String("head", "", "head branch")
	base := fs.String("base", "", "base branch")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		return errors.New("github pr create requires OWNER/REPO and --title/--head/--base")
	}
	owner, repo, err := gh.ParseSlug(fs.Arg(0))
	if err != nil {
		return err
	}
	client, err := githubClient()
	if err != nil {
		return err
	}
	pr, err := client.CreateDraftPR(ctx, gh.DraftPRRequest{Owner: owner, Repo: repo, Title: *title, Body: *body, Head: *head, Base: *base})
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, pr)
	}
	_, err = fmt.Fprintf(out, "created draft #%d %s\n", pr.Number, pr.HTMLURL)
	return err
}

func runGitHubChecks(ctx context.Context, opts options, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("github checks", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	page := fs.Int("page", 1, "page number")
	perPage := fs.Int("per-page", 30, "results per page")
	if err := fs.Parse(args); err != nil || fs.NArg() != 2 {
		return errors.New("github checks requires OWNER/REPO REF")
	}
	owner, repo, err := gh.ParseSlug(fs.Arg(0))
	if err != nil {
		return err
	}
	client, err := githubClient()
	if err != nil {
		return err
	}
	checks, err := client.ListChecks(ctx, owner, repo, fs.Arg(1), *page, *perPage)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, checks)
	}
	for _, run := range checks.Runs {
		if _, err := fmt.Fprintf(out, "%s %s/%s %s\n", run.Name, run.Status, run.Conclusion, run.HTMLURL); err != nil {
			return err
		}
	}
	return nil
}

package store

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bifrost/internal/domain"
)

// DiscoveredRepository describes a Git repository found inside a workspace root.
type DiscoveredRepository struct {
	Alias         string `json:"alias"`
	Path          string `json:"path"`
	RelPath       string `json:"rel_path"`
	Remote        string `json:"remote,omitempty"`
	GitHubSlug    string `json:"github_slug,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
	CurrentBranch string `json:"current_branch,omitempty"`
	GitAdded      int    `json:"git_added"`
	GitModified   int    `json:"git_modified"`
	GitDeleted    int    `json:"git_deleted"`
}

var githubSlugPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseGitHubRemote extracts OWNER/REPO and a normalized HTTPS remote URL from
// a GitHub remote string (HTTPS, HTTP, SCP-style git@github.com:owner/repo.git,
// ssh://git@github.com/owner/repo.git, or a bare OWNER/REPO slug).
func ParseGitHubRemote(remote string) (owner, repo, canonicalURL string, ok bool) {
	raw := strings.TrimSpace(remote)
	if raw == "" {
		return "", "", "", false
	}
	host := strings.TrimSpace(os.Getenv("GITHUB_HOST"))
	if host == "" {
		host = "github.com"
	}
	host = strings.TrimPrefix(strings.TrimPrefix(host, "https://"), "http://")
	host = strings.TrimSuffix(host, "/")

	// Bare OWNER/REPO slug.
	if !strings.Contains(raw, "://") && !strings.Contains(raw, "@") && strings.Count(raw, "/") == 1 && !strings.HasPrefix(raw, ".") && !strings.HasPrefix(raw, "/") {
		parts := strings.SplitN(raw, "/", 2)
		o, r := parts[0], strings.TrimSuffix(parts[1], ".git")
		if validGitHubSlug(o, r) {
			return o, r, fmt.Sprintf("https://%s/%s/%s.git", host, o, r), true
		}
	}

	// SCP-style SSH URL: git@github.com:owner/repo.git
	if !strings.Contains(raw, "://") {
		if at := strings.IndexByte(raw, '@'); at >= 0 {
			rest := raw[at+1:]
			if colon := strings.IndexByte(rest, ':'); colon > 0 {
				h := rest[:colon]
				pathPart := strings.TrimPrefix(rest[colon+1:], "/")
				if isGitHubHost(h, host) {
					o, r, valid := splitOwnerRepoPath(pathPart)
					if valid {
						return o, r, fmt.Sprintf("https://%s/%s/%s.git", h, o, r), true
					}
				}
			}
		}
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", "", false
	}
	h := u.Hostname()
	if !isGitHubHost(h, host) {
		return "", "", "", false
	}
	o, r, valid := splitOwnerRepoPath(strings.TrimPrefix(u.Path, "/"))
	if !valid {
		return "", "", "", false
	}
	return o, r, fmt.Sprintf("https://%s/%s/%s.git", h, o, r), true
}

func isGitHubHost(candidate, configuredHost string) bool {
	c := strings.ToLower(strings.TrimSpace(candidate))
	cfg := strings.ToLower(strings.TrimSpace(configuredHost))
	return c == "github.com" || c == "www.github.com" || (cfg != "" && c == cfg)
}

func splitOwnerRepoPath(pathPart string) (owner, repo string, ok bool) {
	trimmed := strings.Trim(pathPart, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner = parts[0]
	repo = strings.TrimSuffix(parts[1], ".git")
	if !validGitHubSlug(owner, repo) {
		return "", "", false
	}
	return owner, repo, true
}

func validGitHubSlug(owner, repo string) bool {
	if owner == "" || repo == "" || owner == "." || owner == ".." || repo == "." || repo == ".." {
		return false
	}
	return githubSlugPart.MatchString(owner) && githubSlugPart.MatchString(repo)
}

// SanitizeAlias converts an arbitrary directory or repository name into a valid
// domain name identifier.
func SanitizeAlias(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "repo"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-':
			if b.Len() > 0 {
				b.WriteRune(r)
			}
		default:
			if b.Len() > 0 {
				b.WriteByte('-')
			}
		}
		if b.Len() >= 64 {
			break
		}
	}
	out := strings.TrimRight(b.String(), "._-")
	if out == "" || domain.ValidateName(out) != nil {
		return "repo"
	}
	return out
}

// ScanGitRepositories discovers Git repositories at root and up to 2 directory
// levels below root. It reads local .git metadata without spawning processes.
func ScanGitRepositories(root string) ([]DiscoveredRepository, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(absRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scan root %q is not a directory", absRoot)
	}

	var candidates []string
	if hasGitMarker(absRoot) {
		candidates = append(candidates, absRoot)
	}
	scanSubdirs(absRoot, 0, 2, &candidates)
	sort.Strings(candidates)

	usedAliases := make(map[string]bool, len(candidates))
	discovered := make([]DiscoveredRepository, 0, len(candidates))
	for _, dir := range candidates {
		item, ok := inspectGitDirectory(absRoot, dir)
		if !ok {
			continue
		}
		baseAlias := item.Alias
		alias := baseAlias
		for seq := 2; usedAliases[alias]; seq++ {
			alias = fmt.Sprintf("%s-%d", baseAlias, seq)
		}
		usedAliases[alias] = true
		item.Alias = alias
		discovered = append(discovered, item)
	}
	return discovered, nil
}

func scanSubdirs(dir string, depth, maxDepth int, out *[]string) {
	if depth >= maxDepth {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if shouldSkipScanDir(name) {
			continue
		}
		child := filepath.Join(dir, name)
		if hasGitMarker(child) {
			*out = append(*out, child)
			// Once a Git repository is found, do not recurse into its subdirectories
			// unless they are separate nested checkouts.
			continue
		}
		scanSubdirs(child, depth+1, maxDepth, out)
	}
}

func shouldSkipScanDir(name string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}
	switch name {
	case "node_modules", "vendor", "dist", "build", "target", "__pycache__", "venv":
		return true
	default:
		return false
	}
}

func hasGitMarker(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func inspectGitDirectory(root, dir string) (DiscoveredRepository, bool) {
	gitDir, ok := resolveGitDir(dir)
	if !ok {
		return DiscoveredRepository{}, false
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "" {
		rel = "."
	}
	rel = filepath.ToSlash(rel)

	remote := readGitRemote(gitDir)
	branch := readGitDefaultBranch(gitDir)
	currentBranch := readGitCurrentBranch(gitDir, branch)
	added, modified, deleted := InspectGitWorktreeCounts(dir)
	var slug string
	if owner, repo, _, isGH := ParseGitHubRemote(remote); isGH {
		slug = owner + "/" + repo
	}

	aliasSource := filepath.Base(dir)
	if rel != "." && strings.Contains(rel, "/") {
		aliasSource = strings.ReplaceAll(rel, "/", "-")
	}
	alias := SanitizeAlias(aliasSource)

	return DiscoveredRepository{
		Alias:         alias,
		Path:          dir,
		RelPath:       rel,
		Remote:        remote,
		GitHubSlug:    slug,
		DefaultBranch: branch,
		CurrentBranch: currentBranch,
		GitAdded:      added,
		GitModified:   modified,
		GitDeleted:    deleted,
	}, true
}

func resolveGitDir(dir string) (string, bool) {
	dotGit := filepath.Join(dir, ".git")
	st, err := os.Stat(dotGit)
	if err != nil {
		return "", false
	}
	if st.IsDir() {
		return dotGit, true
	}
	// Worktree or submodule .git file: "gitdir: <path>"
	data, err := os.ReadFile(dotGit)
	if err != nil {
		return "", false
	}
	line := strings.TrimSpace(string(data))
	if !strings.HasPrefix(line, "gitdir:") {
		return "", false
	}
	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	target = filepath.Clean(target)
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		return target, true
	}
	return "", false
}

func readGitRemote(gitDir string) string {
	cfgPath := filepath.Join(gitDir, "config")
	f, err := os.Open(cfgPath)
	if err != nil {
		if common, ok := resolveCommonGitDir(gitDir); ok {
			f, err = os.Open(filepath.Join(common, "config"))
		}
		if err != nil {
			return ""
		}
	}
	defer f.Close()

	var currentRemote string
	var originURL, firstURL string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentRemote = ""
			section := strings.TrimSpace(line[1 : len(line)-1])
			if strings.HasPrefix(strings.ToLower(section), "remote ") {
				rest := strings.TrimSpace(section[len("remote "):])
				rest = strings.Trim(rest, `"`)
				currentRemote = rest
			}
			continue
		}
		if currentRemote == "" {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if key == "url" && val != "" {
			if currentRemote == "origin" {
				originURL = val
			} else if firstURL == "" {
				firstURL = val
			}
		}
	}
	if originURL != "" {
		return originURL
	}
	return firstURL
}

func resolveCommonGitDir(gitDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	rel := strings.TrimSpace(string(data))
	if rel == "" {
		return "", false
	}
	if !filepath.IsAbs(rel) {
		rel = filepath.Join(gitDir, rel)
	}
	return filepath.Clean(rel), true
}

func readGitDefaultBranch(gitDir string) string {
	// Check origin HEAD first for the remote default branch.
	for _, candidate := range []string{
		filepath.Join(gitDir, "refs", "remotes", "origin", "HEAD"),
		filepath.Join(gitDir, "HEAD"),
	} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		line := strings.TrimSpace(string(data))
		if strings.HasPrefix(line, "ref:") {
			ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
			ref = strings.TrimPrefix(ref, "refs/heads/")
			ref = strings.TrimPrefix(ref, "refs/remotes/origin/")
			if ref != "" && ref != "HEAD" {
				return ref
			}
		}
	}
	return "main"
}

func readGitCurrentBranch(gitDir, fallback string) string {
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return fallback
	}
	line := strings.TrimSpace(string(data))
	if strings.HasPrefix(line, "ref:") {
		ref := strings.TrimSpace(strings.TrimPrefix(line, "ref:"))
		ref = strings.TrimPrefix(ref, "refs/heads/")
		if ref != "" && ref != "HEAD" {
			return ref
		}
	}
	if len(line) >= 7 && !strings.Contains(line, " ") {
		return line[:7]
	}
	return fallback
}

// InspectGitWorktreeCounts runs a bounded, read-only `git status --porcelain=v1 -z`
// to count added (including untracked), modified, and deleted files in dir.
func InspectGitWorktreeCounts(dir string) (added, modified, deleted int) {
	if dir == "" {
		return 0, 0, 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "--no-optional-locks", "status", "--porcelain=v1", "-z", "-uall")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return 0, 0, 0
	}
	raw := out.Bytes()
	for len(raw) > 0 {
		nul := bytes.IndexByte(raw, 0)
		var token []byte
		if nul < 0 {
			token = raw
			raw = nil
		} else {
			token = raw[:nul]
			raw = raw[nul+1:]
		}
		if len(token) < 3 {
			continue
		}
		x, y := token[0], token[1]
		// Rename/copy in porcelain v1 -z consumes an extra NUL-terminated path.
		if (x == 'R' || x == 'C' || y == 'R' || y == 'C') && len(raw) > 0 {
			if nextNul := bytes.IndexByte(raw, 0); nextNul >= 0 {
				raw = raw[nextNul+1:]
			} else {
				raw = nil
			}
		}
		switch {
		case x == '?' || y == '?' || x == 'A' || y == 'A':
			added++
		case x == 'D' || y == 'D':
			deleted++
		case x != ' ' || y != ' ':
			modified++
		}
	}
	return added, modified, deleted
}

// Package marketplace provides a small, inert catalog for explicitly selected
// skill installations. Catalog data is metadata only; installing an entry is
// always an explicit operation by the caller.
package marketplace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"bifrost/internal/skill"
)

const (
	OfficialRepo          = "https://github.com/openai/skills"
	OfficialRef           = "49f948faa9258a0c61caceaf225e179651397431"
	AnthropicOfficialRepo = "https://github.com/anthropics/skills"
	AnthropicOfficialRef  = "main"
	SkillsMPDefaultAPI    = "https://skillsmp.com/api/v1/skills/search"
	maxCatalog            = 2 << 20
)

// Entry is one installable skill. Ref should be a full immutable commit for
// catalogs that intend to provide reproducible installs.
type Entry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Repo        string `json:"repo"`
	Ref         string `json:"ref"`
	Path        string `json:"path"`
	SourceName  string `json:"source_name,omitempty"`
}

// Catalog is the JSON document accepted from a configured catalog URL.
type Catalog struct {
	Entries []Entry `json:"entries"`
}

type Source struct {
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	Trusted bool   `json:"trusted"`
	Count   int    `json:"count"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9]+(?:[-_.][a-z0-9]+)*$`)
var refPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,255}$`)

// Builtin returns the curated set of OpenAI and Anthropic skills shipped with
// Bifrost so the marketplace is immediately useful offline and out of the box.
func Builtin() []Entry {
	return []Entry{
		// OpenAI official curated & system skills (github.com/openai/skills)
		{ID: "openai-cli-creator", Name: "CLI Creator", Description: "Create robust command-line interfaces, flag parsers, subcommands, and ergonomic terminal tools.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/cli-creator", SourceName: "OpenAI curated"},
		{ID: "openai-define-goal", Name: "Define Goal", Description: "Turn an ambiguous request into a concrete, testable engineering goal with clear acceptance criteria.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/define-goal", SourceName: "OpenAI curated"},
		{ID: "openai-skill-creator", Name: "Skill Creator", Description: "Create, structure, and update effective Agent Skills with SKILL.md metadata and instructions.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.system/skill-creator", SourceName: "OpenAI curated"},
		{ID: "openai-skill-installer", Name: "Skill Installer", Description: "Discover, verify, and install curated agent skills from Git repositories.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.system/skill-installer", SourceName: "OpenAI curated"},
		{ID: "openai-gh-fix-ci", Name: "GitHub Fix CI", Description: "Inspect failing GitHub Actions checks, diagnose root causes from logs, and apply targeted fixes.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/gh-fix-ci", SourceName: "OpenAI curated"},
		{ID: "openai-gh-address-comments", Name: "GitHub Address PR Comments", Description: "Review pull request review threads systematically and implement requested code changes.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/gh-address-comments", SourceName: "OpenAI curated"},
		{ID: "openai-playwright", Name: "Playwright Browser Automation", Description: "Automate browser testing, end-to-end verification, and DOM inspection with Playwright.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/playwright", SourceName: "OpenAI curated"},
		{ID: "openai-security-best-practices", Name: "Security Best Practices", Description: "Audit code for injection, auth flaws, secret exposure, and language-specific security vulnerabilities.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/security-best-practices", SourceName: "OpenAI curated"},
		{ID: "openai-security-threat-model", Name: "Security Threat Model", Description: "Build structured threat models covering trust boundaries, attack surfaces, and mitigations.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/security-threat-model", SourceName: "OpenAI curated"},
		{ID: "openai-pdf", Name: "PDF Processing & Generation", Description: "Extract text/tables from PDFs, merge/split documents, and generate formatted PDF reports.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/pdf", SourceName: "OpenAI curated"},
		{ID: "openai-spreadsheet", Name: "Spreadsheet & Data Analysis", Description: "Build, inspect, and transform Excel/CSV spreadsheets with formulas and structured validation.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/spreadsheet", SourceName: "OpenAI curated"},
		{ID: "openai-doc", Name: "Word & Technical Docs", Description: "Create and edit structured technical documents, specifications, and DOCX files.", Repo: OfficialRepo, Ref: OfficialRef, Path: "skills/.curated/doc", SourceName: "OpenAI curated"},
		// Anthropic official skills (github.com/anthropics/skills)
		{ID: "anthropic-mcp-builder", Name: "MCP Server Builder", Description: "Design and implement Model Context Protocol (MCP) servers with typed tools, resources, and prompts.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/mcp-builder", SourceName: "Anthropic skills"},
		{ID: "anthropic-frontend-design", Name: "Frontend & UI Design", Description: "Build distinctive, polished interfaces with strong typography, color hierarchy, and responsive layout.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/frontend-design", SourceName: "Anthropic skills"},
		{ID: "anthropic-webapp-testing", Name: "Web Application Testing", Description: "Verify web applications with automated browser assertions, screenshots, and console diagnostics.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/webapp-testing", SourceName: "Anthropic skills"},
		{ID: "anthropic-doc-coauthoring", Name: "Technical Doc Co-Authoring", Description: "Collaboratively draft RFCs, architecture docs, decision records, and engineering specs.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/doc-coauthoring", SourceName: "Anthropic skills"},
		{ID: "anthropic-pdf", Name: "Anthropic PDF Toolkit", Description: "Read, annotate, fill forms, and extract structured layout information from PDF files.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/pdf", SourceName: "Anthropic skills"},
		{ID: "anthropic-xlsx", Name: "Anthropic XLSX Toolkit", Description: "Create financial models, data tables, and formula-driven spreadsheets in XLSX format.", Repo: AnthropicOfficialRepo, Ref: AnthropicOfficialRef, Path: "skills/xlsx", SourceName: "Anthropic skills"},
	}
}

type Config struct {
	CatalogURL string
	HTTPClient *http.Client
	// AllowLocal permits absolute/file repository paths in a catalog served by
	// a caller-controlled local fixture. It is false for normal HTTPS catalogs.
	AllowLocal bool
}

type Client struct {
	config Config
	client *http.Client
	mu     sync.Mutex
	cached map[string]Entry
}

func New(cfg Config) (*Client, error) {
	if cfg.CatalogURL != "" {
		if err := ValidateCatalogURL(cfg.CatalogURL); err != nil {
			return nil, err
		}
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		// Do not trust a caller-provided client to follow catalog redirects or
		// have an unbounded request lifetime.
		copy := *hc
		hc = &copy
		hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
		if hc.Timeout <= 0 {
			hc.Timeout = 20 * time.Second
		}
	}
	return &Client{config: cfg, client: hc, cached: make(map[string]Entry)}, nil
}

func ValidateCatalogURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("catalog URL must be an absolute HTTPS URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && isLoopback(u.Hostname()) {
		return nil
	}
	return errors.New("catalog URL must use HTTPS (HTTP is allowed only for loopback tests)")
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) Entries(ctx context.Context) ([]Entry, error) {
	return c.entriesForQuery(ctx, "")
}

func (c *Client) entriesForQuery(ctx context.Context, query string) ([]Entry, error) {
	if c == nil {
		return nil, errors.New("marketplace client is nil")
	}
	entries := append([]Entry(nil), Builtin()...)
	if c.config.CatalogURL != "" {
		reqURL := c.catalogRequestURL(c.config.CatalogURL, query)
		v, err := c.fetch(ctx, reqURL)
		if err != nil {
			return nil, err
		}
		entries = append(entries, v...)
	} else if apiEnv := strings.TrimSpace(os.Getenv("BIFROST_MARKETPLACE_API_URL")); apiEnv != "" {
		if ValidateCatalogURL(apiEnv) == nil {
			reqURL := c.catalogRequestURL(apiEnv, query)
			if v, err := c.fetch(ctx, reqURL); err == nil {
				entries = append(entries, v...)
			}
		}
	}
	seen := make(map[string]bool, len(entries))
	out := entries[:0]
	c.mu.Lock()
	if c.cached == nil {
		c.cached = make(map[string]Entry)
	}
	for _, e := range entries {
		if err := validateEntry(e, c.config.AllowLocal && isCatalogLocal(c.config.CatalogURL)); err != nil {
			c.mu.Unlock()
			return nil, err
		}
		c.cached[e.ID] = e
		if seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	c.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (c *Client) catalogRequestURL(rawURL, query string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	isSearchAPI := strings.Contains(strings.ToLower(u.Path), "/search") || strings.Contains(strings.ToLower(u.Host), "skillsmp.com")
	if !isSearchAPI {
		return rawURL
	}
	q := u.Query()
	trimmed := strings.TrimSpace(query)
	if trimmed != "" {
		q.Set("q", trimmed)
	} else if q.Get("q") == "" && strings.Contains(strings.ToLower(u.Host), "skillsmp.com") {
		q.Set("q", "agent")
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *Client) List(ctx context.Context, query string) ([]Entry, error) {
	all, err := c.entriesForQuery(ctx, query)
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return all, nil
	}
	out := make([]Entry, 0)
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.ID), query) || strings.Contains(strings.ToLower(e.Name), query) || strings.Contains(strings.ToLower(e.Description), query) || strings.Contains(strings.ToLower(e.SourceName), query) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (c *Client) Get(ctx context.Context, id string) (Entry, error) {
	if !idPattern.MatchString(id) {
		return Entry{}, errors.New("invalid marketplace skill ID")
	}
	if c != nil {
		c.mu.Lock()
		if cached, ok := c.cached[id]; ok {
			c.mu.Unlock()
			return cached, nil
		}
		c.mu.Unlock()
	}
	all, err := c.Entries(ctx)
	if err != nil {
		return Entry{}, err
	}
	for _, e := range all {
		if e.ID == id {
			return e, nil
		}
	}
	return Entry{}, fmt.Errorf("marketplace skill %q not found", id)
}

func (c *Client) Sources(ctx context.Context) ([]Source, error) {
	openAICount, anthropicCount := 0, 0
	for _, b := range Builtin() {
		if strings.HasPrefix(b.ID, "anthropic-") {
			anthropicCount++
		} else {
			openAICount++
		}
	}
	result := []Source{
		{Name: "OpenAI curated", URL: OfficialRepo, Trusted: true, Count: openAICount},
		{Name: "Anthropic skills", URL: AnthropicOfficialRepo, Trusted: true, Count: anthropicCount},
	}
	if c != nil && c.config.CatalogURL != "" {
		v, err := c.fetch(ctx, c.catalogRequestURL(c.config.CatalogURL, ""))
		if err != nil {
			return nil, err
		}
		result = append(result, Source{Name: "configured catalog", URL: c.config.CatalogURL, Count: len(v)})
	}
	return result, nil
}

func (c *Client) fetch(ctx context.Context, raw string) ([]Entry, error) {
	if err := ValidateCatalogURL(raw); err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, fmt.Errorf("catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if u, uErr := url.Parse(raw); uErr == nil && strings.Contains(strings.ToLower(u.Host), "skillsmp.com") {
		if token := strings.TrimSpace(os.Getenv("SKILLSMP_API_KEY")); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if retryAfter != "" {
			return nil, fmt.Errorf("catalog rate limited (HTTP 429, Retry-After: %s)", retryAfter)
		}
		return nil, errors.New("catalog rate limited (HTTP 429)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("catalog returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalog+1))
	if err != nil {
		return nil, fmt.Errorf("read catalog: %w", err)
	}
	if len(b) > maxCatalog {
		return nil, errors.New("catalog exceeds size limit")
	}
	entries, err := decodeCatalogResponse(b, raw)
	if err != nil {
		return nil, err
	}
	if len(entries) > 1000 {
		return nil, errors.New("catalog contains too many entries")
	}
	allowLocal := c.config.AllowLocal && isCatalogLocal(raw)
	for _, e := range entries {
		if err := validateEntry(e, allowLocal); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// NormalizeID maps upstream skill identifiers (which may contain slashes,
// spaces, or uppercase characters) into a safe lowercase Bifrost skill ID.
func NormalizeID(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastSep := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastSep = false
		case r == '-' || r == '_' || r == '.':
			if b.Len() > 0 && !lastSep {
				b.WriteRune(r)
				lastSep = true
			}
		case r == '/' || r == ':' || r == ' ':
			if b.Len() > 0 && !lastSep {
				b.WriteByte('-')
				lastSep = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-_.")
	if len(out) > 120 {
		out = strings.TrimRight(out[:120], "-_.")
	}
	return out
}

type skillsMPItem struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Summary     string `json:"summary"`
	Repo        string `json:"repo"`
	Repository  string `json:"repository"`
	GitHubURL   string `json:"github_url"`
	RepoURL     string `json:"repo_url"`
	Ref         string `json:"ref"`
	Branch      string `json:"branch"`
	Commit      string `json:"commit"`
	Path        string `json:"path"`
	SkillPath   string `json:"skill_path"`
	Directory   string `json:"directory"`
	Author      string `json:"author"`
}

func decodeCatalogResponse(b []byte, rawURL string) ([]Entry, error) {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(b, &rawMap); err != nil {
		return nil, fmt.Errorf("decode catalog: %w", err)
	}
	// 1. Native Bifrost {"entries": [...]} schema
	if _, hasEntries := rawMap["entries"]; hasEntries {
		var doc Catalog
		if err := json.Unmarshal(b, &doc); err != nil {
			return nil, fmt.Errorf("decode catalog: %w", err)
		}
		return doc.Entries, nil
	}
	// 2. GitHub Git Trees API {"sha": "...", "tree": [{"path": "skills/foo/SKILL.md"}]}
	if treeRaw, hasTree := rawMap["tree"]; hasTree {
		return decodeGitHubTreeCatalog(rawMap["sha"], treeRaw, rawURL)
	}
	// 3. SkillsMP / skills.sh API {"skills": [...]} or {"data": {"skills": [...]}} or {"items": [...]} or {"results": [...]}
	var itemsRaw json.RawMessage
	for _, key := range []string{"skills", "items", "results"} {
		if v, ok := rawMap[key]; ok {
			itemsRaw = v
			break
		}
	}
	if len(itemsRaw) == 0 {
		if dataRaw, ok := rawMap["data"]; ok {
			var dataMap map[string]json.RawMessage
			if json.Unmarshal(dataRaw, &dataMap) == nil {
				for _, key := range []string{"skills", "items", "results"} {
					if v, ok := dataMap[key]; ok {
						itemsRaw = v
						break
					}
				}
			}
		}
	}
	if len(itemsRaw) > 0 {
		var items []skillsMPItem
		if err := json.Unmarshal(itemsRaw, &items); err != nil {
			return nil, fmt.Errorf("decode skills API items: %w", err)
		}
		out := make([]Entry, 0, len(items))
		for _, item := range items {
			if e, ok := normalizeSkillsMPItem(item); ok {
				out = append(out, e)
			}
		}
		return out, nil
	}
	return nil, errors.New("decode catalog: unrecognized catalog JSON schema")
}

func normalizeSkillsMPItem(item skillsMPItem) (Entry, bool) {
	rawID := firstNonEmpty(item.ID, item.Slug, item.Name)
	id := NormalizeID(rawID)
	if id == "" || !idPattern.MatchString(id) {
		return Entry{}, false
	}
	name := strings.TrimSpace(firstNonEmpty(item.Name, item.Title, rawID))
	if len(name) > 128 {
		name = name[:128]
	}
	desc := strings.TrimSpace(firstNonEmpty(item.Description, item.Summary))
	if len(desc) > 4096 {
		desc = desc[:4096]
	}
	repo := strings.TrimSpace(firstNonEmpty(item.Repo, item.Repository, item.GitHubURL, item.RepoURL))
	if repo != "" && !strings.Contains(repo, "://") && !strings.HasPrefix(repo, "/") && strings.Count(repo, "/") == 1 {
		repo = "https://github.com/" + repo
	}
	skillPath := strings.Trim(strings.ReplaceAll(firstNonEmpty(item.Path, item.SkillPath, item.Directory), "\\", "/"), "/")
	if strings.HasSuffix(skillPath, "/SKILL.md") {
		skillPath = strings.TrimSuffix(skillPath, "/SKILL.md")
	} else if skillPath == "SKILL.md" {
		skillPath = ""
	}
	// Handle GitHub blob/tree URLs in repo field (e.g. https://github.com/owner/repo/tree/main/skills/foo)
	if u, err := url.Parse(repo); err == nil && strings.EqualFold(u.Host, "github.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 4 && (parts[2] == "tree" || parts[2] == "blob") {
			repo = "https://github.com/" + parts[0] + "/" + parts[1]
			if item.Ref == "" && item.Commit == "" && item.Branch == "" {
				item.Ref = parts[3]
			}
			if skillPath == "" && len(parts) > 4 {
				sub := strings.Join(parts[4:], "/")
				sub = strings.TrimSuffix(sub, "/SKILL.md")
				skillPath = sub
			}
		}
	}
	if repo == "" {
		return Entry{}, false
	}
	ref := strings.TrimSpace(firstNonEmpty(item.Commit, item.Ref, item.Branch, "main"))
	return Entry{
		ID:          id,
		Name:        name,
		Description: desc,
		Repo:        repo,
		Ref:         ref,
		Path:        skillPath,
		SourceName:  firstNonEmpty(item.Author, "SkillsMP"),
	}, true
}

func decodeGitHubTreeCatalog(shaRaw, treeRaw json.RawMessage, rawURL string) ([]Entry, error) {
	var sha string
	_ = json.Unmarshal(shaRaw, &sha)
	if sha == "" {
		sha = "main"
	}
	var nodes []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal(treeRaw, &nodes); err != nil {
		return nil, fmt.Errorf("decode github tree: %w", err)
	}
	repo := OfficialRepo
	if u, err := url.Parse(rawURL); err == nil {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "repos" {
			repo = "https://github.com/" + parts[1] + "/" + parts[2]
		}
	}
	var out []Entry
	for _, n := range nodes {
		if !strings.HasSuffix(n.Path, "/SKILL.md") {
			continue
		}
		dir := strings.TrimSuffix(n.Path, "/SKILL.md")
		base := path.Base(dir)
		id := NormalizeID(base)
		if id == "" || !idPattern.MatchString(id) {
			continue
		}
		out = append(out, Entry{
			ID:          id,
			Name:        base,
			Description: fmt.Sprintf("Skill discovered from %s (%s)", repo, dir),
			Repo:        repo,
			Ref:         sha,
			Path:        dir,
			SourceName:  "GitHub tree",
		})
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ValidateEntry rejects ambiguous or path-escaping catalog records before a
// caller can pass one to Manager.InstallGit.
func ValidateEntry(e Entry) error {
	return validateEntry(e, false)
}

func validateEntry(e Entry, allowLocal bool) error {
	if !idPattern.MatchString(e.ID) || len(e.ID) > 128 {
		return fmt.Errorf("invalid marketplace skill ID %q", e.ID)
	}
	if strings.TrimSpace(e.Name) == "" || len(e.Name) > 128 {
		return fmt.Errorf("invalid marketplace skill name %q", e.ID)
	}
	if len(e.Description) > 4096 {
		return fmt.Errorf("marketplace description too long for %q", e.ID)
	}
	if err := validateRepo(e.Repo, allowLocal); err != nil {
		return fmt.Errorf("marketplace %q repository: %w", e.ID, err)
	}
	if !refPattern.MatchString(e.Ref) || strings.Contains(e.Ref, "..") || strings.HasPrefix(e.Ref, "-") {
		return fmt.Errorf("invalid marketplace ref for %q", e.ID)
	}
	if e.Path != "" {
		clean := path.Clean(strings.ReplaceAll(e.Path, "\\", "/"))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") || clean != strings.ReplaceAll(e.Path, "\\", "/") {
			return fmt.Errorf("invalid marketplace path for %q", e.ID)
		}
	}
	return nil
}

func validateRepo(raw string, allowLocal bool) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("repository is empty")
	}
	if allowLocal && strings.HasPrefix(raw, "/") {
		return nil
	} // useful for explicit local test fixtures
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" {
		return errors.New("repository URL is invalid")
	}
	if u.Scheme == "https" && u.Host != "" {
		return nil
	}
	if u.Scheme == "http" && isLoopback(u.Hostname()) && u.Host != "" {
		return nil
	}
	if allowLocal && u.Scheme == "file" && u.Host == "" {
		return nil
	}
	return errors.New("repository must use HTTPS (or an explicit local fixture)")
}

func isCatalogLocal(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "http" && isLoopback(u.Hostname())
}

// Install resolves an entry by ID and hands the exact pinned source to the
// existing skill manager. It never executes catalog resources.
func (c *Client) Install(ctx context.Context, manager *skill.Manager, id string) (skill.Skill, Entry, error) {
	e, err := c.Get(ctx, id)
	if err != nil {
		return skill.Skill{}, Entry{}, err
	}
	v, err := manager.InstallGit(ctx, e.Repo, e.Ref, e.Path)
	return v, e, err
}

// Package github is a small, read-bounded GitHub REST client. It performs
// only the operation requested by its caller and never pushes or retries.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const defaultMaxResponseBytes = 8 << 20

var slugPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type Config struct {
	BaseURL          string
	Token            string
	Timeout          time.Duration
	MaxResponseBytes int
	HTTPClient       *http.Client
}

type Client struct {
	baseURL, token string
	http           *http.Client
	maxBody        int
}

func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = "https://api.github.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("github: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("github: credentials, queries, and fragments are not allowed in base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("github: HTTPS is required except for loopback HTTP")
	}
	if strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("github: invalid token")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	} else {
		copyClient := *hc
		copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		hc = &copyClient
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	return &Client{baseURL: strings.TrimRight(u.String(), "/"), token: cfg.Token, http: hc, maxBody: cfg.MaxResponseBytes}, nil
}

func FromEnv() (*Client, error) {
	token := os.Getenv("GH_TOKEN")
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	return New(Config{BaseURL: os.Getenv("GITHUB_API_URL"), Token: token})
}

func FromEnvOrGHCLI(envVar string) (*Client, error) {
	var token string
	if strings.TrimSpace(envVar) != "" {
		token = os.Getenv(strings.TrimSpace(envVar))
	}
	if token == "" {
		token = os.Getenv("GH_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GITHUB_TOKEN")
	}
	if token == "" {
		token = os.Getenv("GITHUB_PERSONAL_ACCESS_TOKEN")
	}
	if token == "" {
		if out, err := exec.Command("gh", "auth", "token").Output(); err == nil {
			token = strings.TrimSpace(string(out))
		}
	}
	return New(Config{BaseURL: os.Getenv("GITHUB_API_URL"), Token: token})
}

type APIError struct {
	StatusCode int
	RetryAfter string
}

func (e *APIError) Error() string {
	if e.RetryAfter != "" {
		return fmt.Sprintf("github: HTTP %d (retry-after %s)", e.StatusCode, e.RetryAfter)
	}
	return fmt.Sprintf("github: HTTP %d", e.StatusCode)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if c == nil || c.http == nil {
		return errors.New("github: nil client")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("github: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("github: create request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "bifrost")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{StatusCode: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxBody)+1))
	if err != nil {
		return fmt.Errorf("github: read response: %w", err)
	}
	if len(b) > c.maxBody {
		return errors.New("github: response exceeds configured limit")
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return errors.New("github: invalid JSON response")
		}
	}
	return nil
}

type AuthStatus struct {
	Authenticated bool   `json:"authenticated"`
	Login         string `json:"login,omitempty"`
}

func (c *Client) AuthStatus(ctx context.Context) (AuthStatus, error) {
	if c.token == "" {
		return AuthStatus{}, nil
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := c.do(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return AuthStatus{}, err
	}
	return AuthStatus{Authenticated: true, Login: u.Login}, nil
}

type Repository struct {
	ID              int64    `json:"id"`
	FullName        string   `json:"full_name"`
	Description     string   `json:"description"`
	HTMLURL         string   `json:"html_url"`
	Homepage        string   `json:"homepage"`
	Language        string   `json:"language"`
	Topics          []string `json:"topics"`
	StargazersCount int      `json:"stargazers_count"`
	ForksCount      int      `json:"forks_count"`
	OpenIssuesCount int      `json:"open_issues_count"`
	CloneURL        string   `json:"clone_url"`
	SSHURL          string   `json:"ssh_url"`
	DefaultBranch   string   `json:"default_branch"`
	Private         bool     `json:"private"`
	Archived        bool     `json:"archived"`
	UpdatedAt       string   `json:"updated_at"`
	Owner           struct {
		Login string `json:"login"`
	} `json:"owner"`
}

func (c *Client) InspectRepository(ctx context.Context, owner, name string) (Repository, error) {
	if err := validateSlug(owner, name); err != nil {
		return Repository{}, err
	}
	var r Repository
	err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name), nil, &r)
	return r, err
}

type PullRequest struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	Draft     bool   `json:"draft"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Head struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

type Issue struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Body      string `json:"body"`
	State     string `json:"state"`
	HTMLURL   string `json:"html_url"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Comments  int    `json:"comments"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request,omitempty"`
}

func (c *Client) ListIssues(ctx context.Context, owner, name, state string, page, perPage int) ([]Issue, error) {
	if err := validateSlug(owner, name); err != nil {
		return nil, err
	}
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, errors.New("github: invalid issue state")
	}
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	var raw []Issue
	p := fmt.Sprintf("/repos/%s/%s/issues?state=%s&page=%d&per_page=%d", url.PathEscape(owner), url.PathEscape(name), url.QueryEscape(state), page, perPage)
	if err := c.do(ctx, http.MethodGet, p, nil, &raw); err != nil {
		return nil, err
	}
	// GitHub's /issues endpoint includes pull requests; filter them out so
	// ListIssues returns only true issues.
	issues := make([]Issue, 0, len(raw))
	for _, item := range raw {
		if item.PullRequest == nil {
			issues = append(issues, item)
		}
	}
	return issues, nil
}

func (c *Client) GetReadme(ctx context.Context, owner, name string) (string, error) {
	if err := validateSlug(owner, name); err != nil {
		return "", err
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	p := fmt.Sprintf("/repos/%s/%s/readme", url.PathEscape(owner), url.PathEscape(name))
	if err := c.do(ctx, http.MethodGet, p, nil, &payload); err != nil {
		return "", err
	}
	if strings.EqualFold(payload.Encoding, "base64") && payload.Content != "" {
		cleaned := strings.ReplaceAll(payload.Content, "\n", "")
		cleaned = strings.ReplaceAll(cleaned, "\r", "")
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return "", fmt.Errorf("github: decode readme: %w", err)
		}
		return string(decoded), nil
	}
	return payload.Content, nil
}

func (c *Client) ListPullRequests(ctx context.Context, owner, name, state string, page, perPage int) ([]PullRequest, error) {
	if err := validateSlug(owner, name); err != nil {
		return nil, err
	}
	if state == "" {
		state = "open"
	}
	if state != "open" && state != "closed" && state != "all" {
		return nil, errors.New("github: invalid pull request state")
	}
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	var prs []PullRequest
	p := fmt.Sprintf("/repos/%s/%s/pulls?state=%s&page=%d&per_page=%d", url.PathEscape(owner), url.PathEscape(name), url.QueryEscape(state), page, perPage)
	if err := c.do(ctx, http.MethodGet, p, nil, &prs); err != nil {
		return nil, err
	}
	return prs, nil
}

type CheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
}
type Checks struct {
	Total int        `json:"total_count"`
	Runs  []CheckRun `json:"check_runs"`
}

func (c *Client) ListChecks(ctx context.Context, owner, name, ref string, page, perPage int) (Checks, error) {
	if err := validateSlug(owner, name); err != nil {
		return Checks{}, err
	}
	if err := validateRef(ref); err != nil {
		return Checks{}, err
	}
	if page <= 0 {
		page = 1
	}
	if perPage <= 0 {
		perPage = 30
	}
	if perPage > 100 {
		perPage = 100
	}
	var out Checks
	p := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?page=%d&per_page=%d", url.PathEscape(owner), url.PathEscape(name), url.PathEscape(ref), page, perPage)
	if err := c.do(ctx, http.MethodGet, p, nil, &out); err != nil {
		return Checks{}, err
	}
	return out, nil
}

type DraftPRRequest struct {
	Owner string
	Repo  string
	Title string
	Body  string
	Head  string
	Base  string
}

func (c *Client) CreateDraftPR(ctx context.Context, in DraftPRRequest) (PullRequest, error) {
	if err := validateSlug(in.Owner, in.Repo); err != nil {
		return PullRequest{}, err
	}
	if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Head) == "" || strings.TrimSpace(in.Base) == "" {
		return PullRequest{}, errors.New("github: title, head, and base are required")
	}
	if err := validateRef(in.Head); err != nil {
		return PullRequest{}, err
	}
	if err := validateRef(in.Base); err != nil {
		return PullRequest{}, err
	}
	var out PullRequest
	body := map[string]any{"title": in.Title, "body": in.Body, "head": in.Head, "base": in.Base, "draft": true}
	err := c.do(ctx, http.MethodPost, "/repos/"+url.PathEscape(in.Owner)+"/"+url.PathEscape(in.Repo)+"/pulls", body, &out)
	return out, err
}

func ParseSlug(value string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 {
		return "", "", errors.New("github: repository must be OWNER/REPO")
	}
	if err := validateSlug(parts[0], parts[1]); err != nil {
		return "", "", err
	}
	return parts[0], parts[1], nil
}
func validateSlug(owner, name string) error {
	if !slugPart.MatchString(owner) || !slugPart.MatchString(name) || strings.HasPrefix(owner, "-") || strings.HasPrefix(name, "-") {
		return errors.New("github: invalid repository name")
	}
	return nil
}
func validateRef(ref string) error {
	if ref == "" || strings.HasPrefix(ref, "-") || strings.ContainsAny(ref, "\r\n\x00") || strings.Contains(ref, "..") || strings.ContainsAny(ref, " \t") {
		return errors.New("github: invalid ref")
	}
	return nil
}
func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() || strings.EqualFold(host, "localhost")
}

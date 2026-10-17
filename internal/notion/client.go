// Package notion provides a bounded, standard-library REST client for the
// Notion v1 API (user status, workspace search, page inspection, and block
// previews). It stores no secrets and never mutates workspace content unless
// explicitly asked.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	defaultBaseURL          = "https://api.notion.com"
	defaultNotionVersion    = "2022-06-28"
	defaultMaxResponseBytes = 4 << 20
)

type Config struct {
	BaseURL          string
	Token            string
	Timeout          time.Duration
	MaxResponseBytes int
	HTTPClient       *http.Client
}

type Client struct {
	baseURL string
	token   string
	http    *http.Client
	maxBody int
}

func New(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = defaultBaseURL
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errors.New("notion: base URL must include scheme and host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("notion: credentials, queries, and fragments are not allowed in base URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, errors.New("notion: HTTPS is required except for loopback HTTP")
	}
	if strings.ContainsAny(cfg.Token, "\r\n") {
		return nil, errors.New("notion: invalid token")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 20 * time.Second
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = defaultMaxResponseBytes
	}
	return &Client{
		baseURL: strings.TrimRight(u.String(), "/"),
		token:   strings.TrimSpace(cfg.Token),
		http:    hc,
		maxBody: cfg.MaxResponseBytes,
	}, nil
}

func FromEnv() (*Client, error) {
	return FromEnvName("")
}

func FromEnvName(envVar string) (*Client, error) {
	var token string
	if strings.TrimSpace(envVar) != "" {
		token = os.Getenv(strings.TrimSpace(envVar))
	}
	if token == "" {
		token = os.Getenv("NOTION_API_KEY")
	}
	if token == "" {
		token = os.Getenv("NOTION_TOKEN")
	}
	return New(Config{BaseURL: os.Getenv("NOTION_API_URL"), Token: token})
}

func (c *Client) HasToken() bool {
	return c != nil && c.token != ""
}

type AuthStatus struct {
	Authenticated bool   `json:"authenticated"`
	BotName       string `json:"bot_name,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
}

type PageSummary struct {
	ID             string `json:"id"`
	Object         string `json:"object"`
	Title          string `json:"title"`
	URL            string `json:"url"`
	LastEditedTime string `json:"last_edited_time"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	if c == nil || c.http == nil {
		return errors.New("notion: nil client")
	}
	if c.token == "" {
		return errors.New("notion: no API token configured (set NOTION_API_KEY or NOTION_TOKEN)")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("notion: encode request: %w", err)
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return fmt.Errorf("notion: create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Notion-Version", defaultNotionVersion)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notion: request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notion: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(c.maxBody)+1))
	if err != nil {
		return fmt.Errorf("notion: read response: %w", err)
	}
	if len(b) > c.maxBody {
		return errors.New("notion: response exceeds configured limit")
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			return errors.New("notion: invalid JSON response")
		}
	}
	return nil
}

func (c *Client) AuthStatus(ctx context.Context) (AuthStatus, error) {
	if c == nil || c.token == "" {
		return AuthStatus{}, nil
	}
	var raw struct {
		Name string `json:"name"`
		Bot  struct {
			WorkspaceName string `json:"workspace_name"`
		} `json:"bot"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/users/me", nil, &raw); err != nil {
		return AuthStatus{}, err
	}
	return AuthStatus{
		Authenticated: true,
		BotName:       raw.Name,
		WorkspaceName: raw.Bot.WorkspaceName,
	}, nil
}

func (c *Client) Search(ctx context.Context, query string, limit int) ([]PageSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	payload := map[string]any{
		"page_size": limit,
	}
	if strings.TrimSpace(query) != "" {
		payload["query"] = strings.TrimSpace(query)
	}
	var raw struct {
		Results []struct {
			ID             string                     `json:"id"`
			Object         string                     `json:"object"`
			URL            string                     `json:"url"`
			LastEditedTime string                     `json:"last_edited_time"`
			Title          []richTextItem             `json:"title"`
			Properties     map[string]json.RawMessage `json:"properties"`
		} `json:"results"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/search", payload, &raw); err != nil {
		return nil, err
	}
	items := make([]PageSummary, 0, len(raw.Results))
	for _, r := range raw.Results {
		title := joinRichText(r.Title)
		if title == "" {
			title = extractPagePropertyTitle(r.Properties)
		}
		if title == "" {
			title = r.ID
		}
		items = append(items, PageSummary{
			ID:             r.ID,
			Object:         r.Object,
			Title:          title,
			URL:            r.URL,
			LastEditedTime: r.LastEditedTime,
		})
	}
	return items, nil
}

func (c *Client) ReadPageText(ctx context.Context, pageID string) (string, error) {
	pageID = strings.TrimSpace(pageID)
	if pageID == "" {
		return "", errors.New("notion: page ID is required")
	}
	// Strip URL if user passed a full notion.so URL
	if strings.HasPrefix(pageID, "http://") || strings.HasPrefix(pageID, "https://") {
		if u, err := url.Parse(pageID); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "-")
			if len(parts) > 0 {
				pageID = parts[len(parts)-1]
			}
		}
	}
	var blocks struct {
		Results []map[string]any `json:"results"`
	}
	path := fmt.Sprintf("/v1/blocks/%s/children?page_size=50", url.PathEscape(pageID))
	if err := c.do(ctx, http.MethodGet, path, nil, &blocks); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, blk := range blocks.Results {
		bType, _ := blk["type"].(string)
		if bType == "" {
			continue
		}
		inner, _ := blk[bType].(map[string]any)
		if inner == nil {
			continue
		}
		text := extractRichTextFromMap(inner)
		switch bType {
		case "heading_1":
			b.WriteString("# " + text + "\n\n")
		case "heading_2":
			b.WriteString("## " + text + "\n\n")
		case "heading_3":
			b.WriteString("### " + text + "\n\n")
		case "bulleted_list_item":
			b.WriteString("- " + text + "\n")
		case "numbered_list_item":
			b.WriteString("1. " + text + "\n")
		case "to_do":
			checked, _ := inner["checked"].(bool)
			box := "[ ] "
			if checked {
				box = "[x] "
			}
			b.WriteString("- " + box + text + "\n")
		case "code":
			lang, _ := inner["language"].(string)
			b.WriteString("```" + lang + "\n" + text + "\n```\n\n")
		case "quote", "callout":
			b.WriteString("> " + text + "\n\n")
		default:
			if text != "" {
				b.WriteString(text + "\n\n")
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

type richTextItem struct {
	PlainText string `json:"plain_text"`
}

func joinRichText(items []richTextItem) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.PlainText)
	}
	return strings.TrimSpace(b.String())
}

func extractPagePropertyTitle(props map[string]json.RawMessage) string {
	for _, raw := range props {
		var p struct {
			Type  string         `json:"type"`
			Title []richTextItem `json:"title"`
		}
		if err := json.Unmarshal(raw, &p); err == nil && p.Type == "title" {
			if t := joinRichText(p.Title); t != "" {
				return t
			}
		}
	}
	return ""
}

func extractRichTextFromMap(m map[string]any) string {
	raw, ok := m["rich_text"]
	if !ok {
		return ""
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return ""
	}
	var items []richTextItem
	if err := json.Unmarshal(b, &items); err != nil {
		return ""
	}
	return joinRichText(items)
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return (ip != nil && ip.IsLoopback()) || strings.EqualFold(host, "localhost")
}

package marketplace

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const OfficialMCPRegistryURL = "https://registry.modelcontextprotocol.io/v0/servers"

// MCPEntry describes an installable Model Context Protocol (MCP) server from
// the built-in catalog or the official MCP Registry API.
type MCPEntry struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Command     []string `json:"command,omitempty"`
	URL         string   `json:"url,omitempty"`
	EnvVars     []string `json:"env_vars,omitempty"`
	Repo        string   `json:"repo,omitempty"`
	Category    string   `json:"category,omitempty"`
	SourceName  string   `json:"source_name,omitempty"`
}

// BuiltinMCPServers returns the curated catalog of popular official and
// community MCP servers so the MCP Marketplace works immediately offline and
// out of the box.
func BuiltinMCPServers() []MCPEntry {
	return []MCPEntry{
		{
			ID:          "mcp-filesystem",
			Name:        "Filesystem MCP Server",
			Description: "Secure file operations (read, write, directory tree, search, metadata) with configurable allowed directories.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-filesystem", "."},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Workspace & Files",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-github",
			Name:        "GitHub MCP Server",
			Description: "Repository management, pull requests, issues, code search, file operations, and GitHub Actions inspection.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-github"},
			EnvVars:     []string{"GITHUB_PERSONAL_ACCESS_TOKEN"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Developer Tools",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-git",
			Name:        "Git MCP Server",
			Description: "Read, search, diff, log, branch, and manipulate local Git repositories via Model Context Protocol tools.",
			Command:     []string{"uvx", "mcp-server-git", "--repository", "."},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Developer Tools",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-fetch",
			Name:        "Web Fetch MCP Server",
			Description: "Fetch web pages and convert HTML into clean Markdown for LLM consumption and documentation lookup.",
			Command:     []string{"uvx", "mcp-server-fetch"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Web & Search",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-memory",
			Name:        "Knowledge Graph Memory",
			Description: "Persistent entity-relation knowledge graph memory across agent sessions.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-memory"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Memory & RAG",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-sequential-thinking",
			Name:        "Sequential Thinking",
			Description: "Dynamic, reflective step-by-step problem solving through structured thought chains and revisions.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-sequential-thinking"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Reasoning",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-playwright",
			Name:        "Playwright Browser MCP",
			Description: "Official Microsoft Playwright MCP server for browser automation, accessibility snapshots, and web testing.",
			Command:     []string{"npx", "-y", "@playwright/mcp@latest"},
			Repo:        "https://github.com/microsoft/playwright-mcp",
			Category:    "Browser & UI",
			SourceName:  "Microsoft",
		},
		{
			ID:          "mcp-postgres",
			Name:        "PostgreSQL MCP Server",
			Description: "Inspect database schemas and execute read-only SQL queries against PostgreSQL databases.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Databases",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-sqlite",
			Name:        "SQLite MCP Server",
			Description: "Query, inspect schemas, and analyze data in local SQLite database files.",
			Command:     []string{"uvx", "mcp-server-sqlite", "--db-path", "database.db"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Databases",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-brave-search",
			Name:        "Brave Search MCP",
			Description: "Web and local search capabilities powered by the Brave Search API.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-brave-search"},
			EnvVars:     []string{"BRAVE_API_KEY"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Web & Search",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-context7",
			Name:        "Context7 Live Docs MCP",
			Description: "Up-to-date, version-specific documentation and code examples for libraries and frameworks directly in prompts.",
			Command:     []string{"npx", "-y", "@upstash/context7-mcp@latest"},
			Repo:        "https://github.com/upstash/context7",
			Category:    "Documentation",
			SourceName:  "Upstash",
		},
		{
			ID:          "mcp-ripgrep",
			Name:        "Ripgrep Code Search MCP",
			Description: "Fast regex code and file search across large repositories using ripgrep.",
			Command:     []string{"npx", "-y", "mcp-ripgrep@latest"},
			Repo:        "https://github.com/mcollina/mcp-ripgrep",
			Category:    "Developer Tools",
			SourceName:  "Community",
		},
		{
			ID:          "mcp-puppeteer",
			Name:        "Puppeteer Browser Automation",
			Description: "Navigate pages, take screenshots, click elements, and evaluate JavaScript in headless Chrome.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-puppeteer"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Browser & UI",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-slack",
			Name:        "Slack Workspace MCP",
			Description: "Read channels, search messages, post updates, and manage threads in Slack workspaces.",
			Command:     []string{"npx", "-y", "@modelcontextprotocol/server-slack"},
			EnvVars:     []string{"SLACK_BOT_TOKEN", "SLACK_TEAM_ID"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Collaboration",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-linear",
			Name:        "Linear Issue Tracker MCP",
			Description: "Search, create, and update Linear issues, projects, cycles, and team workflows.",
			Command:     []string{"npx", "-y", "mcp-remote", "https://mcp.linear.app/sse"},
			Repo:        "https://linear.app/docs/mcp",
			Category:    "Project Management",
			SourceName:  "Linear",
		},
		{
			ID:          "mcp-notion",
			Name:        "Notion Workspace MCP",
			Description: "Search pages, query databases, create documentation, and update Notion workspaces.",
			Command:     []string{"npx", "-y", "@notionhq/notion-mcp-server"},
			EnvVars:     []string{"OPENAPI_MCP_HEADERS"},
			Repo:        "https://github.com/makenotion/notion-mcp-server",
			Category:    "Collaboration",
			SourceName:  "Notion",
		},
		{
			ID:          "mcp-sentry",
			Name:        "Sentry Error Tracking MCP",
			Description: "Inspect production issues, stacktraces, release health, and error events from Sentry.",
			Command:     []string{"uvx", "mcp-server-sentry"},
			EnvVars:     []string{"SENTRY_AUTH_TOKEN"},
			Repo:        "https://github.com/modelcontextprotocol/servers",
			Category:    "Observability",
			SourceName:  "Official MCP",
		},
		{
			ID:          "mcp-docker",
			Name:        "Docker Containers MCP",
			Description: "Manage Docker containers, images, volumes, compose stacks, and container logs.",
			Command:     []string{"uvx", "mcp-server-docker"},
			Repo:        "https://github.com/ckreiling/mcp-server-docker",
			Category:    "Infrastructure",
			SourceName:  "Community",
		},
	}
}

// ListMCP returns matching MCP servers from the built-in catalog and, when
// reachable, queries the official MCP Registry API.
func (c *Client) ListMCP(ctx context.Context, query string) ([]MCPEntry, error) {
	entries := append([]MCPEntry(nil), BuiltinMCPServers()...)

	// Optionally query the official MCP Registry if query is provided or online
	if c != nil && c.client != nil && strings.TrimSpace(query) != "" {
		if remote, err := c.fetchOfficialMCPRegistry(ctx, query); err == nil {
			entries = append(entries, remote...)
		}
	}

	seen := make(map[string]bool, len(entries))
	deduped := make([]MCPEntry, 0, len(entries))
	for _, e := range entries {
		if e.ID == "" || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		deduped = append(deduped, e)
	}

	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return deduped, nil
	}
	words := strings.Fields(q)
	var filtered []MCPEntry
	for _, e := range deduped {
		haystack := strings.ToLower(strings.Join([]string{
			e.ID, e.Name, e.Description, e.Category, e.SourceName, strings.Join(e.Command, " "),
		}, " "))
		match := true
		for _, w := range words {
			if !strings.Contains(haystack, w) {
				match = false
				break
			}
		}
		if match {
			filtered = append(filtered, e)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	return filtered, nil
}

func (c *Client) GetMCP(ctx context.Context, id string) (MCPEntry, error) {
	id = strings.TrimSpace(id)
	for _, e := range BuiltinMCPServers() {
		if e.ID == id {
			return e, nil
		}
	}
	if c != nil && c.client != nil {
		if remote, err := c.fetchOfficialMCPRegistry(ctx, id); err == nil {
			for _, e := range remote {
				if e.ID == id {
					return e, nil
				}
			}
		}
	}
	return MCPEntry{}, fmt.Errorf("MCP server %q not found in catalog", id)
}

func (c *Client) fetchOfficialMCPRegistry(ctx context.Context, query string) ([]MCPEntry, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	u, err := url.Parse(OfficialMCPRegistryURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("limit", "30")
	if strings.TrimSpace(query) != "" {
		q.Set("search", strings.TrimSpace(query))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("registry status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var payload struct {
		Servers []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Repository  struct {
				URL string `json:"url"`
			} `json:"repository"`
			Packages []struct {
				RegistryName string `json:"registry_name"`
				Name         string `json:"name"`
			} `json:"packages"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, err
	}
	var out []MCPEntry
	for _, s := range payload.Servers {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		slug := sanitizeMCPSlug(name)
		if slug == "" {
			continue
		}
		cmd := []string{"npx", "-y", name}
		if len(s.Packages) > 0 {
			pkg := s.Packages[0]
			if pkg.RegistryName == "pypi" && pkg.Name != "" {
				cmd = []string{"uvx", pkg.Name}
			} else if pkg.Name != "" {
				cmd = []string{"npx", "-y", pkg.Name}
			}
		}
		out = append(out, MCPEntry{
			ID:          "mcp-" + slug,
			Name:        name,
			Description: strings.TrimSpace(s.Description),
			Command:     cmd,
			Repo:        strings.TrimSpace(s.Repository.URL),
			Category:    "MCP Registry",
			SourceName:  "MCP Registry",
		})
	}
	return out, nil
}

func sanitizeMCPSlug(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	var b strings.Builder
	lastDash := false
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

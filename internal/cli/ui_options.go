package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"bifrost/internal/config"
	"bifrost/internal/marketplace"
	"bifrost/internal/mcptools"
	"bifrost/internal/ui"
)

func workspaceUIOptions(opts options, settings config.Settings) ([]ui.Option, error) {
	market, err := workspaceMarketplaceCallbacks(opts, settings)
	if err != nil {
		return nil, err
	}
	settingsOption, err := settingsUIOption(opts)
	if err != nil {
		return nil, err
	}
	options := append(appearanceOptions(settings.UI), settingsOption)
	options = append(options, ui.WithLayoutSettingsChange(workspaceLayoutCallback(opts)), ui.WithMarketplace(market))
	return options, nil
}

func workspaceLayoutCallback(opts options) func(ui.LayoutSettings) error {
	return func(layout ui.LayoutSettings) error {
		path, err := opts.configFile()
		if err != nil {
			return err
		}
		fresh, revision, err := config.LoadSettingsRevision(path)
		if err != nil {
			return err
		}
		fresh.UI.Layout = layout.Name
		fresh.UI.PaneProportions = [3]int{layout.Files, layout.Viewer, layout.Chat}
		fresh.UI.TerminalVisible = &layout.TerminalVisible
		fresh.UI.TerminalPercent = layout.TerminalPercent
		return config.SaveSettingsRevision(path, fresh, revision)
	}
}

func workspaceMarketplaceCallbacks(opts options, settings config.Settings) (ui.MarketplaceCallbacks, error) {
	client, err := marketplace.New(marketplace.Config{CatalogURL: settings.Marketplace.CatalogURL})
	if err != nil {
		return ui.MarketplaceCallbacks{}, err
	}
	var previewMu sync.Mutex
	previewed := make(map[string]marketplace.Entry)
	previewedMCP := make(map[string]marketplace.MCPEntry)
	return ui.MarketplaceCallbacks{
		List: func(ctx context.Context, query string) ([]ui.MarketplaceEntry, error) {
			entries, err := client.List(ctx, query)
			if err != nil {
				return nil, err
			}
			result := make([]ui.MarketplaceEntry, 0, len(entries))
			for _, e := range entries {
				result = append(result, ui.MarketplaceEntry{ID: e.ID, Name: e.Name, Description: e.Description, Source: e.Repo + " @ " + e.Ref + " / " + e.Path, Kind: "skill"})
			}
			return result, nil
		},
		ListMCP: func(ctx context.Context, query string) ([]ui.MarketplaceEntry, error) {
			entries, err := client.ListMCP(ctx, query)
			if err != nil {
				return nil, err
			}
			result := make([]ui.MarketplaceEntry, 0, len(entries))
			for _, e := range entries {
				source := e.Repo
				if source == "" {
					source = e.SourceName
				}
				result = append(result, ui.MarketplaceEntry{
					ID:          e.ID,
					Name:        e.Name,
					Description: e.Description,
					Source:      source,
					Kind:        "mcp",
				})
			}
			return result, nil
		},
		Preview: func(ctx context.Context, id string) (string, error) {
			if strings.HasPrefix(id, "mcp-") || strings.HasPrefix(id, "mcp/") {
				mcpEntry, err := client.GetMCP(ctx, id)
				if err != nil {
					return "", err
				}
				previewMu.Lock()
				previewedMCP[id] = mcpEntry
				previewMu.Unlock()
				var details strings.Builder
				details.WriteString(mcpEntry.Description + "\n\n### MCP Server Configuration\n\n")
				if mcpEntry.Category != "" {
					details.WriteString(fmt.Sprintf("Category: `%s`\n\n", mcpEntry.Category))
				}
				if len(mcpEntry.Command) > 0 {
					details.WriteString(fmt.Sprintf("Command: `%s`\n\n", strings.Join(mcpEntry.Command, " ")))
				}
				if mcpEntry.URL != "" {
					details.WriteString(fmt.Sprintf("Endpoint URL: `%s`\n\n", mcpEntry.URL))
				}
				if len(mcpEntry.EnvVars) > 0 {
					details.WriteString(fmt.Sprintf("Environment Variables: `%s`\n\n", strings.Join(mcpEntry.EnvVars, "`, `")))
				}
				if mcpEntry.Repo != "" {
					details.WriteString(fmt.Sprintf("Repository: %s\n\n", mcpEntry.Repo))
				}
				details.WriteString("Installing adds this server to your project or global `mcp.json` configuration.")
				return details.String(), nil
			}
			e, err := client.Get(ctx, id)
			if err != nil {
				return "", err
			}
			previewMu.Lock()
			previewed[id] = e
			previewMu.Unlock()
			return fmt.Sprintf("%s\n\n### Source\n\nRepository: %s\n\nRevision: `%s`\n\nPath: `%s`\n\nInstallation adds this skill to your library. Activate it separately in your project settings.", e.Description, e.Repo, e.Ref, e.Path), nil
		},
		Install: func(ctx context.Context, id, scope string) (string, error) {
			if strings.HasPrefix(id, "mcp-") || strings.HasPrefix(id, "mcp/") {
				previewMu.Lock()
				mcpEntry, ok := previewedMCP[id]
				previewMu.Unlock()
				if !ok {
					var err error
					mcpEntry, err = client.GetMCP(ctx, id)
					if err != nil {
						return "", errors.New("preview this MCP server before installing")
					}
				}
				return installMarketplaceMCPServer(opts, mcpEntry, scope)
			}
			previewMu.Lock()
			entry, ok := previewed[id]
			previewMu.Unlock()
			if !ok {
				return "", errors.New("preview this skill before installing")
			}
			manager, err := marketplaceManager(opts, scope)
			if err != nil {
				return "", err
			}
			s, err := manager.InstallGit(ctx, entry.Repo, entry.Ref, entry.Path)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Installed %s (%s). Activate with --skill %s or defaults.skills.", s.Name, scope, s.Name), nil
		},
	}, nil
}

func installMarketplaceMCPServer(opts options, entry marketplace.MCPEntry, scope string) (string, error) {
	if scope == "" {
		scope = "project"
	}
	serverName := strings.TrimPrefix(entry.ID, "mcp/")
	serverName = strings.ReplaceAll(serverName, "/", "-")
	if serverName == "" {
		serverName = "mcp-server"
	}
	cmd := append([]string(nil), entry.Command...)
	for i := range cmd {
		if cmd[i] == "." && strings.TrimSpace(opts.root) != "" {
			cmd[i] = opts.root
		}
	}
	if len(cmd) == 0 && entry.URL == "" {
		return "", fmt.Errorf("MCP server %s does not define a launch command or endpoint URL", entry.Name)
	}
	cfgPath, err := opts.configFile()
	if err != nil {
		return "", err
	}
	mcpPath := filepath.Join(filepath.Dir(cfgPath), "mcp.json")
	if scope == "project" && strings.TrimSpace(opts.root) != "" {
		stateDir, err := opts.workspaceStatePath(opts.root)
		if err == nil && stateDir != "" {
			mcpPath = filepath.Join(stateDir, "mcp.json")
		}
	}
	if err := mcptools.AddServer(mcpPath, mcptools.ServerConfig{
		Name:    serverName,
		URL:     entry.URL,
		Command: cmd,
	}); err != nil {
		return "", err
	}
	if settings, rev, err := config.LoadSettingsRevision(cfgPath); err == nil {
		changed := false
		if scope == "global" && strings.TrimSpace(settings.Defaults.MCPConfig) == "" {
			settings.Defaults.MCPConfig = mcpPath
			changed = true
		} else if scope == "project" && strings.TrimSpace(opts.root) != "" {
			if settings.Projects == nil {
				settings.Projects = make(map[string]config.ProjectDefaults)
			}
			key := filepath.Clean(opts.root)
			pd := settings.Projects[key]
			if strings.TrimSpace(pd.MCPConfig) == "" {
				pd.MCPConfig = mcpPath
				settings.Projects[key] = pd
				changed = true
			}
		}
		if changed {
			_ = config.SaveSettingsRevision(cfgPath, settings, rev)
		}
	}
	return fmt.Sprintf("Installed MCP server %s (%s) into %s.", entry.Name, scope, filepath.Base(mcpPath)), nil
}

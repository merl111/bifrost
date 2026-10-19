package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"bifrost/internal/config"
	"bifrost/internal/mcptools"
	"bifrost/internal/skill"
	"bifrost/internal/ui"
)

func workspaceExtensionCommand(opts options) ui.ExtensionCommand {
	return func(ctx context.Context, request ui.ExtensionRequest) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if request.Scope != "project" && request.Scope != "global" {
			return "", fmt.Errorf("scope must be project or global")
		}
		if request.Kind == "mcp-config" {
			return configureCustomMCP(ctx, opts, request)
		}
		if request.Kind != "skill-local" && request.Kind != "skill-git" {
			return "", fmt.Errorf("unknown extension operation")
		}
		manager, err := marketplaceManager(opts, request.Scope)
		if err != nil {
			return "", err
		}
		var installed skill.Skill
		if request.Kind == "skill-git" {
			installed, err = manager.InstallGit(ctx, request.Source, request.Ref, request.Directory)
		} else {
			var source string
			source, err = extensionPath(opts, request.Source)
			if err == nil {
				installed, err = manager.Install(source)
			}
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Installed skill %s (%s). Installation does not activate instructions. It is available for skill discovery on the next turn; configure auto-load in Skills.", installed.Name, request.Scope), nil
	}
}

func extensionPath(opts options, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		root, err := resolveRoot(opts)
		if err != nil {
			return "", err
		}
		path = filepath.Join(root, path)
	}
	return filepath.Clean(path), nil
}

// MCP files are validated without launching their servers. Saving only changes
// the selected defaults; existing sessions keep their connections until restart.
func configureCustomMCP(ctx context.Context, opts options, request ui.ExtensionRequest) (string, error) {
	configPath, err := opts.configFile()
	if err != nil {
		return "", err
	}
	settings, revision, err := config.LoadSettingsRevision(configPath)
	if err != nil {
		return "", err
	}
	key, err := mcpProjectKey(opts)
	if err != nil {
		return "", err
	}
	path := settings.ForProject(key).MCPConfig
	if request.Scope == "global" {
		path = settings.Defaults.MCPConfig
	}
	if request.Source != "" {
		path, err = extensionPath(opts, request.Source)
		if err != nil {
			return "", err
		}
	}
	if path == "" {
		return "No MCP config selected. Use /mcp config PATH [--scope project|global].", nil
	}
	servers, err := mcptools.ConfigServers(path)
	if err != nil {
		return "", err
	}
	if request.Source != "" {
		if request.Scope == "global" {
			settings.Defaults.MCPConfig = path
		} else {
			if settings.Projects == nil {
				settings.Projects = map[string]config.ProjectDefaults{}
			}
			project := settings.Projects[key]
			project.MCPConfig = path
			settings.Projects[key] = project
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := config.SaveSettingsRevision(configPath, settings, revision); err != nil {
			return "", err
		}
	}
	names := make([]string, 0, len(servers))
	for _, server := range servers {
		names = append(names, server.Name)
	}
	return fmt.Sprintf("MCP config (%s): %s\n%d configured servers: %s\nRestart Bifrost to connect using these defaults. An explicit --mcp-config overrides them. Native providers use these servers; vendor CLI backends manage their own MCP connections.", request.Scope, path, len(servers), strings.Join(names, ", ")), nil
}

// Folder workspaces use their existing canonical identity, so symlink aliases
// select the same defaults. Named projects share settings across repositories.
func mcpProjectKey(opts options) (string, error) {
	if opts.project != "" {
		return opts.project, nil
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return "", err
	}
	state, err := opts.workspaceStatePath(root)
	if err != nil {
		return "", err
	}
	return "workspace-" + filepath.Base(state), nil
}

func workspaceRunDefaults(opts options, settings config.Settings) (config.RunDefaults, error) {
	defaults := settings.ForProject(opts.project)
	key, err := mcpProjectKey(opts)
	if err != nil {
		return defaults, err
	}
	defaults.MCPConfig = settings.ForProject(key).MCPConfig
	return defaults, nil
}

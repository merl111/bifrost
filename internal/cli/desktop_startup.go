package cli

import (
	"context"
	"io"
	"os"

	"bifrost/internal/domain"
)

// DesktopLaunchCatalog is read before opening a workspace or starting providers.
// Projects retain all repository mappings; selecting one never flattens a project.
type DesktopLaunchCatalog struct {
	Projects                               []domain.Project
	Directory, Environment, DefaultProject string
}

// DesktopNeedsProjectChoice bypasses the chooser only for an explicit workspace.
func DesktopNeedsProjectChoice(args []string) (bool, error) {
	global, _, err := extractGlobalFlags(args)
	if err != nil {
		return false, err
	}
	opts, _, err := parseGlobal(global, io.Discard)
	return opts.root == "" && opts.project == "", err
}

func DesktopProjects(ctx context.Context, args []string) (DesktopLaunchCatalog, error) {
	var result DesktopLaunchCatalog
	global, _, err := extractGlobalFlags(args)
	if err != nil {
		return result, err
	}
	opts, _, err := parseGlobal(global, io.Discard)
	if err != nil {
		return result, err
	}
	result.Environment = opts.environment
	result.Directory, _ = os.Getwd()
	settings, err := opts.settings()
	if err != nil {
		return result, err
	}
	result.DefaultProject = settings.Defaults.Project
	if err := ctx.Err(); err != nil {
		return result, err
	}
	registry, err := openRegistry(opts)
	if err != nil {
		return result, err
	}
	defer registry.Close()
	result.Projects, err = registry.List()
	return result, err
}

// DesktopSelectionArguments preserves runtime flags and replaces only workspace
// selectors. Prepending keeps the selectors ahead of a possible -- terminator.
func DesktopSelectionArguments(args []string, project, repository, root string) ([]string, error) {
	global, runtimeArgs, err := extractGlobalFlags(args)
	if err != nil {
		return nil, err
	}
	var selected []string
	if project != "" {
		selected = []string{"--project", project, "--repository", repository}
	} else {
		selected = []string{"--root", root}
	}
	for i := 0; i < len(global); i++ {
		arg := global[i]
		name := arg
		for j, c := range arg {
			if c == '=' {
				name = arg[:j]
				break
			}
		}
		if name == "--root" || name == "--project" || name == "--repository" {
			if name == arg {
				i++
			}
			continue
		}
		selected = append(selected, arg)
	}
	return append(selected, runtimeArgs...), nil
}

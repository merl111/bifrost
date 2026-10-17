package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"bifrost/internal/marketplace"
	"bifrost/internal/skill"
)

// runSkillMarket implements the catalog subcommands. The top-level dispatcher
// intentionally remains in cli.go so applications embedding the CLI can route
// `skill market` without changing the existing skill command.
func runSkillMarket(ctx context.Context, opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: skill market list [QUERY] | show ID | install ID [--scope project|global] | sources")
	}
	var catalogURL, scope string
	pos := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--catalog":
			if i+1 >= len(args) {
				return errors.New("--catalog requires a URL")
			}
			i++
			catalogURL = args[i]
		case strings.HasPrefix(a, "--catalog="):
			if catalogURL != "" {
				return errors.New("duplicate --catalog")
			}
			catalogURL = strings.TrimPrefix(a, "--catalog=")
		case a == "--scope":
			if i+1 >= len(args) {
				return errors.New("--scope requires project or global")
			}
			i++
			scope = args[i]
		case strings.HasPrefix(a, "--scope="):
			if scope != "" {
				return errors.New("duplicate --scope")
			}
			scope = strings.TrimPrefix(a, "--scope=")
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown skill market flag %q", a)
		default:
			pos = append(pos, a)
		}
	}
	if catalogURL == "" {
		settings, err := opts.settings()
		if err != nil {
			return err
		}
		catalogURL = settings.Marketplace.CatalogURL
	}
	allowLocal := strings.HasPrefix(catalogURL, "http://127.0.0.1:") || strings.HasPrefix(catalogURL, "http://localhost:")
	client, err := marketplace.New(marketplace.Config{CatalogURL: catalogURL, AllowLocal: allowLocal})
	if err != nil {
		return err
	}
	if len(pos) == 0 {
		return errors.New("usage: skill market list [QUERY] | show ID | install ID [--scope project|global] | sources")
	}
	switch pos[0] {
	case "list":
		if len(pos) > 2 {
			return errors.New("skill market list accepts one query")
		}
		query := ""
		if len(pos) == 2 {
			query = pos[1]
		}
		if scope != "" {
			return errors.New("--scope is only valid for install")
		}
		v, err := client.List(ctx, query)
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "show":
		if len(pos) != 2 {
			return errors.New("skill market show requires an ID")
		}
		if scope != "" {
			return errors.New("--scope is only valid for install")
		}
		v, err := client.Get(ctx, pos[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "sources":
		if len(pos) != 1 {
			return errors.New("skill market sources takes no arguments")
		}
		if scope != "" {
			return errors.New("--scope is only valid for install")
		}
		v, err := client.Sources(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, v)
	case "install":
		if len(pos) != 2 {
			return errors.New("skill market install requires an ID")
		}
		if scope == "" {
			scope = "project"
		}
		if scope != "project" && scope != "global" {
			return errors.New("skill market install scope must be project or global")
		}
		m, err := marketplaceManager(opts, scope)
		if err != nil {
			return err
		}
		installed, entry, err := client.Install(ctx, m, pos[1])
		if err != nil {
			return err
		}
		return writeJSON(stdout, struct {
			Entry marketplace.Entry `json:"entry"`
			Scope string            `json:"scope"`
			Skill skill.Skill       `json:"skill"`
		}{entry, scope, installed})
	default:
		return fmt.Errorf("unknown skill market command %q", pos[0])
	}
}

func marketplaceManager(opts options, scope string) (*skill.Manager, error) {
	if scope == "project" {
		return skillManager(opts)
	}
	if scope != "global" {
		return nil, errors.New("scope must be project or global")
	}
	return skill.New(opts.statePath(), "global")
}

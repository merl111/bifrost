package cli

import (
	"bifrost/internal/agent"
	"bifrost/internal/config"
	"bifrost/internal/skill"
	"context"
	"errors"
	"slices"
	"strings"
)

type InstalledSkill struct {
	Skill              skill.Skill
	Scope              string
	Shadowed, AutoLoad bool
}
type SkillInventory struct {
	Items                           []InstalledSkill
	Project                         string
	GlobalDefaults, ProjectDefaults []string
	ProjectOverride                 bool
}

func loadDefaultSkills(opts options, cfg *agent.Config) error {
	if !cfg.LoadDefaultSkills {
		return nil
	}
	settings, err := opts.settings()
	if err != nil {
		return err
	}
	return activateSkills(opts, cfg, strings.Join(settings.ForProject(opts.project).Skills, ","))
}
func (d *DesktopWorkspace) InstalledSkills(ctx context.Context) (SkillInventory, error) {
	if err := ctx.Err(); err != nil {
		return SkillInventory{}, err
	}
	d.chat.mu.Lock()
	opts := d.chat.opts
	d.chat.mu.Unlock()
	DiscoverWorkspaceSkills(opts, d.Root)
	project, err := skillManager(opts)
	if err != nil {
		return SkillInventory{}, err
	}
	global, err := skill.New(opts.statePath(), "global")
	if err != nil {
		return SkillInventory{}, err
	}
	settings, err := opts.settings()
	if err != nil {
		return SkillInventory{}, err
	}
	result := SkillInventory{Project: opts.project, GlobalDefaults: settings.Defaults.Skills, ProjectDefaults: settings.Projects[opts.project].Skills, ProjectOverride: settings.Projects[opts.project].Skills != nil}
	active := settings.ForProject(opts.project).Skills
	seen := map[string]bool{}
	for _, source := range []struct {
		name    string
		manager *skill.Manager
	}{{"project", project}, {"global", global}, {"built-in", nil}} {
		items := skill.Bundled()
		if source.manager != nil {
			items, err = source.manager.ListLocal()
			if err != nil {
				return result, err
			}
		}
		for _, s := range items {
			result.Items = append(result.Items, InstalledSkill{Skill: s, Scope: source.name, Shadowed: seen[s.Name], AutoLoad: !seen[s.Name] && slices.Contains(active, s.Name)})
			seen[s.Name] = true
		}
	}
	return result, nil
}

// SetSkillAutoload saves defaults with a revision check. Running turns keep their
// instructions; new turns read the saved defaults. nil project names inherit global.
func (d *DesktopWorkspace) SetSkillAutoload(ctx context.Context, scope string, names []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.chat.mu.Lock()
	opts := d.chat.opts
	d.chat.mu.Unlock()
	if scope != "global" && (scope != "project" || opts.project == "") {
		return errors.New("project auto-load requires a named project; use global defaults or create a project")
	}
	manager, err := skillManager(opts)
	if err != nil {
		return err
	}
	if scope == "global" {
		manager, err = skill.New(opts.statePath(), "global")
		if err != nil {
			return err
		}
	}
	for _, name := range names {
		if _, err := skill.LoadAvailable(manager, name); err != nil {
			return err
		}
	}
	path, err := opts.configFile()
	if err != nil {
		return err
	}
	settings, revision, err := config.LoadSettingsRevision(path)
	if err != nil {
		return err
	}
	if scope == "global" {
		settings.Defaults.Skills = names
	} else {
		if settings.Projects == nil {
			settings.Projects = map[string]config.ProjectDefaults{}
		}
		p := settings.Projects[opts.project]
		p.Skills = names
		settings.Projects[opts.project] = p
	}
	return config.SaveSettingsRevision(path, settings, revision)
}

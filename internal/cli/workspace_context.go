package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"bifrost/internal/agent"
	"bifrost/internal/provider"
	"bifrost/internal/skill"
	"bifrost/internal/store"
)

const (
	maxWorkspaceAgentsFileBytes = 64 << 10
	maxWorkspaceContextBudget   = 160 << 10
	maxAutoSkillsPerProject     = 16
)

// DiscoverWorkspaceSkills finds and installs/loads local SKILL.md directories inside
// the workspace root (.agents/skills, .cursor/skills, .claude/skills, .bifrost/skills, skills)
// and any linked project repositories.
func DiscoverWorkspaceSkills(opts options, root string) []skill.Skill {
	manager, mErr := skillManager(opts)
	var discovered []skill.Skill
	seenNames := make(map[string]bool)

	if mErr == nil && manager != nil {
		if installed, err := manager.List(); err == nil {
			for _, s := range installed {
				seenNames[s.Name] = true
				discovered = append(discovered, s)
			}
		}
	}

	roots := []string{root}
	if registry, rErr := openRegistry(opts); rErr == nil {
		if proj, ok, pErr := resolveOrMatchWorkspaceProject(registry, opts, root, opts.environment); pErr == nil && ok {
			for _, repo := range proj.Repositories {
				rPath := repo.Roots[opts.environment]
				if rPath == "" {
					rPath = repo.Roots["local"]
				}
				if rPath != "" && rPath != root {
					roots = append(roots, rPath)
				}
			}
		}
		_ = registry.Close()
	}

	candidateParentDirs := []string{
		"skills",
		".agents/skills",
		".cursor/skills",
		".claude/skills",
		".bifrost/skills",
		".codex/skills",
	}

	for _, r := range roots {
		if strings.TrimSpace(r) == "" {
			continue
		}
		for _, relParent := range candidateParentDirs {
			parent := filepath.Join(r, filepath.FromSlash(relParent))
			entries, err := os.ReadDir(parent)
			if err != nil {
				continue
			}
			for _, entry := range entries {
				if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
					continue
				}
				skillDir := filepath.Join(parent, entry.Name())
				skillFile := filepath.Join(skillDir, "SKILL.md")
				info, err := os.Lstat(skillFile)
				if err != nil || !info.Mode().IsRegular() {
					continue
				}
				if mErr == nil && manager != nil {
					if installed, instErr := manager.Install(skillDir); instErr == nil {
						if !seenNames[installed.Name] {
							seenNames[installed.Name] = true
							discovered = append(discovered, installed)
						}
						continue
					}
				}
				// Even if already installed or manager unavailable, inspect SKILL.md directly.
				if s, ok := readStandaloneSkillDir(skillDir); ok && !seenNames[s.Name] {
					seenNames[s.Name] = true
					discovered = append(discovered, s)
				}
			}
		}
	}
	sort.Slice(discovered, func(i, j int) bool { return discovered[i].Name < discovered[j].Name })
	if len(discovered) > maxAutoSkillsPerProject {
		discovered = discovered[:maxAutoSkillsPerProject]
	}
	return discovered
}

func readStandaloneSkillDir(skillDir string) (skill.Skill, bool) {
	b, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
	if err != nil || len(b) == 0 || len(b) > 256<<10 || !utf8.Valid(b) {
		return skill.Skill{}, false
	}
	text := string(b)
	name := filepath.Base(skillDir)
	desc := "Project skill discovered in " + filepath.Base(filepath.Dir(skillDir))
	body := text
	if strings.HasPrefix(text, "---\n") || strings.HasPrefix(text, "---\r\n") {
		if idx := strings.Index(text[4:], "\n---"); idx >= 0 {
			header := text[4 : 4+idx]
			rest := text[4+idx+4:]
			body = strings.TrimPrefix( strings.TrimPrefix(rest, "\r"), "\n")
			for _, line := range strings.Split(header, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				k = strings.TrimSpace(k)
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				if k == "name" && v != "" {
					name = store.SanitizeAlias(v)
				} else if k == "description" && v != "" {
					desc = v
				}
			}
		}
	}
	return skill.Skill{
		Name:        name,
		Description: desc,
		Body:        body,
		Source:      skillDir,
		Digest:      "local",
	}, true
}

// applyWorkspaceProjectContext automatically loads AGENTS.md (and CLAUDE.md)
// plus discovered project skills into the turn's agent.Config when operating on a local workspace.
func applyWorkspaceProjectContext(opts options, cfg *agent.Config) {
	if cfg == nil || strings.TrimSpace(cfg.Root) == "" {
		return
	}
	if cfg.Backend != nil && cfg.Backend.Identity().Kind != "local" {
		return
	}

	var sections []string
	// 1. Load AGENTS.md / CLAUDE.md from workspace root (and linked repos if not already in Instructions)
	for _, docName := range []string{"AGENTS.md", "CLAUDE.md", ".github/copilot-instructions.md"} {
		p := filepath.Join(cfg.Root, filepath.FromSlash(docName))
		content, ok := readBoundedUTF8File(p, maxWorkspaceAgentsFileBytes)
		if !ok {
			continue
		}
		header := "Workspace instructions from " + docName + ":"
		if strings.Contains(cfg.Instructions, header) {
			continue
		}
		sections = append(sections, header+"\n"+content)
	}

	// Also check linked repositories for nested AGENTS.md
	if registry, rErr := openRegistry(opts); rErr == nil {
		if proj, ok, pErr := resolveOrMatchWorkspaceProject(registry, opts, cfg.Root, opts.environment); pErr == nil && ok {
			for _, repo := range proj.Repositories {
				rPath := repo.Roots[opts.environment]
				if rPath == "" {
					rPath = repo.Roots["local"]
				}
				if rPath == "" || rPath == cfg.Root {
					continue
				}
				p := filepath.Join(rPath, "AGENTS.md")
				if content, ok := readBoundedUTF8File(p, maxWorkspaceAgentsFileBytes/2); ok {
					header := "Repository instructions (" + repo.Alias + "/AGENTS.md):"
					if !strings.Contains(cfg.Instructions, header) {
						sections = append(sections, header+"\n"+content)
					}
				}
			}
		}
		_ = registry.Close()
	}

	// 2. Auto-discover and load project skills
	skills := DiscoverWorkspaceSkills(opts, cfg.Root)
	activatedNames := make(map[string]bool)
	for _, s := range skills {
		if s.Name == "" {
			continue
		}
		activatedNames[s.Name] = true
		marker := "Active skill " + s.Name
		if strings.Contains(cfg.Instructions, marker) {
			continue
		}
		sections = append(sections, "Active skill "+s.Name+" ("+s.Description+"):\n"+s.Body)
	}

	// 3. Instruct the assistant to format workspace file and directory references as clickable links
	linkGuidance := "Formatting note: When referencing files or directories in the workspace, format them as Markdown links with relative workspace paths (e.g. [internal/ui/chat.go](internal/ui/chat.go) or [cmd/bifrost/main.go:42](cmd/bifrost/main.go:42)) or backticked relative paths (`internal/ui/chat.go`) so the user can click them to open directly in the Bifrost viewer."
	if !strings.Contains(cfg.Instructions, "Formatting note: When referencing files") {
		sections = append(sections, linkGuidance)
	}

	if len(sections) > 0 {
		addition := strings.Join(sections, "\n\n")
		if len(cfg.Instructions)+len(addition) <= maxWorkspaceContextBudget {
			cfg.Instructions = strings.TrimSpace(cfg.Instructions + "\n\n" + addition)
		}
	}

	// Ensure read_skill_resource tool is registered if we auto-activated skills and it's not yet present
	if len(activatedNames) > 0 {
		hasTool := false
		for _, t := range cfg.ExtraTools {
			if t.Definition.Name == "read_skill_resource" {
				hasTool = true
				break
			}
		}
		if !hasTool {
			if manager, err := skillManager(opts); err == nil && manager != nil {
				cfg.ExtraTools = append(cfg.ExtraTools, agent.Tool{
					Definition: provider.ToolDefinition{
						Name:        "read_skill_resource",
						Description: "Read a text resource from an active project or installed skill.",
						Parameters:  json.RawMessage(`{"type":"object","required":["skill","path"],"properties":{"skill":{"type":"string"},"path":{"type":"string"}}}`),
					},
					Call: func(ctx context.Context, raw json.RawMessage) (string, error) {
						if err := ctx.Err(); err != nil {
							return "", err
						}
						var args struct {
							Skill string `json:"skill"`
							Path  string `json:"path"`
						}
						if err := json.Unmarshal(raw, &args); err != nil {
							return "", err
						}
						if !activatedNames[args.Skill] {
							return "", errors.New("skill is not activated")
						}
						return manager.ReadResource(args.Skill, args.Path)
					},
				})
			}
		}
	}
}

func readBoundedUTF8File(path string, maxBytes int) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	buf := make([]byte, maxBytes+1)
	n, _ := f.Read(buf)
	if n <= 0 {
		return "", false
	}
	b := buf[:n]
	truncated := false
	if len(b) > maxBytes {
		b = b[:maxBytes]
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
		truncated = true
	}
	if !utf8.Valid(b) {
		return "", false
	}
	out := strings.TrimSpace(string(b))
	if out == "" {
		return "", false
	}
	if truncated {
		out += "\n… [truncated]"
	}
	return out, true
}

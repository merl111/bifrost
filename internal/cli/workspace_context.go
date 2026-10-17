package cli

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"bifrost/internal/agent"
	"bifrost/internal/skill"
)

const (
	maxWorkspaceAgentsFileBytes = 64 << 10
	maxWorkspaceContextBudget   = 160 << 10
	maxAutoSkillsPerProject     = 256
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
			}
		}
	}
	discovered = skill.WithBundled(discovered)
	if len(discovered) > maxAutoSkillsPerProject {
		discovered = discovered[:maxAutoSkillsPerProject]
	}
	return discovered
}

// applyWorkspaceProjectContext automatically loads AGENTS.md (and CLAUDE.md)
// plus descriptions of available skills into the turn's agent.Config when operating on a local workspace.
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

	// Skills are discovered as metadata; bodies are loaded only on selection.
	attachSkillDiscovery(cfg, DiscoverWorkspaceSkills(opts, cfg.Root), opts)

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

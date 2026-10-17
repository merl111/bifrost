package skill

import (
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"sort"
)

// Built-in guidance is original Bifrost content, embedded without installing or
// running any external analyzer. Installed skills may override these names.
//
//go:embed bundled/*/SKILL.md
var bundledFS embed.FS

func Bundled() []Skill {
	entries, _ := bundledFS.ReadDir("bundled")
	result := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		content, err := bundledFS.ReadFile("bundled/" + entry.Name() + "/SKILL.md")
		if err != nil {
			continue
		}
		parsed, err := parseSkillMarkdown(string(content))
		if err != nil {
			panic("invalid bundled skill: " + err.Error())
		}
		result = append(result, Skill{Name: parsed.Name, Description: parsed.Description, Body: parsed.Body, Source: "bifrost:builtin/" + parsed.Name, Digest: fmt.Sprintf("%x", sha256.Sum256(content))})
	}
	return result
}

// WithBundled adds built-ins as the lowest-priority source.
func WithBundled(installed []Skill) []Skill {
	result := append([]Skill(nil), installed...)
	seen := map[string]bool{}
	for _, s := range result {
		seen[s.Name] = true
	}
	for _, s := range Bundled() {
		if !seen[s.Name] {
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// LoadAvailable preserves project/global precedence and falls back only for an
// absent skill; corruption or validation failures must never select a fallback.
func LoadAvailable(manager *Manager, name string) (Skill, error) {
	s, err := manager.Load(name)
	if !errors.Is(err, ErrSkillNotFound) {
		return s, err
	}
	for _, s := range Bundled() {
		if s.Name == name {
			return s, nil
		}
	}
	return Skill{}, err
}

// ReadAvailableResource supports embedded SKILL.md and rooted installed resources.
func ReadAvailableResource(manager *Manager, name, path string) (string, error) {
	loaded, err := LoadAvailable(manager, name)
	if err != nil {
		return "", err
	}
	if IsBundled(loaded) {
		if path == "SKILL.md" {
			return loaded.Body, nil
		}
		return "", errors.New("bundled skill has no additional resources")
	}
	return manager.ReadResource(name, path)
}

// IsBundled checks immutable embedded identity, not just a source label that a
// local directory name could imitate.
func IsBundled(s Skill) bool {
	for _, b := range Bundled() {
		if s.Name == b.Name && s.Source == b.Source && s.Digest == b.Digest {
			return true
		}
	}
	return false
}

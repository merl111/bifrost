// Package config contains the portable project manifest and machine-local bindings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"bifrost/internal/domain"
)

const SchemaVersion = 1

var (
	ErrUnsupportedVersion = errors.New("unsupported configuration schema version")
	ErrInvalidConfig      = errors.New("invalid configuration")
)

type ProjectManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Name          string           `json:"name"`
	Repositories  []RepositorySpec `json:"repositories"`
}

type RepositorySpec struct {
	Alias         string `json:"alias"`
	Remote        string `json:"remote,omitempty"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type LocalConfig struct {
	SchemaVersion int                    `json:"schema_version"`
	ProjectRef    string                 `json:"project_ref"`
	Environments  map[string]Environment `json:"environments"`
}

type Environment struct {
	Kind         string            `json:"kind"`
	Repositories map[string]string `json:"repositories"`
}

type ResolvedProject struct {
	Manifest ProjectManifest
	Local    LocalConfig
	Roots    map[string]map[string]string // environment -> repository alias -> absolute root
}

func LoadProject(r io.Reader) (ProjectManifest, error) {
	var manifest ProjectManifest
	if err := decodeStrict(r, &manifest); err != nil {
		return manifest, err
	}
	if err := manifest.Validate(); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func LoadProjectFile(path string) (ProjectManifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return ProjectManifest{}, fmt.Errorf("open project manifest %q: %w", path, err)
	}
	defer f.Close()
	return LoadProject(f)
}

func LoadLocal(r io.Reader) (LocalConfig, error) {
	var local LocalConfig
	if err := decodeStrict(r, &local); err != nil {
		return local, err
	}
	if err := local.Validate(); err != nil {
		return local, err
	}
	return local, nil
}

func LoadLocalFile(path string) (LocalConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return LocalConfig{}, fmt.Errorf("open local config %q: %w", path, err)
	}
	defer f.Close()
	return LoadLocal(f)
}

func (m ProjectManifest) Validate() error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, m.SchemaVersion, SchemaVersion)
	}
	if err := domain.ValidateName(m.Name); err != nil {
		return fmt.Errorf("%w: name: %v", ErrInvalidConfig, err)
	}
	if len(m.Repositories) == 0 {
		return fmt.Errorf("%w: at least one repository is required", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(m.Repositories))
	for i, repo := range m.Repositories {
		if err := domain.ValidateName(repo.Alias); err != nil {
			return fmt.Errorf("%w: repository %d alias: %v", ErrInvalidConfig, i, err)
		}
		if _, ok := seen[repo.Alias]; ok {
			return fmt.Errorf("%w: duplicate repository alias %q", ErrInvalidConfig, repo.Alias)
		}
		seen[repo.Alias] = struct{}{}
	}
	return nil
}

func (c LocalConfig) Validate() error {
	if c.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: got %d, want %d", ErrUnsupportedVersion, c.SchemaVersion, SchemaVersion)
	}
	if err := domain.ValidateName(c.ProjectRef); err != nil {
		return fmt.Errorf("%w: project_ref: %v", ErrInvalidConfig, err)
	}
	if len(c.Environments) == 0 {
		return fmt.Errorf("%w: at least one environment is required", ErrInvalidConfig)
	}
	for name, env := range c.Environments {
		if err := domain.ValidateName(name); err != nil {
			return fmt.Errorf("%w: environment %q: %v", ErrInvalidConfig, name, err)
		}
		if env.Kind == "" {
			return fmt.Errorf("%w: environment %q has no kind", ErrInvalidConfig, name)
		}
		for alias, root := range env.Repositories {
			if err := domain.ValidateName(alias); err != nil {
				return fmt.Errorf("%w: environment %q alias: %v", ErrInvalidConfig, name, err)
			}
			if root == "" || !filepath.IsAbs(root) || strings.ContainsRune(root, '\x00') {
				return fmt.Errorf("%w: environment %q root for %q must be absolute", ErrInvalidConfig, name, alias)
			}
		}
	}
	return nil
}

func Resolve(manifest ProjectManifest, local LocalConfig) (ResolvedProject, error) {
	if err := manifest.Validate(); err != nil {
		return ResolvedProject{}, err
	}
	if err := local.Validate(); err != nil {
		return ResolvedProject{}, err
	}
	if manifest.Name != local.ProjectRef {
		return ResolvedProject{}, fmt.Errorf("%w: local project_ref %q does not match manifest name %q", ErrInvalidConfig, local.ProjectRef, manifest.Name)
	}
	known := make(map[string]struct{}, len(manifest.Repositories))
	for _, repo := range manifest.Repositories {
		known[repo.Alias] = struct{}{}
	}
	resolved := ResolvedProject{Manifest: manifest, Local: local, Roots: make(map[string]map[string]string, len(local.Environments))}
	for envName, env := range local.Environments {
		resolved.Roots[envName] = make(map[string]string, len(env.Repositories))
		for alias, root := range env.Repositories {
			if _, ok := known[alias]; !ok {
				return ResolvedProject{}, fmt.Errorf("%w: environment %q references unknown repository %q", ErrInvalidConfig, envName, alias)
			}
			resolved.Roots[envName][alias] = filepath.Clean(root)
		}
	}
	return resolved, nil
}

func decodeStrict(r io.Reader, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	if len(b) > 1<<20 {
		return fmt.Errorf("configuration exceeds 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("%w: multiple JSON values", ErrInvalidConfig)
		}
		return fmt.Errorf("decode trailing configuration: %w", err)
	}
	return nil
}

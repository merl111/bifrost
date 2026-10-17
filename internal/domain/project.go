package domain

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	ErrInvalidProject    = errors.New("invalid project")
	ErrInvalidName       = errors.New("invalid name")
	ErrInvalidRepository = errors.New("invalid repository")
)

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type Project struct {
	ID           ID                     `json:"id"`
	Name         string                 `json:"name"`
	Description  string                 `json:"description,omitempty"`
	ManifestPath string                 `json:"manifest_path,omitempty"`
	Repositories []Repository           `json:"repositories,omitempty"`
	Environments map[string]Environment `json:"environments,omitempty"`
	CreatedAt    time.Time              `json:"created_at"`
	UpdatedAt    time.Time              `json:"updated_at"`
	Revision     uint64                 `json:"revision"`
}

type Repository struct {
	ID            ID                `json:"id"`
	ProjectID     ID                `json:"project_id"`
	Alias         string            `json:"alias"`
	Remote        string            `json:"remote,omitempty"`
	DefaultBranch string            `json:"default_branch,omitempty"`
	Roots         map[string]string `json:"roots,omitempty"`
}

type Environment struct {
	Kind         string            `json:"kind"`
	Repositories map[string]string `json:"repositories"`
}

func ValidateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return nil
}

func (p Project) Validate() error {
	if err := p.ID.Validate(); err != nil {
		return fmt.Errorf("%w: id: %v", ErrInvalidProject, err)
	}
	kind, _ := p.ID.Kind()
	if kind != KindProject {
		return fmt.Errorf("%w: id kind is %s", ErrInvalidProject, kind)
	}
	if err := ValidateName(p.Name); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidProject, err)
	}
	if p.Revision == 0 {
		return fmt.Errorf("%w: revision must be positive", ErrInvalidProject)
	}
	for i, repo := range p.Repositories {
		if err := repo.Validate(); err != nil {
			return fmt.Errorf("%w: repository %d: %v", ErrInvalidProject, i, err)
		}
		if repo.ProjectID != p.ID {
			return fmt.Errorf("%w: repository %d belongs to %s", ErrInvalidProject, i, repo.ProjectID)
		}
		for j := 0; j < i; j++ {
			if p.Repositories[j].Alias == repo.Alias {
				return fmt.Errorf("%w: duplicate repository alias %q", ErrInvalidProject, repo.Alias)
			}
		}
	}
	return nil
}

func (r Repository) Validate() error {
	if err := r.ID.Validate(); err != nil {
		return fmt.Errorf("%w: id: %v", ErrInvalidRepository, err)
	}
	kind, _ := r.ID.Kind()
	if kind != KindRepo {
		return fmt.Errorf("%w: id kind is %s", ErrInvalidRepository, kind)
	}
	if err := r.ProjectID.Validate(); err != nil {
		return fmt.Errorf("%w: project id: %v", ErrInvalidRepository, err)
	}
	projectKind, _ := r.ProjectID.Kind()
	if projectKind != KindProject {
		return fmt.Errorf("%w: project id kind is %s", ErrInvalidRepository, projectKind)
	}
	if !namePattern.MatchString(r.Alias) {
		return fmt.Errorf("%w: alias %q", ErrInvalidRepository, r.Alias)
	}
	for alias, root := range r.Roots {
		if !namePattern.MatchString(alias) || root == "" || !filepath.IsAbs(root) || strings.ContainsRune(root, '\x00') {
			return fmt.Errorf("%w: root mapping %q", ErrInvalidRepository, alias)
		}
	}
	return nil
}

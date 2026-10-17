package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"bifrost/internal/domain"
)

var (
	ErrRevisionConflict = errors.New("project revision conflict")
	ErrProjectNotFound  = errors.New("project not found")
	ErrDuplicateProject = errors.New("project name already exists")
)

const registrySchemaVersion = 1

type CreateProjectRequest struct {
	Name         string
	Description  string
	ManifestPath string
	Repositories []RepositoryInput
}

type RepositoryInput struct {
	Alias         string
	Remote        string
	DefaultBranch string
	Roots         map[string]string
}

type registryFile struct {
	SchemaVersion int              `json:"schema_version"`
	Projects      []domain.Project `json:"projects"`
}

type Registry struct {
	mu       sync.RWMutex
	dir      string
	path     string
	lock     *fileLock
	projects map[domain.ID]domain.Project
	closed   bool
}

func OpenRegistry(dir string) (*Registry, error) {
	if dir == "" {
		return nil, errors.New("registry directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create registry directory: %w", err)
	}
	lock, err := acquireLock(filepath.Join(dir, "registry.lock"))
	if err != nil {
		return nil, err
	}
	r := &Registry{dir: dir, path: filepath.Join(dir, "registry.json"), lock: lock, projects: make(map[domain.ID]domain.Project)}
	if err := r.load(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return r, nil
}

func (r *Registry) Create(req CreateProjectRequest) (domain.Project, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return domain.Project{}, err
	}
	if err := domain.ValidateName(req.Name); err != nil {
		return domain.Project{}, err
	}
	for _, p := range r.projects {
		if p.Name == req.Name {
			return domain.Project{}, fmt.Errorf("%w: %q", ErrDuplicateProject, req.Name)
		}
	}
	id, err := domain.NewID(domain.KindProject)
	if err != nil {
		return domain.Project{}, err
	}
	now := time.Now().UTC()
	p := domain.Project{ID: id, Name: req.Name, Description: strings.TrimSpace(req.Description), ManifestPath: req.ManifestPath, CreatedAt: now, UpdatedAt: now, Revision: 1}
	for _, input := range req.Repositories {
		if err := domain.ValidateName(input.Alias); err != nil {
			return domain.Project{}, err
		}
		rid, err := domain.NewID(domain.KindRepo)
		if err != nil {
			return domain.Project{}, err
		}
		p.Repositories = append(p.Repositories, domain.Repository{ID: rid, ProjectID: id, Alias: input.Alias, Remote: input.Remote, DefaultBranch: input.DefaultBranch, Roots: cloneStrings(input.Roots)})
	}
	if err := p.Validate(); err != nil {
		return domain.Project{}, err
	}
	r.projects[p.ID] = p
	if err := r.saveLocked(); err != nil {
		delete(r.projects, p.ID)
		return domain.Project{}, err
	}
	return cloneProject(p), nil
}

func (r *Registry) Get(id domain.ID) (domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureOpen(); err != nil {
		return domain.Project{}, err
	}
	p, ok := r.projects[id]
	if !ok {
		return domain.Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	}
	return cloneProject(p), nil
}

func (r *Registry) GetByName(name string) (domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureOpen(); err != nil {
		return domain.Project{}, err
	}
	for _, p := range r.projects {
		if p.Name == name {
			return cloneProject(p), nil
		}
	}
	return domain.Project{}, fmt.Errorf("%w: %q", ErrProjectNotFound, name)
}

func (r *Registry) List() ([]domain.Project, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := r.ensureOpen(); err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(r.projects))
	for _, p := range r.projects {
		projects = append(projects, cloneProject(p))
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name < projects[j].Name })
	return projects, nil
}

func (r *Registry) Update(p domain.Project) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ensureOpen(); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	if _, ok := r.projects[p.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrProjectNotFound, p.ID)
	}
	for id, other := range r.projects {
		if id != p.ID && other.Name == p.Name {
			return fmt.Errorf("%w: %q", ErrDuplicateProject, p.Name)
		}
	}
	previous := r.projects[p.ID]
	if p.Revision != previous.Revision {
		return fmt.Errorf("%w: got %d, want %d", ErrRevisionConflict, p.Revision, previous.Revision)
	}
	p.CreatedAt = previous.CreatedAt
	p.UpdatedAt = time.Now().UTC()
	p.Revision++
	r.projects[p.ID] = cloneProject(p)
	if err := r.saveLocked(); err != nil {
		r.projects[p.ID] = previous
		return err
	}
	return nil
}

func (r *Registry) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	return r.lock.Close()
}

func (r *Registry) load() error {
	b, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read registry: %w", err)
	}
	var file registryFile
	if err := json.Unmarshal(b, &file); err != nil {
		return fmt.Errorf("decode registry: %w", err)
	}
	if file.SchemaVersion != registrySchemaVersion {
		return fmt.Errorf("registry: %w: got %d, want %d", ErrUnsupportedSchema, file.SchemaVersion, registrySchemaVersion)
	}
	for _, p := range file.Projects {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("registry project %s: %w", p.ID, err)
		}
		if _, ok := r.projects[p.ID]; ok {
			return fmt.Errorf("registry: duplicate id %s", p.ID)
		}
		for _, other := range r.projects {
			if other.Name == p.Name {
				return fmt.Errorf("registry: %w: %q", ErrDuplicateProject, p.Name)
			}
		}
		r.projects[p.ID] = cloneProject(p)
	}
	return nil
}

var ErrUnsupportedSchema = errors.New("unsupported store schema")

func (r *Registry) saveLocked() error {
	projects := make([]domain.Project, 0, len(r.projects))
	for _, p := range r.projects {
		projects = append(projects, p)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	return atomicJSON(r.path, registryFile{SchemaVersion: registrySchemaVersion, Projects: projects})
}

func (r *Registry) ensureOpen() error {
	if r.closed {
		return errors.New("registry is closed")
	}
	return nil
}

func cloneProject(p domain.Project) domain.Project {
	p.Repositories = append([]domain.Repository(nil), p.Repositories...)
	for i := range p.Repositories {
		p.Repositories[i].Roots = cloneStrings(p.Repositories[i].Roots)
	}
	p.Environments = cloneEnvironments(p.Environments)
	return p
}

func cloneStrings(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
func cloneEnvironments(m map[string]domain.Environment) map[string]domain.Environment {
	if m == nil {
		return nil
	}
	out := make(map[string]domain.Environment, len(m))
	for k, v := range m {
		v.Repositories = cloneStrings(v.Repositories)
		out[k] = v
	}
	return out
}

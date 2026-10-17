package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestRegistryCreateDuplicateRestartAndLock(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	project, err := r.Create(CreateProjectRequest{Name: "storefront", Repositories: []RepositoryInput{{Alias: "web", Roots: map[string]string{"local": filepath.Join(dir, "web")}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(CreateProjectRequest{Name: "storefront"}); !errors.Is(err, ErrDuplicateProject) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := OpenRegistry(dir); !errors.Is(err, ErrStoreLocked) {
		t.Fatalf("second open error = %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r, err = OpenRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.Get(project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != project.Name || got.Repositories[0].ID == "" {
		t.Fatalf("restart lost project: %+v", got)
	}
}

func TestRegistryConcurrentOpenAllowsOneWriter(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	release := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := OpenRegistry(dir)
			if err == nil {
				results <- nil
				<-release
				_ = r.Close()
				return
			}
			results <- err
		}()
	}
	opened := 0
	for i := 0; i < 8; i++ {
		err := <-results
		if err == nil {
			opened++
		} else if !errors.Is(err, ErrStoreLocked) {
			t.Fatalf("unexpected concurrent open error: %v", err)
		}
	}
	close(release)
	wg.Wait()
	if opened != 1 {
		t.Fatalf("concurrent opens succeeded %d times", opened)
	}
}

func TestUpdateRejectsStaleRevision(t *testing.T) {
	r, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	p, err := r.Create(CreateProjectRequest{Name: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	old := p
	p.Name = "fresh"
	if err := r.Update(p); err != nil {
		t.Fatal(err)
	}
	old.Name = "lost"
	if err := r.Update(old); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	got, err := r.Get(p.ID)
	if err != nil || got.Name != "fresh" {
		t.Fatalf("lost update: %#v %v", got, err)
	}
}

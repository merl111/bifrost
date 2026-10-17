// Package atomicfile provides durable replacement of private state snapshots.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Write replaces path with a 0600 file on the same filesystem, syncing the
// contents before rename and the containing directory afterwards. Callers own
// data validation and locking. A directory-sync error can occur after rename;
// callers must treat that outcome as uncertain rather than retry a mutation.
func Write(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".bifrost-state-")
	if err != nil {
		return fmt.Errorf("create state snapshot: %w", err)
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write state snapshot: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync state snapshot: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close state snapshot: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("replace state snapshot: %w", err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open state directory: %w", err)
	}
	if err := errors.Join(dir.Sync(), dir.Close()); err != nil {
		return fmt.Errorf("sync state directory: %w", err)
	}
	return nil
}

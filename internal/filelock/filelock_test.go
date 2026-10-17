package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireContendsAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "writer.lock")
	first, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("contending acquire error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stable lock inode missing: %v", err)
	}
}

// Package filelock provides a process-wide advisory lock backed by a stable
// inode. On Unix, the kernel releases the lock if the process is killed.
package filelock

import (
	"errors"
	"fmt"
	"os"
)

var ErrLocked = errors.New("file is already locked")

type Lock struct {
	path string
	file *os.File
}

func Acquire(path string) (*Lock, error) {
	f, busy, err := openAndLock(path)
	if err != nil {
		return nil, fmt.Errorf("open lock %q: %w", path, err)
	}
	if busy {
		return nil, fmt.Errorf("%w: %s", ErrLocked, path)
	}
	if err := f.Truncate(0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("prepare lock: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("seek lock: %w", err)
	}
	if _, err := fmt.Fprintf(f, "pid=%d\n", os.Getpid()); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write lock: %w", err)
	}
	return &Lock{path: path, file: f}, nil
}

func (l *Lock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := unlockFile(l.file)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

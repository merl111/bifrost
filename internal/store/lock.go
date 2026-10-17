package store

import "bifrost/internal/filelock"

var ErrStoreLocked = filelock.ErrLocked

type fileLock = filelock.Lock

func acquireLock(path string) (*fileLock, error) { return filelock.Acquire(path) }

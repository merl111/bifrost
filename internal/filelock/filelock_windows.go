//go:build windows

package filelock

import "os"

// Windows fallback: exclusive creation prevents concurrent writers. A killed
// process can leave the marker behind; a LockFileEx backend is needed before
// claiming crash recovery on that platform.
func openAndLock(path string) (*os.File, bool, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil, true, nil
	}
	return f, false, err
}

func unlockFile(*os.File) error { return nil }

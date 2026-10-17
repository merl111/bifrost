//go:build windows

package daemon

import (
	"errors"
	"os"
)

func daemonSocketOwned(info os.FileInfo) (bool, error) {
	return false, errors.New("daemon socket ownership cannot be verified on windows")
}

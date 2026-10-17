//go:build !windows

package daemon

import (
	"os"
	"syscall"
)

func daemonSocketOwned(info os.FileInfo) (bool, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	return uint32(st.Uid) == uint32(os.Getuid()), nil
}

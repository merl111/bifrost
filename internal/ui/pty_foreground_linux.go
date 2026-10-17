//go:build linux

package ui

import (
	"os"
	"syscall"
	"unsafe"
)

func foregroundProcessGroup(file *os.File) int {
	if file == nil {
		return 0
	}
	var pgid int32
	raw, err := file.SyscallConn()
	if err != nil {
		return 0
	}
	var errno syscall.Errno
	err = raw.Control(func(fd uintptr) {
		_, _, errno = syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGPGRP), uintptr(unsafe.Pointer(&pgid)))
	})
	if err != nil || errno != 0 || pgid <= 0 {
		return 0
	}
	return int(pgid)
}

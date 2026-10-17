//go:build linux

package ui

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

const (
	// _IOR('E', 0x18, 96) queries the kernel evdev key bitmap (96 bytes = 768 key bits).
	evdevIOCGKey96 = uintptr((2 << 30) | (uintptr('E') << 8) | 0x18 | (96 << 16))
	evdevLeftShift = 42
	evdevRightShift = 54
)

// platformShiftHeld checks whether a local hardware Shift key is currently
// depressed according to the Linux evdev driver.
//
// Terminals based on VTE (such as GNOME Console / kgx and GNOME Terminal) do
// not implement XTerm modifyOtherKeys or the Kitty keyboard protocol for Return
// and therefore write a bare CR ('\r') when Shift+Enter is pressed. When input
// is local (no SSH_TTY) and the user has read access to /dev/input, querying
// EVIOCGKEY at the moment CR arrives recovers the Shift modifier without
// intercepting normal typing.
func platformShiftHeld() bool {
	if os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != "" {
		return false
	}
	paths, _ := filepath.Glob("/dev/input/by-path/*-event-kbd")
	if len(paths) == 0 {
		paths, _ = filepath.Glob("/dev/input/by-id/*-event-kbd")
	}
	seen := make(map[string]bool, len(paths))
	for _, path := range paths {
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			realPath = path
		}
		if seen[realPath] {
			continue
		}
		seen[realPath] = true
		if evdevShiftBitSet(realPath) {
			return true
		}
	}
	return false
}

func evdevShiftBitSet(path string) bool {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	defer syscall.Close(fd)
	var bits [96]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), evdevIOCGKey96, uintptr(unsafe.Pointer(&bits[0])))
	if errno != 0 {
		return false
	}
	left := bits[evdevLeftShift/8]&(1<<(evdevLeftShift%8)) != 0
	right := bits[evdevRightShift/8]&(1<<(evdevRightShift%8)) != 0
	return left || right
}

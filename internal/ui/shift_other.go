//go:build !linux

package ui

func platformShiftHeld() bool {
	return false
}

//go:build !linux

package ui

import "os"

func foregroundProcessGroup(_ *os.File) int { return 0 }

package main

import "path/filepath"

// Each repository window needs its own AppImage mount lifetime. Starting the
// extracted ELF directly would lose schemas/modules when the original app exits.
// Ignore inherited AppImage variables when running an ordinary desktop binary.
func desktopLaunchPath(executable, appdir, image string) string {
	if appdir != "" && filepath.IsAbs(image) && filepath.Clean(executable) == filepath.Join(appdir, "usr", "bin", "bifrost-desktop") {
		return image
	}
	return executable
}

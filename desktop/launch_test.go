package main

import "testing"

func TestDesktopLaunchPath(t *testing.T) {
	for _, tc := range []struct{ name, executable, appdir, image, want string }{
		{"mounted", "/tmp/mount/usr/bin/bifrost-desktop", "/tmp/mount", "/home/user/My Apps/Bifrost.AppImage", "/home/user/My Apps/Bifrost.AppImage"},
		{"ordinary", "/opt/bifrost-desktop", "", "", "/opt/bifrost-desktop"},
		{"inherited environment", "/opt/bifrost-desktop", "/tmp/other", "/other.AppImage", "/opt/bifrost-desktop"},
		{"sibling prefix", "/tmp/mount-other/usr/bin/bifrost-desktop", "/tmp/mount", "/Bifrost.AppImage", "/tmp/mount-other/usr/bin/bifrost-desktop"},
		{"relative image", "/tmp/mount/usr/bin/bifrost-desktop", "/tmp/mount", "Bifrost.AppImage", "/tmp/mount/usr/bin/bifrost-desktop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := desktopLaunchPath(tc.executable, tc.appdir, tc.image); got != tc.want {
				t.Fatalf("launch %q, want %q", got, tc.want)
			}
		})
	}
}

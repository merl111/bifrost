package ui

// Terminal rendition of the bridge/portal mark in docs/images/bifrost-logo.png.
// It uses ordinary cells; neither image protocols nor a patched font are needed.
func (m *Model) brandMark() string {
	if m.icons == IconsASCII {
		return "|>|"
	}
	return "▐›▌"
}

func (m *Model) welcomeBrand(width int) []string {
	p := m.colors()
	if width < 32 || m.chatViewport() < 22 || m.icons == IconsASCII {
		return []string{"", p.violetText(m.brandMark()) + p.title("  Bifrost"), ""}
	}
	return []string{
		"",
		p.violetText("▗█        █▖"),
		p.violetText("██▀▀▄▄    ██"),
		p.violetText("██    ▀▀▄▄██") + p.title("   Bifrost"),
		p.violetText("██") + p.cyanText("  ▄▄▀▀") + p.violetText("  ██"),
		p.violetText("▝█") + p.cyanText("▀▀    ") + p.violetText("  █▘"),
		"",
	}
}

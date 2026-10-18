package main

import (
	"io/fs"
	"path/filepath"
	"strings"

	"bifrost/internal/workspace"
	cui "cui.local/cui"
)

// Filename conventions take precedence over extensions (Dockerfile.gateway,
// .env.example, go.mod). Unknown files always retain a readable document icon.
func fileKind(entry workspace.Entry) string {
	if entry.Mode&fs.ModeSymlink != 0 {
		return "Symbolic link"
	}
	if entry.IsDir {
		return "Folder"
	}
	name := strings.ToLower(entry.Name)
	switch {
	case name == "dockerfile" || strings.HasPrefix(name, "dockerfile.") || strings.HasPrefix(name, "docker-compose.") || strings.HasPrefix(name, "compose."):
		return "Docker"
	case name == ".env" || strings.HasPrefix(name, ".env."):
		return "Environment"
	case name == ".gitignore" || name == ".gitattributes" || name == ".gitmodules":
		return "Git"
	case name == "go.mod" || name == "go.sum":
		return "Go"
	case name == "makefile" || name == "gnumakefile" || name == "justfile":
		return "Build script"
	case name == "license" || strings.HasPrefix(name, "license.") || name == "copying":
		return "License"
	case name == "cargo.lock" || name == "package-lock.json" || name == "yarn.lock" || name == "pnpm-lock.yaml":
		return "Lockfile"
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".go":
		return "Go"
	case ".rs":
		return "Rust"
	case ".py", ".pyi":
		return "Python"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".zig", ".java", ".swift", ".kt", ".rb", ".php", ".sh", ".bash", ".zsh":
		return "Source code"
	case ".md", ".mdx", ".markdown", ".rst":
		return "Documentation"
	case ".json", ".jsonc":
		return "JSON"
	case ".yaml", ".yml", ".toml", ".ini", ".conf", ".xml":
		return "Configuration"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".ico":
		return "Image"
	case ".zip", ".gz", ".tar", ".xz", ".7z":
		return "Archive"
	case ".lock":
		return "Lockfile"
	case ".html", ".css", ".scss", ".vue", ".svelte":
		return "Web source"
	default:
		return "File"
	}
}

// Original 24-unit vector artwork. No icon font or runtime asset dependency.
// Shapes remain meaningful without color; names/type descriptions are exposed
// to accessibility and tooltips. CUI scales the paths to the display density.
func fileIcon(kind string) (*cui.IconAsset, error) {
	var commands []cui.IconCommand
	color := themeColor(muted)
	line := func(points ...float32) {
		for i := 0; i+1 < len(points); i += 2 {
			op := cui.IconLine
			if i == 0 {
				op = cui.IconMove
			}
			commands = append(commands, cui.IconCommand{Op: op, Values: [6]float32{points[i], points[i+1]}})
		}
		commands = append(commands, cui.IconCommand{Op: cui.IconStroke, Values: [6]float32{1.7}, RGBA: color})
	}
	switch kind {
	case "Folder":
		color = themeColor(violet)
		line(2, 19, 2, 5, 9, 5, 11, 8, 22, 8, 22, 19, 2, 19)
		line(2, 10, 22, 10)
	case "Go", "Source code", "Rust", "Web source":
		color = themeColor(cyan)
		if kind == "Rust" {
			color = themeColor(violet)
		}
		line(8, 6, 2, 12, 8, 18)
		line(16, 6, 22, 12, 16, 18)
		line(14, 4, 10, 20)
	case "Git":
		color = themeColor(violet)
		line(6, 4, 6, 20, 10, 20)
		line(6, 14, 18, 8, 18, 4)
		line(4, 2, 8, 2, 8, 6, 4, 6, 4, 2)
		line(16, 2, 20, 2, 20, 6, 16, 6, 16, 2)
	case "Docker":
		color = themeColor(cyan)
		line(2, 14, 22, 14, 19, 20, 7, 20, 2, 14)
		for _, p := range [][2]float32{{4, 8}, {10, 8}, {16, 8}, {10, 2}} {
			x, y := p[0], p[1]
			line(x, y, x+4, y, x+4, y+4, x, y+4, x, y)
		}
	case "Environment", "Lockfile":
		color = themeColor(violet)
		line(7, 10, 7, 4, 17, 4, 17, 10)
		line(4, 10, 20, 10, 20, 21, 4, 21, 4, 10)
		line(12, 14, 12, 18)
	case "Configuration", "Build script":
		color = themeColor(violet)
		line(3, 6, 21, 6)
		line(3, 12, 21, 12)
		line(3, 18, 21, 18)
		line(8, 3, 8, 9)
		line(16, 9, 16, 15)
		line(10, 15, 10, 21)
	case "JSON":
		color = themeColor(violet)
		line(9, 3, 6, 3, 6, 10, 3, 12, 6, 14, 6, 21, 9, 21)
		line(15, 3, 18, 3, 18, 10, 21, 12, 18, 14, 18, 21, 15, 21)
	case "Documentation":
		color = themeColor(cyan)
		line(3, 18, 3, 6, 8, 12, 13, 6, 13, 18)
		line(19, 6, 19, 18)
		line(16, 15, 19, 18, 22, 15)
	case "TypeScript", "JavaScript", "Python":
		color = themeColor(cyan)
		if kind == "JavaScript" {
			color = themeColor(violet)
		}
		line(2, 3, 22, 3, 22, 21, 2, 21, 2, 3)
		if kind == "TypeScript" {
			line(5, 8, 11, 8)
			line(8, 8, 8, 17)
		}
		if kind == "JavaScript" {
			line(10, 8, 10, 17, 5, 17, 5, 14)
		}
		if kind == "Python" {
			line(6, 17, 6, 7, 11, 7, 11, 12, 6, 12)
		}
		line(19, 8, 14, 8, 14, 12, 19, 12, 19, 17, 14, 17)
	case "Symbolic link":
		color = themeColor(cyan)
		line(9, 7, 5, 7, 2, 10, 2, 14, 5, 17, 9, 17)
		line(15, 7, 19, 7, 22, 10, 22, 14, 19, 17, 15, 17)
		line(7, 12, 17, 12)
	default:
		line(5, 2, 14, 2, 20, 8, 20, 22, 5, 22, 5, 2)
		line(14, 2, 14, 8, 20, 8)
		switch kind {
		case "Image":
			line(7, 18, 11, 12, 15, 17, 17, 14, 19, 18)
		case "Archive":
			line(12, 9, 14, 11, 12, 13, 14, 15, 12, 17, 14, 19)
		case "License":
			line(8, 14, 11, 17, 17, 11)
		default:
			line(8, 13, 16, 13)
			line(8, 17, 14, 17)
		}
	}
	return cui.NewVectorIcon(24, 24, commands)
}

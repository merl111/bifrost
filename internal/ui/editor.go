package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

type editorFinishedMsg struct {
	path   string
	focus  focus
	review bool
	err    error
}

// editorCommand creates the process used by tea.ExecProcess. The workspace
// root is checked with os.Root before its path is handed to the editor, and
// the resolved target is required to remain below the resolved root.
func (m *Model) editorCommand(rel string) (*exec.Cmd, error) {
	if m.remoteFS {
		return nil, fmt.Errorf("external editor is unavailable for remote workspaces; use the remote terminal")
	}
	path, err := m.editorPath(rel)
	if err != nil {
		return nil, err
	}
	argv, err := m.editorArgv()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(argv[0], append(append([]string(nil), argv[1:]...), path)...)
	cmd.Dir = m.tree.rootPath
	return cmd, nil
}

func (m *Model) editorArgv() ([]string, error) {
	if len(m.editor) > 0 {
		return validateEditorArgv(m.editor)
	}
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if value, ok := os.LookupEnv(name); ok && strings.TrimSpace(value) != "" {
			argv, err := parseEditorEnv(value)
			if err != nil {
				return nil, fmt.Errorf("invalid %s: %w", name, err)
			}
			return argv, nil
		}
	}
	return []string{"vim"}, nil
}

func validateEditorArgv(argv []string) ([]string, error) {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, fmt.Errorf("editor executable is empty")
	}
	copyArgv := append([]string(nil), argv...)
	for i, arg := range copyArgv {
		if arg == "" {
			return nil, fmt.Errorf("editor argument %d is empty", i)
		}
		if strings.IndexByte(arg, 0) >= 0 {
			return nil, fmt.Errorf("editor argument %d contains NUL", i)
		}
		for _, r := range arg {
			if unicode.IsControl(r) {
				return nil, fmt.Errorf("editor argument %d contains a control character", i)
			}
		}
	}
	return copyArgv, nil
}

// Environment editor values are deliberately a small argv syntax. Quoting,
// escapes, and shell operators are rejected instead of being interpreted.
func parseEditorEnv(value string) ([]string, error) {
	if strings.ContainsAny(value, "|&;<>()$`\\\"'*") {
		return nil, fmt.Errorf("shell syntax is not allowed")
	}
	argv := strings.Fields(value)
	if len(argv) == 0 {
		return nil, fmt.Errorf("editor executable is empty")
	}
	return validateEditorArgv(argv)
}

func (m *Model) editorPath(rel string) (string, error) {
	if m.remoteFS {
		return "", fmt.Errorf("external editor is unavailable for remote workspaces; use the remote terminal")
	}
	if m.tree.root == nil {
		return "", fmt.Errorf("workspace is unavailable")
	}
	clean, err := cleanRelative(rel)
	if err != nil {
		return "", err
	}
	f, err := m.tree.root.Open(clean)
	if err != nil {
		return "", err
	}
	info, statErr := f.Stat()
	_ = f.Close()
	if statErr != nil {
		return "", statErr
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}

	absPath := filepath.Join(m.tree.rootPath, filepath.FromSlash(clean))
	resolvedRoot, err := filepath.EvalSymlinks(m.tree.rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	resolvedPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", fmt.Errorf("resolve editor file: %w", err)
	}
	relPath, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", errOutsideRoot
	}
	resolvedInfo, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if !resolvedInfo.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	return resolvedPath, nil
}

func (m *Model) editorTarget() (string, error) {
	switch m.focus {
	case focusViewer:
		if m.viewPath == "" || m.viewPath == "review" {
			return "", fmt.Errorf("select a file before opening the editor")
		}
		return m.viewPath, nil
	case focusFiles:
		if m.selected < 0 || m.selected >= len(m.files) {
			return "", fmt.Errorf("select a file before opening the editor")
		}
		entry := m.files[m.selected]
		if entry.Dir {
			return "", fmt.Errorf("select a file before opening the editor")
		}
		if entry.Error != "" {
			return "", fmt.Errorf("selected file is unavailable: %s", entry.Error)
		}
		return filepath.ToSlash(filepath.Join(m.dir, entry.Path)), nil
	default:
		return "", fmt.Errorf("focus a file or viewer before opening the editor")
	}
}

func (m *Model) startEditor() tea.Cmd {
	path, err := m.editorTarget()
	if err != nil {
		m.status = "editor unavailable: " + err.Error()
		return nil
	}
	cmd, err := m.editorCommand(path)
	if err != nil {
		m.status = "editor unavailable: " + err.Error()
		return nil
	}
	returnFocus := m.focus
	reviewVisible := m.viewPath == "review"
	m.status = "editor running"
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		return editorFinishedMsg{path: path, focus: returnFocus, review: reviewVisible, err: err}
	})
}

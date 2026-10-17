package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxViewerBytes = 2 << 20

var errOutsideRoot = errors.New("path is outside workspace root")

// FileEntry is a path shown in the read-only workspace browser. Path is always
// relative to the canonical workspace root and uses slash separators.
type FileEntry struct {
	Path  string
	Dir   bool
	Error string
}

// Filesystem is the read boundary used by the tree and viewer. Implementations
// may be remote; callers must not assume Path names are host filesystem paths.
type Filesystem interface {
	List(context.Context, string) ([]FileEntry, error)
	Read(context.Context, string) (string, error)
	Close() error
}

type callbackFilesystem struct {
	list func(context.Context, string) ([]FileEntry, error)
	read func(context.Context, string) (string, error)
}

func (f callbackFilesystem) List(ctx context.Context, path string) ([]FileEntry, error) {
	if f.list == nil {
		return nil, errors.New("filesystem list callback is unavailable")
	}
	return f.list(ctx, path)
}
func (f callbackFilesystem) Read(ctx context.Context, path string) (string, error) {
	if f.read == nil {
		return "", errors.New("filesystem read callback is unavailable")
	}
	return f.read(ctx, path)
}
func (callbackFilesystem) Close() error { return nil }

type localFilesystem struct{ tree *fileTree }

func (f localFilesystem) List(_ context.Context, path string) ([]FileEntry, error) {
	return f.tree.list(path), nil
}
func (f localFilesystem) Read(_ context.Context, path string) (string, error) {
	return f.tree.read(path)
}
func (f localFilesystem) Close() error { return f.tree.close() }

type fileTree struct {
	rootPath string
	root     *os.Root
}

func newFileTree(root string) (fileTree, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return fileTree{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fileTree{}, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return fileTree{}, fmt.Errorf("workspace root is not a directory")
	}
	root = filepath.Clean(root)
	rootHandle, err := os.OpenRoot(root)
	if err != nil {
		return fileTree{}, fmt.Errorf("open workspace root: %w", err)
	}
	return fileTree{rootPath: root, root: rootHandle}, nil
}

func cleanRelative(rel string) (string, error) {
	if rel == "" || rel == "." {
		return ".", nil
	}
	rel = filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errOutsideRoot
	}
	return filepath.ToSlash(rel), nil
}

func (t fileTree) list(rel ...string) []FileEntry {
	base := ""
	if len(rel) > 0 {
		base = rel[0]
	}
	dir, err := cleanRelative(base)
	if err != nil {
		return []FileEntry{{Path: base, Error: err.Error()}}
	}
	file, err := t.root.Open(dir)
	if err != nil {
		return []FileEntry{{Path: ".", Error: err.Error()}}
	}
	defer file.Close()
	entries, err := file.ReadDir(-1)
	if err != nil {
		return []FileEntry{{Path: ".", Error: err.Error()}}
	}
	result := make([]FileEntry, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if name == ".git" {
			continue
		}
		path := filepath.ToSlash(name)
		info, infoErr := entry.Info()
		if infoErr != nil {
			result = append(result, FileEntry{Path: path, Error: infoErr.Error()})
			continue
		}
		result = append(result, FileEntry{Path: path, Dir: info.IsDir()})
	}
	if base != "" {
		result = append(result, FileEntry{Path: "..", Dir: true})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Dir != result[j].Dir {
			return result[i].Dir
		}
		return strings.ToLower(result[i].Path) < strings.ToLower(result[j].Path)
	})
	return result
}

func (t fileTree) read(rel string) (string, error) {
	path, err := cleanRelative(rel)
	if err != nil {
		return "", err
	}
	file, err := t.root.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		if info.IsDir() {
			return "", fmt.Errorf("%s is a directory", rel)
		}
		return "", fmt.Errorf("%s is not a regular file", rel)
	}
	b, err := io.ReadAll(io.LimitReader(file, maxViewerBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > maxViewerBytes {
		return "", fmt.Errorf("file is larger than %d bytes", maxViewerBytes)
	}
	if strings.IndexByte(string(b), 0) >= 0 {
		return "[binary file; read-only viewer does not render binary content]", nil
	}
	return string(b), nil
}

func (t fileTree) close() error {
	if t.root == nil {
		return nil
	}
	return t.root.Close()
}

package cursorclient

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"bifrost/internal/workspace"
)

// Cursor uses regular expressions, while the shared workspace Search API is
// intentionally literal. Keep this adapter rooted and bounded without requiring
// shell execution permission or invoking a host search command.
func (e *ToolEnv) search(ctx context.Context, args *Value) (workspace.SearchResult, error) {
	var result workspace.SearchResult
	pattern := args.String("pattern")
	if pattern == "" || len(pattern) > 4096 {
		return result, errors.New("search requires a pattern up to 4096 bytes")
	}
	if args.Bool("literal") {
		pattern = regexp.QuoteMeta(pattern)
	}
	flags := "(?m)"
	if args.Bool("case_insensitive") || args.Bool("ignore_case") {
		flags += "(?i)"
	}
	if args.Bool("multiline") {
		flags += "(?s)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return result, err
	}
	root, err := relativePath(e.Root, args.String("path"))
	if err != nil {
		return result, err
	}
	glob := args.String("glob")
	if kind := args.String("type"); kind != "" {
		extensions := map[string]string{"go": "*.go", "py": "*.py", "rust": "*.rs", "js": "*.js", "ts": "*.ts", "json": "*.json", "md": "*.md", "c": "*.c", "cpp": "*.cpp"}
		typeGlob, ok := extensions[kind]
		if !ok {
			return result, errors.New("unsupported file type; use glob instead")
		}
		if glob == "" {
			glob = typeGlob
		} else {
			return result, errors.New("use either glob or type")
		}
	}
	if _, err := globMatch(glob, ""); err != nil {
		return result, err
	}
	limit := int(args.Int("head_limit"))
	if limit <= 0 {
		limit = int(args.Int("limit"))
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	offset := int(args.Int("offset"))
	if offset < 0 || offset > 10000 {
		return result, errors.New("search offset must be between 0 and 10000")
	}
	var read, seen int
	stopped, err := e.walkFiles(ctx, root, func(entry workspace.Entry) (bool, error) {
		if entry.Size > 1<<20 {
			result.Truncated = true
			return true, nil
		}
		relative, _ := filepath.Rel(root, entry.Path)
		if relative == "." {
			relative = filepath.Base(entry.Path)
		}
		ok, err := globMatch(glob, filepath.ToSlash(relative))
		if err != nil || !ok {
			return true, err
		}
		if read+int(entry.Size) > 32<<20 {
			return false, nil
		}
		body, _, err := e.Files.Read(ctx, entry.Path)
		if errors.Is(err, workspace.ErrBinary) {
			return true, nil
		}
		if errors.Is(err, workspace.ErrTooLarge) {
			result.Truncated = true
			return true, nil
		}
		if err != nil {
			return false, err
		}
		read += len(body)
		result.Scanned++
		if len(body) > 1<<20 {
			result.Truncated = true
			return true, nil
		}
		if strings.IndexByte(body, 0) >= 0 {
			return true, nil
		}
		matches := re.FindAllStringIndex(body, 10001)
		previous := -1
		line, pos := 1, 0
		for _, match := range matches {
			line += strings.Count(body[pos:match[0]], "\n")
			pos = match[0]
			if line == previous {
				continue
			}
			previous = line
			if seen++; seen <= offset {
				continue
			}
			if len(result.Matches) >= limit {
				return false, nil
			}
			start := strings.LastIndexByte(body[:match[0]], '\n') + 1
			end := strings.IndexByte(body[match[0]:], '\n')
			if end < 0 {
				end = len(body)
			} else {
				end += match[0]
			}
			text := body[start:end]
			if len(text) > 2048 {
				text = strings.ToValidUTF8(text[:2048], "") + "…"
			}
			result.Matches = append(result.Matches, workspace.Match{Path: entry.Path, Line: line, Text: text})
		}
		if len(matches) == 10001 {
			return false, nil
		}
		return true, ctx.Err()
	})
	result.Truncated = result.Truncated || stopped
	return result, err
}

// walkFiles never follows symlinks and bounds traversal independently of matches.
// Returning false from visit marks the result as truncated.
func (e *ToolEnv) walkFiles(ctx context.Context, root string, visit func(workspace.Entry) (bool, error)) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if root == "" {
		root = "."
	}
	var count int
	var walk func(string, int) (bool, error)
	walk = func(dir string, depth int) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if depth > 32 || count >= 10000 {
			return true, nil
		}
		entries, err := e.Files.List(ctx, dir)
		if err != nil {
			return false, err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		for _, entry := range entries {
			count++
			if count > 10000 {
				return true, nil
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if entry.Mode&fs.ModeSymlink != 0 {
				continue
			}
			if entry.IsDir {
				if entry.Name == ".git" || entry.Name == "node_modules" || entry.Name == ".cache" {
					continue
				}
				stopped, err := walk(entry.Path, depth+1)
				if stopped || err != nil {
					return stopped, err
				}
			} else if entry.Mode.IsRegular() {
				more, err := visit(entry)
				if !more || err != nil {
					return !more, err
				}
			}
		}
		return false, nil
	}
	// Inspect the parent to distinguish a file scope without swallowing List errors.
	if root != "." {
		entries, err := e.Files.List(ctx, filepath.Dir(root))
		if err != nil {
			return false, err
		}
		for _, entry := range entries {
			if filepath.Clean(entry.Path) != filepath.Clean(root) {
				continue
			}
			if entry.Mode&fs.ModeSymlink != 0 {
				return false, errors.New("search does not follow symlinks")
			}
			if !entry.IsDir {
				more, err := visit(entry)
				return !more, err
			}
			return walk(root, 0)
		}
		return false, fs.ErrNotExist
	}
	return walk(root, 0)
}

// Match path segments with ** spanning zero or more directories. A bare glob
// matches a basename anywhere, as Cursor's grep/glob tools expect.
func globMatch(pattern, name string) (bool, error) {
	if pattern == "" {
		return true, nil
	}
	pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
	if !strings.Contains(pattern, "/") {
		return path.Match(pattern, path.Base(name))
	}
	parts, names := strings.Split(pattern, "/"), strings.Split(name, "/")
	if len(parts) > 64 || len(names) > 64 {
		return false, errors.New("glob path is too deep")
	}
	for _, part := range parts {
		if part != "**" {
			if _, err := path.Match(part, ""); err != nil {
				return false, err
			}
		}
	}
	// Dynamic programming avoids exponential backtracking through repeated **.
	current := make([]bool, len(names)+1)
	current[0] = true
	for _, part := range parts {
		next := make([]bool, len(names)+1)
		for j := 0; j <= len(names); j++ {
			if part == "**" {
				next[j] = current[j] || (j > 0 && next[j-1])
				continue
			}
			if j > 0 && current[j-1] {
				next[j], _ = path.Match(part, names[j-1])
			}
		}
		current = next
	}
	return current[len(names)], nil
}

package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"unicode/utf8"
)

type Match struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}
type SearchResult struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
	Scanned   int     `json:"scanned"`
}

// Search performs bounded literal text search; it never follows symlink entries.
// Gitignore filtering is not yet implemented. .git and common build/cache trees are skipped.
func (w *Workspace) Search(ctx context.Context, query string) (SearchResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.check(); err != nil {
		return SearchResult{}, err
	}
	if query == "" || len(query) > 4096 {
		return SearchResult{}, errors.New("search requires a nonempty query up to 4096 bytes")
	}
	result := SearchResult{Matches: []Match{}}
	var bytesRead int64
	err := fs.WalkDir(w.root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", ".cache":
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if result.Scanned >= 10000 || bytesRead >= 32<<20 || len(result.Matches) >= 200 {
			result.Truncated = true
			return fs.SkipAll
		}
		f, err := w.root.Open(path)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 1<<20 {
			f.Close()
			return nil
		}
		data, err := readSearchFile(ctx, f, 1<<20+1)
		f.Close()
		if err != nil {
			return err
		}
		bytesRead += int64(len(data))
		result.Scanned++
		if len(data) > 1<<20 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, query) {
				continue
			}
			if len(line) > 2048 {
				line = line[:2048]
				for !utf8.ValidString(line) {
					line = line[:len(line)-1]
				}
			}
			result.Matches = append(result.Matches, Match{path, i + 1, line})
			if len(result.Matches) >= 200 {
				result.Truncated = true
				return fs.SkipAll
			}
		}
		return nil
	})
	return result, err
}

func readSearchFile(ctx context.Context, f fs.File, limit int64) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, 32<<10)
	for out.Len() <= int(limit) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		want := len(buf)
		remaining := limit - int64(out.Len())
		if remaining < int64(want) {
			want = int(remaining)
		}
		if want == 0 {
			break
		}
		n, err := f.Read(buf[:want])
		if n > 0 {
			_, _ = out.Write(buf[:n])
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if n == 0 {
			break
		}
	}
	return out.Bytes(), nil
}

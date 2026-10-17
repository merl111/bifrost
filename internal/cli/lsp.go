package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"bifrost/internal/lsp"
)

// runLSP uses only the explicit command after --. Repository configuration is
// never consulted to discover or execute a language server.
func runLSP(ctx context.Context, opts options, args []string, stdout io.Writer) error {
	if len(args) < 2 {
		return errors.New("usage: lsp check -- COMMAND... | lsp hover PATH LINE CHAR -- COMMAND...")
	}
	sep := -1
	for i, arg := range args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 1 || sep == len(args)-1 {
		return errors.New("lsp requires an explicit server command after --")
	}
	mode := args[0]
	root, err := resolveRoot(opts)
	if err != nil {
		return fmt.Errorf("resolve LSP workspace: %w", err)
	}
	c, err := lsp.Start(ctx, lsp.Config{Command: append([]string(nil), args[sep+1:]...), WorkspaceRoot: root})
	if err != nil {
		return err
	}
	defer c.Close()
	switch mode {
	case "check":
		if opts.json {
			return json.NewEncoder(stdout).Encode(map[string]string{"status": "initialized"})
		}
		_, _ = fmt.Fprintln(stdout, "language server initialized")
		return nil
	case "hover", "definition":
		if sep != 4 {
			return errors.New("usage: lsp hover|definition PATH LINE CHAR -- COMMAND...")
		}
		line, err := strconv.Atoi(args[2])
		if err != nil || line < 0 {
			return errors.New("invalid hover line")
		}
		character, err := strconv.Atoi(args[3])
		if err != nil || character < 0 {
			return errors.New("invalid hover character")
		}
		path, err := rootedLSPPath(root, args[1])
		if err != nil {
			return err
		}
		text, err := readLSPDocument(root, path)
		if err != nil {
			return err
		}
		if err := c.DidOpen(ctx, path, lspLanguageID(path), string(text), 1); err != nil {
			return fmt.Errorf("open LSP document: %w", err)
		}
		var value json.RawMessage
		if mode == "hover" {
			value, err = c.Hover(ctx, path, line, character)
		} else {
			value, err = c.Definition(ctx, path, line, character)
		}
		if err != nil {
			return err
		}
		if opts.json {
			_, _ = stdout.Write(value)
			_, _ = fmt.Fprintln(stdout)
			return nil
		}
		var pretty any
		if json.Unmarshal(value, &pretty) == nil {
			b, _ := json.MarshalIndent(pretty, "", "  ")
			_, _ = stdout.Write(b)
			_, _ = fmt.Fprintln(stdout)
		} else {
			_, _ = stdout.Write(value)
			_, _ = fmt.Fprintln(stdout)
		}
		return nil
	default:
		return fmt.Errorf("unknown lsp command %q", mode)
	}
}

const maxLSPDocumentBytes = 8 << 20

func rootedLSPPath(root, value string) (string, error) {
	if strings.TrimSpace(value) == "" || strings.ContainsRune(value, '\x00') {
		return "", errors.New("LSP document path is empty or contains NUL")
	}
	path := value
	if filepath.IsAbs(path) {
		path = filepath.Clean(path)
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", errors.New("LSP document is outside workspace root")
		}
		return path, nil
	}
	clean := filepath.Clean(path)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("LSP document is outside workspace root")
	}
	return filepath.Join(root, clean), nil
}

func readLSPDocument(root, path string) ([]byte, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("LSP document is outside workspace root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, fmt.Errorf("open LSP workspace: %w", err)
	}
	defer r.Close()
	f, err := r.Open(filepath.ToSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("read LSP document: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("LSP document is not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLSPDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxLSPDocumentBytes {
		return nil, errors.New("LSP document exceeds 8 MiB")
	}
	if !utf8.Valid(b) {
		return nil, errors.New("LSP document is not valid UTF-8")
	}
	return b, nil
}

func lspLanguageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".js", ".jsx":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".cc", ".cpp", ".cxx", ".h", ".hpp":
		return "cpp"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "plaintext"
	}
}

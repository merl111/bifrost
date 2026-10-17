package main

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"

	"bifrost/internal/ui"
)

func (g *desktop) openChatLink(raw string) {
	parsed, err := url.Parse(raw)
	if err != nil {
		g.showError(err)
		return
	}
	if parsed.Scheme == "http" || parsed.Scheme == "https" {
		if parsed.Host == "" {
			g.showError(fmt.Errorf("link has no host"))
			return
		}
		g.work(func(context.Context) func() {
			err := ui.OpenExternalURL(raw)
			return func() {
				if err != nil {
					g.showError(err)
				}
			}
		})
		return
	}
	path, line, err := sourceLink(parsed, raw)
	if err != nil {
		g.showError(err)
		return
	}
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(g.ws.Root, path)
		if err != nil {
			g.showError(err)
			return
		}
		path = rel
	}
	path = strings.TrimPrefix(path, "./")
	if line > 0 {
		g.displayViewer(ui.ViewerCommandPayload{Action: "open", Path: path, Line: line})
	} else {
		g.openEditor(path)
	}
}

// sourceLink decodes file links without bypassing the rooted read service.
func sourceLink(parsed *url.URL, raw string) (string, int, error) {
	path, line := parsed.Path, 0
	if parsed.Scheme == "file" {
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", 0, fmt.Errorf("remote file URLs are unsupported")
		}
	} else if parsed.Scheme != "" {
		// Markdown also commonly uses filename.go:123 references.
		if n, err := strconv.Atoi(parsed.Opaque); err == nil && n > 0 {
			path, line = parsed.Scheme, n
		} else {
			return "", 0, fmt.Errorf("unsupported link scheme %q", parsed.Scheme)
		}
	} else if i := strings.LastIndex(path, ":"); i >= 0 {
		if n, err := strconv.Atoi(path[i+1:]); err == nil && n > 0 {
			path, line = path[:i], n
		}
	}
	if strings.HasPrefix(parsed.Fragment, "L") {
		line, _ = strconv.Atoi(strings.Split(parsed.Fragment[1:], "-")[0])
	}
	if path == "" {
		return "", 0, fmt.Errorf("link has no file path: %q", raw)
	}
	return path, line, nil
}

// Package browsertools exposes an explicitly enabled, isolated Chromium tab to
// the agent. Observations are accessibility text; visual model input is separate.
package browsertools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/browser"
	"bifrost/internal/provider"
)

const maxScreenshotBytes = 8 << 20

type tab interface {
	Navigate(string) error
	Snapshot() (browser.Snapshot, error)
	Click(string) error
	Type(string, string) error
	Screenshot() ([]byte, error)
	Close() error
}

type controller struct {
	mu           sync.Mutex
	ctx          context.Context
	ctxForLaunch context.Context
	launch       func(context.Context) (tab, error)
	tab          tab
	cancel       context.CancelFunc
	closed       bool
	image        *provider.Image
}

// Open prepares tools without launching a browser. The first navigation creates
// a fresh profile. Cancellation discards that tab; a later navigation can start
// a new one, but interrupted interactions are never retried.
func Open(ctx context.Context, cfg browser.Config) ([]agent.Tool, func()) {
	if ctx == nil {
		ctx = context.Background()
	}
	c := &controller{ctx: ctx, launch: func(ctx context.Context) (tab, error) { return browser.Launch(ctx, cfg) }}
	return c.tools(), c.close
}

func (c *controller) tools() []agent.Tool {
	return []agent.Tool{
		c.tool("browser_navigate", "Navigate an isolated Chromium tab to an HTTP(S) URL and return accessibility references.", `{"type":"object","required":["url"],"properties":{"url":{"type":"string"}}}`),
		c.tool("browser_snapshot", "Read the current tab accessibility tree; refreshes element references.", `{"type":"object","properties":{}}`),
		c.tool("browser_click", "Click an element using a reference from the latest snapshot; returns a fresh snapshot.", `{"type":"object","required":["ref"],"properties":{"ref":{"type":"string"}}}`),
		c.tool("browser_type", "Focus an input and insert text at its caret using a current snapshot reference; returns a fresh snapshot.", `{"type":"object","required":["ref","text"],"properties":{"ref":{"type":"string"},"text":{"type":"string"}}}`),
		c.tool("browser_screenshot", "Capture the current viewport as a bounded PNG image.", `{"type":"object","properties":{}}`),
	}
}

func (c *controller) tool(name, description, schema string) agent.Tool {
	return agent.Tool{Definition: provider.ToolDefinition{Name: name, Description: description, Parameters: json.RawMessage(schema)}, Call: func(ctx context.Context, raw json.RawMessage) (string, error) { return c.call(ctx, name, raw) }, Images: c.takeImage}
}

type arguments struct {
	URL  string  `json:"url"`
	Ref  string  `json:"ref"`
	Text *string `json:"text"`
}

func parseArguments(name string, raw json.RawMessage) (arguments, error) {
	var a arguments
	if len(raw) > 128<<10 {
		return a, errors.New("browser arguments exceed limit")
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, err
	}
	switch name {
	case "browser_navigate":
		u, err := url.Parse(a.URL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
			return a, errors.New("browser URL must use HTTP(S), a hostname, and no embedded credentials")
		}
	case "browser_click", "browser_type":
		if strings.TrimSpace(a.Ref) == "" {
			return a, errors.New("browser ref is required")
		}
		if name == "browser_type" && a.Text == nil {
			return a, errors.New("browser text is required")
		}
	}
	return a, nil
}

func (c *controller) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	a, err := parseArguments(name, raw)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.image = nil
	if c.closed {
		return "", browser.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.tab == nil && name != "browser_navigate" {
		return "", errors.New("navigate first: no active browser tab")
	}
	// Register cancellation before launch. Canceling a turn also cancels an
	// already-running tab's current operation, without replaying it on a new tab.
	if c.cancel == nil {
		var launchCtx context.Context
		launchCtx, c.cancel = context.WithCancel(c.ctx)
		c.ctxForLaunch = launchCtx
	}
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { c.cancel(); close(done) })
	defer func() {
		if !stop() {
			<-done
		}
		if ctx.Err() != nil || c.tab == nil {
			c.reset()
		}
	}()
	if c.tab == nil {
		c.tab, err = c.launch(c.ctxForLaunch)
		if err != nil {
			return "", err
		}
	}
	if err := c.perform(name, a); err != nil {
		return "", err
	}
	if name == "browser_screenshot" {
		data, err := c.tab.Screenshot()
		if err != nil {
			return "", err
		}
		if len(data) == 0 || len(data) > maxScreenshotBytes {
			return "", fmt.Errorf("browser screenshot exceeds %d bytes", maxScreenshotBytes)
		}
		c.image = &provider.Image{MediaType: "image/png", Data: append([]byte(nil), data...)}
	}
	snapshot, err := c.tab.Snapshot()
	if err != nil {
		c.image = nil
		return "", err
	}
	b, err := json.Marshal(snapshot)
	if err != nil {
		c.image = nil
	}
	return string(b), err
}

func (c *controller) perform(name string, a arguments) error {
	switch name {
	case "browser_navigate":
		return c.tab.Navigate(a.URL)
	case "browser_snapshot":
		return nil
	case "browser_click":
		return c.tab.Click(a.Ref)
	case "browser_type":
		return c.tab.Type(a.Ref, *a.Text)
	case "browser_screenshot":
		return nil
	default:
		return errors.New("unknown browser tool")
	}
}

func (c *controller) reset() {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
	}
	if c.tab != nil {
		_ = c.tab.Close()
		c.tab = nil
	}
	c.image = nil
}

func (c *controller) takeImage() []provider.Image {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.image == nil {
		return nil
	}
	image := *c.image
	image.Data = append([]byte(nil), image.Data...)
	c.image = nil
	return []provider.Image{image}
}

func (c *controller) close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.reset()
}

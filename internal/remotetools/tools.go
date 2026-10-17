// Package remotetools adapts explicitly enabled worker browser/desktop RPC to
// agent tools. It never falls back to host controls when the worker lacks a
// capability.
package remotetools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/environment"
	"bifrost/internal/provider"
	"bifrost/internal/worker"
)

const maxImageBytes = worker.MaxRemoteImageBytes

type controller struct {
	mu      sync.Mutex
	ctx     context.Context
	backend environment.Backend
	browser environment.BrowserController
	desktop environment.DesktopController
	closed  bool
	image   *provider.Image
}

func Open(ctx context.Context, backend environment.Backend, browserEnabled, desktopEnabled bool) ([]agent.Tool, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if backend == nil {
		return nil, func() {}, errors.New("remote tools require a worker backend")
	}
	c := &controller{ctx: ctx, backend: backend}
	if browserEnabled {
		if !backend.Identity().Capabilities.Browser {
			return nil, func() {}, errors.New("worker browser capability is disabled")
		}
		var ok bool
		c.browser, ok = backend.(environment.BrowserController)
		if !ok {
			return nil, func() {}, errors.New("backend does not expose worker browser controls")
		}
	}
	if desktopEnabled {
		if !backend.Identity().Capabilities.Desktop {
			return nil, func() {}, errors.New("worker desktop capability is disabled")
		}
		var ok bool
		c.desktop, ok = backend.(environment.DesktopController)
		if !ok {
			return nil, func() {}, errors.New("backend does not expose worker desktop controls")
		}
	}
	return c.tools(browserEnabled, desktopEnabled), c.close, nil
}

func (c *controller) tools(browserEnabled, desktopEnabled bool) []agent.Tool {
	var out []agent.Tool
	if browserEnabled {
		out = append(out,
			c.tool("browser_navigate", "Navigate the explicitly configured worker Chromium tab.", `{"type":"object","required":["url"],"properties":{"url":{"type":"string"}}}`),
			c.tool("browser_snapshot", "Read the worker browser accessibility tree.", `{"type":"object","properties":{}}`),
			c.tool("browser_click", "Click a current worker browser accessibility ref.", `{"type":"object","required":["ref"],"properties":{"ref":{"type":"string"}}}`),
			c.tool("browser_type", "Type into a current worker browser accessibility ref.", `{"type":"object","required":["ref","text"],"properties":{"ref":{"type":"string"},"text":{"type":"string"}}}`),
			c.tool("browser_screenshot", "Capture a bounded worker browser screenshot.", `{"type":"object","properties":{}}`))
	}
	if desktopEnabled {
		out = append(out,
			c.tool("desktop_capabilities", "Report worker desktop capabilities.", `{"type":"object","properties":{}}`),
			c.tool("desktop_capture", "Capture a fresh worker desktop observation.", `{"type":"object","properties":{}}`),
			c.tool("desktop_move", "Move the worker pointer using the latest observation.", `{"type":"object","required":["observation_id","display","x","y"]}`),
			c.tool("desktop_click", "Click the worker display using the latest observation.", `{"type":"object","required":["observation_id","display","x","y"]}`),
			c.tool("desktop_keypress", "Send worker display key names using the latest observation.", `{"type":"object","required":["observation_id","display","keys"]}`),
			c.tool("desktop_type", "Type worker display text using the latest observation.", `{"type":"object","required":["observation_id","display","text"]}`))
	}
	return out
}

func (c *controller) tool(name, description, schema string) agent.Tool {
	return agent.Tool{Definition: provider.ToolDefinition{Name: name, Description: description, Parameters: json.RawMessage(schema)}, Call: func(ctx context.Context, raw json.RawMessage) (string, error) { return c.call(ctx, name, raw) }, Images: c.takeImage}
}

type desktopArgs struct {
	ObservationID, Display string
	X, Y, Button           int
	Keys                   []string
	Text                   string
}

func (c *controller) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("remote tools are closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.image = nil
	switch name {
	case "browser_navigate":
		var a struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
		v, err := c.browser.BrowserNavigate(ctx, a.URL)
		return marshal(v, &c.image, nil, err)
	case "browser_snapshot":
		v, err := c.browser.BrowserSnapshot(ctx)
		return marshal(v, &c.image, nil, err)
	case "browser_click":
		var a struct {
			Ref string `json:"ref"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
		v, err := c.browser.BrowserClick(ctx, a.Ref)
		return marshal(v, &c.image, nil, err)
	case "browser_type":
		var a struct {
			Ref  string `json:"ref"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
		v, err := c.browser.BrowserType(ctx, a.Ref, a.Text)
		return marshal(v, &c.image, nil, err)
	case "browser_screenshot":
		data, err := c.browser.BrowserScreenshot(ctx)
		return marshal(map[string]any{"bytes": len(data)}, &c.image, data, err)
	case "desktop_capabilities":
		v, err := c.desktop.DesktopCapabilities(ctx)
		return marshal(v, &c.image, nil, err)
	case "desktop_capture":
		v, err := c.desktop.DesktopCapture(ctx)
		return marshal(v, &c.image, v.PNG, err)
	case "desktop_move", "desktop_click", "desktop_keypress", "desktop_type":
		var a desktopArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
		action := worker.DesktopAction{ObservationID: a.ObservationID, Display: a.Display, X: a.X, Y: a.Y, Button: a.Button, Keys: a.Keys, Text: a.Text}
		var err error
		switch name {
		case "desktop_move":
			err = c.desktop.DesktopMove(ctx, action)
		case "desktop_click":
			err = c.desktop.DesktopClick(ctx, action)
		case "desktop_keypress":
			err = c.desktop.DesktopKeyPress(ctx, action)
		case "desktop_type":
			err = c.desktop.DesktopType(ctx, action)
		}
		if err != nil {
			return "", err
		}
		return `{"ok":true,"capture_required":true}`, nil
	default:
		return "", fmt.Errorf("unknown remote tool %q", name)
	}
}

func marshal(value any, image **provider.Image, data []byte, err error) (string, error) {
	if err != nil {
		return "", err
	}
	if len(data) > maxImageBytes {
		return "", fmt.Errorf("remote image exceeds %d bytes", maxImageBytes)
	}
	if len(data) > 0 {
		*image = &provider.Image{MediaType: "image/png", Data: append([]byte(nil), data...)}
	}
	b, err := json.Marshal(value)
	return string(b), err
}

func (c *controller) takeImage() []provider.Image {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.image == nil {
		return nil
	}
	v := *c.image
	v.Data = append([]byte(nil), v.Data...)
	c.image = nil
	return []provider.Image{v}
}
func (c *controller) close() { c.mu.Lock(); defer c.mu.Unlock(); c.closed = true; c.image = nil }

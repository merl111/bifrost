// Package desktoptools adapts the observation-based desktop package to agent
// tools. Every action requires the latest capture observation and a fresh
// capture is required after an action.
package desktoptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"bifrost/internal/agent"
	"bifrost/internal/desktop"
	"bifrost/internal/provider"
)

const maxImageBytes = 8 << 20

type controller struct {
	mu      sync.Mutex
	ctx     context.Context
	manager *desktop.Manager
	session *desktop.Session
	closed  bool
	image   *provider.Image
}

// Open detects the current display once and returns a persistent tool set.
// The interaction lease is held only while this adapter is active; callers
// should invoke the returned cleanup function when the agent run ends.
func Open(ctx context.Context, cfg desktop.Config) ([]agent.Tool, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	manager, err := desktop.New(cfg)
	if err != nil {
		return nil, func() {}, err
	}
	c := &controller{ctx: ctx, manager: manager}
	return c.tools(), c.close, nil
}

func (c *controller) tools() []agent.Tool {
	return []agent.Tool{
		c.tool("desktop_capabilities", "Report detected desktop backend capabilities.", `{"type":"object","properties":{}}`),
		c.tool("desktop_capture", "Capture a fresh desktop observation. Actions must use its observation_id and display.", `{"type":"object","properties":{}}`),
		c.tool("desktop_move", "Move the pointer using the latest observation coordinates.", `{"type":"object","required":["observation_id","display","x","y"],"properties":{"observation_id":{"type":"string"},"display":{"type":"string"},"x":{"type":"integer"},"y":{"type":"integer"}}}`),
		c.tool("desktop_click", "Click using the latest observation coordinates.", `{"type":"object","required":["observation_id","display","x","y"],"properties":{"observation_id":{"type":"string"},"display":{"type":"string"},"x":{"type":"integer"},"y":{"type":"integer"},"button":{"type":"integer"}}}`),
		c.tool("desktop_keypress", "Send explicit key names using the latest observation.", `{"type":"object","required":["observation_id","display","keys"],"properties":{"observation_id":{"type":"string"},"display":{"type":"string"},"keys":{"type":"array","items":{"type":"string"}}}}`),
		c.tool("desktop_type", "Type bounded text using the latest observation.", `{"type":"object","required":["observation_id","display","text"],"properties":{"observation_id":{"type":"string"},"display":{"type":"string"},"text":{"type":"string"}}}`),
	}
}

func (c *controller) tool(name, description, schema string) agent.Tool {
	return agent.Tool{
		Definition: provider.ToolDefinition{Name: name, Description: description, Parameters: json.RawMessage(schema)},
		Call:       func(ctx context.Context, raw json.RawMessage) (string, error) { return c.call(ctx, name, raw) },
		Images:     c.takeImage,
	}
}

type arguments struct {
	ObservationID string   `json:"observation_id"`
	Display       string   `json:"display"`
	X             int      `json:"x"`
	Y             int      `json:"y"`
	Button        int      `json:"button"`
	Keys          []string `json:"keys"`
	Text          string   `json:"text"`
}

func (c *controller) call(ctx context.Context, name string, raw json.RawMessage) (string, error) {
	if len(raw) > 128<<10 {
		return "", errors.New("desktop arguments exceed limit")
	}
	var a arguments
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", errors.New("desktop tools are closed")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.image = nil
	if name == "desktop_capabilities" {
		return marshal(c.manager.Capabilities())
	}
	if c.session == nil {
		var err error
		c.session, err = c.manager.Acquire(ctx)
		if err != nil {
			return "", err
		}
	}
	var err error
	switch name {
	case "desktop_capture":
		var observation desktop.Observation
		observation, err = c.session.Capture(ctx)
		if err == nil {
			err = c.loadImage(observation.PNGPath)
			if err == nil {
				return marshal(observation)
			}
		}
	case "desktop_move":
		err = c.session.Move(ctx, a.ObservationID, a.Display, a.X, a.Y)
	case "desktop_click":
		err = c.session.Click(ctx, a.ObservationID, a.Display, a.X, a.Y, a.Button)
	case "desktop_keypress":
		err = c.session.KeyPress(ctx, a.ObservationID, a.Display, a.Keys...)
	case "desktop_type":
		err = c.session.Type(ctx, a.ObservationID, a.Display, a.Text)
	default:
		err = errors.New("unknown desktop tool")
	}
	if err != nil {
		return "", err
	}
	return `{"ok":true,"capture_required":true}`, nil
}

func (c *controller) loadImage(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("read desktop screenshot: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxImageBytes+1))
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return errors.New("desktop screenshot exceeds image limit")
	}
	c.image = &provider.Image{MediaType: "image/png", Data: data}
	return nil
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
	if c.closed {
		return
	}
	c.closed = true
	if c.session != nil {
		_ = c.session.Close()
		c.session = nil
	}
	c.image = nil
}

func marshal(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

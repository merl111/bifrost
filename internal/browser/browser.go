// Package browser provides the bounded local browser capability used by the
// workspace. It talks to Chromium through the Chrome DevTools Protocol; no
// Node.js or browser-side application runtime is required.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"
)

const (
	defaultTimeout       = 15 * time.Second
	defaultMaxNodes      = 2000
	defaultMaxTextBytes  = 64 * 1024
	defaultProfilePrefix = "bifrost-browser-"
)

var ErrClosed = errors.New("browser session is closed")

// Config controls one browser session. UserDataDir is created and removed by
// Launch when empty. A caller supplied profile is never removed by Close.
type Config struct {
	Timeout      time.Duration
	ChromePath   string
	UserDataDir  string
	MaxNodes     int
	MaxTextBytes int
}

func (c Config) normalized() Config {
	if c.Timeout <= 0 {
		c.Timeout = defaultTimeout
	}
	if c.MaxNodes <= 0 {
		c.MaxNodes = defaultMaxNodes
	}
	if c.MaxTextBytes <= 0 {
		c.MaxTextBytes = defaultMaxTextBytes
	}
	return c
}

type reference struct {
	backend    cdp.BackendNodeID
	actionable bool
}

// Session is a single serialized browser tab. Methods are safe to call from
// multiple goroutines, though operations execute in order on the same page.
type Session struct {
	mu sync.Mutex

	ctx             context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
	config          Config
	profileDir      string
	removeProfile   bool
	closed          bool
	refs            map[string]reference
	generation      uint64
}

// Launch starts a headless Chromium instance with an isolated profile.
func Launch(parent context.Context, config Config) (*Session, error) {
	if parent == nil {
		parent = context.Background()
	}
	config = config.normalized()
	profile := config.UserDataDir
	owned := false
	var err error
	if profile == "" {
		profile, err = os.MkdirTemp("", defaultProfilePrefix)
		if err != nil {
			return nil, fmt.Errorf("create browser profile: %w", err)
		}
		owned = true
	} else if err := os.MkdirAll(profile, 0o700); err != nil {
		return nil, fmt.Errorf("create browser profile: %w", err)
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.UserDataDir(profile))
	if config.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(config.ChromePath))
	}
	allocCtx, allocCancel := chromedp.NewExecAllocator(parent, opts...)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	s := &Session{ctx: tabCtx, cancel: tabCancel, allocatorCancel: allocCancel, config: config, profileDir: profile, removeProfile: owned, refs: make(map[string]reference)}
	if err := s.initialize(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("start browser: %w", err)
	}
	return s, nil
}

// Connect attaches to an already running Chromium DevTools endpoint. The
// endpoint can be an HTTP debugging URL or a browser websocket URL.
func Connect(parent context.Context, endpoint string, config Config) (*Session, error) {
	if strings.TrimSpace(endpoint) == "" {
		return nil, fmt.Errorf("browser endpoint is empty")
	}
	if parent == nil {
		parent = context.Background()
	}
	config = config.normalized()
	allocCtx, allocCancel := remote.NewAllocator(parent, endpoint)
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	s := &Session{ctx: tabCtx, cancel: tabCancel, allocatorCancel: allocCancel, config: config, refs: make(map[string]reference)}
	if err := s.initialize(); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	return s, nil
}

// The initial Run owns the browser lifetime. Never give it a per-operation
// context that is canceled as soon as startup succeeds.
func (s *Session) initialize() error {
	timer := time.AfterFunc(s.config.Timeout, s.cancel)
	_, err := chromedp.Run(s.ctx, chromedp.Navigate("about:blank"))
	timer.Stop()
	return err
}

func run[T any](s *Session, action chromedp.Action[T]) (T, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return runLocked(s, action)
}

func runLocked[T any](s *Session, action chromedp.Action[T]) (T, error) {
	var zero T
	if s.closed {
		return zero, ErrClosed
	}
	ctx, cancel := context.WithTimeout(s.ctx, s.config.Timeout)
	defer cancel()
	return chromedp.Run(ctx, action)
}

// Close stops the tab and, for Launch sessions, the browser and its temporary
// profile. It is idempotent.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel, allocatorCancel := s.cancel, s.allocatorCancel
	profile, remove := s.profileDir, s.removeProfile
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if allocatorCancel != nil {
		allocatorCancel()
	}
	if remove && profile != "" {
		return os.RemoveAll(filepath.Clean(profile))
	}
	return nil
}

// Navigate loads a URL and waits for Chrome's navigation action to complete.
func (s *Session) Navigate(url string) error {
	if strings.TrimSpace(url) == "" {
		return fmt.Errorf("URL is empty")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs = make(map[string]reference)
	_, err := runLocked(s, chromedp.Navigate(url))
	return err
}

// Screenshot captures the current viewport as PNG bytes.
func (s *Session) Screenshot() ([]byte, error) { return run(s, chromedp.CaptureScreenshot()) }

// Node is an accessibility-tree observation. Ref is valid until the next
// Snapshot or navigation and is accepted by Click and Type.
type Node struct {
	Ref         string
	Role        string
	Name        string
	Value       string
	Description string
	ParentRef   string
	Children    []string
	BackendID   string
	Ignored     bool
	Actionable  bool
}

type Snapshot struct {
	URL   string
	Title string
	Nodes []Node
}

// Snapshot returns a bounded accessibility tree with stable per-snapshot
// refs. Backend IDs remain internal to the session; callers use Ref values.
func (s *Session) Snapshot() (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	type result struct {
		url   string
		title string
		nodes []*accessibility.Node
	}
	r, err := runLocked(s, func(ctx context.Context, target *chromedp.Target) (result, error) {
		if _, err := cdp.Call(ctx, target, accessibility.Enable, cdp.Empty{}); err != nil {
			return result{}, err
		}
		ax, err := cdp.Call(ctx, target, accessibility.GetFullAXTree, accessibility.GetFullAXTreeParams{})
		if err != nil {
			return result{}, err
		}
		url, err := chromedp.Location()(ctx, target)
		if err != nil {
			return result{}, err
		}
		title, err := chromedp.Title()(ctx, target)
		if err != nil {
			return result{}, err
		}
		return result{url: url, title: title, nodes: ax.Nodes}, nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	if len(r.nodes) > s.config.MaxNodes {
		r.nodes = r.nodes[:s.config.MaxNodes]
	}
	s.generation++
	s.refs = make(map[string]reference, len(r.nodes))
	budget := s.config.MaxTextBytes
	out := Snapshot{URL: takeText(r.url, &budget), Title: takeText(r.title, &budget), Nodes: make([]Node, 0, len(r.nodes))}
	refByAXID := make(map[accessibility.NodeID]string, len(r.nodes))
	for i, n := range r.nodes {
		if n == nil {
			continue
		}
		ref := fmt.Sprintf("node-%d-%d", s.generation, i+1)
		actionable := isActionableNode(n)
		s.refs[ref] = reference{backend: n.BackendDOMNodeID, actionable: actionable}
		node := Node{Ref: ref, BackendID: fmt.Sprint(n.BackendDOMNodeID), Ignored: n.Ignored, Actionable: actionable}
		node.Role, node.Name, node.Value, node.Description = takeText(axValue(n.Role), &budget), takeText(axValue(n.Name), &budget), takeText(axValue(n.Value), &budget), takeText(axValue(n.Description), &budget)
		out.Nodes = append(out.Nodes, node)
		refByAXID[n.NodeID] = ref
	}
	// Resolve relationships in a second pass, so refs remain stable even when
	// Chrome omits ignored nodes from a future bounded result.
	for _, n := range r.nodes {
		if n == nil {
			continue
		}
		index := -1
		for i := range out.Nodes {
			if out.Nodes[i].Ref == refByAXID[n.NodeID] {
				index = i
				break
			}
		}
		if index < 0 {
			continue
		}
		if n.ParentID != "" {
			if p := refByAXID[n.ParentID]; p != "" {
				out.Nodes[index].ParentRef = p
			}
		}
		for _, child := range n.ChildIDs {
			if ref := refByAXID[child]; ref != "" {
				out.Nodes[index].Children = append(out.Nodes[index].Children, ref)
			}
		}
	}
	return out, nil
}

func axValue(v *accessibility.Value) string {
	if v == nil || len(v.Value) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(v.Value, &text) == nil {
		return text
	}
	return strings.Trim(string(v.Value), `"`)
}

func (s *Session) backend(ref string) (cdp.BackendNodeID, error) {
	if s.closed {
		return 0, ErrClosed
	}
	node, ok := s.refs[ref]
	if !ok || node.backend == 0 || !node.actionable {
		return 0, fmt.Errorf("unknown or non-actionable node ref %q", ref)
	}
	return node.backend, nil
}

var actionableRoles = map[string]struct{}{
	"button": {}, "checkbox": {}, "combobox": {}, "gridcell": {},
	"link": {}, "listbox": {}, "menuitem": {}, "menuitemcheckbox": {},
	"menuitemradio": {}, "option": {}, "radio": {}, "scrollbar": {},
	"searchbox": {}, "slider": {}, "spinbutton": {}, "switch": {},
	"tab": {}, "textbox": {}, "treeitem": {},
}

func isActionableNode(node *accessibility.Node) bool {
	if node == nil || node.Ignored || node.BackendDOMNodeID == 0 {
		return false
	}
	role := strings.ToLower(axValue(node.Role))
	if _, ok := actionableRoles[role]; ok {
		return true
	}
	for _, property := range node.Properties {
		if property == nil || property.Value == nil {
			continue
		}
		switch property.Name {
		case accessibility.PropertyNameFocusable, accessibility.PropertyNameEditable, accessibility.PropertyNameSettable:
			if axBool(property.Value) {
				return true
			}
		}
	}
	return false
}

func axBool(value *accessibility.Value) bool {
	if value == nil {
		return false
	}
	b, err := json.Marshal(value.Value)
	if err != nil {
		return false
	}
	var out bool
	return json.Unmarshal(b, &out) == nil && out
}

// Click clicks the center of a node from the latest Snapshot.
func (s *Session) Click(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	backend, err := s.backend(ref)
	if err != nil {
		return err
	}
	_, err = runLocked(s, func(ctx context.Context, target *chromedp.Target) (struct{}, error) {
		if _, err := cdp.Call(ctx, target, dom.ScrollIntoViewIfNeeded, dom.ScrollIntoViewIfNeededParams{BackendNodeID: backend}); err != nil {
			return struct{}{}, err
		}
		box, err := cdp.Call(ctx, target, dom.GetContentQuads, dom.GetContentQuadsParams{BackendNodeID: backend})
		if err != nil || len(box.Quads) == 0 || len(box.Quads[0]) < 8 {
			if err == nil {
				err = fmt.Errorf("node has no visible content quad")
			}
			return struct{}{}, err
		}
		q := box.Quads[0]
		x, y := (q[0]+q[2]+q[4]+q[6])/4, (q[1]+q[3]+q[5]+q[7])/4
		for _, typ := range []input.DispatchMouseEventType{input.DispatchMouseEventTypeMousePressed, input.DispatchMouseEventTypeMouseReleased} {
			if _, err := cdp.Call(ctx, target, input.DispatchMouseEvent, input.DispatchMouseEventParams{Type: typ, X: x, Y: y, Button: input.MouseButtonLeft, ClickCount: 1}); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, nil
	})
	return err
}

// Type focuses a node and inserts text into the active input or contenteditable
// element via the native CDP input protocol. Text is inserted at the caret.
func (s *Session) Type(ref, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(text) > s.config.MaxTextBytes {
		return fmt.Errorf("input exceeds %d bytes", s.config.MaxTextBytes)
	}
	backend, err := s.backend(ref)
	if err != nil {
		return err
	}
	_, err = runLocked(s, func(ctx context.Context, target *chromedp.Target) (struct{}, error) {
		if _, err := cdp.Call(ctx, target, dom.Focus, dom.FocusParams{BackendNodeID: backend}); err != nil {
			return struct{}{}, err
		}
		if _, err := cdp.Call(ctx, target, input.InsertText, input.InsertTextParams{Text: text}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, nil
	})
	return err
}

func takeText(text string, budget *int) string {
	if len(text) <= *budget {
		*budget -= len(text)
		return text
	}
	text = text[:*budget]
	for !utf8.ValidString(text) && len(text) > 0 {
		text = text[:len(text)-1]
	}
	*budget = 0
	return text
}

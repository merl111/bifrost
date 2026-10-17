package browser

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/cdp/jsonv2"
)

func TestConfigNormalizesBounds(t *testing.T) {
	c := (Config{}).normalized()
	if c.Timeout <= 0 || c.MaxNodes <= 0 || c.MaxTextBytes <= 0 {
		t.Fatalf("normalized config = %#v", c)
	}
}

func TestSessionCloseIsIdempotent(t *testing.T) {
	s := &Session{closed: false}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAXActionableExcludesStaticLabels(t *testing.T) {
	label := &accessibility.Node{BackendDOMNodeID: cdp.BackendNodeID(1), Role: &accessibility.Value{Value: jsonv2.Value(`"label"`)}, Name: &accessibility.Value{Value: jsonv2.Value(`"Name"`)}}
	textbox := &accessibility.Node{BackendDOMNodeID: cdp.BackendNodeID(2), Role: &accessibility.Value{Value: jsonv2.Value(`"textbox"`)}, Name: &accessibility.Value{Value: jsonv2.Value(`"Name"`)}}
	focusable := &accessibility.Node{BackendDOMNodeID: cdp.BackendNodeID(3), Role: &accessibility.Value{Value: jsonv2.Value(`"generic"`)}, Properties: []*accessibility.Property{{Name: accessibility.PropertyNameFocusable, Value: &accessibility.Value{Value: jsonv2.Value("true")}}}}
	ignoredButton := &accessibility.Node{BackendDOMNodeID: cdp.BackendNodeID(4), Ignored: true, Role: &accessibility.Value{Value: jsonv2.Value(`"button"`)}}
	if isActionableNode(label) {
		t.Fatal("static label was marked actionable")
	}
	if !isActionableNode(textbox) || !isActionableNode(focusable) {
		t.Fatal("textbox/focusable node was not actionable")
	}
	if isActionableNode(ignoredButton) {
		t.Fatal("ignored button was marked actionable")
	}
	s := &Session{refs: map[string]reference{"label": {backend: 1}, "textbox": {backend: 2, actionable: true}}}
	if _, err := s.backend("label"); err == nil {
		t.Fatal("static label ref was accepted for actions")
	}
	if _, err := s.backend("textbox"); err != nil {
		t.Fatalf("textbox ref rejected: %v", err)
	}
}

func TestBrowserFixtureIntegration(t *testing.T) {
	if os.Getenv("BIFROST_BROWSER_TEST") != "1" {
		t.Skip("set BIFROST_BROWSER_TEST=1 for Chromium integration")
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("local fixture listener unavailable: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><title>Bifrost fixture</title></head><body>
<main><label for="name">Name</label><input id="name" aria-label="Name"><button id="go" onclick="document.querySelector('#result').textContent='clicked:'+document.querySelector('#name').value">Run</button><p id="result">idle</p></main>
</body></html>`))
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := Launch(ctx, Config{ChromePath: "/usr/bin/chromium", Timeout: 8 * time.Second})
	if err != nil {
		t.Fatalf("Chromium unavailable: %v", err)
	}
	defer s.Close()
	if err := s.Navigate(server.URL); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var inputRef, buttonRef string
	staticNameSeen := false
	for _, node := range snap.Nodes {
		if strings.EqualFold(node.Role, "textbox") && node.Name == "Name" {
			inputRef = node.Ref
		}
		if strings.EqualFold(node.Role, "button") && node.Name == "Run" {
			buttonRef = node.Ref
		}
		if node.Name == "Name" && !strings.EqualFold(node.Role, "textbox") {
			staticNameSeen = true
			if node.Actionable {
				t.Fatal("static fixture label was marked actionable")
			}
		}
	}
	if !staticNameSeen {
		t.Log("fixture did not expose a separate static Name label in the AX tree")
	}
	if inputRef == "" || buttonRef == "" {
		t.Fatalf("actionable fixture nodes not found: input=%q button=%q snapshot=%#v", inputRef, buttonRef, snap)
	}
	if err := s.Type(inputRef, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := s.Click(buttonRef); err != nil {
		t.Fatal(err)
	}
	updated, err := s.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, node := range updated.Nodes {
		if strings.Contains(node.Name, "clicked:hello") || strings.Contains(node.Value, "clicked:hello") {
			found = true
		}
	}
	if !found {
		t.Fatalf("click/type result missing from snapshot: %#v", updated)
	}
	if err := s.Click(buttonRef); err == nil {
		t.Fatal("old snapshot ref was reused")
	}
	png, err := s.Screenshot()
	if err != nil || len(png) < 8 {
		t.Fatalf("screenshot bytes=%d err=%v", len(png), err)
	}
}

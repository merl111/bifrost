package browsertools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"bifrost/internal/browser"
)

type fakeTab struct {
	ctx         context.Context
	block       bool
	closed      bool
	navigations int
}

func (f *fakeTab) Navigate(string) error {
	f.navigations++
	if f.block {
		<-f.ctx.Done()
		return f.ctx.Err()
	}
	return nil
}
func (f *fakeTab) Snapshot() (browser.Snapshot, error) {
	return browser.Snapshot{Title: "fixture"}, nil
}
func (f *fakeTab) Click(string) error          { return nil }
func (f *fakeTab) Type(string, string) error   { return nil }
func (f *fakeTab) Screenshot() ([]byte, error) { return []byte("png"), nil }
func (f *fakeTab) Close() error                { f.closed = true; return nil }

func TestCancellationDiscardsTabWithoutReplaying(t *testing.T) {
	var tabs []*fakeTab
	c := &controller{ctx: context.Background(), launch: func(ctx context.Context) (tab, error) {
		f := &fakeTab{ctx: ctx, block: len(tabs) == 0}
		tabs = append(tabs, f)
		return f, nil
	}}
	defer c.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.call(ctx, "browser_navigate", json.RawMessage(`{"url":"http://localhost"}`))
	if !errors.Is(err, context.Canceled) || len(tabs) != 1 || !tabs[0].closed || tabs[0].navigations != 1 {
		t.Fatalf("err=%v tabs=%+v", err, tabs)
	}
	if _, err := c.call(context.Background(), "browser_snapshot", json.RawMessage(`{}`)); err == nil {
		t.Fatal("snapshot reused canceled tab")
	}
	if _, err := c.call(context.Background(), "browser_navigate", json.RawMessage(`{"url":"http://localhost"}`)); err != nil {
		t.Fatal(err)
	}
	if len(tabs) != 2 || tabs[1].navigations != 1 {
		t.Fatal("unexpected replay or restart")
	}
	c.close()
	if !tabs[1].closed {
		t.Fatal("tab leaked")
	}
}

func TestInvalidArgumentsNeverLaunch(t *testing.T) {
	c := &controller{ctx: context.Background(), launch: func(context.Context) (tab, error) { t.Fatal("unexpected browser launch"); return nil, nil }}
	for _, raw := range []string{`{"url":"file:///etc/passwd"}`, `{"url":"https://user:pass@localhost/"}`, `{"url":"javascript:alert(1)"}`, `{}`} {
		if _, err := c.call(context.Background(), "browser_navigate", json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if _, err := c.call(context.Background(), "browser_type", json.RawMessage(`{"ref":"e1"}`)); err == nil {
		t.Fatal("missing text accepted")
	}
}

func TestScreenshotImageIsBoundedAndOneShot(t *testing.T) {
	c := &controller{ctx: context.Background(), launch: func(ctx context.Context) (tab, error) { return &fakeTab{ctx: ctx}, nil }}
	defer c.close()
	if _, err := c.call(context.Background(), "browser_navigate", json.RawMessage(`{"url":"http://localhost"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(context.Background(), "browser_screenshot", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	images := c.takeImage()
	if len(images) != 1 || string(images[0].Data) != "png" || images[0].MediaType != "image/png" {
		t.Fatalf("images=%+v", images)
	}
	if got := c.takeImage(); len(got) != 0 {
		t.Fatalf("image callback was not one-shot: %+v", got)
	}
}

func TestBrowserToolsIntegration(t *testing.T) {
	if os.Getenv("BIFROST_BROWSER_TEST") != "1" {
		t.Skip("set BIFROST_BROWSER_TEST=1 for Chromium")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<title>Tools fixture</title><label>Name<input aria-label="Name"></label><button onclick="document.querySelector('p').textContent='ok:'+document.querySelector('input').value">Send</button><p>idle</p>`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tools, closeTools := Open(ctx, browser.Config{ChromePath: "/usr/bin/chromium", Timeout: 8 * time.Second})
	defer closeTools()
	call := func(name string, args map[string]string) browser.Snapshot {
		t.Helper()
		b, _ := json.Marshal(args)
		for _, tool := range tools {
			if tool.Definition.Name != name {
				continue
			}
			out, err := tool.Call(ctx, b)
			if err != nil {
				t.Fatal(err)
			}
			var snap browser.Snapshot
			if err := json.Unmarshal([]byte(out), &snap); err != nil {
				t.Fatal(err)
			}
			return snap
		}
		t.Fatalf("missing tool %s", name)
		return browser.Snapshot{}
	}
	ref := func(s browser.Snapshot, name, role string) string {
		t.Helper()
		for _, n := range s.Nodes {
			if n.Name == name && n.Role == role && n.Actionable {
				return n.Ref
			}
		}
		t.Fatalf("missing ref %q in %+v", name, s)
		return ""
	}
	snap := call("browser_navigate", map[string]string{"url": srv.URL})
	snap = call("browser_type", map[string]string{"ref": ref(snap, "Name", "textbox"), "text": "Bifrost"})
	snap = call("browser_click", map[string]string{"ref": ref(snap, "Send", "button")})
	for _, n := range snap.Nodes {
		if strings.Contains(n.Name, "ok:Bifrost") {
			return
		}
	}
	t.Fatalf("browser action result absent: %+v", snap)
}

package remotetools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"bifrost/internal/browser"
	"bifrost/internal/desktop"
	"bifrost/internal/environment"
	"bifrost/internal/gitview"
	"bifrost/internal/process"
	"bifrost/internal/provider"
	"bifrost/internal/worker"
	"bifrost/internal/workspace"
)

func TestOpenRejectsDisabledWorkerCapability(t *testing.T) {
	backend := &fakeBackend{}
	if _, _, err := Open(context.Background(), backend, true, false); err == nil {
		t.Fatal("browser capability unexpectedly accepted")
	}
}

func TestRemoteDesktopImageIsOneShot(t *testing.T) {
	backend := &fakeBackend{caps: worker.Capabilities{Desktop: true}}
	tools, closeTools, err := Open(context.Background(), backend, false, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	var capture agentTool
	for _, tool := range tools {
		if tool.Definition.Name == "desktop_capture" {
			capture = agentTool{call: tool.Call, images: tool.Images}
		}
	}
	if capture.call == nil {
		t.Fatal("missing capture tool")
	}
	if _, err := capture.call(context.Background(), json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if got := capture.images(); len(got) != 1 || string(got[0].Data) != "png" {
		t.Fatalf("images=%+v", got)
	}
	if got := capture.images(); len(got) != 0 {
		t.Fatalf("image callback not one-shot: %+v", got)
	}
}

type agentTool struct {
	call   func(context.Context, json.RawMessage) (string, error)
	images func() []provider.Image
}
type fakeBackend struct{ caps worker.Capabilities }

func (f *fakeBackend) Identity() environment.Identity {
	return environment.Identity{Kind: "ssh", Capabilities: f.caps}
}
func (*fakeBackend) List(context.Context, string) ([]workspace.Entry, error) {
	return nil, errors.New("unused")
}
func (*fakeBackend) Read(context.Context, string) (string, workspace.Revision, error) {
	return "", "", errors.New("unused")
}
func (*fakeBackend) Search(context.Context, string) (workspace.SearchResult, error) {
	return workspace.SearchResult{}, errors.New("unused")
}
func (*fakeBackend) Write(context.Context, string, *workspace.Revision, string) (workspace.Change, error) {
	return workspace.Change{}, errors.New("unused")
}
func (*fakeBackend) Edit(context.Context, string, workspace.Revision, string, string) (workspace.Change, error) {
	return workspace.Change{}, errors.New("unused")
}
func (*fakeBackend) Undo(context.Context, workspace.ChangeID) error { return errors.New("unused") }
func (*fakeBackend) Exec(context.Context, environment.ExecRequest) (process.Result, error) {
	return process.Result{}, errors.New("unused")
}
func (*fakeBackend) Review(context.Context) (gitview.Review, error) {
	return gitview.Review{}, errors.New("unused")
}
func (*fakeBackend) Close() error { return nil }
func (f *fakeBackend) DesktopCapabilities(context.Context) (desktop.Capability, error) {
	return desktop.Capability{Backend: desktop.BackendX11}, nil
}
func (f *fakeBackend) DesktopCapture(context.Context) (worker.DesktopResult, error) {
	return worker.DesktopResult{Observation: desktop.Observation{ID: "obs", Display: ":0"}, PNG: []byte("png")}, nil
}
func (*fakeBackend) DesktopMove(context.Context, worker.DesktopAction) error     { return nil }
func (*fakeBackend) DesktopClick(context.Context, worker.DesktopAction) error    { return nil }
func (*fakeBackend) DesktopKeyPress(context.Context, worker.DesktopAction) error { return nil }
func (*fakeBackend) DesktopType(context.Context, worker.DesktopAction) error     { return nil }
func (*fakeBackend) BrowserNavigate(context.Context, string) (browser.Snapshot, error) {
	return browser.Snapshot{}, nil
}
func (*fakeBackend) BrowserSnapshot(context.Context) (browser.Snapshot, error) {
	return browser.Snapshot{}, nil
}
func (*fakeBackend) BrowserClick(context.Context, string) (browser.Snapshot, error) {
	return browser.Snapshot{}, nil
}
func (*fakeBackend) BrowserType(context.Context, string, string) (browser.Snapshot, error) {
	return browser.Snapshot{}, nil
}
func (*fakeBackend) BrowserScreenshot(context.Context) ([]byte, error) { return []byte("png"), nil }

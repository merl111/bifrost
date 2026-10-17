package desktop

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bifrost/internal/process"
)

func fixturePNG(t *testing.T, path string, width, height uint32) {
	t.Helper()
	header := make([]byte, 24)
	copy(header[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10})
	binary.BigEndian.PutUint32(header[8:12], 13)
	copy(header[12:16], []byte("IHDR"))
	binary.BigEndian.PutUint32(header[16:20], width)
	binary.BigEndian.PutUint32(header[20:24], height)
	if err := os.WriteFile(path, append(header, []byte("fixture")...), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestX11CaptureActionsUseExplicitArgvAndFreshObservations(t *testing.T) {
	artifactDir := t.TempDir()
	var mu sync.Mutex
	var calls [][]string
	run := func(_ context.Context, spec process.Spec) (process.Result, error) {
		mu.Lock()
		calls = append(calls, append([]string(nil), spec.Argv...))
		mu.Unlock()
		if strings.Contains(spec.Argv[0], "import") {
			fixturePNG(t, spec.Argv[len(spec.Argv)-1], 800, 600)
			return process.Result{ExitCode: 0}, nil
		}
		if strings.Contains(spec.Argv[0], "xdotool") && len(spec.Argv) > 1 && spec.Argv[1] == "getdisplaygeometry" {
			return process.Result{ExitCode: 0, Stdout: []byte("800 600\n")}, nil
		}
		return process.Result{ExitCode: 0}, nil
	}
	m, err := New(Config{
		Backend:     BackendX11,
		Display:     ":fixture",
		ArtifactDir: artifactDir,
		Tools:       ToolPaths{Import: "/fixture/import", Xdotool: "/fixture/xdotool"},
		Run:         run,
	})
	if err != nil {
		t.Fatal(err)
	}
	capability := m.Capabilities()
	if !capability.Capture || !capability.Click || !capability.Type {
		t.Fatalf("capabilities = %#v", capability)
	}
	s, err := m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	observation, err := s.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if observation.Width != 800 || observation.Height != 600 || observation.Display != ":fixture" {
		t.Fatalf("observation = %#v", observation)
	}
	if _, err := os.Stat(observation.PNGPath); err != nil {
		t.Fatalf("PNG artifact: %v", err)
	}
	if err := s.Click(context.Background(), observation.ID, observation.Display, 799, 599, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.Type(context.Background(), observation.ID, observation.Display, "stale"); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale type error = %v", err)
	}
	observation, err = s.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Move(context.Background(), observation.ID, observation.Display, 800, 0); err == nil {
		t.Fatal("out-of-bounds move unexpectedly succeeded")
	}
	if err := s.Type(context.Background(), observation.ID, observation.Display, "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) < 5 || calls[0][0] != "/fixture/import" || calls[1][0] != "/fixture/xdotool" || calls[len(calls)-1][0] != "/fixture/xdotool" {
		t.Fatalf("command calls = %#v", calls)
	}
}

func TestWaylandReportsPartialCapabilities(t *testing.T) {
	run := func(_ context.Context, spec process.Spec) (process.Result, error) {
		if spec.Argv[0] == "/fixture/grim" {
			fixturePNG(t, spec.Argv[len(spec.Argv)-1], 320, 200)
		}
		return process.Result{ExitCode: 0}, nil
	}
	m, err := New(Config{Backend: BackendWayland, Display: "wayland-fixture", ArtifactDir: t.TempDir(), Tools: ToolPaths{Grim: "/fixture/grim", Wtype: "/fixture/wtype"}, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	capability := m.Capabilities()
	if !capability.Capture || !capability.Type || capability.Move || capability.Click || capability.KeyPress {
		t.Fatalf("wayland capabilities = %#v", capability)
	}
	s, err := m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	observation, err := s.Capture(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Type(context.Background(), observation.ID, observation.Display, "text"); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopLeasesSerializeAndContextCanCancel(t *testing.T) {
	m, err := New(Config{Backend: BackendUnknown, ArtifactDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.Acquire(ctx); !errors.Is(err, ErrLeaseBusy) {
		t.Fatalf("busy lease error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestParseDimensionsRejectsMalformedOutput(t *testing.T) {
	if _, _, err := parseDimensions("not dimensions"); err == nil {
		t.Fatal("malformed dimensions accepted")
	}
	if width, height, err := parseDimensions("1920 1080 extra"); err != nil || width != 1920 || height != 1080 {
		t.Fatalf("dimensions = %d,%d,%v", width, height, err)
	}
}

func TestArtifactDirectoryIsBoundedToConfiguredPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "artifact")
	m, err := New(Config{Backend: BackendUnknown, ArtifactDir: path})
	if err != nil {
		t.Fatal(err)
	}
	if m.cfg.ArtifactDir != path {
		t.Fatalf("artifact dir = %q", m.cfg.ArtifactDir)
	}
}

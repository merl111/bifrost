package desktoptools

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"bifrost/internal/desktop"
	"bifrost/internal/process"
)

func TestCaptureExposesOneShotImageAndActionsRequireFreshObservation(t *testing.T) {
	artifactDir := t.TempDir()
	run := func(_ context.Context, spec process.Spec) (process.Result, error) {
		if strings.Contains(spec.Argv[0], "import") {
			png := make([]byte, 31)
			copy(png[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10})
			binary.BigEndian.PutUint32(png[8:12], 13)
			copy(png[12:16], []byte("IHDR"))
			binary.BigEndian.PutUint32(png[16:20], 16)
			binary.BigEndian.PutUint32(png[20:24], 16)
			if err := os.WriteFile(spec.Argv[len(spec.Argv)-1], png, 0600); err != nil {
				return process.Result{}, err
			}
		}
		if len(spec.Argv) > 1 && spec.Argv[1] == "getdisplaygeometry" {
			return process.Result{ExitCode: 0, Stdout: []byte("16 16")}, nil
		}
		return process.Result{ExitCode: 0}, nil
	}
	tools, closeTools, err := Open(context.Background(), desktop.Config{Backend: desktop.BackendX11, Display: ":fixture", ArtifactDir: artifactDir, Tools: desktop.ToolPaths{Import: "/fixture/import", Xdotool: "/fixture/xdotool"}, Run: run})
	if err != nil {
		t.Fatal(err)
	}
	defer closeTools()
	call := func(name string, args any) string {
		t.Helper()
		raw, _ := json.Marshal(args)
		for _, tool := range tools {
			if tool.Definition.Name == name {
				result, err := tool.Call(context.Background(), raw)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
		}
		t.Fatalf("missing tool %s", name)
		return ""
	}
	var observation desktop.Observation
	if err := json.Unmarshal([]byte(call("desktop_capture", map[string]any{})), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.ID == "" {
		t.Fatal("missing observation id")
	}
	var screenshotToolImages []int
	for _, tool := range tools {
		if tool.Definition.Name == "desktop_capture" {
			screenshotToolImages = append(screenshotToolImages, len(tool.Images()))
			screenshotToolImages = append(screenshotToolImages, len(tool.Images()))
		}
	}
	if len(screenshotToolImages) != 2 || screenshotToolImages[0] != 1 || screenshotToolImages[1] != 0 {
		t.Fatalf("images=%v", screenshotToolImages)
	}
	call("desktop_click", map[string]any{"observation_id": observation.ID, "display": observation.Display, "x": 1, "y": 1})
	for _, tool := range tools {
		if tool.Definition.Name == "desktop_click" {
			raw, _ := json.Marshal(map[string]any{"observation_id": observation.ID, "display": observation.Display, "x": 1, "y": 1})
			if _, err := tool.Call(context.Background(), raw); err == nil {
				t.Fatal("stale action accepted")
			}
		}
	}
}

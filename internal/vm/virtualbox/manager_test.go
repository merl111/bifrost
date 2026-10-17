package virtualbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"bifrost/internal/process"
)

func fakeManager(t *testing.T, stdout string) (*Manager, *[]string) {
	t.Helper()
	var calls []string
	m := &Manager{timeout: time.Second}
	m.run = func(_ context.Context, args []string) (process.Result, error) {
		calls = append(calls, strings.Join(args, "\x00"))
		return process.Result{Argv: args, Stdout: []byte(stdout)}, nil
	}
	return m, &calls
}

func TestListAndShowMachineReadable(t *testing.T) {
	m, calls := fakeManager(t, `"demo" {11111111-1111-1111-1111-111111111111}
"quoted \"vm\"" {22222222-2222-2222-2222-222222222222}
`)
	machines, err := m.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Machine{{Name: "demo", UUID: "11111111-1111-1111-1111-111111111111"}, {Name: `quoted "vm"`, UUID: "22222222-2222-2222-2222-222222222222"}}
	if !reflect.DeepEqual(machines, want) || len(*calls) != 1 || (*calls)[0] != "list\x00vms\x00--sorted" {
		t.Fatalf("list = %+v calls=%v", machines, *calls)
	}
	m, calls = fakeManager(t, "name=\"demo\"\nUUID=\"111\"\nVMState=\"running\"\nCfgFile=\"/tmp/demo.vbox\"\nGuestAdditionsRunLevel=2\n")
	machine, err := m.Show(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if machine.State != "running" || machine.GuestAdditionsRunLevel != "2" || machine.ConfigFile != "/tmp/demo.vbox" || (*calls)[0] != "showvminfo\x00demo\x00--machinereadable" {
		t.Fatalf("show = %+v calls=%v", machine, *calls)
	}
}

func TestScreenshotAndExplicitKeyboardInput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.png")
	m := &Manager{timeout: time.Second}
	var calls [][]string
	m.run = func(_ context.Context, args []string) (process.Result, error) {
		calls = append(calls, append([]string(nil), args...))
		if len(args) >= 4 && args[0] == "controlvm" && args[2] == "screenshotpng" {
			if err := os.WriteFile(args[3], []byte("png"), 0o600); err != nil {
				return process.Result{}, err
			}
		}
		return process.Result{}, nil
	}
	if err := m.Screenshot(context.Background(), "demo", output); err != nil {
		t.Fatal(err)
	}
	if err := m.SendText(context.Background(), "demo", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := m.SendScancodes(context.Background(), "demo", []string{"1d", "9d"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SendPointer(context.Background(), "demo", 1, 2, "left", true); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("pointer = %v", err)
	}
	want := [][]string{{"controlvm", "demo", "screenshotpng", output}, {"controlvm", "demo", "keyboardputstring", "hello"}, {"controlvm", "demo", "keyboardputscancode", "1d", "9d"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v want %#v", calls, want)
	}
	for _, bad := range []string{"-vm", "", "vm\nname"} {
		if err := m.SendText(context.Background(), bad, "x"); !errors.Is(err, ErrInvalidSelector) {
			t.Errorf("selector %q error = %v", bad, err)
		}
	}
}

func TestLifecycleAndSnapshotActionsUseFixedVBoxArgs(t *testing.T) {
	m, calls := fakeManager(t, "SnapshotName=\"base\"\nSnapshotUUID=\"snap-1\"\nSnapshotDescription=\"clean\"\n")
	if err := m.Start(context.Background(), "demo", true); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"pause", "resume", "reset", "acpi-shutdown", "poweroff"} {
		if err := m.Control(context.Background(), "demo", action); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	items, err := m.ListSnapshots(context.Background(), "demo")
	if err != nil || len(items) != 1 || items[0].Name != "base" {
		t.Fatalf("snapshots = %#v err=%v", items, err)
	}
	if err := m.TakeSnapshot(context.Background(), "demo", "before", "clean state", true); err != nil {
		t.Fatal(err)
	}
	if err := m.RestoreSnapshot(context.Background(), "demo", "before"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteSnapshot(context.Background(), "demo", "before"); err != nil {
		t.Fatal(err)
	}
	if err := m.Control(context.Background(), "demo", "monitor savevm"); !errors.Is(err, ErrUnsupportedCapability) {
		t.Fatalf("arbitrary lifecycle action = %v", err)
	}
	if len(*calls) != 10 {
		t.Fatalf("calls = %v", *calls)
	}
	if (*calls)[0] != "startvm\x00demo\x00--type\x00headless" || (*calls)[6] != "snapshot\x00demo\x00list\x00--machinereadable" {
		t.Fatalf("fixed calls = %v", *calls)
	}
}

func TestSnapshotNamesRejectInjection(t *testing.T) {
	m, _ := fakeManager(t, "")
	for _, name := range []string{"", "-bad", "bad\nname"} {
		if err := m.TakeSnapshot(context.Background(), "demo", name, "", false); err == nil {
			t.Errorf("snapshot name %q accepted", name)
		}
	}
}

func TestInstalledVirtualBoxHelpMatchesClaimedReadOnlyInputAPIs(t *testing.T) {
	executable, err := exec.LookPath("VBoxManage")
	if err != nil {
		t.Skip("VBoxManage unavailable")
	}
	result, err := process.Run(context.Background(), process.Spec{Argv: []string{executable, "help", "controlvm"}, MaxOutputBytes: 1 << 20})
	help := string(result.Stdout)
	if err != nil && !strings.Contains(help, "keyboardputstring") {
		t.Skipf("VBoxManage help unavailable: %v exit=%d", err, result.ExitCode)
	}
	if !strings.Contains(help, "keyboardputstring") || !strings.Contains(help, "keyboardputscancode") || !strings.Contains(help, "screenshotpng") {
		t.Fatalf("installed VBoxManage help does not contain claimed APIs")
	}
	if strings.Contains(strings.ToLower(help), "mouseput") {
		t.Fatalf("pointer API appeared in installed help; update capability claims")
	}
}

func TestListRejectsMalformedOutput(t *testing.T) {
	m, _ := fakeManager(t, "not a machine line\n")
	if _, err := m.List(context.Background()); err == nil {
		t.Fatal("malformed list output accepted")
	}
}

func TestListRejectsTruncatedOutput(t *testing.T) {
	m := &Manager{timeout: time.Second}
	m.run = func(context.Context, []string) (process.Result, error) {
		return process.Result{Stdout: []byte(`"partial" {11111111-1111-1111-1111-111111111111}`), StdoutTruncated: true}, nil
	}
	if _, err := m.List(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeded configured limit") {
		t.Fatalf("truncated list error = %v", err)
	}
}

func TestScreenshotRejectsDirectoryOutput(t *testing.T) {
	output := filepath.Join(t.TempDir(), "capture.png")
	m := &Manager{timeout: time.Second}
	m.run = func(_ context.Context, args []string) (process.Result, error) {
		if err := os.Mkdir(args[3], 0o700); err != nil {
			return process.Result{}, err
		}
		return process.Result{}, nil
	}
	if err := m.Screenshot(context.Background(), "demo", output); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("directory screenshot error = %v", err)
	}
}

func TestScreenshotParentMustBeDirectory(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newOutputPath(filepath.Join(parent, "capture.png")); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("parent error = %v", err)
	}
}

package cli

import (
	"context"
	"io"
	"path/filepath"
	"testing"

	"bifrost/internal/config"
)

func TestDesktopStartupSelection(t *testing.T) {
	ctx := context.Background()
	state, a, b := t.TempDir(), t.TempDir(), t.TempDir()
	opts := options{stateDir: state, environment: "local"}
	if err := runProject(opts, []string{"add", "example", "--root", a, "--alias", "frontend"}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for alias, root := range map[string]string{"backend": b} {
		if err := runProjectAddRepo(opts, []string{"example", "--alias", alias, "--root", root}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.DefaultSettings()
	cfg.Defaults.Project = "example"
	if err := config.SaveSettings(filepath.Join(state, "config.json"), cfg, false); err != nil {
		t.Fatal(err)
	}
	args := []string{"--state-dir", state, "--model", "fixture", "--allow-write"}
	choose, err := DesktopNeedsProjectChoice(args)
	if err != nil || !choose {
		t.Fatalf("saved default bypassed choice: %v %v", choose, err)
	}
	catalog, err := DesktopProjects(ctx, args)
	if err != nil || len(catalog.Projects) != 1 || len(catalog.Projects[0].Repositories) != 2 {
		t.Fatalf("catalog: %+v %v", catalog, err)
	}
	for _, selection := range []struct{ project, repo, root, want string }{{"example", "backend", "", b}, {"", "", a, a}} {
		selected, err := DesktopSelectionArguments(args, selection.project, selection.repo, selection.root)
		if err != nil {
			t.Fatal(err)
		}
		choose, err := DesktopNeedsProjectChoice(selected)
		if err != nil || choose {
			t.Fatalf("explicit selection did not bypass picker: %v", err)
		}
		global, runtime, err := extractGlobalFlags(selected)
		if err != nil {
			t.Fatal(err)
		}
		parsed, _, err := parseGlobal(global, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err = applyProjectDefault(parsed)
		if err != nil {
			t.Fatal(err)
		}
		root, err := resolveRoot(parsed)
		if err != nil || root != selection.want {
			t.Fatalf("root %q %v", root, err)
		}
		if len(runtime) != 3 || runtime[1] != "fixture" {
			t.Fatalf("lost runtime flags: %q", runtime)
		}
	}
}

func TestDesktopStartupExplicitAndInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"--root=/tmp"}, {"--project=example"}, {"--model", "--project=not-a-flag"}} {
		got, err := DesktopNeedsProjectChoice(args)
		want := args[0] == "--model"
		if err != nil || got != want {
			t.Fatalf("%q => %v %v", args, got, err)
		}
	}
	if _, err := DesktopNeedsProjectChoice([]string{"--root"}); err == nil {
		t.Fatal("accepted missing root")
	}
	args, err := DesktopSelectionArguments([]string{"--repository=old", "--model", "fixture"}, "new", "backend", "")
	if err != nil {
		t.Fatal(err)
	}
	global, _, err := extractGlobalFlags(args)
	if err != nil {
		t.Fatal(err)
	}
	opts, _, err := parseGlobal(global, io.Discard)
	if err != nil || opts.repository != "backend" {
		t.Fatalf("selection: %+v %v", opts, err)
	}
}

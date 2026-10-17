package skill

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestIsolatedGitEnvDropsInheritedGitConfiguration(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.ext.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")
	t.Setenv("GIT_DIR", "/tmp/untrusted")
	env := isolatedGitEnv()
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	if values["GIT_CONFIG_COUNT"] != "0" {
		t.Fatalf("GIT_CONFIG_COUNT = %q", values["GIT_CONFIG_COUNT"])
	}
	for _, key := range []string{"GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0", "GIT_DIR"} {
		if _, ok := values[key]; ok {
			t.Fatalf("inherited %s was retained", key)
		}
	}
}

func TestExtractSkillArchiveRejectsOversizedEntryBeforeWriting(t *testing.T) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	if err := tw.WriteHeader(&tar.Header{Name: "huge.bin", Mode: 0o600, Size: maxSkillFileBytes + 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(make([]byte, maxSkillFileBytes+1)); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := extractSkillArchive(bytes.NewReader(archive.Bytes()), destination); err == nil {
		t.Fatal("oversized archive entry was accepted")
	}
	entries, err := os.ReadDir(destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("oversized archive wrote %d entries", len(entries))
	}
}

func writeFixture(t *testing.T, root, markdown string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte(markdown), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "guide.txt"), []byte("resource\n"), 0o640); err != nil {
		t.Fatal(err)
	}
}

const validSkill = "---\nname: go-helper\ndescription: Helps with Go work.\nmetadata:\n  author: test\n---\n\nRead the guide before editing.\n"

func TestInstallLoadListRemoveAndSourceSnapshot(t *testing.T) {
	state, source := t.TempDir(), t.TempDir()
	writeFixture(t, source, validSkill)
	m, err := New(state, "prj_demo")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := m.Install(source)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "go-helper" || installed.Body != "\nRead the guide before editing.\n" || installed.Digest == "" {
		t.Fatalf("installed skill = %+v", installed)
	}
	if _, err := m.Install(source); !errors.Is(err, ErrSkillExists) {
		t.Fatalf("second install error = %v, want ErrSkillExists", err)
	}
	if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "guide.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := m.Load("go-helper")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Body != installed.Body || loaded.Digest != installed.Digest || loaded.Source != source {
		t.Fatalf("installed snapshot changed after source edit: loaded=%+v installed=%+v", loaded, installed)
	}
	resource, err := m.ReadResource("go-helper", "guide.txt")
	if err != nil || resource != "resource\n" {
		t.Fatalf("read resource = %q, %v", resource, err)
	}
	for _, resourcePath := range []string{"../outside", "/absolute", ".bifrost-skill.json"} {
		if _, err := m.ReadResource("go-helper", resourcePath); err == nil {
			t.Errorf("resource path %q unexpectedly accepted", resourcePath)
		}
	}
	list, err := m.List()
	if err != nil || len(list) != 1 || list[0].Name != "go-helper" {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err := m.Remove("go-helper"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load("go-helper"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("load after remove = %v", err)
	}
	if err := m.Remove("go-helper"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("remove after remove = %v", err)
	}
}

func TestProjectManagerFallsBackToGlobalSkills(t *testing.T) {
	state := t.TempDir()
	globalSource, projectSource, overrideSource := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, globalSource, "---\nname: global-helper\ndescription: global\n---\nGlobal instructions.\n")
	writeFixture(t, projectSource, "---\nname: project-helper\ndescription: project\n---\nProject instructions.\n")
	writeFixture(t, overrideSource, "---\nname: global-helper\ndescription: project override\n---\nProject override.\n")
	global, err := New(state, "global")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := global.Install(globalSource); err != nil {
		t.Fatal(err)
	}
	project, err := New(state, "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.Install(projectSource); err != nil {
		t.Fatal(err)
	}
	if _, err := project.Install(overrideSource); err != nil {
		t.Fatal(err)
	}
	project.WithFallback(global)
	if got, err := project.Load("global-helper"); err != nil || got.Body != "Project override.\n" {
		t.Fatalf("global fallback load = %+v, %v", got, err)
	}
	if got, err := project.ReadResource("global-helper", "guide.txt"); err != nil || got != "resource\n" {
		t.Fatalf("project precedence resource = %q, %v", got, err)
	}
	if got, err := project.ReadResource("global-helper", "guide.txt"); err != nil || got != "resource\n" {
		t.Fatalf("global fallback resource = %q, %v", got, err)
	}
	items, err := project.List()
	if err != nil || len(items) != 2 || items[0].Name != "global-helper" || items[1].Name != "project-helper" {
		t.Fatalf("merged skill list = %+v, %v", items, err)
	}
}

func TestScopeIsolationAndNameValidation(t *testing.T) {
	state, source := t.TempDir(), t.TempDir()
	writeFixture(t, source, validSkill)
	first, err := New(state, "project-one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(state, "project-two")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Load("go-helper"); !errors.Is(err, ErrSkillNotFound) {
		t.Fatalf("cross-scope load = %v", err)
	}
	for _, scope := range []string{"../escape", "a/b", "", "."} {
		if _, err := New(state, scope); err == nil {
			t.Errorf("scope %q unexpectedly accepted", scope)
		}
	}
	for _, name := range []string{"../escape", "Upper", "two--hyphens", "-leading", strings.Repeat("a", 65)} {
		if _, err := first.Load(name); err == nil {
			t.Errorf("name %q unexpectedly accepted", name)
		}
	}
}

func TestInstallRejectsSymlinkAndTraversalResources(t *testing.T) {
	state, source, outside := t.TempDir(), t.TempDir(), t.TempDir()
	writeFixture(t, source, validSkill)
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(source, "secret.txt")); err != nil {
		t.Skipf("symlink fixture unavailable: %v", err)
	}
	m, err := New(state, "project")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(source); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink install error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(state, "skill", "project", "go-helper")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed install left destination: %v", err)
	}
}

func TestFrontmatterValidationAndBlockScalars(t *testing.T) {
	cases := []struct {
		name string
		text string
		want string
		err  bool
	}{
		{"quoted and folded", "---\nname: 'shell-helper'\ndescription: >\n  First line\n  second line.\nextra: value\n---\nBody\n", "First line second line.", false},
		{"literal", "---\nname: \"file-helper\"\ndescription: |\n  one\n  two\n---\nBody\n", "one\ntwo", false},
		{"missing description", "---\nname: bad\n---\nBody\n", "", true},
		{"duplicate name", "---\nname: one\nname: two\ndescription: desc\n---\nBody\n", "", true},
		{"invalid quoted value", "---\nname: \"unterminated\ndescription: desc\n---\nBody\n", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte(tc.text), 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := New(t.TempDir(), "scope")
			if err != nil {
				t.Fatal(err)
			}
			got, err := m.Install(source)
			if tc.err {
				if err == nil {
					t.Fatal("malformed skill unexpectedly installed")
				}
				return
			}
			if err != nil || got.Description != tc.want {
				t.Fatalf("install = %+v, %v", got, err)
			}
		})
	}
}

func TestLoadDetectsManagedSnapshotMutation(t *testing.T) {
	state, source := t.TempDir(), t.TempDir()
	writeFixture(t, source, validSkill)
	m, err := New(state, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(source); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "skill", "scope", "go-helper", "guide.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Load("go-helper"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("tampered load = %v", err)
	}
}

func TestReadResourceRejectsBinaryContent(t *testing.T) {
	state, source := t.TempDir(), t.TempDir()
	writeFixture(t, source, "---\nname: binary-helper\ndescription: Binary fixture.\n---\nBody\n")
	if err := os.WriteFile(filepath.Join(source, "binary.dat"), []byte("bad\x00data"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := New(state, "scope")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(source); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReadResource("binary-helper", "binary.dat"); !errors.Is(err, ErrResourceBinary) {
		t.Fatalf("binary resource error = %v", err)
	}
}

func TestInstallGitUsesExplicitRefAndNeverExecutesSkill(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	remote, source := t.TempDir(), t.TempDir()
	if err := runGit(remote, "init", "--initial-branch=main"); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, source, validSkill)
	if err := runGit(remote, "config", "user.email", "test@example.invalid"); err != nil {
		t.Fatal(err)
	}
	if err := runGit(remote, "config", "user.name", "Bifrost Test"); err != nil {
		t.Fatal(err)
	}
	if err := copyDirForTest(source, remote); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "SKILL.md"), []byte("---\nname: git-helper\ndescription: From git.\n---\nGit body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(remote, "nested")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "SKILL.md"), []byte("---\nname: git-nested\ndescription: Nested git skill.\n---\nNested body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := runGit(remote, "add", "."); err != nil {
		t.Fatal(err)
	}
	if err := runGit(remote, "commit", "-m", "skill"); err != nil {
		t.Fatal(err)
	}
	m, err := New(t.TempDir(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := m.InstallGit(context.Background(), remote, "main", "nested")
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "git-nested" || !strings.Contains(installed.Source, "@") || strings.Contains(installed.Source, "@main") || !strings.HasSuffix(installed.Source, ":nested") {
		t.Fatalf("git install = %+v", installed)
	}
	parts := strings.SplitN(installed.Source, "@", 2)
	revision := ""
	if len(parts) == 2 {
		revision = strings.SplitN(parts[1], ":", 2)[0]
	}
	if len(parts) != 2 || len(revision) < 40 || len(revision) > 64 {
		t.Fatalf("git install did not record resolved revision: %+v", installed)
	}
	for _, r := range revision {
		if !strings.ContainsRune("0123456789abcdef", r) {
			t.Fatalf("git install revision is not hexadecimal: %q", revision)
		}
	}
	byCommit, err := New(t.TempDir(), "commit-scope")
	if err != nil {
		t.Fatal(err)
	}
	installedByCommit, err := byCommit.InstallGit(context.Background(), remote, revision, "nested")
	if err != nil {
		t.Fatalf("install by immutable commit: %v", err)
	}
	if installedByCommit.Name != "git-nested" || !strings.Contains(installedByCommit.Source, "@"+revision) {
		t.Fatalf("commit install = %+v", installedByCommit)
	}
	badCommit := strings.Repeat("f", 40)
	if _, err := byCommit.InstallGit(context.Background(), remote, badCommit, "nested"); err == nil {
		t.Fatal("invalid commit was accepted")
	}
}

func TestInstallGitCancellationKillsGitProcessGroup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process-group cancellation fixture uses POSIX shell")
	}
	bin := t.TempDir()
	fakeGit := filepath.Join(bin, "git")
	script := `#!/bin/sh
for arg in "$@"; do
  if [ "$arg" = "clone" ]; then
    (trap '' TERM; while :; do printf x; sleep 0.01; done) &
    while :; do printf x; sleep 0.01; done
  fi
done
exit 2
`
	if err := os.WriteFile(fakeGit, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	m, err := New(t.TempDir(), "scope")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = m.InstallGit(ctx, "https://example.invalid/private.git", "main", "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if strings.Contains(err.Error(), "example.invalid") {
		t.Fatal("cancellation error exposed repository URL")
	}
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

func copyDirForTest(source, target string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(target, entry.Name()), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

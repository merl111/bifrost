// Package skill installs and reads project-scoped Agent Skills as inert data.
//
// A skill is copied into a managed state directory before it is loaded. The
// manager never executes files from a skill; scripts and other resources are
// available to a later, policy-aware caller only as data.
package skill

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bifrost/internal/process"
)

// Skill is the metadata and instruction body of one installed skill.
// Digest is the SHA-256 revision of SKILL.md and all installed resources.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	Source      string `json:"source"`
	Digest      string `json:"digest"`
}

var (
	ErrSkillExists      = errors.New("skill already installed")
	ErrSkillNotFound    = errors.New("skill not found")
	ErrResourceBinary   = errors.New("skill resource is not UTF-8 text")
	ErrResourceTooLarge = errors.New("skill resource exceeds the read limit")
)

const (
	maxSkillFiles          = 512
	maxSkillBytes          = 32 << 20
	maxSkillFileBytes      = 8 << 20
	maxSkillArchiveEntries = maxSkillFiles * 2
	maxGitArchiveBytes     = maxSkillBytes + maxSkillArchiveEntries*1024
	maxSkillDescription    = 4096
	maxSkillName           = 64
	gitInstallTimeout      = 2 * time.Minute
	maxGitOutputBytes      = 256
)

var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type Manager struct {
	root     string
	scope    string
	fallback *Manager
	mu       sync.Mutex
}

// WithFallback makes skills in fallback available when this manager does not
// contain the requested name. Project managers use this for the user-global
// scope; project copies always take precedence.
func (m *Manager) WithFallback(fallback *Manager) *Manager {
	if m != nil {
		m.fallback = fallback
	}
	return m
}

type storedMetadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
	Digest      string `json:"digest"`
	InstalledAt string `json:"installed_at"`
}

// New creates a manager rooted at stateDir/skill/scope. scope is an opaque
// project identity (for example a project ID or a hexadecimal hash), and is
// rejected when it could name a parent or another scope.
func New(stateDir, scope string) (*Manager, error) {
	if strings.TrimSpace(stateDir) == "" {
		return nil, errors.New("skill state directory is empty")
	}
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	root, err := filepath.Abs(stateDir)
	if err != nil {
		return nil, fmt.Errorf("skill state directory: %w", err)
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(filepath.Join(root, "skill", scope), 0o700); err != nil {
		return nil, fmt.Errorf("create skill state directory: %w", err)
	}
	if err := ensureDirectoryTree(root, filepath.Join(root, "skill", scope)); err != nil {
		return nil, fmt.Errorf("skill state directory: %w", err)
	}
	return &Manager{root: root, scope: scope}, nil
}

// Install copies a local skill directory into the manager. source must name a
// directory containing SKILL.md. Existing names are never overwritten.
func (m *Manager) Install(source string) (Skill, error) {
	if m == nil {
		return Skill{}, errors.New("skill manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installLocal(source, source)
}

// InstallGit clones repo at the explicit ref, selects subdir, and installs its
// SKILL.md. The clone is temporary and no skill file or hook is executed. The
// resolved commit is recorded in Source along with the repository and selected path.
func (m *Manager) InstallGit(ctx context.Context, repo, ref, subdir string) (Skill, error) {
	if m == nil {
		return Skill{}, errors.New("skill manager is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, gitInstallTimeout)
	defer cancel()
	if strings.TrimSpace(repo) == "" {
		return Skill{}, errors.New("git repository is empty")
	}
	if err := validateGitRef(ref); err != nil {
		return Skill{}, err
	}
	subdir, err := cleanSubdir(subdir)
	if err != nil {
		return Skill{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tmp, err := os.MkdirTemp("", "bifrost-skill-git-")
	if err != nil {
		return Skill{}, fmt.Errorf("create temporary git directory: %w", err)
	}
	defer os.RemoveAll(tmp)
	resolveRef := ref
	if isCommitRef(ref) {
		// `git clone --branch` treats a commit ID as a branch name. Fetch the
		// explicitly requested object into a fresh bare repository instead, so
		// immutable catalog entries work without weakening ref validation.
		if err := runGitChecked(ctx, []string{"git", "-c", "alias.clone=", "-c", "core.hooksPath=/dev/null", "init", "--quiet", "--bare", "--", tmp}, "initialize skill repository"); err != nil {
			return Skill{}, err
		}
		if err := runGitChecked(ctx, []string{"git", "-C", tmp, "-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "credential.helper=", "fetch", "--quiet", "--no-tags", "--depth", "1", "--", repo, ref}, "fetch skill repository"); err != nil {
			return Skill{}, err
		}
		resolveRef = "FETCH_HEAD"
	} else {
		cloneArgs := []string{"git", "-c", "alias.clone=", "-c", "core.hooksPath=/dev/null", "-c", "protocol.ext.allow=never", "-c", "credential.helper=", "clone", "--bare", "--no-tags", "--depth", "1", "--branch", ref, "--", repo, tmp}
		if err := runGitChecked(ctx, cloneArgs, "clone skill repository"); err != nil {
			return Skill{}, err
		}
	}
	revision, err := resolveGitRevision(ctx, tmp, resolveRef)
	if err != nil {
		return Skill{}, err
	}
	archiveArgs := []string{"-C", tmp, "-c", "alias.archive=", "-c", "core.attributesFile=" + os.DevNull, "archive", "--format=tar", revision}
	if subdir != "" {
		archiveArgs = append(archiveArgs, "--", subdir)
	}
	extracted, err := os.MkdirTemp("", "bifrost-skill-extract-")
	if err != nil {
		return Skill{}, fmt.Errorf("create skill extraction directory: %w", err)
	}
	defer os.RemoveAll(extracted)
	archiveResult, err := process.Run(ctx, process.Spec{Argv: append([]string{"git"}, archiveArgs...), Env: isolatedGitEnv(), MaxOutputBytes: maxGitArchiveBytes})
	if err != nil {
		if ctx.Err() != nil {
			return Skill{}, ctx.Err()
		}
		return Skill{}, fmt.Errorf("archive skill repository: %w", err)
	}
	if archiveResult.StdoutTruncated || archiveResult.StderrTruncated {
		return Skill{}, errors.New("archive skill repository output exceeded limit")
	}
	if archiveResult.ExitCode != 0 {
		return Skill{}, fmt.Errorf("archive skill repository failed (exit %d)", archiveResult.ExitCode)
	}
	if err := extractSkillArchive(bytes.NewReader(archiveResult.Stdout), extracted); err != nil {
		return Skill{}, err
	}
	sourceDir := extracted
	if subdir != "" {
		sourceDir = filepath.Join(extracted, filepath.FromSlash(subdir))
	}
	return m.installLocal(sourceDir, repo+"@"+revision+":"+subdir)
}

func runGitChecked(ctx context.Context, argv []string, action string) error {
	result, err := process.Run(ctx, process.Spec{Argv: argv, Env: isolatedGitEnv(), MaxOutputBytes: maxGitOutputBytes})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%s: %w", action, err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return fmt.Errorf("%s output exceeded limit", action)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("%s failed (exit %d)", action, result.ExitCode)
	}
	return nil
}

func isolatedGitEnv() []string {
	env := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		if strings.HasPrefix(value, "GIT_") {
			continue
		}
		env = append(env, value)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_SYSTEM="+os.DevNull, "GIT_CONFIG_COUNT=0")
	return env
}

func resolveGitRevision(ctx context.Context, repoDir, ref string) (string, error) {
	result, err := process.Run(ctx, process.Spec{Argv: []string{"git", "-C", repoDir, "-c", "core.hooksPath=" + os.DevNull, "rev-parse", "--verify", ref + "^{commit}"}, Env: isolatedGitEnv(), MaxOutputBytes: maxGitOutputBytes})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("resolve git skill revision: %w", err)
	}
	if result.StdoutTruncated || result.StderrTruncated {
		return "", errors.New("git revision output exceeded limit")
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("resolve git skill revision failed (exit %d)", result.ExitCode)
	}
	revision := strings.TrimSpace(string(result.Stdout))
	if (len(revision) != 40 && len(revision) != 64) || strings.ContainsAny(revision, "\r\n ") {
		return "", errors.New("git returned an invalid skill revision")
	}
	for _, r := range revision {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return "", errors.New("git returned an invalid skill revision")
		}
	}
	return revision, nil
}

func extractSkillArchive(input io.Reader, destination string) error {
	tr := tar.NewReader(input)
	seen := make(map[string]struct{})
	var total int64
	var files int
	var entries int
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read skill archive: %w", err)
		}
		if header.Typeflag == tar.TypeXHeader || header.Typeflag == tar.TypeXGlobalHeader {
			continue
		}
		entries++
		if entries > maxSkillArchiveEntries {
			return fmt.Errorf("skill archive contains more than %d entries", maxSkillArchiveEntries)
		}
		name := filepath.ToSlash(header.Name)
		clean := path.Clean(name)
		if name == "" || clean == "." || strings.HasPrefix(name, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("skill archive path escapes repository: %q", header.Name)
		}
		if _, ok := seen[clean]; ok {
			return fmt.Errorf("skill archive contains duplicate path %q", clean)
		}
		seen[clean] = struct{}{}
		dst := filepath.Join(destination, filepath.FromSlash(clean))
		if err := ensureContained(destination, dst); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return fmt.Errorf("create archived directory: %w", err)
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maxSkillFileBytes {
				return fmt.Errorf("archived resource %q exceeds %d bytes", clean, maxSkillFileBytes)
			}
			files++
			if files > maxSkillFiles {
				return fmt.Errorf("skill archive contains more than %d files", maxSkillFiles)
			}
			total += header.Size
			if total > maxSkillBytes {
				return fmt.Errorf("skill archive exceeds %d bytes", maxSkillBytes)
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("create archived resource %q: %w", clean, err)
			}
			_, copyErr := io.CopyN(out, tr, header.Size)
			closeErr := out.Close()
			if copyErr != nil || closeErr != nil {
				if copyErr != nil {
					return fmt.Errorf("extract archived resource %q: %w", clean, copyErr)
				}
				return fmt.Errorf("close archived resource %q: %w", clean, closeErr)
			}
		default:
			return fmt.Errorf("skill archive resource %q is not a regular file or directory", clean)
		}
	}
	return nil
}

// ListLocal returns only this scope, including copies shadowed in other scopes.
func (m *Manager) ListLocal() ([]Skill, error) {
	if m == nil {
		return nil, errors.New("skill manager is nil")
	}
	local := &Manager{root: m.root, scope: m.scope}
	return local.List()
}

// List returns installed skills in stable name order.
func (m *Manager) List() ([]Skill, error) {
	if m == nil {
		return nil, errors.New("skill manager is nil")
	}
	m.mu.Lock()
	entries, err := os.ReadDir(m.scopeRoot())
	if err != nil {
		m.mu.Unlock()
		return nil, fmt.Errorf("list skills: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			m.mu.Unlock()
			return nil, fmt.Errorf("skill %q is a symlink", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	result := make([]Skill, 0, len(names))
	for _, name := range names {
		s, err := m.loadLocked(name)
		if err != nil {
			m.mu.Unlock()
			return nil, err
		}
		result = append(result, s)
	}
	m.mu.Unlock()
	if m.fallback == nil {
		return result, nil
	}
	other, err := m.fallback.List()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(result)+len(other))
	for _, s := range result {
		seen[s.Name] = true
	}
	for _, s := range other {
		if !seen[s.Name] {
			result = append(result, s)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Load returns one installed skill and verifies its complete snapshot digest.
func (m *Manager) Load(name string) (Skill, error) {
	if m == nil {
		return Skill{}, errors.New("skill manager is nil")
	}
	if err := validateName(name); err != nil {
		return Skill{}, err
	}
	m.mu.Lock()
	v, err := m.loadLocked(name)
	m.mu.Unlock()
	if err != nil && errors.Is(err, ErrSkillNotFound) && m.fallback != nil {
		return m.fallback.Load(name)
	}
	return v, err
}

// ReadResource returns a bounded UTF-8 text resource from an installed skill.
// It verifies the installed snapshot before opening the rooted resource, so a
// caller cannot accidentally read a changed or escaped file.
func (m *Manager) ReadResource(name, resourcePath string) (string, error) {
	if m == nil {
		return "", errors.New("skill manager is nil")
	}
	if err := validateName(name); err != nil {
		return "", err
	}
	rel, err := cleanResourcePath(resourcePath)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	_, loadErr := m.loadLocked(name)
	m.mu.Unlock()
	if loadErr != nil {
		if errors.Is(loadErr, ErrSkillNotFound) && m.fallback != nil {
			return m.fallback.ReadResource(name, resourcePath)
		}
		return "", loadErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	dir := filepath.Join(m.scopeRoot(), name)
	r, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("open installed skill: %w", err)
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return "", fmt.Errorf("read skill resource %q: %w", resourcePath, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("skill resource %q is not a regular file", resourcePath)
	}
	if info.Size() > maxSkillFileBytes {
		return "", ErrResourceTooLarge
	}
	b, err := io.ReadAll(io.LimitReader(f, maxSkillFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("read skill resource %q: %w", resourcePath, err)
	}
	if len(b) > maxSkillFileBytes {
		return "", ErrResourceTooLarge
	}
	if !utf8.Valid(b) || strings.IndexByte(string(b), 0) >= 0 {
		return "", ErrResourceBinary
	}
	return string(b), nil
}

// Remove deletes one installed skill. It does not touch the original source.
func (m *Manager) Remove(name string) error {
	if m == nil {
		return errors.New("skill manager is nil")
	}
	if err := validateName(name); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	target := filepath.Join(m.scopeRoot(), name)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	}
	if err != nil {
		return fmt.Errorf("inspect skill %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("skill %q is not a managed directory", name)
	}
	if err := os.RemoveAll(target); err != nil {
		return fmt.Errorf("remove skill %q: %w", name, err)
	}
	return nil
}

func (m *Manager) installLocal(source, recordedSource string) (Skill, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return Skill{}, fmt.Errorf("skill source: %w", err)
	}
	info, err := os.Lstat(source)
	if err != nil {
		return Skill{}, fmt.Errorf("skill source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Skill{}, errors.New("skill source must be a real directory")
	}
	if err := ensureNoSymlink(source); err != nil {
		return Skill{}, fmt.Errorf("skill source: %w", err)
	}
	files, total, err := collectFiles(source)
	if err != nil {
		return Skill{}, err
	}
	content, err := os.ReadFile(filepath.Join(source, "SKILL.md"))
	if err != nil {
		return Skill{}, fmt.Errorf("read SKILL.md: %w", err)
	}
	parsed, err := parseSkillMarkdown(string(content))
	if err != nil {
		return Skill{}, err
	}
	if parsed.Name == "" || parsed.Description == "" {
		return Skill{}, errors.New("SKILL.md requires name and description")
	}
	digest, err := snapshotDigest(source, files)
	if err != nil {
		return Skill{}, err
	}
	target := filepath.Join(m.scopeRoot(), parsed.Name)
	if err := ensureDirectoryTree(m.root, m.scopeRoot()); err != nil {
		return Skill{}, fmt.Errorf("skill state directory: %w", err)
	}
	if existing, err := os.Lstat(target); err == nil {
		if existing.Mode()&os.ModeSymlink != 0 {
			return Skill{}, fmt.Errorf("skill %q destination is a symlink", parsed.Name)
		}
		return Skill{}, fmt.Errorf("%w: %s", ErrSkillExists, parsed.Name)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Skill{}, fmt.Errorf("inspect skill destination: %w", err)
	}
	tmp, err := os.MkdirTemp(m.scopeRoot(), ".install-")
	if err != nil {
		return Skill{}, fmt.Errorf("create skill staging directory: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := copySkillFiles(source, tmp, files, total); err != nil {
		return Skill{}, err
	}
	metadata := storedMetadata{Name: parsed.Name, Description: parsed.Description, Source: recordedSource, Digest: digest, InstalledAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := writeJSONFile(filepath.Join(tmp, ".bifrost-skill.json"), metadata); err != nil {
		return Skill{}, err
	}
	if err := syncDirectory(tmp); err != nil {
		return Skill{}, err
	}
	if err := os.Rename(tmp, target); err != nil {
		if errors.Is(err, os.ErrExist) {
			return Skill{}, fmt.Errorf("%w: %s", ErrSkillExists, parsed.Name)
		}
		return Skill{}, fmt.Errorf("commit skill %q: %w", parsed.Name, err)
	}
	keep = true
	return Skill{Name: parsed.Name, Description: parsed.Description, Body: parsed.Body, Source: recordedSource, Digest: digest}, nil
}

func (m *Manager) loadLocked(name string) (Skill, error) {
	if err := validateName(name); err != nil {
		return Skill{}, err
	}
	dir := filepath.Join(m.scopeRoot(), name)
	if err := ensureNoSymlink(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Skill{}, fmt.Errorf("%w: %s", ErrSkillNotFound, name)
		}
		return Skill{}, err
	}
	metaBytes, err := os.ReadFile(filepath.Join(dir, ".bifrost-skill.json"))
	if err != nil {
		return Skill{}, fmt.Errorf("load skill %q metadata: %w", name, err)
	}
	var metadata storedMetadata
	if err := json.Unmarshal(metaBytes, &metadata); err != nil {
		return Skill{}, fmt.Errorf("load skill %q metadata: %w", name, err)
	}
	if metadata.Name != name || metadata.Digest == "" {
		return Skill{}, fmt.Errorf("load skill %q metadata is invalid", name)
	}
	files, _, err := collectFiles(dir)
	if err != nil {
		return Skill{}, err
	}
	digest, err := snapshotDigest(dir, files)
	if err != nil {
		return Skill{}, err
	}
	if digest != metadata.Digest {
		return Skill{}, fmt.Errorf("skill %q has changed on disk", name)
	}
	content, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, fmt.Errorf("load skill %q: %w", name, err)
	}
	parsed, err := parseSkillMarkdown(string(content))
	if err != nil {
		return Skill{}, err
	}
	if parsed.Name != name || parsed.Description != metadata.Description {
		return Skill{}, fmt.Errorf("skill %q metadata does not match SKILL.md", name)
	}
	return Skill{Name: parsed.Name, Description: parsed.Description, Body: parsed.Body, Source: metadata.Source, Digest: metadata.Digest}, nil
}

func (m *Manager) scopeRoot() string { return filepath.Join(m.root, "skill", m.scope) }

type sourceFile struct {
	rel  string
	mode os.FileMode
	size int64
}

func collectFiles(root string) ([]sourceFile, int64, error) {
	var files []sourceFile
	var total int64
	err := filepath.WalkDir(root, func(pathName string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if pathName == root {
			return nil
		}
		rel, err := filepath.Rel(root, pathName)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return errors.New("skill resource escapes source directory")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("skill resource %q is a symlink", rel)
		}
		if rel == ".bifrost-skill.json" {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("skill resource %q is not a regular file", rel)
		}
		if info.Size() > maxSkillFileBytes {
			return fmt.Errorf("skill resource %q exceeds %d bytes", rel, maxSkillFileBytes)
		}
		if len(files) >= maxSkillFiles {
			return fmt.Errorf("skill contains more than %d files", maxSkillFiles)
		}
		total += info.Size()
		if total > maxSkillBytes {
			return fmt.Errorf("skill exceeds %d bytes", maxSkillBytes)
		}
		files = append(files, sourceFile{rel: rel, mode: info.Mode(), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("inspect skill resources: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return filepath.ToSlash(files[i].rel) < filepath.ToSlash(files[j].rel) })
	return files, total, nil
}

func snapshotDigest(root string, files []sourceFile) (string, error) {
	h := sha256.New()
	for _, file := range files {
		rel := filepath.ToSlash(file.rel)
		_, _ = io.WriteString(h, rel+"\x00"+file.mode.Perm().String()+"\x00")
		f, err := os.Open(filepath.Join(root, file.rel))
		if err != nil {
			return "", fmt.Errorf("read skill resource %q: %w", rel, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("hash skill resource %q: %w", rel, err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("close skill resource %q: %w", rel, err)
		}
		_, _ = io.WriteString(h, "\x00")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func copySkillFiles(source, target string, files []sourceFile, total int64) error {
	if total > maxSkillBytes {
		return fmt.Errorf("skill exceeds %d bytes", maxSkillBytes)
	}
	for _, file := range files {
		src := filepath.Join(source, file.rel)
		dst := filepath.Join(target, file.rel)
		if err := ensureContained(target, dst); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return fmt.Errorf("create resource directory: %w", err)
		}
		in, err := os.Open(src)
		if err != nil {
			return fmt.Errorf("open skill resource %q: %w", file.rel, err)
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.mode.Perm())
		if err != nil {
			_ = in.Close()
			return fmt.Errorf("create skill resource %q: %w", file.rel, err)
		}
		_, copyErr := io.Copy(out, io.LimitReader(in, maxSkillFileBytes+1))
		closeInErr := in.Close()
		closeOutErr := out.Close()
		if copyErr != nil {
			return fmt.Errorf("copy skill resource %q: %w", file.rel, copyErr)
		}
		if closeInErr != nil || closeOutErr != nil {
			return fmt.Errorf("close skill resource %q", file.rel)
		}
	}
	return nil
}

func writeJSONFile(name string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create skill metadata: %w", err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return fmt.Errorf("write skill metadata: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync skill metadata: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close skill metadata: %w", err)
	}
	return nil
}

func syncDirectory(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func ensureContained(root, candidate string) error {
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("skill resource escapes destination directory")
	}
	return nil
}

func ensureNoSymlink(name string) error {
	info, err := os.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("symlink is not allowed")
	}
	if info.IsDir() {
		err := filepath.WalkDir(name, func(pathName string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("symlink %q is not allowed", pathName)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func ensureDirectoryTree(root, target string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return errors.New("path escapes skill state directory")
	}
	current := root
	parts := []string{}
	if rel != "." {
		parts = strings.Split(rel, string(filepath.Separator))
	}
	for _, part := range append([]string{"."}, parts...) {
		if part != "." {
			current = filepath.Join(current, part)
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is not allowed", current)
		}
		if !info.IsDir() {
			return fmt.Errorf("%q is not a directory", current)
		}
	}
	return nil
}

func validateScope(scope string) error {
	if scope == "" || scope == "." || scope == ".." || filepath.Base(scope) != scope || strings.ContainsAny(scope, `/\\`) {
		return errors.New("invalid skill scope")
	}
	for _, r := range scope {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return errors.New("invalid skill scope")
	}
	return nil
}

func validateName(name string) error {
	if len(name) == 0 || len(name) > maxSkillName || !skillNamePattern.MatchString(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	return nil
}

func validateGitRef(ref string) error {
	if strings.TrimSpace(ref) == "" || strings.HasPrefix(ref, "-") || strings.ContainsRune(ref, '\x00') || strings.ContainsAny(ref, "\r\n") {
		return errors.New("git skill ref must be an explicit non-empty ref")
	}
	return nil
}

func isCommitRef(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for _, r := range ref {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func cleanSubdir(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || value == "." {
		return "", nil
	}
	if strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return "", errors.New("git skill subdirectory must be relative")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("git skill subdirectory escapes repository")
	}
	return clean, nil
}

func cleanResourcePath(value string) (string, error) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || value == "." || strings.HasPrefix(value, "/") || strings.ContainsRune(value, '\x00') {
		return "", errors.New("skill resource path must be relative")
	}
	clean := path.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == ".bifrost-skill.json" {
		return "", errors.New("invalid skill resource path")
	}
	return clean, nil
}

type parsedSkill struct {
	Name        string
	Description string
	Body        string
}

func parseSkillMarkdown(text string) (parsedSkill, error) {
	if !strings.HasPrefix(text, "---\n") && !strings.HasPrefix(text, "---\r\n") {
		return parsedSkill{}, errors.New("SKILL.md must start with YAML frontmatter")
	}
	firstEnd := strings.IndexByte(text, '\n')
	if firstEnd < 0 {
		return parsedSkill{}, errors.New("SKILL.md frontmatter is unterminated")
	}
	headerStart := firstEnd + 1
	pos := headerStart
	closeStart, closeEnd := -1, -1
	for pos < len(text) {
		lineEnd := strings.IndexByte(text[pos:], '\n')
		if lineEnd < 0 {
			lineEnd = len(text) - pos
		}
		line := strings.TrimSuffix(text[pos:pos+lineEnd], "\r")
		if line == "---" {
			closeStart, closeEnd = pos, pos+lineEnd
			if closeEnd < len(text) && text[closeEnd] == '\n' {
				closeEnd++
			}
			break
		}
		pos += lineEnd
		if pos < len(text) && text[pos] == '\n' {
			pos++
		}
	}
	if closeStart < 0 {
		return parsedSkill{}, errors.New("SKILL.md frontmatter is unterminated")
	}
	header := text[headerStart:closeStart]
	name, description, err := parseMetadata(header)
	if err != nil {
		return parsedSkill{}, err
	}
	description = strings.TrimSpace(description)
	if err := validateName(name); err != nil {
		return parsedSkill{}, err
	}
	if strings.TrimSpace(description) == "" || len(description) > maxSkillDescription {
		return parsedSkill{}, errors.New("SKILL.md description is empty or too long")
	}
	return parsedSkill{Name: name, Description: description, Body: text[closeEnd:]}, nil
}

func parseMetadata(header string) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(header, "\r\n", "\n"), "\n")
	var name, description string
	seenName, seenDescription := false, false
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			return "", "", fmt.Errorf("malformed frontmatter line %q", line)
		}
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			return "", "", fmt.Errorf("frontmatter field %q is malformed", line)
		}
		key := strings.TrimSpace(line[:colon])
		value := strings.TrimSpace(line[colon+1:])
		isBlock := value == ">" || value == "|"
		blockLines := []string(nil)
		if isBlock {
			for i+1 < len(lines) {
				next := lines[i+1]
				if strings.TrimSpace(next) == "" {
					blockLines = append(blockLines, "")
					i++
					continue
				}
				if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
					break
				}
				blockLines = append(blockLines, strings.TrimLeft(next, " \t"))
				i++
			}
		}
		if key != "name" && key != "description" {
			// Standard skills may carry additional metadata. Consume its
			// scalar/block value, but do not interpret it as instructions.
			for i+1 < len(lines) {
				next := lines[i+1]
				if next == "" {
					i++
					continue
				}
				if !strings.HasPrefix(next, " ") && !strings.HasPrefix(next, "\t") {
					break
				}
				i++
			}
			continue
		}
		if key == "name" {
			if seenName {
				return "", "", errors.New("duplicate frontmatter name")
			}
			seenName = true
			if isBlock {
				return "", "", errors.New("name cannot use a block scalar")
			}
			var parseErr error
			name, parseErr = parseScalar(value)
			if parseErr != nil {
				return "", "", parseErr
			}
		} else {
			if seenDescription {
				return "", "", errors.New("duplicate frontmatter description")
			}
			seenDescription = true
			if isBlock {
				description = joinBlock(blockLines, value == ">")
			} else {
				var parseErr error
				description, parseErr = parseScalar(value)
				if parseErr != nil {
					return "", "", parseErr
				}
			}
		}
	}
	if !seenName || !seenDescription {
		return "", "", errors.New("frontmatter requires name and description")
	}
	return name, description, nil
}

func parseScalar(value string) (string, error) {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), nil
	}
	if strings.HasPrefix(value, `"`) {
		parsed, err := strconv.Unquote(value)
		if err != nil || !strings.HasSuffix(value, `"`) {
			return "", errors.New("invalid quoted frontmatter value")
		}
		return parsed, nil
	}
	if strings.HasPrefix(value, "'") {
		return "", errors.New("invalid quoted frontmatter value")
	}
	return strings.TrimSpace(value), nil
}

func joinBlock(lines []string, folded bool) string {
	if len(lines) == 0 {
		return ""
	}
	if folded {
		var b strings.Builder
		for i, line := range lines {
			if i > 0 {
				if line == "" || lines[i-1] == "" {
					b.WriteByte('\n')
				} else {
					b.WriteByte(' ')
				}
			}
			b.WriteString(line)
		}
		return b.String()
	}
	return strings.Join(lines, "\n")
}

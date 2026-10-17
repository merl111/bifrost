// Package gitview reads repository changes without executing external diff helpers.
package gitview

import (
	"bifrost/internal/process"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

type Review struct {
	Root      string
	Status    string
	Unstaged  string
	Staged    string
	Truncated bool
}

func Load(ctx context.Context, root string) (Review, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return Review{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Review{}, err
	}
	run := func(args ...string) (process.Result, error) {
		// A repository can define aliases such as `status = !script`. Override
		// the aliases for every porcelain command before asking Git to inspect
		// the tree. fsmonitor is also disabled because its configured hook is an
		// external executable. Diff itself is further constrained below with
		// --no-ext-diff and --no-textconv.
		command := ""
		if len(args) > 0 {
			command = args[0]
		}
		argv := []string{"git", "--no-pager", "-c", "core.quotePath=true", "-c", "core.fsmonitor=false"}
		if command != "" {
			argv = append(argv, "-c", "alias."+command+"=")
		}
		argv = append(argv, args...)
		r, err := process.Run(ctx, process.Spec{Argv: argv, Dir: root, MaxOutputBytes: 1 << 20})
		if err == nil && r.ExitCode != 0 {
			err = fmt.Errorf("git exited %d: %s", r.ExitCode, strings.TrimSpace(string(r.Stderr)))
		}
		return r, err
	}
	top, err := run("rev-parse", "--show-toplevel")
	if err != nil {
		return Review{}, err
	}
	canonical, err := filepath.EvalSymlinks(strings.TrimSpace(string(top.Stdout)))
	if err != nil {
		return Review{}, err
	}
	if canonical != root {
		return Review{}, errors.New("review root must be the repository top-level directory")
	}
	out := Review{Root: root}
	status, err := run("status", "--short", "--untracked-files=normal", "--ignore-submodules=all")
	if err != nil {
		return Review{}, err
	}
	out.Status = string(status.Stdout)
	unstaged, err := run("diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--")
	if err != nil {
		return Review{}, err
	}
	out.Unstaged = string(unstaged.Stdout)
	staged, err := run("diff", "--cached", "--no-ext-diff", "--no-textconv", "--ignore-submodules=all", "--")
	if err != nil {
		return Review{}, err
	}
	out.Staged = string(staged.Stdout)
	out.Truncated = status.StdoutTruncated || unstaged.StdoutTruncated || staged.StdoutTruncated
	return out, nil
}
func (r Review) Text() string {
	s := "STATUS\n" + r.Status + "\nUNSTAGED\n" + r.Unstaged + "\nSTAGED\n" + r.Staged
	if r.Status == "" {
		s = "Working tree clean.\n" + s
	}
	if r.Truncated {
		s += "\n[Review truncated at output limit]"
	}
	return s + "\nUntracked files appear in status; open them in the file viewer.\n"
}

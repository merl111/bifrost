package cli

import (
	"bifrost/internal/gitview"
	"bifrost/internal/workspace"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

func runReview(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) != 0 {
		return errors.New("review takes no positional arguments")
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return err
	}
	review, err := gitview.Load(ctx, root)
	if err != nil {
		return err
	}
	if opts.json {
		return writeJSON(out, review)
	}
	_, err = fmt.Fprint(out, review.Text())
	return err
}
func runSearch(ctx context.Context, opts options, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("search requires a literal query")
	}
	root, err := resolveRoot(opts)
	if err != nil {
		return err
	}
	state, err := opts.workspaceStatePath(root)
	if err != nil {
		return err
	}
	w, err := workspace.Open(root, state)
	if err != nil {
		return err
	}
	defer w.Close()
	result, err := w.Search(ctx, strings.Join(args, " "))
	if err != nil {
		return err
	}
	return writeJSON(out, result)
}

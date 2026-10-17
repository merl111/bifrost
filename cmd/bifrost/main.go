package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"bifrost/internal/cli"
)

func main() {
	ctx, stop := newSignalContext()
	defer stop()
	if err := cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "bifrost:", err)
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			os.Exit(130)
		}
		os.Exit(cli.ExitCode(err))
	}
}

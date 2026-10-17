package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"bifrost/internal/desktop"
)

// runDesktop implements capability discovery and fresh capture. The caller
// owns dispatch; actions stay in the agent desktop tools so every action can
// enforce its observation lease.
func runDesktop(ctx context.Context, args []string, stdout io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: bifrost desktop capabilities [flags]")
		_, _ = fmt.Fprintln(stdout, "       bifrost desktop capture [flags]")
		return nil
	}
	if args[0] != "capabilities" && args[0] != "capture" {
		return fmt.Errorf("unknown desktop command %q", args[0])
	}
	fs := flag.NewFlagSet("bifrost desktop "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	backendName, display, artifactDir := "", "", ""
	fs.StringVar(&backendName, "backend", "", "display backend: x11, wayland, or auto")
	fs.StringVar(&display, "display", "", "display identifier")
	fs.StringVar(&artifactDir, "artifact-dir", "", "directory for PNG artifacts")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if len(fs.Args()) != 0 {
		return errors.New("desktop command does not accept positional arguments")
	}
	backend, err := parseDesktopBackend(backendName)
	if err != nil {
		return err
	}
	manager, err := desktop.New(desktop.Config{Backend: backend, Display: display, ArtifactDir: artifactDir})
	if err != nil {
		return err
	}
	if args[0] == "capabilities" {
		return writeJSON(stdout, manager.Capabilities())
	}
	session, err := manager.Acquire(ctx)
	if err != nil {
		return err
	}
	defer session.Close()
	observation, err := session.Capture(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, observation)
}

func parseDesktopBackend(value string) (desktop.Backend, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return desktop.BackendAuto, nil
	case "x11":
		return desktop.BackendX11, nil
	case "wayland":
		return desktop.BackendWayland, nil
	default:
		return desktop.BackendUnknown, fmt.Errorf("unsupported desktop backend %q", value)
	}
}

package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"bifrost/internal/mcpclient"
	"bifrost/internal/mcpserver"
	"bifrost/internal/mcptools"
)

// runMCPExtra handles MCP features that are independent of the historical
// inspect/call command. The root dispatcher can call it first and fall back
// to runMCP when handled is false.
func runMCPExtra(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) (handled bool, err error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "serve":
		return true, runMCPServe(ctx, opts, args[1:], stderr)
	case "config":
		return true, runMCPConfig(opts, args[1:], stdout)
	case "resources":
		return true, runMCPResources(ctx, opts, args[1:], stdout, stderr)
	case "prompts":
		return true, runMCPPrompts(ctx, opts, args[1:], stdout, stderr)
	default:
		return false, nil
	}
}

func runMCPServe(ctx context.Context, opts options, args []string, stderr io.Writer) error {
	fs := flag.NewFlagSet("bifrost mcp serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root, stateDir := opts.root, opts.stateDir
	allowWrite, allowExec := false, false
	maxOutput := 0
	fs.StringVar(&root, "root", root, "workspace root")
	fs.StringVar(&stateDir, "state-dir", stateDir, "workspace state directory")
	fs.BoolVar(&allowWrite, "allow-write", false, "expose revision-checked write tool")
	fs.BoolVar(&allowExec, "allow-exec", false, "expose explicit argv exec tool")
	fs.IntVar(&maxOutput, "max-output", 0, "maximum tool output bytes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) != 0 || strings.TrimSpace(root) == "" {
		return errors.New("usage: bifrost mcp serve --root PATH [--state-dir PATH] [--allow-write] [--allow-exec]")
	}
	return mcpserver.Serve(ctx, mcpserver.Config{Root: root, StateDir: stateDir, AllowWrite: allowWrite, AllowExec: allowExec, MaxOutputBytes: maxOutput})
}

func runMCPConfig(opts options, args []string, stdout io.Writer) error {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, _ = fmt.Fprintln(stdout, "Usage: bifrost mcp config list|add|remove --mcp-config PATH")
		return nil
	}
	// --config is the application settings file and is consumed by the global
	// dispatcher before this command runs. Keep MCP endpoint storage explicit
	// so a config-management command cannot accidentally rewrite app settings.
	mcpPath := ""
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("mcp config list", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		fs.StringVar(&mcpPath, "mcp-config", "", "MCP configuration path")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if mcpPath == "" || len(fs.Args()) != 0 {
			return errors.New("usage: mcp config list --mcp-config PATH")
		}
		servers, err := mcptools.ConfigServers(mcpPath)
		if err != nil {
			return err
		}
		return writeJSON(stdout, servers)
	case "remove":
		fs := flag.NewFlagSet("mcp config remove", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name := ""
		fs.StringVar(&mcpPath, "mcp-config", "", "MCP configuration path")
		fs.StringVar(&name, "name", "", "server name")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 || name == "" || mcpPath == "" {
			return errors.New("usage: mcp config remove --mcp-config PATH --name NAME")
		}
		return mcptools.RemoveServer(mcpPath, name)
	case "add":
		fs := flag.NewFlagSet("mcp config add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		name, endpointURL := "", ""
		var headerTexts stringListFlag
		fs.StringVar(&mcpPath, "mcp-config", "", "MCP configuration path")
		fs.StringVar(&name, "name", "", "server name")
		fs.StringVar(&endpointURL, "url", "", "Streamable HTTP endpoint")
		fs.Var(&headerTexts, "header", "header binding NAME=ENV (repeatable)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		command := append([]string(nil), fs.Args()...)
		if len(command) > 0 && command[0] == "--" {
			command = command[1:]
		}
		if mcpPath == "" || name == "" || (endpointURL == "" && len(command) == 0) || (endpointURL != "" && len(command) > 0) {
			return errors.New("usage: mcp config add --mcp-config PATH --name NAME (--url URL | -- COMMAND [ARG ...])")
		}
		headers, err := parseHeaderRefs(headerTexts)
		if err != nil {
			return err
		}
		return mcptools.AddServer(mcpPath, mcptools.ServerConfig{Name: name, URL: endpointURL, Command: command, Headers: headers})
	default:
		return fmt.Errorf("unknown MCP config command %q", args[0])
	}
}

func runMCPResources(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: mcp resources list|templates|read|subscribe|unsubscribe")
	}
	fs := flag.NewFlagSet("mcp resources", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpointURL, protocol, uri := "", "", ""
	var headerTexts stringListFlag
	fs.StringVar(&endpointURL, "url", "", "MCP endpoint")
	fs.StringVar(&protocol, "protocol", "", "protocol version")
	fs.StringVar(&uri, "uri", "", "resource URI")
	fs.Var(&headerTexts, "header", "header binding NAME=ENV (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	session, err := dialExtra(ctx, endpointURL, protocol, headerTexts, fs.Args(), stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	switch args[0] {
	case "list":
		value, err := session.ListResources(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "templates":
		value, err := session.ListResourceTemplates(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "read":
		if uri == "" {
			return errors.New("mcp resources read requires --uri")
		}
		value, err := session.ReadResource(ctx, uri)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "subscribe":
		if uri == "" {
			return errors.New("mcp resources subscribe requires --uri")
		}
		return session.Subscribe(ctx, uri)
	case "unsubscribe":
		if uri == "" {
			return errors.New("mcp resources unsubscribe requires --uri")
		}
		return session.Unsubscribe(ctx, uri)
	default:
		return fmt.Errorf("unknown MCP resources command %q", args[0])
	}
}

func runMCPPrompts(ctx context.Context, opts options, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: mcp prompts list|get")
	}
	fs := flag.NewFlagSet("mcp prompts", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	endpointURL, protocol, name := "", "", ""
	var headerTexts stringListFlag
	fs.StringVar(&endpointURL, "url", "", "MCP endpoint")
	fs.StringVar(&protocol, "protocol", "", "protocol version")
	fs.StringVar(&name, "name", "", "prompt name")
	fs.Var(&headerTexts, "header", "header binding NAME=ENV (repeatable)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	session, err := dialExtra(ctx, endpointURL, protocol, headerTexts, fs.Args(), stderr)
	if err != nil {
		return err
	}
	defer session.Close()
	switch args[0] {
	case "list":
		value, err := session.ListPrompts(ctx)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	case "get":
		if name == "" {
			return errors.New("mcp prompts get requires --name")
		}
		value, err := session.GetPrompt(ctx, name, nil)
		if err != nil {
			return err
		}
		return writeJSON(stdout, value)
	default:
		return fmt.Errorf("unknown MCP prompts command %q", args[0])
	}
}

func dialExtra(ctx context.Context, endpointURL, protocol string, headerTexts []string, command []string, stderr io.Writer) (*mcpclient.Session, error) {
	if len(command) > 0 && command[0] == "--" {
		command = command[1:]
	}
	if endpointURL != "" && len(command) > 0 || endpointURL == "" && len(command) == 0 {
		return nil, errors.New("MCP endpoint requires exactly one of --url or -- COMMAND")
	}
	headers, err := parseHeaderRefs(headerTexts)
	if err != nil {
		return nil, err
	}
	return mcpclient.Dial(ctx, mcpclient.Endpoint{URL: endpointURL, Command: command, ProtocolVersion: protocol, Headers: headers}, stderr)
}

type stringListFlag []string

func (f *stringListFlag) String() string         { return strings.Join(*f, ",") }
func (f *stringListFlag) Set(value string) error { *f = append(*f, value); return nil }

func parseHeaderRefs(values []string) ([]mcpclient.HeaderRef, error) {
	refs := make([]mcpclient.HeaderRef, 0, len(values))
	for _, value := range values {
		ref, err := parseHeaderRef(value)
		if err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	if err := mcpclient.ValidateHeaderRefs(refs); err != nil {
		return nil, err
	}
	return refs, nil
}

func parseHeaderRef(value string) (mcpclient.HeaderRef, error) {
	parts := strings.SplitN(value, "=", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return mcpclient.HeaderRef{}, errors.New("header must be NAME=ENV; secret values are not accepted")
	}
	return mcpclient.HeaderRef{Name: strings.TrimSpace(parts[0]), Env: strings.TrimSpace(parts[1])}, mcpclient.ValidateHeaderRefs([]mcpclient.HeaderRef{{Name: strings.TrimSpace(parts[0]), Env: strings.TrimSpace(parts[1])}})
}

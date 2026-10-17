# Dependencies — Linux preview

Recorded 2026-10-15. Module `bifrost` is local; no publication destination has been invented. Selected Go version: 1.26; tested with Go 1.26.6-X:nodwarf5 on Linux amd64.

## Selection

- Bubble Tea v2 and Lip Gloss: native terminal UI. Ultraviolet and ANSI are also imported for key events and cell-aware text.
- creack/pty and Charm VT: pseudo-terminal and ANSI emulator.
- chromedp and cdproto: native Go browser automation; optional external Chromium executable.
- Official MCP Go SDK: protocol and transports. Its auth dependencies are transitives, not an additional application auth framework.
- Goldmark v1.8.5: CommonMark/GFM parsing for rich chat; no third-party dependencies. Terminal rendering uses the existing ANSI and Lip Gloss packages.
- Standard library: model HTTP/JSON, CLI parsing, config, persistence, files, and commands. No provider SDK, database, ORM, JS/TS application runtime, or web frontend.

There are **10 direct modules, 20 additional compiled modules, and 40 external modules in the complete selected graph**. Graph/test-only modules are distinguished below. The Ultraviolet and go-json-experiment pseudo-versions come from the selected released dependency graph; they are pinned, not floating branches. Add dependencies only for a concrete need, with a graph and binary-size check.

## Build

```sh
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/bifrost ./cmd/bifrost
go version -m bin/bifrost
go list -m all
go mod graph
```

The Linux binary builds without CGO and is statically linked. Exact size is in [initial-build.md](initial-build.md). Default Go builds may use standard-library CGO DNS/user lookup, but no application library requires it. Git, ripgrep, SSH, Chromium, QEMU, and VirtualBox are separate optional executables. Their availability does not establish working integrations.

## License inventory

These are pinned module-root license observations. MCP SDK 1.8.0 explicitly describes an Apache-2.0 transition with retained MIT contributions; its documentation has separate terms. Preserve the full notice. [Third-party notices](third-party-notices.md) retain module-root texts for compiled dependencies and the Go toolchain. A release still needs a file-level notice audit and a license choice for Bifrost.

| Module | Version | Use | Module-root license |
|---|---|---|---|
| `charm.land/bubbletea/v2` | `v2.1.0` | binary | MIT |
| `charm.land/lipgloss/v2` | `v2.0.6` | binary | MIT |
| `cloud.google.com/go/compute/metadata` | `v0.3.0` | graph/test only | metadata only; not compiled |
| `github.com/aymanbagabas/go-udiff` | `v0.4.1` | graph/test only | MIT |
| `github.com/bits-and-blooms/bitset` | `v1.24.6` | graph/test only | metadata only; not compiled |
| `github.com/charmbracelet/colorprofile` | `v0.4.3` | binary | MIT |
| `github.com/charmbracelet/ultraviolet` | `v0.0.0-20261008173134-6b8d4baf91b4` | binary | MIT |
| `github.com/charmbracelet/x/ansi` | `v0.11.9` | binary | MIT |
| `github.com/charmbracelet/x/exp/golden` | `v0.0.0-20250806222409-83e3a29d542f` | graph/test only | MIT |
| `github.com/charmbracelet/x/exp/ordered` | `v0.1.0` | binary | MIT |
| `github.com/charmbracelet/x/term` | `v0.2.2` | binary | MIT |
| `github.com/charmbracelet/x/termios` | `v0.1.1` | binary | MIT |
| `github.com/charmbracelet/x/vt` | `v0.1.0` | binary | MIT |
| `github.com/charmbracelet/x/windows` | `v0.2.2` | binary | MIT |
| `github.com/chromedp/cdproto` | `v0.157.8` | binary | MIT |
| `github.com/chromedp/chromedp` | `v0.21.0` | binary | MIT |
| `github.com/clipperhouse/displaywidth` | `v0.11.0` | binary | MIT |
| `github.com/clipperhouse/stringish` | `v0.1.1` | graph/test only | metadata only; not compiled |
| `github.com/clipperhouse/uax29/v2` | `v2.7.0` | binary | MIT |
| `github.com/creack/pty` | `v1.1.24` | binary | MIT |
| `github.com/go-json-experiment/json` | `v0.0.0-20260213210345-44df1a37e875` | binary | BSD (see upstream text) |
| `github.com/golang-jwt/jwt/v5` | `v5.3.1` | graph/test only | MIT |
| `github.com/google/go-cmp` | `v0.7.0` | graph/test only | BSD (see upstream text) |
| `github.com/google/jsonschema-go` | `v0.4.3` | binary | MIT |
| `github.com/lucasb-eyer/go-colorful` | `v1.4.1` | binary | MIT |
| `github.com/mattn/go-runewidth` | `v0.0.30` | binary | MIT |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | binary | Apache-2.0 + retained MIT; docs CC-BY-4.0 |
| `github.com/muesli/cancelreader` | `v0.2.2` | binary | MIT |
| `github.com/rivo/uniseg` | `v0.4.7` | binary | MIT |
| `github.com/segmentio/asm` | `v1.1.3` | binary | MIT |
| `github.com/segmentio/encoding` | `v0.5.4` | binary | MIT |
| `github.com/xo/terminfo` | `v1.0.0` | binary | MIT |
| `github.com/yuin/goldmark` | `v1.8.5` | binary | MIT |
| `github.com/yosida95/uritemplate/v3` | `v3.0.2` | binary | BSD (see upstream text) |
| `golang.org/x/exp` | `v0.0.0-20231006140011-7918f672742d` | graph/test only | BSD (see upstream text) |
| `golang.org/x/oauth2` | `v0.35.0` | binary | BSD (see upstream text) |
| `golang.org/x/sync` | `v0.22.0` | binary | BSD (see upstream text) |
| `golang.org/x/sys` | `v0.47.0` | binary | BSD (see upstream text) |
| `golang.org/x/time` | `v0.15.0` | binary | BSD (see upstream text) |
| `golang.org/x/tools` | `v0.42.0` | graph/test only | BSD (see upstream text) |

Full directed graph: [module-graph.txt](module-graph.txt).

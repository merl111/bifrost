# Current build evidence

Date: 2026-10-15. Platform: Linux amd64. Toolchain: Go 1.26.6-X:nodwarf5. This note records the current local workflow increment; it does not claim the full development-plan gates or a completed developer preview.

The current code connects a bounded provider loop to a Bubble Tea workspace, project-scoped skills, MCP tool adapters, durable session snapshots, and host-side VM inspection/input commands. The implementation remains a single-process prototype. The daemon/client/worker architecture, remote environments, and complete review/editor workflow are still backlog work.

## Evidence-backed behavior

| Surface | Implemented behavior | Remaining boundary |
|---|---|---|
| TUI chat | Bubble Tea chat pane accepts bounded multiline prompts, streams provider deltas and tool activity through `ChatRunner`, keeps bounded history, and cancels an active turn | Provider configuration is explicit; no approval/input forms, attachments, steering queue, persisted chat viewport, or daemon event stream |
| Review | Review loader displays a bounded Git status/diff snapshot in the viewer and refreshes it asynchronously with cancellation | Review is read-only; no hunk actions, staging, patch planner, or Git commit/worktree workflow |
| Editor | Focused rooted files can be opened in a validated external editor argv using `tea.ExecProcess`; focus and review state return after exit | This is external-editor integration, not the planned in-pane editor, Vim mode, draft recovery, or shared revision-aware save buffer |
| Agent/provider | OpenAI-compatible Chat Completions supports bounded non-streaming and SSE streaming responses, tool calls, local HTTP endpoints, and explicit write/exec policy flags | No native Responses/Anthropic/Gemini adapters, paid-provider smoke tests, automatic retries, compaction, or usage accounting |
| Skills | Local skill snapshots validate metadata, copy bounded resources, isolate project scopes, preserve digests, support resource reads, and expose explicit runtime activation; Git sources are archived without checkout execution | No full update/rollback UX, trust prompts, or broad skill discovery/marketplace workflow |
| MCP tools | Stdio and Streamable HTTP sessions support modern/legacy protocol fixtures, pagination, bounded tool catalogs, structured/media result mapping, and cancellation; configured tools are added to the agent loop | Authentication/OAuth, credentials, resources/templates/prompts/subscriptions, elicitation continuation, and large-catalog UI remain unsupported |
| Durable sessions | JSON snapshots persist complete turns, enforce root/provider binding and revisions, expose CLI list/show, and allow `run --session` resume | No daemon event journal, multi-client protocol, task graph, export/import, or reconnect reconciliation |
| Browser tools | Explicit `--browser` exposes isolated Chromium navigation, snapshots, click, and insertion to the model with per-turn cancellation | Accessibility text only; no screenshot model input, browser pane, or native desktop control |
| VM CLI | `vm qemu status/screenshot/key/pointer` attaches to an existing QMP socket; `vm virtualbox list/show/screenshot/text/scancodes` uses explicit `VBoxManage` argv. No command starts, stops, resets, or reconfigures a VM | VM lifecycle/snapshots, guest worker readiness, desktop leases/observations, VirtualBox pointer input, and host/guest coding workflow remain |

## Commands and test evidence

The focused milestone suites passed with the configured isolated Go caches:

```sh
BIFROST_TERMINAL_TEST=1 \
  GOCACHE=/tmp/bifrost-go-cache \
  GOMODCACHE=/tmp/bifrost-go-mod \
  GOPATH=/tmp/bifrost-go \
  go test -race ./internal/ui -count=1
# ok bifrost/internal/ui 2.820s

GOCACHE=/tmp/bifrost-go-cache \
GOMODCACHE=/tmp/bifrost-go-mod \
GOPATH=/tmp/bifrost-go \
go test ./internal/agent ./internal/provider ./internal/skill ./internal/mcpclient ./internal/session ./internal/cli -count=1
# ok for all six packages

GOCACHE=/tmp/bifrost-go-cache \
GOMODCACHE=/tmp/bifrost-go-mod \
GOPATH=/tmp/bifrost-go \
go test -race ./internal/vm/qemu ./internal/vm/virtualbox
# ok for both packages
```

The CLI integration selection passed the local HTTP provider, durable-session, runtime activation, VM QMP, VM argument validation, and VirtualBox fixture cases:

```sh
go test ./internal/cli -run \
  'TestRunWiresReadEditExecAndFinalThroughLocalHTTP|TestRunSessionTurnPersistsAndResumesCompleteHistory|TestRuntimeCLIFlagsAndNoImplicitActivation|TestRunVM' \
  -count=1 -v
```

The opt-in disposable QEMU test passed against the hardened QMP client. It launched a disposable `qemu-system-x86_64` instance with `-machine none -nodefaults -display none -S -no-reboot`, attached through a temporary QMP Unix socket, queried host-side status, and terminated the disposable process during cleanup:

```sh
BIFROST_QEMU_INTEGRATION=1 \
  GOCACHE=/tmp/bifrost-go-cache \
  GOMODCACHE=/tmp/bifrost-go-mod \
  GOPATH=/tmp/bifrost-go \
  go test ./internal/vm/qemu -run TestDisposableQEMUQueryStatus -count=1 -v
# PASS: TestDisposableQEMUQueryStatus (0.03s)
```

These checks use fake/local fixtures and a disposable QEMU process. No user VM, disk, browser profile, paid model account, or remote repository was changed. Root ran the whole-repository race suite with Chromium, PTY/Vim, and disposable QEMU enabled. It exposed a QMP context-deadline race, which was fixed and repeated successfully 20 times before the complete suite passed. Final verification after the maintainability pass is recorded below.

## Known limitations to carry forward

- The TUI still lacks the in-pane editor and complete review mutation flow required by P4.
- Session snapshots provide restart/resume foundation but are not the daemon-owned durable event protocol described by P1/P3.
- MCP authentication and continuation features are intentionally surfaced as unsupported rather than silently simulated.
- QMP reports hypervisor state only; it does not establish guest OS readiness or SSH availability. VirtualBox pointer input is an explicit unsupported capability because the verified `VBoxManage` interface does not provide it.
- Native provider families, remote workers, GitHub/worktrees/LSP, desktop capture, VM lifecycle, and cross-platform qualification remain open backlog work.

## Review findings and validation scope

Root reviewed agent output bounds, MCP catalog/result limits, browser reference/action handling, session pre-execution locking and interruption markers, skill Git isolation, and QMP deadline/close behavior. Fixes include validating the entire tool-call batch before mutation, session revision checks before execution, joined cancellation watchers, bounded QMP events, resolved Git skill provenance, and correct QMP absolute pointer coordinates (0..32767, from [upstream input.h](https://raw.githubusercontent.com/qemu/qemu/master/include/ui/input.h)).

No new third-party module was added in this increment. State snapshot replacement is shared between the registry and conversation store, with file and directory synchronization. The original module/license inventory still applies.

The structural quality scan did not pass its comparison gate: the repository has no committed HEAD and comparison uses a local sidecar snapshot taken during early development. It flags Go test entrypoints as dead code, unrelated short functions as clones, and some real remaining complexity. Root reduced the agent loop, SSE parsing, UI event handlers, and MCP conversion responsibilities and removed duplicated snapshot-write code. Remaining large dispatchers/parsers (notably skill metadata and VM command handling) are tracked maintenance work; no clean-quality claim is made.

## Final integration verification

After the reviewed refactors, all packages passed:

```sh
BIFROST_BROWSER_TEST=1 BIFROST_TERMINAL_TEST=1 BIFROST_QEMU_INTEGRATION=1 \
  GOCACHE=/tmp/bifrost-go-cache GOMODCACHE=/tmp/bifrost-go-mod GOPATH=/tmp/bifrost-go \
  go test -race -count=1 ./...
GOCACHE=/tmp/bifrost-go-cache GOMODCACHE=/tmp/bifrost-go-mod GOPATH=/tmp/bifrost-go go vet ./...
CGO_ENABLED=0 GOCACHE=/tmp/bifrost-go-cache GOMODCACHE=/tmp/bifrost-go-mod GOPATH=/tmp/bifrost-go \
  go build -trimpath -ldflags='-s -w' -o bin/bifrost ./cmd/bifrost
```

A separate executable smoke test used a loopback SSE fixture, temporary workspace/state directories, and a 120×36 PTY. It completed a streamed model-requested file creation, resumed the saved conversation in a second CLI process, restored that history in the TUI, submitted another chat prompt, received streamed text, exited normally, and verified the final persisted message. Four fixture model requests were served; no credentials or paid APIs were used.

`file bin/bifrost` reports a stripped, statically linked Linux amd64 executable. The final binary is 12,452,002 bytes (about 11.88 MiB). The module dependency set remains unchanged from the initial build (9 direct modules).

A final skill-install cancellation regression uses a fake Git process whose descendant retains stdout and ignores SIGTERM. Git operations now reuse the bounded process runner and group cleanup; the fixture returns after the deadline and cleanup grace period instead of hanging. Archive extraction uses a bounded in-memory tar, with size/path/type checks before installation.

# Initial implementation evidence

Date: 2026-10-15. Platform: Linux amd64. Toolchain: Go 1.26.6-X:nodwarf5. This is the first runnable implementation increment, not the completed developer preview.

Three GPT-5.6-luna subagents implemented independent packages. The primary agent reviewed, integrated, and corrected the code, then ran independent whole-repository checks. No live paid model requests were made, no VM was modified, and no remote repository was created or pushed.

## Implemented surfaces

| Area | Actual behavior | Limitations |
|---|---|---|
| Projects | Persistent IDs, registry, multiple repository aliases/roots, metadata revisions, add/list/add-repo CLI | No cloning, GitHub authentication, PRs, sessions, or project skill bindings |
| Config | Strict versioned portable manifest and machine-local config parsing/resolution | Not wired to effective config UI/CLI; no full layering/provenance |
| Store | Atomic metadata files, kernel writer locks, append JSONL journal with sequence checks and tail recovery | Journal is infrastructure, not a persisted agent transcript; quotas/migrations/artifact store pending |
| Files | Rooted list/read, SHA-256 preconditions, exact unique replacement, whole-file writes, saved before-images, guarded undo | Text limit 8 MiB; single-file operations; no patch engine, search UI, LSP or concurrent external-writer CAS |
| Commands | Explicit argv, cwd, capped stdout/stderr, exit code, cancellation and Unix group cleanup | Host permissions, no isolation policy or persistent background-job service |
| Models | Configurable OpenAI-compatible Chat Completions, stdlib HTTP, bounded responses, no automatic retries | Fixture tested only, nonstreaming, no native Responses/Anthropic/Gemini, usage accounting, compaction or resume |
| Agent | In-memory bounded read/list/edit/write/exec loop; writes/exec explicitly enabled; tool results preserved | No MCP/browser dispatch, skill loading, durable session, approval UI or in-product subagents |
| TUI | Files, scrolling viewer, activity/chat placeholder, real PTY; keyboard focus; terminal key/paste forwarding; responsive dimensions | No chat composer/model integration, review/editor pane, layout persistence, terminal scrollback or controller leases |
| MCP | Stdio and Streamable HTTP, modern/legacy handshake, paginated listing, calls, content mapping, cancellation | No credentials/OAuth, resources/prompts/subscriptions or input-required continuation; unresolved input is reported explicitly |
| Browser | Dedicated headless Chromium, AX snapshot refs, native CDP click/type, PNG capture, explicit attach API | Launch/action path tested; remote attachment, frames, downloads, browser pane, remote worker and agent tool integration pending |
| Environment doctor | Git, rg, SSH, Chromium, QEMU and VBoxManage executable discovery | No SSH/QMP/VirtualBox connection or guest-readiness claim |

The prototype currently runs in one process. The daemon/client/worker architecture and detailed contracts remain the target, not implemented abstractions hidden behind these commands. Independent feasibility and foundation packages were developed in parallel; later full feature gates are not considered passed.

## Tests and build

Run from the repository root:

```sh
BIFROST_BROWSER_TEST=1 BIFROST_TERMINAL_TEST=1 go test -race -count=1 ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/bifrost ./cmd/bifrost
./bin/bifrost --version
./bin/bifrost doctor --json
```

The browser and terminal flags enable actual local Chromium and Vim integration tests. The full test invocation uses loopback HTTP fixture servers and local subprocesses and was run with those capabilities allowed. Restricted-sandbox socket failures are not evidence of a passing integration. Default tests skip the explicit optional browser/Vim probes; the recorded full run enables both. No paid service or user browser profile is used.

Validated scenarios include:

- Restarted project registry, stale update rejection, duplicate names, writer exclusion, lock recovery, strict config parsing.
- Journal sequence validation, incomplete final JSON recovery, unterminated complete record followed by append, malformed record rejection, poisoned writer handling.
- Revision conflicts, ambiguous replacement, missing/binary/invalid files, rooted symlink escape rejection, modes/line endings, undo after restart, corrupt before-image rejection, separate root histories and concurrent writer exclusion.
- High-output quick-exit capture without lost bytes, explicit cwd/environment, output caps, cancellation and process-group cleanup.
- HTTP fixture through the CLI: read file → edit using its returned revision → execute a real command → final model response. Default write/exec denial, cancellation, step/context/tool limits, duplicate calls, truncated completions, redirect rejection, credential URL rejection.
- MCP stdio and HTTP tool calls; protocol modes 2026-07-28 and 2025-11-25; pagination/cursor bounds; cancellation; structured/media mapping; modern input-required result is surfaced without retry.
- Chromium local form: launch → navigate → observe → type via CDP → click → observe result → capture PNG. Reusing old snapshot refs is rejected.
- PTY shell output, Unicode/ANSI state, pane dimensions, focus escape, scrolling, large paste, concurrent output/close, closing a child with blocked input, and actual Vim edit/save/quit after resize.

Terminal input is bounded: UI paste limit 64 KiB, queued output-to-child chunks capped. If the child stops consuming input and the queue fills, the status reports undelivered input instead of freezing the UI. It is not a promise of lossless arbitrary input flooding. Full terminal performance and compatibility qualification is still open.

Binary baseline: **11,985,058 bytes** (11.43 MiB), stripped, statically linked Linux amd64. A real 120×36 PTY launch of `bin/bifrost` rendered all panes; entering terminal mode, executing `printf`, receiving `BIFROST_SMOKE_OK`, escaping with Ctrl-], and quitting with `q` succeeded.

## Review corrections

The independent review found and fixed: process pipe-drain loss at quick exit; missing project revision conflict checks; workspace state reuse across roots; malformed change identifiers and corrupt undo images; lock ownership after process death; missing JSONL separator recovery; silent MCP input-required handling; browser lifetime canceled at startup; reusable stale browser refs; unintended provider redirects; malformed/null tool arguments; cancellation before mutations; Unicode display widths and pane clipping; unrooted viewer access; and PTY pipe/backpressure/shutdown deadlocks. Regression tests exercise these boundaries.

Ripwire quality-delta could not compare against a baseline because this new repository has no HEAD or prior quality snapshot. Its automatic diff-based test gate likewise had no committed diff to inspect. These are recorded as unavailable checks, not passes. Go vet, race tests, fixture integrations, source review and the executable build are the validation used here.

## Remaining work

The backlog remains authoritative. The next coherent increment is to complete the local coding workflow: wire the provider loop into a streaming chat pane and tool event feed; add actual Git diff/review and patch/search tools; implement skill installation and project activation; and route MCP tools through the common agent dispatcher. Establish durable session/events and cancellation semantics before claiming resume or multiple clients.

Native providers, authenticated MCP/resources/prompts/continuation, GitHub workflows, worktrees/LSP, SSH workers, browser integration, QEMU/VirtualBox management and guest desktop input, host computer use, in-pane editing and platform qualification all remain required. Linux is the tested runtime. Native Windows and macOS are not claimed supported based on compilation alone.

No upstream OpenCode or Crush implementation was copied. A source-port audit is required before selecting any future port. Dependency inventory and license texts are in [dependencies.md](dependencies.md).

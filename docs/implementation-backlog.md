# Bifrost implementation backlog

This is an ordered execution plan. Completed work is checked only when its full acceptance scope is met; partial implementations remain unchecked and are recorded in [initial-build.md](initial-build.md) and [current-build.md](current-build.md). Read the [development plan](development-plan.md) for scope and the [contracts](contracts.md) for schemas and invariants before implementing an item.

Each work package should be a reviewable change with its acceptance evidence. Add packages only when they have implemented behavior. Do not check off a phase because a screen renders or because a model transcript says it succeeded. Record exact commands, platforms, and limitations in a milestone evidence note.

## P0. Resolve foundation risks

Dependencies: none. Output: a small Go module, real feasibility prototypes, and pinned dependency decisions.

- [x] P0.1 Establish repository metadata without overwriting the environment-managed placeholders. Inspect actual permissions and choose an authorized usable Git location if required. Do not invent a remote or publish code.
- [x] P0.2 Create `go.mod` with the actual chosen module path and supported Go toolchain. Pin released Bubble Tea/Lip Gloss versions, candidate PTY/VT libraries, chromedp, and MCP SDK in an isolated spike or staged module. Write `docs/dependencies.md` with full graph, licenses, binary-size baseline, and CGO requirements. Resolve package/version compatibility by building, not by copying upstream `main` requirements blindly.
- [ ] P0.3 In `internal/ui/terminal`, prove a real PTY can render through the chosen emulator inside a resizable Bubble Tea pane. Test shell input, alternate screen, `less`/editor interaction, Ctrl-C, bracketed paste, Unicode widths, ANSI colors, and focus escape. Run alongside simulated chat streaming and a large diff.
- [ ] P0.4 In `internal/mcp`, connect Go fixture servers using modern and legacy SDK protocol modes. Prove stdio, Streamable HTTP, cancellation, pagination, a tool call, and elicitation. Identify and pin OAuth/task-extension support. Record any actual SDK gaps and implement a bounded compatibility adapter or explicitly revise the supported matrix before P5.
- [x] P0.5 Prove chromedp launches or attaches to a dedicated Chromium profile and captures a local Go-served form page. Confirm no Node application runtime is required.
- [ ] P0.6 Record Linux X11/Wayland desktop capture/input routes, portal/session constraints, QMP capture/input behavior, and VirtualBox input path. Prototype the uncertain pointer-input path and Wayland permission flow. Do not defer these discoveries until final polish.
- [ ] P0.7 Inspect candidate upstream OpenCode edit/patch units and tests at a pinned commit. Create `docs/upstream-ports.md`; retain license notices for any copied code. Current Crush stays a behavior reference unless separately eligible source is identified.

Gate: select the UI/emulator adapter only after a real nested terminal survives resize, cancellation, and focus switching. Record a supported dependency/toolchain set and pass MCP/browser probes. If a VT candidate fails, compare another bounded Go implementation or a limited upstream port; do not substitute an output-only panel and declare terminal support complete.

Evidence: build commands, module graph, benchmark hardware, terminal emulator versions, fixture transcripts, and a short manual interaction checklist. This phase establishes measured targets and enables realistic remaining-work estimates.

## Current milestone status

The 15 October 2026 continuation implements the following features. Broad phase checkboxes remain open when their acceptance text includes unimplemented or unqualified behavior. See [current-build.md](current-build.md) for evidence and limitations.

| Area | Implemented behavior | Remaining qualification/scope |
|---|---|---|
| Setup/config | Interactive onboarding, strict private configuration, native provider profiles, project defaults, SSH targets, permission presets, imports | Full effective-config provenance and migrations |
| Agent/providers | Native Responses/Anthropic/Gemini plus compatible APIs, streaming, usage, images, opaque continuation, bounded tools, explicit compaction/export/import | Paid-account smoke tests, automatic retry/compaction policy |
| Sub-agents | Spawn/status/wait/cancel tools; bounded queue/concurrency/depth/children; isolated writing worktrees; durable graph, live usage/events, cross-client cancellation | Remote child isolation, full multi-repository task coordination |
| TUI | Inline editor/drafts, palette/help/themes, task monitor, SSH file/review/terminal adapters | Integrated approval/OAuth forms, LSP diagnostics UI, hunk actions, layout persistence |
| Daemon | Private socket/token, detached start, durable jobs/events/cursors, cancellation, daemon-backed chat/session reuse | Background PTY reattachment and broader platform qualification |
| Native editing | Recoverable multi-file JSON transactions, revisions, create/replace/delete/move, conflict-safe undo | Unified-diff parser and broader filesystem failure-injection qualification |
| MCP/skills | Auth header env refs, tools/resources/templates/prompts, subscriptions and elicitation callback APIs, native stdio server, project skill snapshots | OAuth login and TUI elicitation continuation UX |
| Git/GitHub/LSP/hooks | Stage/unstage/commit/worktrees; auth/repo/PR/checks/draft PR; explicit LSP hover/definition; trusted lifecycle hooks | Live GitHub account validation and richer integrated review/editor workflows |
| Browser/desktop/VM | Model-visible screenshots, Linux display adapter, guest RPC/SSH, QEMU/VBox lifecycle/snapshots and managed QEMU launch | Real graphical guest validation, Wayland compositor matrix, VM provisioning and unsupported snapshot-list paths |

No broad phase gate is marked complete solely by this table.

## P1. Domain, persistence, daemon, and project registry

Dependencies: P0. Output: a persistent Go workspace with no model required.

- [ ] P1.1 Implement IDs, entities, lifecycle enums, and errors in `internal/domain`. Test invalid IDs, identifier/path separation, and domain state transitions.
- [ ] P1.2 Implement config loading/resolution in `internal/config`: version checks, source precedence, trusted local overrides, explicit array behavior, and effective-config provenance. Convert the sample manifests into fixture tests. Unknown secrets remain references.
- [ ] P1.3 Implement `internal/store` append journal, snapshot/replay, atomic metadata write, artifact storage, quotas, lock ownership, and schema migrations. Cover truncated final records, interior corruption, checksum mismatch, restart, and concurrent writer rejection.
- [ ] P1.4 Implement `internal/protocol` framing/handshake and `internal/app` daemon lifecycle. Test simultaneous client startup, stale socket handling, incompatible versions, oversized frames, slow consumers, replay cursors, and disconnect/resubscribe.
- [ ] P1.5 Implement project/repository/environment registration and session/task metadata in `internal/session`. Task execution is not yet enabled. Names can change without changing IDs.
- [ ] P1.6 Add `serve`, `attach`, `project`, `config show --effective`, and basic `doctor` commands through `cmd/bifrost`. JSON output must be parseable without filtering logs.

Gate: create a project, attach two clients, change metadata, restart the daemon, and observe identical durable state. A disconnected slow client cannot block the writer or exhaust memory. A second daemon cannot write the same store.

## P2. Local environment and native coding tools

Dependencies: P1. Output: safe, directly testable coding tools and process control.

- [ ] P2.1 Implement local `FileService` and environment root resolution. Test symlinks, traversal, missing files, invalid UTF-8, binary detection, line/byte boundaries, and platform path behavior. Use rooted operations where supported rather than relying only on string prefixes.
- [ ] P2.2 Implement listing, glob discovery, and text search with `rg` adapter and Go fallback. Define semantics explicitly. Test ignore rules, filenames containing whitespace/newlines/dashes, result limits, cursors, and deterministic repository qualification.
- [ ] P2.3 Implement `internal/edit` exact replacements and write/move/delete operations with expected revisions. Reject ambiguous edits and unexpected destination existence.
- [ ] P2.4 Implement patch parser and change planner, selectively porting verified upstream units. Prevalidate all operations; add prepared/applying/committed journals and before-image storage. Fuzz malformed patches and inject failure between every write/journal boundary.
- [ ] P2.5 Implement selective undo, conflict detection, interrupted-operation reconciliation, and local editor coordination hooks. Verify manual edits survive failed undo and recovery.
- [ ] P2.6 Implement `internal/process` structured commands, explicit shell mode, environment/cwd handling, timeout, process groups, output spooling, cursors, background state, and interactive PTYs. Test descendant cancellation and sustained output under a slow observer.
- [ ] P2.7 Implement `internal/tool` registry, canonical schemas, argument validation, operation deduplication, result envelopes, and execution policy. Built-in tools receive scoped services. Bind approvals to the actual canonical action.
- [ ] P2.8 Add command-line or fixture entry points that exercise native tools through the dispatcher, without a model. Start the test artifact retention policy and dependency-free structured logging.
- [ ] P2.9 Implement the initial `internal/git` subprocess adapter for repository discovery, status, diff, base-commit capture, and one-repository task worktree creation. This supplies P3 task isolation and P4 review; multi-repository coordination and GitHub arrive in P6. Test dirty-checkout preservation and failure cleanup restricted to task-owned paths.

Gate: apply a multi-file patch, observe changes, undo it, and recover from injected partial failure without losing unrelated edits. Run and cancel a process tree and reconnect to a background terminal. Confirm repeated operation IDs do not repeat completed changes.

Testing focus: `internal/edit`, `internal/tool`, `internal/process`, and local environment contract tests. Add fuzz corpora and race tests around journals/locks. These are foundational correctness tests, not snapshot-only tests.

## P3. Agent loop, providers, and headless workflow

Dependencies: P2. Output: a functional headless coding agent.

- [ ] P3.1 Implement fake provider and deterministic stream fixtures before live adapters. Include fragmented UTF-8/text/tool arguments, multiple tool calls, tool failure, late cancellation, and transport disconnects.
- [ ] P3.2 Implement native OpenAI Responses and Anthropic Messages adapters with standard-library HTTP. Keep request mapping, stream parsing, usage, and provider continuation separate from tool execution. Obtain actual API schemas from official documentation at implementation time.
- [ ] P3.3 Add OpenAI-compatible Chat Completions adapter with configurable base URL and capability overrides. Test a local fixture endpoint and one opt-in real local server. Unsupported features produce clear errors.
- [ ] P3.4 Implement `internal/agent` turn state machine, context builder, tool scheduling, input/approval suspension, cancellation, and persisted outcome states.
- [ ] P3.5 Implement instruction loading, context limits, artifact references, compaction, usage/budget accounting, and model switching at turn boundaries. Preserve original history and opaque provider data without leaking it across adapters.
- [ ] P3.6 Implement retry policy and uncertain-operation handling. Do not replay tools after a model stream retry. Interrupted tool chains can be reconciled and continued.
- [ ] P3.7 Implement headless `run`, `provider add/test`, `model list/set`, task/session resume, and export/import. Imports validate schema and referenced artifacts; they do not execute embedded commands.

Gate: a fake-model coding scenario reads a temporary repository, edits a bug, runs a real test command, fixes a failure, and produces a correct final result. Run bounded live smoke tests for each initial provider when credentials are available. A provider outage and cancellation leave resumable, accurate histories.

Provider claims require conformance evidence; lack of a paid API credential does not justify claiming a live provider was tested. Fixture and live status must be recorded separately.

## P4. Usable terminal workspace

Dependencies: P0 terminal gate, P3. Output: the local coding vertical slice.

- [ ] P4.1 Implement `internal/ui` root model, pane tree, focus owner, mode-aware keymap, command palette, themes, resize, and persisted layout. Test wide, 80-column, narrow, monochrome, and Unicode cases.
- [ ] P4.2 Add project/task picker and lazy file tree. Repository/environment provenance is visible in every relevant view.
- [ ] P4.3 Add chat composer, multiline input, history, file/selection attachments, streaming messages, collapsible tool activity, approval/input forms, cancellation, and steering. Preserve viewport position while new messages arrive.
- [ ] P4.4 Add file viewer and review pane with unified/side-by-side diffs, hunk navigation, binary/rename state, and local change-undo actions. Refresh from events and reconcile external edits.
- [ ] P4.5 Integrate process output and real terminal panes using the proven emulator. Implement terminal-controller leases, input forwarding, resizing, scrollback, pane close versus process stop, and attach/detach.
- [ ] P4.6 Implement external editor launch and focus restoration. For remote-capable design, pass an environment-qualified file reference rather than a host-only path.
- [ ] P4.7 Add UI error/reconnect states and performance benchmarks under simultaneous large output, model streaming, search, and diff updates.

Gate: complete a local coding task entirely from the TUI, switch panes with the keyboard, manually inspect files, edit through the configured editor, run an interactive command, review the actual diff, and detach/reattach. No token stream steals focus or corrupts the terminal. Measure P0 latency budgets.

This is the local vertical slice. It is not yet the developer preview because skills and MCP are required next.

## P5. Skills and first-class MCP

Dependencies: P3/P4 and MCP spike. Output: the first developer preview.

- [ ] P5.1 Implement Agent Skills discovery, frontmatter validation, explicit activation, progressive loading, name resolution, and per-turn revision capture in `internal/skill`.
- [ ] P5.2 Add local/Git installation, subdirectory/ref selection, commit resolution, managed cache, lockfiles, update diff, rollback binding, and uninstall. Installation cannot execute bundled scripts. Git source and path validation prevents checkout traversal.
- [ ] P5.3 Implement MCP manager states, local stdio launches, Streamable HTTP, SDK-version negotiation, optional legacy HTTP+SSE compatibility, log separation, deadlines, bounded reconnect, and tool catalog caching.
- [ ] P5.4 Implement namespaced tools, schema validation, structured/media result mapping, pagination, catalog refresh, search/activation of large catalogs, and dispatch through the native policy/history path.
- [ ] P5.5 Implement resources, templates, subscriptions where supported, prompt invocation, and arguments UI. Resource content keeps server provenance.
- [ ] P5.6 Implement credentials and MCP auth flows supported by the selected SDK: bearer references, OAuth metadata, login, PKCE requirements, refresh, audience/resource validation, and redirect rules. Add an in-process fixture authorization server rather than depending on a live account for CI.
- [ ] P5.7 Implement negotiated form/URL elicitation, modern input-required continuation, task-extension lifecycle, and legacy capability handling. Roots/sampling/logging are version-gated; legacy sampling is opt-in and budgeted.
- [ ] P5.8 Build skills/MCP panes and CLI commands: install/add, inspect, enable/disable, update, restart, login, import preview, and inspect errors. New imported executable configurations require applicable trust before launch.
- [ ] P5.9 Test servers in the local environment worker abstraction. Remote worker-launched stdio parity lands in P7; the UI must not claim it is available yet.

Gate: install a pinned project skill and activate it in a real agent turn; call one stdio and one authenticated HTTP MCP tool; attach a resource; invoke a prompt; handle elicitation; cancel a slow call; recover a crashed server. A server with thousands of tools remains usable without loading its entire catalog into model context. Account changes do not reuse another account's credentials or tool cache.

Compatibility matrix must name protocol versions/transports/features actually exercised. Never advertise a feature merely because the underlying SDK has a similarly named API.

## P6. Multiple repositories, GitHub, and diagnostics

Dependencies: P5. Output: complete project-level development workflows.

- [ ] P6.1 Implement isolated task worktrees per selected repository with recorded base commits, branches, paths, and ownership. Handle partial creation failures without deleting user-owned checkouts. Existing-worktree mode preserves initial dirty/index state.
- [ ] P6.2 Implement aggregated project search/review and per-repository Git status. File aliases and relative paths never cross repository boundaries accidentally.
- [ ] P6.3 Implement hunk stage/unstage with fresh index/worktree checks, commits, fetch/push, and conflict presentation. Respect Git hooks and user credential configuration.
- [ ] P6.4 Implement GitHub identity mapping, optional `gh` authentication, configured token references, metadata/PR/check reads, conditional refresh, and explicit PR preparation/publication. Support configurable host identity for Enterprise; verify host-specific behavior before claiming full compatibility.
- [ ] P6.5 Implement PR bundles linking independent repository PRs, dependency ordering, and readiness display. No automatic cross-repository merge claim.
- [ ] P6.6 Implement lazy language-server lifecycle, JSON-RPC framing, document synchronization, position encoding, capability discovery, diagnostics, definition/reference/symbol lookup, and cancellation.
- [ ] P6.7 Integrate named checks and formatting into tools/UI. Formatting creates an attributable revision-checked change; stale LSP diagnostics are ignored or labeled. Language servers are optional external prerequisites.

Gate: change two repositories in one task; test both; inspect diagnostics; stage different hunks; commit separately; prepare a linked PR bundle. Deliberately introduce stale file/index state and a crashed language server. Preserve unrelated modifications and keep the core workflow usable without LSP.

## P7. Remote worker and SSH parity

Dependencies: P6. Output: the same tool contracts on a remote machine.

- [ ] P7.1 Implement `bifrost worker --stdio` handshake, allowed roots, capability/version advertisement, platform mapping, request deadlines, journal ownership, and artifact streaming.
- [ ] P7.2 Implement system-SSH transport with configured host aliases, validated worker invocation, user agent/config reuse, stderr separation, checksum-verified optional provisioning, and reconnect.
- [ ] P7.3 Port local file/process/Git contracts to worker RPC. Keep environment paths worker-local. Run the identical file/edit/process conformance suite against a disposable SSH target.
- [ ] P7.4 Implement operation status reconciliation and terminal reconnect with boot ID validation. Distinguish disconnected, running, completed, and unknown results; reject stale handles.
- [ ] P7.5 Run MCP stdio servers, language servers, checks, and external editors at the configured environment location. Keep model and unrelated credentials on the host.
- [ ] P7.6 Implement explicit repository provisioning and workspace export/transfer. No automatic host/guest file mirroring. Switching environments validates binding/version compatibility and creates a new task or an explicit migration record.

Gate: run a complete coding task over SSH, drop the connection during a file edit and a long command, reconnect, and recover without duplicate execution. Verify credentials/root scopes and identify any command whose outcome cannot be known. File tree, Git diff, diagnostics, and shell all show the remote source of truth.

## P8. Browser automation

Dependencies: P7 and browser spike. Output: browser tools on local and remote environments.

- [ ] P8.1 Implement browser session/profile/tab lifecycle, dedicated default profiles, explicit existing-CDP attachment, and cleanup in `internal/browser`.
- [ ] P8.2 Implement semantic observations and actions with stable observation IDs, stale-element detection, navigation waits, frames, input, screenshots, downloads, console logs, and bounded selected network metadata.
- [ ] P8.3 Add artifact and model-image delivery, size limits, capability checking, and browser pane observations. Implement terminal image preview only behind feature detection; text-only terminals remain supported.
- [ ] P8.4 Integrate browser interaction leases, cancellation, task ownership, target URL visibility, and environment policy. Keep profiles/cookies isolated unless explicitly shared.
- [ ] P8.5 Test Go-served fixture sites locally and through an SSH worker; include redirects, cross-origin frames, download failures, stale observations, page crash, and disconnect.

Gate: edit a local test application, launch its server in the task environment, use a Chromium session in that environment to reproduce and verify behavior, and attach actual screenshots/observations to the session. No Node automation service is needed.

## P9. QEMU, VirtualBox, and VM desktops

Dependencies: P7/P8 and VM feasibility probes. Output: the complete requested VM development workflow.

- [ ] P9.1 Implement QMP connection/version detection, state queries, attach, lifecycle actions, supported snapshots, input, and screenshots. Test error/event interleaving and disconnected QMP independently of guest SSH.
- [ ] P9.2 Implement QEMU managed launch definitions after attach works: disk/firmware ownership, architecture/accelerator checks, resources, networking, display, and guest provisioning. Never delete user disks on connection removal.
- [ ] P9.3 Implement VirtualBox discovery/inspection/lifecycle/snapshot/capture through `VBoxManage` with version fixtures and argument-safe invocation. Represent remote hypervisor hosts explicitly.
- [ ] P9.4 Implement the verified VirtualBox pointer/keyboard route through available management SDK facilities or a Go guest helper. Add Guest Additions/guest-control capability detection where used. Keep extension-pack-dependent functionality optional.
- [ ] P9.5 Implement shared desktop capture/action contracts: display IDs, dimensions, coordinate conversion, timestamps, leases, cancellation, and screenshot artifact references. Require a fresh observation after reconnect or display change.
- [ ] P9.6 Bind guest workers, repositories, browsers, diagnostics, and terminals to VM environments. Separate hypervisor connected state from guest execution ready state in the UI.
- [ ] P9.7 Add VM/environment picker, capability display, provisioning flow, doctor checks, and opt-in disposable-VM integration suites.

Gate: repeat the multi-repository coding scenario in both QEMU and VirtualBox. Run tests, navigate the app in its guest browser, capture a desktop, and execute a validated pointer/key action. Exercise guest reboot, management disconnect, missing Guest Additions, unsupported snapshots, and worker version mismatch. Claim capabilities individually based on evidence.

## P10. Complete developer experience

Dependencies: P9 for full integration; independent UI/provider work can begin after P6 when it does not destabilize shared contracts. Output: full feature scope on the reference platform.

- [ ] P10.1 Implement the in-pane editor with selection, basic Vim-style navigation/modes, insert/delete, local undo/redo, find, save, dirty state, crash-recovered drafts, and external-change detection. Every save uses the shared revision-aware edit service.
- [ ] P10.2 Resolve concurrent human/agent edits with save/discard/conflict workflows. Integrate editor buffers with LSP versions and preserve manual undo history where possible. Add optional measured pure-Go highlighting only if necessary.
- [ ] P10.3 Implement Linux host desktop capture/input for the tested X11 and Wayland routes. Detect unsupported portal/compositor combinations, request actual OS permission through its normal flow, and show capability limitations.
- [ ] P10.4 Implement Gemini native provider and extend provider capability fixtures. Document supported custom endpoints and cloud gateway paths; implement native cloud authentication adapters only as individually tested work packages.
- [ ] P10.5 Implement bounded in-product child tasks with explicit budgets, cancellation propagation, isolated writing worktrees, and reviewable results. No unrestricted recursive spawning or same-worktree write races.
- [ ] P10.6 Implement declarative task/tool hooks through the normal dispatcher, with timeout, bounded output, loop prevention, and explicit executable trust.
- [ ] P10.7 Add optional `bifrost mcp serve --stdio` exposing selected native tools under an explicit project scope and policy. Test that connecting an external client cannot widen access.
- [ ] P10.8 Finish onboarding, keyboard help, configuration import previews, missing-prerequisite actions, skill/MCP update workflows, and session export controls. All actions are accessible from the command palette.

Gate: a developer can manually edit, ask the agent to continue, inspect diagnostics and diffs, resolve a conflict, operate a browser/desktop, use skills/MCP, and resume the task without manually repairing state. External tools unavailable on a machine result in clear feature-specific diagnostics, not application startup failure.

## P11. Platform completion and release qualification

Dependencies: all previous required gates. Output: releasable binaries and truthful compatibility documentation.

- [ ] P11.1 Complete macOS process/PTY, filesystem metadata, credential store, desktop capture/input permission, and terminal behavior. Verify remote VM attach separately from host-managed virtualization capabilities.
- [ ] P11.2 Complete Windows local transport ACLs/authentication, ConPTY, job-object cancellation, path/URI semantics, file replacement/sharing, credential storage, and desktop APIs. Test native Windows separately from WSL; WSL success does not prove native support.
- [ ] P11.3 Run performance budgets against recorded workloads: 100k paths, large file/diff, long sessions, high command output, and large MCP catalogs. Profile before adding caches or changing frameworks.
- [ ] P11.4 Run recovery/soak tests: repeated attach/detach, daemon/worker crash points, disk full, corrupt journal tail, expired tokens, provider rate limits, network loss, and orphan process reconciliation. Verify bounded resource use.
- [ ] P11.5 Package per-OS/architecture Go binaries with checksums, licenses, version metadata, and a dependency manifest. Browser/VM/language servers remain optional prerequisites. A release must not download application JS runtimes at startup.
- [ ] P11.6 Add CI for unit/integration/race/fuzz-smoke checks, vet, build matrix, dependency/license changes, and vulnerability scanning. Keep live APIs and hypervisors in explicit opt-in jobs with bounded resource costs.
- [ ] P11.7 Publish user documentation, keymaps, config schema, provider/MCP compatibility matrix, VM setup, backup/export/recovery instructions, and known platform limitations.
- [ ] P11.8 Execute the full release acceptance scenario from the development plan. Review each fixed requirement against recorded evidence. No unmet requirement can disappear into a generic future-work note.

Gate: all required scenarios pass on the declared supported matrix; any unavailable combination is named precisely. The binary launches into a usable basic workflow without Chromium, a VM, a language server, or an MCP server installed.

## Requirement coverage

- Go and low dependencies: P0, P11; inspect the entire module graph and runtime prerequisites.
- Beautiful terminal panes and Vim-style movement: P0, P4, P10, P11.
- Integrated file/code editing and review: P2, P4, P6, P10.
- Commands and interactive shells: P2, P4, P7, P11.
- Multiple AI providers: P3, P10; distinct fixtures for each API family.
- Projects and linked GitHub repositories: P1, P6, P7.
- Project skills and easy installation: P5, P10.
- MCP servers and related features: P0, P5, P7, P10.
- Browser use: P0, P8, P9.
- Computer use: P0, P9, P10, P11.
- QEMU and VirtualBox: P0, P7, P9.
- Smooth resume/recovery and developer experience: P1 through P11, with dedicated end-to-end gates.

## How to execute this backlog

Start with P0. Record settled library versions and feasibility results before building foundation code. Implement the earliest unblocked package, run its meaningful tests, then update its checkbox and evidence. Keep implementation scope small enough to review without leaving correctness seams untested.

Use `go test ./...` and `go vet ./...` as ordinary gates once the module exists; add `go test -race ./...` for concurrency changes. Use targeted fuzzing for parsers/journals and opt-in integrations for paid services, browsers, SSH, or hypervisors. Do not rerun costly external suites after documentation-only edits.

When a spike changes a decision, update the development plan, contract, and affected backlog items together. Preserve fixed user requirements. Distinguish an unavailable runtime prerequisite from an architectural blocker. Do not replace an unimplemented feature with a mock and mark it done.

The first implementation increment is runnable on Linux. See [initial-build.md](initial-build.md) for exact checks, partial package coverage, and remaining requirements. This increment does not satisfy the full P0 gate or the developer-preview gate; broader terminal, MCP, desktop/VM, and recovery qualification remains open.

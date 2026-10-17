# Bifrost development plan

Implementation status: the first Linux increment is recorded in [initial-build.md](initial-build.md). This document remains the target architecture; packages and APIs in the current prototype implement only the recorded subset.

Status: implementation specification, 15 October 2026.

Build a polished terminal workspace in Go that lets a developer converse with an agent, inspect and edit code, execute commands, review changes, and operate browsers and computers. Projects group multiple independent Git repositories and carry their own skills, MCP servers, model settings, and execution environments. The same coding workflow must work on the local machine and in connected QEMU or VirtualBox guests.

This document defines the intended complete product. The [contracts](contracts.md) make the boundaries concrete; the [backlog](implementation-backlog.md) orders development into testable increments. An early milestone is not a substitute for the complete scope.

## 1. Requirements and decisions

### Fixed requirements

- Implement application code, the agent harness, and remote workers in Go.
- Run the primary UI in a terminal. Provide chat, projects, file tree, file viewing/editing, review, command output, and interactive terminal panes.
- Provide consistent Vim-like movement and keyboard-first workflows, with optional mouse interaction.
- Include native code-reading, searching, editing, patching, command, Git, and diagnostic tools.
- Support multiple AI providers and configurable compatible endpoints without binding the product to one provider's agent SDK.
- Integrate browser automation and computer use.
- Connect to existing QEMU and VirtualBox VMs; manage their lifecycle and execute work within guests.
- Support multiple linked GitHub repositories per project, project skills, and project MCP servers.
- Make skills and MCP configuration convenient to install, inspect, update, and remove.
- Minimize dependencies; additions must justify both direct and transitive cost.
- Selectively port suitable OpenCode behavior and code into Go when this reduces implementation risk.

### Implementation defaults

- Linux is the initial reference platform, on amd64 and arm64 where hardware permits. macOS follows after the core workflow; Windows follows with explicit process and terminal adapters. These are sequencing decisions, not removal of platform scope.
- One executable, `bifrost`, supports interactive UI, headless runs, a local daemon, and remote worker modes.
- Bubble Tea v2 and Lip Gloss provide the terminal foundation. The pane model and product state belong to Bifrost.
- The daemon is a single writer for persistent application state. The UI is a client, even when launched automatically alongside it.
- Use JSON configuration and JSONL journals initially. Add no SQL database, ORM, web framework, vector database, or message broker at bootstrap.
- Chromium browser control uses chromedp. No application-level Node, JavaScript, or TypeScript runtime is required. User project programs, web pages, and explicitly configured third-party MCP servers can have their own runtimes; Bifrost does not install them implicitly.
- API credentials and user-configured endpoints are the initial provider connection mechanism. Provider-specific account login is added only through documented supported flows.
- Each task has one writable environment at a time. Files, Git, diagnostics, commands, and browser targets must refer to that environment consistently.
- Native tools work without MCP. MCP tools participate in the same execution policy, history, and UI.
- Core editing supports arbitrary source languages as text. Rich language intelligence is enabled through configured language servers and commands.

### Product boundaries

The complete first release includes a useful in-pane editor, but does not claim complete Vim compatibility. External editor integration remains available. Real-time desktop video, Firefox/WebKit automation, live VM migration, a hosted collaboration service, an extension marketplace backend, and automatic merging of unrelated repositories are outside the first release. The interfaces must allow later extensions without requiring those systems now.

## 2. Workspace baseline

The current workspace contains no application files, Go module, tests, or existing architecture to preserve. `ripwire --pack-task` found no reusable code. The visible `.git` directory is not a usable Git checkout, so implementation must establish repository metadata deliberately rather than assume a branch or remote exists.

No callable Ruflo MCP tools were exposed during planning. That does not block this specification, and Ruflo is not a product dependency.

The earlier TypeScript proposal is superseded. The prior visual illustration communicates pane placement only; it is not production code or an approved frontend stack.

## 3. Dependency and reuse policy

Approve dependencies by purpose and full module graph, not by the number of import lines. Record selected version, license, imported packages, transitive modules, CGO requirements, and shipped binary impact in `docs/dependencies.md` during P0. Pin releases in `go.mod` and `go.sum`; use a reviewed commit only if a necessary package has no suitable release. Do not use floating versions in release builds.

### Planned external libraries

1. Bubble Tea v2: terminal event loop, rendering, keyboard, mouse, paste, and terminal lifecycle.
2. Lip Gloss: terminal styling and layout primitives. Reuse width and ANSI utilities already present in its dependency graph where suitable.
3. `creack/pty`: Unix PTY creation and resizing. Windows ConPTY is a separate adapter.
4. chromedp and its protocol packages: Chromium automation over CDP.
5. Official MCP Go SDK: MCP client protocol and transport compatibility. Use its schema facilities if they satisfy tool validation; add a separate validator only when needed.
6. One maintained YAML parser: Agent Skills frontmatter. Product configuration itself remains JSON.
7. A vetted Go terminal emulator: evaluate `charmbracelet/x/vt` behind a narrow adapter in P0. A PTY alone is insufficient for rendering an interactive program in a pane.

Platform integration may require `golang.org/x/sys` or an OS IPC package; prefer existing transitive modules. Clipboard, secret storage, and desktop portals require platform adapters. Introduce packages only when the corresponding implementation starts. A pure-Go syntax highlighter is a conditional later addition, measured against existing rendering utilities and product needs.

Use standard-library HTTP, JSON, TLS, logging, contexts, flags, file operations, hashing, process execution, and testing. Use `git` for Git semantics, optionally `rg` for indexed-style file discovery and fast search, system `ssh` for remote transport, and `VBoxManage` for VirtualBox. Browser and hypervisor installations are feature prerequisites, not bundled runtimes.

Do not recreate terminal emulation, OAuth, JSON Schema, or MCP standards merely to reduce module count. Conversely, do not add frameworks for a command registry, event bus, dependency injection, configuration merging, or retry loops that are small product-specific code.

### Reusing upstream work

Port bounded OpenCode units: patch parsing, edit matching, truncation behavior, tool result presentation, and relevant tests. Record upstream commit, original path, license, modifications, and tests in `THIRD_PARTY_NOTICES.md` and `docs/upstream-ports.md`. Port behavior into Bifrost's environment and change-journal contracts rather than copying assumptions about process-global working directories.

The inspected OpenCode license is MIT. Current Crush uses FSL-1.1-MIT; use it as a UX reference and do not assume current code is available under the same terms as the independent Charm libraries. Check the license of each selected source revision and dependency before copying. [OpenCode license](https://github.com/anomalyco/opencode/blob/dev/LICENSE), [Crush license](https://github.com/charmbracelet/crush/blob/main/LICENSE.md).

## 4. Architecture and ownership

```text
Terminal UI / headless client
          |
  versioned local protocol
          |
Go daemon
  projects + tasks + sessions + durable events
  agent loop + provider adapters + context builder
  tool registry + policy + change journal
  skills + MCP manager + GitHub integration
          |
  environment capabilities
          |
local worker / SSH worker / QEMU guest / VirtualBox guest
  files + processes + Git + diagnostics + browser + desktop
```

The daemon owns sessions, credentials, model requests, authorization, and task scheduling. Workers own environment-local paths, file mutations, process handles, and local journals. Workers receive narrowly scoped requests and do not need model API credentials. The TUI owns focus, pane layout, scrolling, selection, and local drafts; it never becomes the authoritative execution state.

The local worker is an in-process implementation initially. The remote implementation exposes the same capabilities through `bifrost worker --stdio`, launched over SSH. QEMU and VirtualBox management lives on the hypervisor host; guest work happens in a separate worker or SSH execution channel. A remote hypervisor host is represented explicitly and does not imply local `VBoxManage` can operate its VMs.

### Package layout

```text
cmd/bifrost/              entry point and subcommand dispatch
internal/app/             composition and service lifecycle
internal/domain/          IDs, entities, errors, common events
internal/config/          JSON loading, validation, resolution
internal/store/           journals, snapshots, artifacts, recovery
internal/protocol/        local client and worker wire contracts
internal/session/         task/session lifecycle and event reducer
internal/agent/           turns, scheduling, context, compaction
internal/provider/        model interface and provider adapters
internal/tool/            definitions, validation, dispatch, policy
internal/edit/            patch engine, revisions, change journal
internal/environment/     capability interfaces and local/SSH adapters
internal/process/         execution, PTY, output, process groups
internal/vm/qemu/         QMP and QEMU host integration
internal/vm/virtualbox/   VBoxManage and optional SDK integration
internal/browser/         chromedp sessions and observations
internal/desktop/         VM and host desktop adapters
internal/git/             repository/worktree/diff operations
internal/github/          accounts, repositories, PRs, checks
internal/skill/           discovery, installation, lockfiles
internal/mcp/             SDK bridge, auth, lifecycle, catalogs
internal/lsp/             server lifecycle, document sync, diagnostics
internal/ui/              root model, panes, editor, keymap, theme
internal/testutil/        deterministic providers and fixture workers
testdata/                 protocol, patch, terminal, and repository fixtures
```

Create packages as their milestone begins; do not generate empty frameworks. Domain code must not import UI packages. Provider adapters must not execute tools. Tool code must not depend on Bubble Tea. VM adapters must not choose models. The application composition root connects implementations.

## 5. Domain model and multiple repositories

A project has a stable ID, display name, repository mappings, instruction sources, skills, MCP configurations, environment bindings, model defaults, and policy references. A repository has its own remote identity, checkout locations, default branch, and GitHub metadata. Repository aliases such as `web`, `api`, and `infra` are scoped to a project.

A task groups a development objective, participating repositories, worktrees, an execution environment, sessions, command processes, and a change set. A session is a conversation branch with ordered turns. A task can have several sessions; one session has at most one active turn. Initially only one turn may mutate a task's worktrees. Independent tasks can run concurrently.

Task creation defaults to isolated worktrees for selected repositories. Existing-checkout mode is explicit and records initial dirty/index state. Never reset or clean a user's checkout to make isolation easier. Local commits and branches remain separate per repository; a task's PR bundle only groups them for display.

Persist the base commit, worktree branch, environment ID, and absolute worker-local root for every task repository. Discover additional repositories through a deliberate add operation. A task cannot quietly start editing a neighboring directory because the model mentioned it.

Code in a VM is authoritative in the VM for that task. Provision guest checkouts or worktrees there, and route the file tree, review, search, Git, and diagnostics through the same worker. Shared mounts are optional explicit bindings. Copying unsaved host files into a guest is not automatic synchronization. Environment switching requires a recorded transfer/export operation or a fresh task; it never silently reuses incompatible absolute paths.

## 6. Agent loop and provider support

Implement an explicit turn state machine: queued, building context, streaming, waiting for tools, waiting for approval/input, completed, cancelled, failed, or interrupted. Persist state transitions. User input during a turn may be queued, used to steer at the next safe boundary, or explicitly interrupt the turn; the UI makes these actions distinct.

The context builder composes system policy, selected project/repository instructions, activated skills, tool definitions, attachments, conversation history, and bounded recent results. Files and tool results retain provenance. Nested repository instructions apply to their directory scope. Project content cannot grant itself execution permissions.

Native tool calls and MCP calls use one dispatcher. Validate arguments before execution, resolve environment/repository identity, evaluate policy, emit an execution record, dispatch, persist results, and continue the model loop. Parallelize only independent operations. Serialize overlapping file mutations and interactive browser/desktop operations. Child tasks use explicit budgets and separate worktrees when writing.

Support these provider families in ordered milestones:

1. OpenAI Responses and Anthropic Messages, plus a deterministic fake provider for tests.
2. OpenAI Chat Completions-compatible endpoints, including configured local servers and gateways; compatibility is tested per endpoint.
3. Gemini native API.
4. Azure, Bedrock, Vertex, and additional services through explicit adapters, optional credential helpers, or a configured compatible gateway. They are extension targets; do not claim native support before their authentication and fixtures exist.

The normalized model stream must retain text, tool-call fragments, usage, completion reasons, errors, and provider-specific opaque continuation data. Never expose hidden reasoning as a generic transcript requirement. Preserve opaque thought signatures or reasoning payloads in the originating adapter when required; do not transfer them blindly to another provider.

Model switching occurs at turn boundaries. Rebuild supported context for the new model; unresolved tool calls finish or cancel first. Models without tool support are chat-only and clearly labeled. Models without image support cannot be selected for screenshot-dependent computer actions without an explicit alternate observer.

Implement bounded retries for eligible transport/rate-limit failures with server retry guidance. Never restart a partially executed tool cycle or repeat remote mutations automatically after an uncertain outcome. Streaming reconnects need either provider-supported continuation or a visible interrupted turn.

Context management uses configurable budgets, bounded tool outputs, artifact references, and explicit compaction records. Summaries retain pending requests, changed files, failures, and decisions, while the original history stays on disk. Usage is exact when reported and labeled estimated otherwise. Costs are shown only with known configured pricing. Step, time, and spend limits stop with a resumable state.

## 7. Native tools and edit correctness

The tool catalog in [contracts](contracts.md) defines stable names and result shapes. The core includes file reading, discovery, search, exact edits, patches, writes, moves/deletes, process management, Git inspection, diagnostics, user questions, skills, and browser/desktop operations. Use a small visible catalog; dynamically expose optional integrations without losing stable tool identity.

### Editing algorithm

1. Resolve the task, repository, worker root, and path. Reject writes outside allowed roots, traversal, forbidden symlink escapes, and unsupported binary edits.
2. Read the current file through the worker and calculate a revision token. Every mutation supplies an expected revision or an explicit new-file precondition.
3. Exact replacement requires a unique match unless all-match replacement is explicit. Default to no fuzzy changes. Optional tolerant whitespace matching must still be unique and recorded in the result.
4. Parse and validate every operation in a patch before writing any file. Reject duplicate destinations and conflicting move/delete/write operations.
5. Acquire task edit coordination and worker-local path locks in sorted order. Recheck revisions immediately before writes. External editors are not covered by these locks; detect observed divergence and never claim universal filesystem compare-and-swap semantics.
6. Write a prepared change-journal record and save before-images. Write replacement files to same-directory temporary files and replace them, preserving documented permissions and line endings. Detect unsupported ownership/ACL situations instead of silently claiming preservation.
7. Record committed file operations and the final transaction status. Publish change events with before/after revisions, diffs, and attribution.
8. Run configured formatting as a separate attributable change, then synchronize diagnostics. Formatting errors never erase the original edit result.

Multi-file changes are prevalidated and recoverable, not magically atomic across filesystems. Journal interrupted operations; on recovery identify applied, unapplied, and divergent files. Compensation or undo is allowed only where expected revisions still match. Conflicts require a visible resolution. Never report a partial patch as fully successful.

Undo restores an agent operation only if subsequent content permits it. It must not discard manual edits, Git index changes, or another agent's work. A task checkpoint stores metadata and file revisions; it is not a Git reset. Shell commands and external MCP tools can edit files outside this engine: refresh resulting workspace diffs, but label their change attribution as observed rather than transaction-guaranteed.

### Search and files

Support bounded line ranges, byte limits, continuation cursors, match locations, repository labels, ignore rules, binary detection, and explicit truncation. Use `rg` when available; provide a slower Go fallback with documented pattern support. Never silently change regex semantics between engines. Handle spaces, Unicode, leading dashes, and unusual Git filenames using structured arguments and NUL-delimited Git output.

## 8. Commands and interactive terminals

Separate structured executable/argument requests from explicit shell scripts. Noninteractive commands preserve stdout and stderr independently. Interactive programs use a PTY with merged terminal output and a tracked terminal size.

A process has an environment, task, working directory, command provenance, process group/job identity, state, exit result, output artifact, and terminal screen state when applicable. Cancellation first requests graceful termination, then escalates after a configurable deadline. On Unix signal the process group; on Windows use job objects. `context` cancellation alone is insufficient for all descendants.

Bound live output buffers; spool complete output within configured disk limits. Slow UI readers must not block the child process indefinitely. Coalesce display notifications, preserve raw terminal bytes in a bounded artifact, and distinguish those bytes from model-readable text. Sanitize control sequences in chat and logs; terminal rendering passes through the emulator, not raw escape codes printed over the parent UI.

Background servers remain alive while the owning daemon or worker lives. Detaching the TUI does not end them. Daemon/worker crashes can interrupt processes; recovery reconciles only verified ownership and never kills or adopts a PID based on its number alone. The product must not promise process survival across a reboot.

Only one client holds interactive input control for a terminal at a time; others can observe. Resize events follow that controller. Support alternate screens, cursor addressing, carriage-return progress, colors, bracketed paste, mouse modes, Unicode widths, scrollback, and input forwarding. The terminal focus escape defaults to `Ctrl-]` so `Esc` and `Ctrl-C` can reach programs normally.

## 9. Terminal UX and editor

### Layout

Default wide layout: project/file tree on the left, chat in the center, file/review inspector on the right, and a collapsible command/terminal area below. The status line always shows project, task, repository/branch, execution target, model, mode, and active work state.

At about 120 columns, allow the three main panes. At about 80 columns, use a tree plus one main pane or two readable panes. Below that, show a single pane with a switcher. Exact thresholds follow minimum pane widths and are tested rather than hard-coded as product truths. Persist layout per project and width class.

Use terminal colors with a restrained theme, clear focused-pane styling, good monochrome/256-color fallbacks, optional icons, and no required patched font. Mouse support complements the keyboard. Rendering is event-driven; streaming text is batched to avoid repainting every token. Large files, trees, diffs, and transcripts render only visible ranges.

### Modes and navigation

- Normal: `h/j/k/l`, `gg/G`, search, selection, and pane commands.
- Insert: composer or editor input. `Esc` returns to Normal.
- Terminal: forward input to the child; `Ctrl-]` returns to Normal.
- `Ctrl-w h/j/k/l`: move pane focus in Normal mode.
- `Ctrl-w v/s`: split compatible panes; `Ctrl-w q`: close a view without killing its underlying process.
- `Space p/f/g/t/m/s`: projects, files, Git/review, terminals, MCP, and skills.
- `]c/[c`: next/previous diff hunk. `Enter`: open the selected item.
- `/`: search within the focused view; project search has a separate command.
- `:`: command palette. Every action also has a discoverable palette entry.

Keymaps are configurable by mode/context. Detect conflicting bindings. Make copy/paste and alternate bindings available for terminals that cannot distinguish particular control keys. One input event belongs to one focus owner.

### Chat and agent activity

Support multiline composition, prompt history, pasted text/images where available, file/selection/hunk attachments, queued messages, cancel/steer actions, and model selection. Compact tool rows expand into exact command arguments, output, patches, and errors. Approvals show the relevant target and requested action. No automatic focus theft or forced scroll-to-bottom when the user is inspecting older content.

Attach source selections with path, repository, revision, and line range. Detect stale attachments before editing. Show waiting, running, disconnected, cancelled, and completed states distinctly. Persist composer drafts outside model history until sent.

### Files and manual editing

Ship a read-only file viewer early, then a basic in-pane editor with cursor movement, selection, insert/delete, undo/redo, search, save, dirty indication, and external-change detection. Saving goes through the same revision-aware editing service as agent changes. An unsaved editor buffer prevents blind agent replacement of that file; offer save, discard, or conflict review. Keep UI-local undo separate from persistent agent change undo.

Provide `$VISUAL`/`$EDITOR` integration with argument-safe configuration and restoration of pane state. For remote files, run a configured editor in the remote terminal or edit through the Bifrost buffer; do not open a nonexistent host path. Syntax highlighting must not execute project code. Full Vim motions, macros, and extension compatibility are not a first-release promise.

### Review

Offer unified and side-by-side diffs with narrow-screen fallback. Distinguish staged, unstaged, untracked, binary, and renamed files. Provide task-base and current-worktree comparisons. Stage/unstage selected hunks against fresh index/worktree revisions. Show manual changes and agent changes without claiming shell changes have perfect attribution. Review comments can be attached locally to chat; publishing comments or PR changes is an explicit separate action.

## 10. MCP integration

MCP is required infrastructure for the first developer preview, not a late plugin. Use the official Go SDK behind a Bifrost adapter. Support project-scoped and user-scoped server definitions with visible provenance, execution location, transport, authentication, enabled tools, and policy.

The currently inspected SDK documents support for the 2026-07-28 protocol and older versions. The modern protocol uses different discovery and interaction semantics from legacy initialization. Keep version handling in the SDK adapter, pin a tested release, and validate both eras with fixtures. Roots, sampling, and logging are compatibility features with deprecation status; they are not mandatory assumptions for every connection. [Go SDK compatibility](https://github.com/modelcontextprotocol/go-sdk), [versioning](https://modelcontextprotocol.io/specification/2026-07-28/basic/versioning).

### Required capabilities

- Local or worker-launched stdio servers, with stdout reserved for protocol traffic and stderr shown as server logs.
- Remote Streamable HTTP with TLS, supported authorization, request/session behavior appropriate to the negotiated protocol, reconnect, and bounded retries.
- Legacy HTTP+SSE as an explicit compatibility transport where supported by the pinned SDK; display its status and test it separately.
- Discovery and pagination for tools, resources, resource templates, and prompts. Cache by server identity, credential identity, protocol version, and catalog revision.
- Tool calls with validated arguments, structured/text/image results, resource links, progress, cancellation, deadlines, and artifact storage.
- Resource reading and subscriptions where supported; expand templates only after collecting required parameters.
- User-selected prompt invocation with argument forms. Server prompts are content, not higher-priority system instructions.
- Catalog invalidation through the applicable notification/subscription mechanism.
- Form and URL elicitation where negotiated, rendered as user input with accept/decline/cancel. Multi-round input-required results must resume the correct pending request.
- Optional negotiated task-extension support for long-running server work, with progress, result retrieval, and cancellation. Do not equate the remote task extension with an ordinary synchronous call.
- Compatibility roots only for task-accessible local file roots; roots are not an enforcement sandbox. Legacy sampling is disabled unless enabled for that server with separate model, context, and budget controls.

A server has states disabled, starting, connecting, ready, degraded, authentication-required, backoff, or stopped. A malformed message or crashed server affects that server's calls, not the entire harness. Restart with bounded exponential backoff; never replay an uncertain mutation merely because a connection recovered. Tool annotations are hints, not proof of read-only behavior.

### Installation and authentication

`mcp add` supports a stdio executable/arguments or remote URL, with project scope by default in an open project. Show the exact launch command and required runtime. Import common existing MCP JSON configurations through a preview mapping; preserve secret references and do not execute newly imported entries before trust is established.

Store tokens through the credential abstraction. Support bearer/header references and SDK-supported OAuth, including discovery, PKCE where required, refresh, and user-driven login. Validate resource/audience and redirect configuration; do not send a token to an unrelated URL after redirects. Account separation must prevent one project's credentials or cached tools being reused under another account. SDK gaps are explicit failing tests or unsupported capabilities, not silent authentication fallbacks. [Authorization](https://modelcontextprotocol.io/specification/2026-07-28/basic/authorization).

The MCP pane lists servers, scope, location, negotiated capabilities, health, tools, resources, prompts, and recent errors. Actions include add, edit, connect, disable, restart, authenticate, inspect schema, test a selected tool under policy, and view logs. Persist a small enabled tool set or use discover-and-activate for large catalogs, with stable namespaced model tool names.

Bifrost can later expose selected native tools through `bifrost mcp serve --stdio`. That server mode uses the same policy and project scopes and does not grant arbitrary host access. It is scheduled separately from required MCP client support.

## 11. Skills, instructions, hooks, and extensions

Implement the Agent Skills directory format with `SKILL.md` metadata and optional referenced scripts/resources. Installation accepts a local path or a Git repository plus subdirectory and ref. Resolve tags/branches to commits, copy into a managed versioned cache, record source/revision/content hash, and enable the skill for the selected project. Install never implicitly executes scripts. [Agent Skills specification](https://agentskills.io/specification).

Load skill metadata for discovery, full text on activation, and supporting files only when needed. Support explicit `/skill-name` activation and model selection through a skill tool. Record the exact activated revision in each turn. Keep project-specific skill bindings independent from the global package cache.

Updates show source and content diffs; approval policy applies to newly added executables/MCP configurations. Updating one project does not silently upgrade every project. Removing a binding does not delete user files or other projects' installations. Detect duplicate skill names and show the winning scope; allow explicit selection by qualified name.

Instruction resolution distinguishes user preferences, project instructions, repository `AGENTS.md`, and nested directory guidance. Deterministic precedence is documented in effective configuration. Instructions cannot disable host enforcement. Read-only project discovery must never auto-launch hooks or MCP processes from an untrusted checkout.

Support declarative hooks for before/after tools, turn completion, and task setup once the native dispatcher is stable. Hooks are explicit executable/argument definitions with bounded input/output, deadlines, and the same execution policy. Prevent recursive hook loops. No embedded JavaScript extension engine, Go runtime plugin loading, or scripting interpreter is required. Executable JSON/stdio extensions and MCP are sufficient initially.

## 12. Browser and computer use

Browser profiles belong to a project/environment, with separate isolated sessions by default and explicitly reusable profiles where requested. Manage tabs, navigation, locators, DOM/accessibility observations, input, screenshots, downloads, console output, and selected network diagnostics through chromedp. Stable observation IDs prevent actions from using stale element references.

Prefer semantic browser actions. Screenshot-based interaction is available when semantic information is insufficient, but requires a vision-capable observation path. Restrict browser network/filesystem access according to environment policy. Store screenshots as artifacts and apply upload/context limits. Browser cookies are not model prompt content.

An existing Chromium connection is opt-in with a clearly identified debugging endpoint. By default Bifrost launches a dedicated profile. Debugging endpoints remain local or SSH-tunneled; never expose unauthenticated CDP on a public interface. chromedp provides direct Go browser control. [chromedp](https://github.com/chromedp/chromedp).

Computer adapters provide display enumeration, capture, pointer input, clicks, scrolling, key sequences, and text entry. Every capture includes display ID, pixel dimensions, coordinate space, and timestamp. Actions reference an observation and transform coordinates explicitly. Serialize interaction per desktop and support immediate user cancellation.

For VM desktops use hypervisor input/capture facilities first; for richer capture or accessibility use an in-guest helper. For host desktops implement Linux X11 and Wayland separately, with an early portal/compositor feasibility test. Wayland permissions and support vary; unsupported sessions must be detected and displayed, never silently approximated with X11 commands. macOS and Windows use their platform APIs and permissions in subsequent platform milestones.

The terminal shows semantic observations, action logs, and image previews only where graphics protocols work. Baseline computer use must work even when the terminal cannot display pixels, because screenshots can still be sent to the model. An external viewer is optional. Continuous video streaming is out of first-release scope.

## 13. VM and remote execution integration

### QEMU

Attach to configured instances through QMP; support query state, start of Bifrost-managed definitions, shutdown/pause/resume, supported snapshots, capture, and input. QMP does not provide general guest shell execution. Detect SSH or a provisioned Go worker separately. Snapshot support depends on the image/device configuration and must be advertised as a capability, not assumed. [QMP reference](https://www.qemu.org/docs/master/interop/qemu-qmp-ref.html).

Initially support attach-first operation against existing VMs. Managed launch definitions follow with explicit disk, firmware, CPU/memory, networking, and display settings. Respect configured architecture/accelerator compatibility and avoid assumptions that host and guest architectures match. Keep disks user-owned; deleting a connection never deletes a disk.

### VirtualBox

Use argument-safe `VBoxManage` execution and machine-readable output where available for discovery, state, lifecycle, snapshots, guest properties, and screenshots. Validate output/version handling with fixtures. For pointer/keyboard capabilities that require the SDK or guest helper, expose that capability separately and implement it before declaring full computer-use support. Do not assume screenshot support alone enables desktop control.

Use SSH plus the worker for complete file/process parity. Guest control is a bounded fallback where Guest Additions and credentials are available; capabilities missing from it, especially interactive process behavior, remain visible. VRDE/extension-pack features are optional and not required by the base architecture. [VirtualBox manual](https://download.virtualbox.org/virtualbox/UserManual.pdf).

### Provisioning and reconnect

`doctor` reports hypervisor availability, guest reachability, worker version, browser/LSP prerequisites, and unsupported capabilities. Provisioning is explicit: install/copy a compatible Go worker, verify its checksum, create roots, and connect. Existing SSH configuration/agents are reused without copying private keys into guests.

Worker RPC uses session and operation IDs. On disconnect, mark calls uncertain where completion is unknown. Reconnect obtains worker state and operation results; never blindly resubmit a file edit, command, Git push, or VM action. Read-only discovery can retry. Pending GUI actions are invalidated until a fresh capture is taken.

## 14. GitHub and project workflows

Link each project repository to an explicit GitHub host/owner/name. Support github.com first and configurable GitHub Enterprise hosts with isolated credentials. Read repository metadata, branches, PRs, reviews, and check status. Open PRs and publish review comments only on an explicit user action or a previously configured applicable policy.

Start with optional `gh` credential integration or configured tokens and standard-library HTTP. A later documented OAuth application flow may improve onboarding; do not invent an OAuth client ID or assume a user's website subscription is an API credential.

Create a task-level PR bundle listing independent repository PRs and dependencies. A cross-repository change cannot be merged atomically; show readiness and ordering separately. Fetch/push retains the user's Git credential behavior. Do not change remotes, force-push, or auto-merge as an incidental part of opening a task.

Review data is refreshed lazily or through bounded conditional requests. Background checks stop when the project is closed unless explicitly configured. Rate limits and offline status are visible. Do not introduce a hosted webhook service for a local CLI.

## 15. Persistence, recovery, and credentials

Use standard user config/state/cache directories, with platform equivalents. Global defaults and locally trusted overrides stay outside repositories. Portable project manifests and skill locks can be checked into a configuration repository; credentials and machine-specific absolute paths stay in local binding files.

Persist append-only session events with schema versions and monotonic sequence numbers. Use one writer per store, explicit flush boundaries for completed tool operations, and periodic snapshots to bound replay time. Large command output, screenshots, and before-images are content-addressed artifacts with references and quotas. Recover a truncated final record; treat internal corruption as an error with recovery/export options rather than skipping arbitrary records.

File workers journal mutations locally before reporting them. The daemon records their stable operation IDs and outcomes. Reconnection reconciles journals so a lost response is not mistaken for a failed edit. Cross-machine durability is not provided by a single host JSONL write.

Credential sources: environment references and explicit command helpers first, OS credential storage adapters for interactive login. Persist only references in project manifests. Logs and exported sessions redact known secrets. Sanitization cannot guarantee removal of every secret a user command prints, so exports show included artifacts and offer exclusion controls. No automatic telemetry or transcript upload.

Migrations are versioned, backed up, and tested with fixtures. Unrecognized newer schemas are refused with a clear message. Do not migrate a store from two concurrent processes. A later SQLite migration must preserve stable IDs and event export; it is not a prerequisite for the first release.

## 16. Execution policy

Policy is an explicit service enforced before dispatch on the daemon and before local operations on workers. UI approval is a presentation layer, not enforcement. Requests carry project, task, repository, environment, tool, canonical arguments, and policy revision.

Offer configurable profiles such as read-only, workspace development, and unrestricted environment access. The normal development profile allows trusted workspace edits and configured build/test commands, while external mutations and new executable integrations can request scoped approval. Users can persist exact applicable rules and inspect effective policy. Do not interrupt repeatedly for an already authorized action.

Filesystem restrictions apply to native tools. Arbitrary shell commands require an OS sandbox or isolated VM for strong filesystem/network isolation; a Go path check is not a sandbox. Display the actual execution boundary. Add a Linux sandbox backend only as a separately tested implementation; do not claim it exists because policy filters a shell string.

Approvals bind to the canonical operation, target, arguments, and relevant revisions. A changed request is evaluated again. Server annotations, skill text, repository instructions, and retrieved web content cannot authorize themselves. Destructive VM disk changes, secret access, GitHub publication, and arbitrary external network actions remain distinguishable policy operations.

## 17. Quality targets and test strategy

Initial targets are engineering budgets, not measured claims. Record reference hardware and workloads in P0, then revise budgets only with evidence.

- UI key-to-render latency: p95 below 50 ms during concurrent model and command streaming on the reference workload.
- Cancellation acknowledgment: below 250 ms locally; process termination follows the configured graceful timeout.
- Warm attach to a saved session: below one second for a snapshot plus bounded replay on the reference dataset.
- Large-workspace test: at least 100,000 paths, a 10 MB viewed file, a large diff, and a 100 MB command-output artifact without rendering all content at once.
- Steady state must show no unbounded goroutine, memory, file-descriptor, or artifact growth.
- All optional dependencies have a useful unavailable/disabled state; launching chat does not require Chromium or a VM.

Testing layers:

1. Unit and fuzz tests for patch parsing, revision checks, paths, journal recovery, protocol framing, config merging, and key routing.
2. Fake-provider tests for fragmented streams, malformed arguments, tool failures, cancellation, compaction, retries, and provider switching.
3. Worker contract tests running identically against local and SSH implementations.
4. Temporary Git repositories for worktrees, dirty/index preservation, renames, unusual filenames, hunk staging, and multi-repository task setup failure.
5. Deterministic MCP fixture servers for modern and legacy discovery, auth, pagination, cancellation, elicitation, tool changes, and disconnect during mutations.
6. TUI golden/snapshot tests plus real PTY interaction tests across widths and color modes; Unicode and terminal escape cases are mandatory.
7. Browser fixture pages served by Go, including forms, iframes, downloads, stale elements, console errors, and interrupted navigation.
8. Opt-in QEMU and VirtualBox integration suites against disposable test guests, with lifecycle, reconnect, command, edit, capture, and input checks.
9. Live provider smoke tests behind explicit credentials and bounded budgets; ordinary CI never needs paid APIs.
10. Race detector, `go vet`, vulnerability inspection, clean build, dependency/license manifest checks, and platform tests.

A transcript claiming tests passed is not an acceptance test. Acceptance asserts actual repository contents, process exit status, worker state, MCP results, and UI behavior. Mock demos remain labeled as mocks.

## 18. Delivery phases and completion criteria

The [backlog](implementation-backlog.md) specifies each work package and its gate. Order:

1. P0: validate dependency/toolchain choices, terminal rendering, protocols, and platform risks.
2. P1: domain, configuration, persistence, daemon/client, and project registry.
3. P2: local files, edit journal, process execution, and native tools.
4. P3: agent loop, first providers, sessions, context, and headless coding workflow.
5. P4: pane UI, file tree, chat, review, PTY, and external editor.
6. P5: skills and complete baseline MCP client integration. This is required for the developer preview.
7. P6: multiple repositories, worktree lifecycle, GitHub, and LSP feedback.
8. P7: worker protocol and SSH parity.
9. P8: Chromium automation and artifact presentation.
10. P9: QEMU, VirtualBox, and VM desktop control.
11. P10: in-pane editing, host desktop support, provider breadth, hooks, and optional MCP server mode.
12. P11: macOS/Windows completion, performance, recovery, packaging, and full release qualification.

P4 yields a local vertical slice, not a feature-complete product. The developer preview requires P5. Multi-repository workflows become usable at P6; the complete requested VM workflow requires P9; first full release requires all P0–P11 gates, with actual platform capability limitations documented.

Do not give a calendar estimate before P0 measures the terminal, desktop, and MCP risks. Track work-package completion and evidence, then estimate remaining work from completed comparable increments.

## 19. Full release acceptance scenario

Create a project with `web`, `api`, and `infra` repositories; connect at least two provider families; install a pinned project skill; connect one stdio and one authenticated HTTP MCP server. Start an isolated task, attach code selections, edit two repositories, run tests, inspect diagnostics, stage selected hunks, and prepare linked PRs without losing unrelated user changes.

Detach and reattach while a background server runs. Interrupt a tool call and continue the conversation. Simulate a provider failure and a disconnected MCP server without corrupting the task. Restart after an interrupted edit and recover accurately.

Repeat the coding workflow in a QEMU guest and a VirtualBox guest. Inspect the resulting app with Chromium and perform a desktop action using a captured observation. Verify environment paths and diffs originate in the guest. Perform a manual edit in the TUI and resolve a conflicting agent edit without data loss.

Run the scenario on supported host platforms, with a plain terminal fallback, no Node/JS/TS runtime required by Bifrost itself, and no optional service needed to launch the basic application. Publish the exact tested provider, MCP, browser, hypervisor, terminal, and OS matrix.

## 20. Primary references

- [OpenCode tools](https://opencode.ai/docs/tools/) and [LSP behavior](https://opencode.ai/docs/lsp/): behavior references for native coding tools.
- [OpenCode server](https://opencode.ai/docs/server/): client/harness separation reference.
- [Codex app-server](https://learn.chatgpt.com/docs/app-server): structured agent event and approval reference, not a runtime dependency.
- [Pi](https://github.com/earendil-works/pi): modular agent/provider architecture reference.
- [Bubble Tea](https://github.com/charmbracelet/bubbletea), [Lip Gloss](https://github.com/charmbracelet/lipgloss), [PTY](https://github.com/creack/pty), and [VT candidate](https://github.com/charmbracelet/x/tree/main/vt): Go terminal building blocks.
- [MCP specification](https://modelcontextprotocol.io/specification/2026-07-28), [Go SDK](https://github.com/modelcontextprotocol/go-sdk), [elicitation](https://modelcontextprotocol.io/specification/2026-07-28/client/elicitation), and [task extension](https://modelcontextprotocol.io/extensions/tasks/overview): protocol implementation references.
- [Agent Skills](https://agentskills.io/specification), [chromedp](https://github.com/chromedp/chromedp), [QEMU](https://www.qemu.org/docs/master/interop/qemu-qmp-ref.html), and [VirtualBox](https://download.virtualbox.org/virtualbox/UserManual.pdf): integration contracts.

These references inform implementation; proposed Bifrost behavior is defined by this plan. Pin code ports and dependency versions during P0, and use protocol conformance results rather than assuming upstream documentation guarantees every behavior.

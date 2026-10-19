# Bifrost development guide

This file applies to the entire repository. Read it before changing code.
It is a development handoff, not a claim that the full roadmap is complete.
Last reconciled with source and the project conversation: 2026-10-17.

## Product and non-negotiable direction

Bifrost is a Go CLI/TUI AI development harness: chat, code editing, review,
commands, multiple repositories, providers/accounts, skills, MCP, monitored
sub-agents, browser/computer use, and local/SSH/QEMU/VirtualBox environments.

- Keep the application native Go. Do not introduce a JavaScript/TypeScript,
  Node, Electron, or web frontend runtime.
- Keep dependencies low. Reuse existing code and the standard library first;
  justify additions with a concrete requirement and dependency cost.
- The UI should feel like a polished developer workspace: charcoal surfaces,
  violet hierarchy, cyan focus, readable Markdown, useful icons/emoji, and a
  colored provider/account/model/environment status bar. Preserve light/plain
  themes and ASCII/Unicode fallbacks.
- Support both keyboard and mouse workflows, configurable layouts, and
  convenient Vim-style movement without intercepting normal text input.
- One project can link multiple GitHub repositories and set its own skills,
  provider, MCP, and environment defaults. Do not reduce a project to one repo.
- The marketplace must discover skills through public/open catalog APIs, with
  searchable sources, not just a bundled list or manually supplied JSON file.
  See [docs/marketplace-sources.md](docs/marketplace-sources.md) for the pending
  integration brief and verified API references.
- Conversations must survive explicit account/provider switching. Mixed-provider
  delegation is supported; do not assume one account owns an entire conversation.
- Optional decision models route bounded choices to reduce spending. Do not
  replace the generation model or send full histories merely to make a choice.
- Reusing upstream implementations is allowed when licenses permit it; preserve
  notices and review fit. Do not import a large framework for a small feature.

## First steps and source of truth

1. Inspect `git status --short`, relevant source/tests, and any nested instructions.
   Preserve unrelated edits. Do not assume the checkout has a commit/clean baseline.
2. Read [README.md](README.md) for runnable workflows and
   [docs/current-build.md](docs/current-build.md) for evidence and limitations.
3. For UI work, read [docs/navigation.md](docs/navigation.md) and
   [docs/tui-design.md](docs/tui-design.md). Navigation proposals are explicitly
   marked pending; do not describe them as existing behavior.
4. Consult [docs/development-plan.md](docs/development-plan.md),
   [docs/contracts.md](docs/contracts.md), and
   [docs/implementation-backlog.md](docs/implementation-backlog.md) for target
   scope and invariants. These contain historical/aspirational sections and
   broader phase gates. An unchecked phase does not mean its code is absent.
5. Prefer current source and reproducible tests over outdated status prose.
   Reconcile contradictions when touching the affected area. Do not restart P0
   or reimplement an existing subsystem simply because the original plan says so.

## Stack and code map

An optional native CUI GUI lives in the separate `desktop/` Go module and builds
`bin/bifrost-desktop` with `make desktop`. Keep CUI/GTK dependencies out of the
parent module and preserve `CGO_ENABLED=0` TUI builds. Its service adapter is
`internal/cli/desktop_workspace.go`; native callbacks use a bounded worker queue
and run on the locked main OS thread. See `docs/desktop.md` for build prerequisites,
workflow coverage and native smoke tests. Desktop reuses settings/account/project/
marketplace/task callbacks, the command registry, editor engine/draft schema,
mention expansion, viewer event contracts, layout persistence and PTY/emulator.
All implemented TUI command families have native handlers. Keep the command
coverage and Xvfb parity regressions synchronized when extending either frontend.
Desktop workspace navigation exposes Project, Tasks, Skills, MCP servers,
Accounts and Settings directly. Project overview and common/advanced configuration
share one staged model; refresh must retain unfinished form edits. Keep catalog
preview/install request identity and install scope intact across navigation.
Native render regressions include 800×720 and 2560×1440 workspace pages.
Chat tabs execute concurrently through `DesktopWorkspace.RunConversation`, with
independent session history, cancellation, drafts, reading position and unread
state. Keep events and command-output IDs attached to their originating tab.
Account/model operations remain exclusive across running tabs. The task monitor
tracks all active turn runtimes; scheduler recovery/registration is serialized.
All tabs share runtime account identity; keep profile labels synchronized. Preserve pending settings across field changes and validate all
edited values on Save. Recovery writes become durable only on success, failed
reloads retain buffers, and asynchronous reloads must not steal file selection.
The README screenshot tour uses native fixtures. Its logo is in
`docs/images/bifrost-logo.png`; `patches/README.md` pins the published CUI base and
bundled compatibility patch needed to reproduce this desktop snapshot.
Linux fixture coverage is not qualification of real OAuth/vendor/SSH/VM sessions
or macOS/Windows. Shared roadmap gaps remain gaps in both frontends.

Go 1.26+; module `bifrost`. `go.mod` is authoritative. There are currently ten
direct modules: Bubble Tea v2, Lip Gloss v2, Ultraviolet, ANSI/VT utilities,
chromedp/cdproto, creack/pty, the official Go MCP SDK, and Goldmark.
External browsers, vendor CLIs, SSH, Git, language servers, and hypervisors are
optional capability-specific prerequisites, not mandatory application runtimes.

| Location | Responsibility |
| --- | --- |
| `cmd/bifrost`, `internal/cli` | Entry point, command parsing, runtime assembly, UI callbacks and integration |
| `internal/ui` | Bubble Tea state/update/rendering, panes, chat, editor, settings, marketplace, accounts and tasks |
| `internal/config`, `internal/setup` | Strict settings schema, validation, revision-safe persistence and onboarding |
| `internal/domain`, `internal/store` | Project/repository identity and persisted project data |
| `internal/agent`, `internal/provider` | Native model/tool loop, events, API adapters, provider history |
| `internal/accounts`, `internal/agentbackend` | Account identities/credentials and official Cursor/Claude client adapters |
| `internal/decision` | Typed TypeSafe/OpenRouter decision requests and response validation |
| `internal/session` | Conversation persistence, portable history, import/export and explicit compaction |
| `internal/workspace`, `internal/process` | Rooted files, revision-checked edits/patches/undo, bounded command execution |
| `internal/environment`, `internal/worker`, `internal/remotetools` | Local/SSH backends and versioned guest-worker RPC |
| `internal/tasks`, `internal/tasktools`, `internal/daemon` | Child worktrees/budgets/monitoring, lifecycle, durable events and detached jobs |
| `internal/skill`, `internal/marketplace` | Discovery/activation, safe installation, catalog metadata and pinned sources |
| `internal/mcpclient`, `internal/mcptools`, `internal/mcpserver` | MCP connections, native tool bridge and rooted stdio server |
| `internal/browser`, `internal/browsertools` | Chromium observations/actions/screenshots and model tool bridge |
| `internal/desktop`, `internal/desktoptools`, `internal/vm` | Desktop capabilities/actions, QMP, VBoxManage and VM lifecycle |
| `internal/gitops`, `internal/gitview`, `internal/github` | Worktrees, Git mutations/review and GitHub integrations |
| `internal/lsp`, `internal/hooks` | Explicit language-server operations and bounded argv hooks |
| `internal/atomicfile`, `internal/filelock` | Shared persistence primitives; reuse rather than duplicate |

## Implemented feature baseline

Verify affected behavior before editing; this is an orientation map, not a
substitute for tests or a declaration of production readiness.

- **Workspace:** real PTY, files, inline Vim-like editor with drafts/search/
  undo/redo and revision-safe saves, external editor, Git review. Studio,
  Conversation, Review, and Focus presets; persisted proportions/terminal
  visibility; mouse-drag resizing with cancel and save-failure rollback.
- **Chat:** streaming, Markdown/code/diff colors, grapheme-aware wrapping,
  collapsible messages/tool activity/provider-supplied thinking, mouse interaction.
  Composer grows up to eight body rows by default. Enter sends; Shift+Enter
  inserts a newline where the terminal reports it; Alt+Enter/Ctrl+J are fallbacks.
- **Settings:** onboarding, strict private JSON, `:settings`/`:config` searchable
  editor, scalar/structured values, staged edits and Ctrl+S save. UI settings
  apply after save; provider/runtime settings currently require restart.
- **Commands:** `:`/Ctrl+P palette, name/argument completion, retained invalid
  input with usage help, `:help COMMAND` and `COMMAND --help`.
- **Skills:** project/global installation and project-over-global resolution.
  Marketplace is a responsive one-to-four-column card grid with live local
  search, clickable detail modal and project/global install actions.
  `:skill browse` and `:skill install ID [--scope project|global]` exist; project
  is default. IDs complete from the loaded catalog. Installation is not activation.
  Built-in catalog contains three pinned OpenAI skills; custom HTTPS catalogs work.
- **Providers/accounts:** native Responses, Anthropic, Gemini, compatible Chat
  Completions and OpenRouter; named ChatGPT OAuth and isolated Cursor/Claude CLI
  accounts; portable conversation switching and mixed-backend children.
- **Decisions:** bounded TypeSafe Jev/OpenRouter choices, allowlisted profiles,
  capability filtering, confidence checks, fallback and decision usage reporting.
- **Sub-agents:** spawn/status/wait/cancel, isolated writable worktrees, read-only
  children, bounded concurrency/depth/steps/output, durable events and live task
  monitor. Parent and children appear in the same graph.
- **Integrations:** multi-repo project/GitHub linking, Git operations and draft
  PR creation; MCP stdio/HTTP tools/resources/templates/prompts; explicit LSP
  calls and hooks; authenticated local daemon and SSH guest worker.
- **Browser/VM/computer use:** Chromium semantic actions and screenshots,
  capability-reported desktop actions, QMP/VBox lifecycle/snapshots, managed
  QEMU launch and optional guest SSH workers. Capabilities and test coverage vary.

## Explanation and skill-selection increment — 17 October 2026

- `:explain TOPIC` prepares an editable prompt; `:walkthrough` navigates code,
  concept and diff steps in either frontend. TUI `p/n/x` and mouse controls expose
  navigation/full explanation; native controls use a scrollable explanation panel.
  Keep `viewer_flow` validation and both renderers synchronized. Presentations are
  in-memory; PR approval/staging/publishing is not part of viewing a walkthrough.
- Four original skills are embedded in `internal/skill/bundled`. Discovery exposes
  descriptions; native agents select bodies via `skills_search`/`skills_activate`
  and resources via `read_skill_resource`. Installing is not activation. Preserve
  project/global-over-builtin precedence and explicit `--skill` resource access.
  Vendor backends have their own tools and cannot call Bifrost's native skill/viewer
  tools. Do not advertise interactive support there.
- The TUI shares the bridge/portal identity through `internal/ui/brand.go`, with
  small-window, ASCII, light and plain variants. Keep real cell-width geometry.

## Known gaps: do not advertise these as finished

- The improved navigation specification in `docs/navigation.md` is the next
  proposed UI increment. A left click focuses the pane under the pointer
  without entering terminal input, and the focused frame uses a heavier cyan
  border. The terminal is a viewer tab, opened with `t`, not a bottom pane.
  Geometric pane focus, pane Tab cycling,
  previous-pane history, pane zoom, prefix help, file finder, and general
  configurable keymaps are not yet implemented as described there.
- Public skill API adapters and federated/paginated remote search are pending.
  The current custom HTTPS JSON catalog is not equivalent to supporting arbitrary
  catalog APIs. Anonymous access and authentication requirements vary by source.
- MCP OAuth login UI and TUI elicitation forms; editor LSP diagnostics overlays;
  hunk-level staging; background PTY reattachment; approval coverage beyond native
  exec and the Cursor Native shell bridge;
  terminal pixel-image previews and automatic font configuration remain pending.
- SSH child-agent isolation and daemon-backed SSH chat are unsupported. Worker
  binaries/guest tools and operating systems are not automatically provisioned.
- Cursor/Claude turns use vendor clients and their tools, not Bifrost's native
  browser/desktop/MCP/SSH tool loop. Cursor multi-account isolation is Linux-only;
  macOS shared-keychain limitations are explicit. Live vendor compatibility is
  unverified. Do not promise subscription access merely because an adapter exists.
  Native Cursor interoperability research is in
  [docs/research/cursor-client-analysis.md](docs/research/cursor-client-analysis.md),
  with versioned static evidence. A separate `cursor-native` backend now exists;
  its local Connect-protocol fixtures do not establish live service compatibility.
- Linux amd64 has runtime evidence. Cross-builds do not prove Windows/macOS
  runtime support; macOS desktop automation is not implemented.
- Native API/GitHub coverage is primarily fixtures. Desktop tests use injected
  commands; real graphical guests were not controlled. The QEMU probe is diskless.
- Context compaction can be requested explicitly or triggered before a turn at
  80% of a known window / 400 KiB of text. It retains the source session and only
  adopts a smaller summary fork. Resume uses the last completed turn. There are no
  automatic provider retries. Worktrees start from committed refs, not dirty
  parent state; results are not automatically merged.
- Artifact retention/GC, long-running resource quotas, and wider recovery/
  platform qualification remain open. The structural quality gate is non-clean;
  the latest recorded broad-baseline comparison had 66 major findings plus
  new-symbol findings. Re-run and report actual results, not this historic count.

## Engineering invariants

- Keep UI presentation separate from provider, filesystem, and config services.
  Use the existing callback/options boundary in `internal/cli` and `internal/ui`.
  Slow I/O belongs in commands; deliver results as messages with request identity.
  Ignore stale completions after cancellation, account changes or modal closure.
- Preserve one active turn per session/account where required. Switching an
  account keeps readable history/conversation identity but drops incompatible
  provider continuation state. Never reuse another account's credentials/cache.
- Use rooted workspace APIs and revision checks for all edits. Preserve
  before-images, journal recovery and conflicting user changes; no blind overwrite.
  Preserve unrelated worktrees/index state and keep writing children isolated.
- Bound model/tool/process output, context, image sizes, concurrency and recursion.
  Propagate cancellation; distinguish an unknown mutation outcome from a retryable
  read failure. Never replay an uncertain mutation blindly.
- State must remain outside the workspace root. Use private settings/state,
  environment-variable credential references and the existing account store.
  Do not put API keys in config, argv, screenshots, fixtures or logs.
- Agent write/exec permissions and capability activation are distinct. Running
  host commands is not an OS sandbox. Preserve host-key checks and guest-local
  paths; VM management connectivity does not imply guest execution readiness.
- Skill installation must not execute or activate instructions. Preserve pinned
  source validation and preview-before-install callback ordering. Explicit
  `:skill install` authorizes installation without an extra UI confirmation.
- Use argument arrays for process execution and hooks. Do not interpolate
  untrusted paths/text into shell snippets. Preserve existing licensing notices.
- Hiding/resizing a pane must not stop its process or lose drafts/selection.
  Streaming must not steal focus. Measure rendered terminal cells, not bytes;
  sanitize external control sequences and keep emoji graphemes intact.
- Keep command parsing, completion and help synchronized. New config options
  require defaults/validation, editor exposure, docs and example-file updates.

## Desktop startup and Cursor-native updates (2026-10-18)

The optional desktop module opens a project/repository chooser when neither
`--root` nor `--project` is supplied. Explicit selectors bypass it. The chooser
loads the registry before acquiring a workspace; failed opens remain retryable.
CLI/TUI startup defaults are unchanged. Cursor-native exposes attached skill/viewer
callbacks over its MCP bridge, coalesces reasoning activity and separates model
step/tool budgets. Live service compatibility remains unverified; see the current
build evidence for fixture coverage and search compatibility limits.

## Build, test and render evidence

```sh
go build -o bin/bifrost ./cmd/bifrost
go test ./internal/ui ./internal/cli       # example affected packages
make test                                # full race suite, 90s timeout
make vet
make release                             # CGO-disabled stripped build
./bin/bifrost --root /path/to/project tui
```

Use temporary projects and separate temporary state/config for tests. A
constrained runner may need writable `GOCACHE`, `GOMODCACHE`, and `GOPATH` outside
the repo, and permission for loopback listeners/PTYs. Those restrictions are not
automatically product failures. Do not change the user's real accounts or VMs to
make a test pass.

- `make integration` opts into Chromium, full-screen terminal and disposable
  QEMU probes. Check prerequisites and scope before running it; do not run paid
  inference or affect real user infrastructure as an incidental test.
- Run focused behavioral tests first, then appropriate race/vet/build checks.
  Add regressions for changed correctness boundaries. Documentation-only work
  needs link/source verification, not a fresh VM or provider integration run.
- UI render fixtures live in `internal/ui/testdata/snapshots`. Regenerate the
  recent UX fixtures with
  `UPDATE_UI_SNAPSHOTS=1 go test ./internal/ui -run TestWriteUXSnapshots -count=1`.
  This writes ANSI fixtures, not PNGs. A prior `/tmp` image conversion helper is
  not a repository dependency; use an available renderer or live terminal capture.
- Inspect actual rendered output at 80×24, 120×36, 160×48 and narrow sizes where
  relevant. Check monochrome, long labels, Unicode, keyboard and measured mouse
  hitboxes. For input changes, include a live PTY smoke when available.
- If ripwire is available, use its bounded structural checks and report remaining
  findings honestly. Passing functional tests does not imply a clean quality gate.
- Update README/current-build evidence for behavior changes. Record fixture vs
  live coverage, commands run, failures and limitations. Rebuild `bin/bifrost`
  after executable changes when the user is testing that binary.

## Collaboration and handoff

Prefer concrete bounded changes. The user has preferred cheaper parallel agents
for independent implementation with primary-agent review; delegate only when
authorized and useful, give each agent clear file ownership, and validate their
results. Shared working files are visible immediately; avoid overlapping edits.

Do not stop at a plan when implementation was requested. Conversely, a research
or documentation request does not make a proposed feature implemented. End with
what changed, what was checked, and remaining work. Keep this guide and the
navigation status accurate when capabilities land.

## Ruflo integration

Project-provided instructions: when working on multi-file tasks or complex
features, use ToolSearch to find and invoke Ruflo MCP tools. Key tools are
`memory_store`, `memory_search`, `hooks_route`, `swarm_init`, and `agent_spawn`.
Check system-reminder tags for `[INTELLIGENCE]` pattern suggestions before work.

Availability note: Ruflo/ToolSearch were not exposed in the handoff session.
Discover them when available; otherwise state that limitation and continue with
normal repository inspection and available tools. Do not invent tool results or
install an orchestration dependency into Bifrost to satisfy this workflow.

# Current implementation evidence — 19 October 2026

## Separate Linux release packages — 19 October 2026

`packaging/Dockerfile` and the Linux release workflow build a static CLI/TUI
executable and a separate native GUI AppImage on Ubuntu 24.04. CUI is fetched
at the documented commit and both compatibility patches are applied. GTK assets
and libraries are bundled; graphics drivers, glibc 2.39+ and X11/XWayland remain
host requirements. No Go or GTK development installation is needed to run it.
The AppImage uses relative ELF library paths without exporting a bundled
`LD_LIBRARY_PATH` to host commands. `--version` works without opening GTK.

The workflow gates publication on both modules' race/vet checks, six independent
native Xvfb regressions and delivered-artifact smoke checks in a clean container
without system GTK. Checksums, version, release notes and third-party notices
accompany the two downloads. Tag/version mismatch prevents publication; assets
are uploaded to a draft before it becomes public. See [packaging](../packaging/README.md).

The first clean Ubuntu CI run passed both race/vet suites and the workspace,
controls and extensions native fixtures, then caught a project chooser height
overflow (640×588 after requesting 640×560). The chooser now scrolls its outer
content when theme metrics need more room, keeping its actions reachable; the
existing narrow-window regression remains unchanged and passed locally.

Local TUI packaging smoke passed (120×36, 80×24, 160×48 and 60×20, command palette,
clean exit). Clean-container and GitHub workflow validation are in progress;
publication is deferred until they pass. FUSE mounting and real provider/VM
integrations are outside this packaging check; CI uses AppImage extract-and-run.

## Custom skill installs and MCP config selection — 19 October 2026

The GUI and TUI chat composers now recognize registered `/commands`, alongside
colon commands. `/skill install` accepts an explicit local folder or the existing
catalog ID; `/skill install-git` exposes the existing safe Git installer.
Project/global scope and quoted paths are shared between frontends. These
commands work offline and do not send their contents to a model. Local/Git
installation leaves activation to task selection or configured auto-load.

`/mcp config PATH` validates a separate MCP endpoint file and saves its reference
with settings revision checks. `/mcp config` shows the saved server list.
Validation shares endpoint and alias rules with connection startup and does not
launch commands or require credential values. Startup honors named-project,
folder-identity and global defaults; explicit `--mcp-config` remains authoritative.
Changes require restart. GUI file/folder pickers prepare editable chat commands.
See [examples/mcp.json](../examples/mcp.json) and the README command reference.

Verification completed: full root race suite (`make test`), desktop race suite,
both modules' vet checks, focused race tests after cleanup, rebuilt native GUI
and CGO-disabled TUI. `TestNativeCustomExtensions` passed under Xvfb with fatal
GTK criticals: offline install, quoted paths, persisted project MCP selection,
and retained invalid command drafts. A loopback MCP fixture confirms saved
folder defaults actually connect on startup. Tests use temporary state and
local/Git/HTTP fixtures; no third-party MCP account or paid inference was used.
The rebuilt TUI passed a live PTY launch, resize (80×24, 120×36, 160×48, 60×20),
command palette and clean-exit smoke.

Native test investigation: the default-size Xvfb run timed out in the existing
640×600 resize phase. The standard 4K run passed `TestNativeWorkspace` in 54s,
then the old unanchored test selector also launched `TestNativeWorkspaceControls`
in the same process and faulted in GTK GL context initialization. The smoke
recipe now runs workspace, controls and custom-extension tests in separate
processes with exact test names, respecting GTK's process/thread lifetime.
The corrected `G_DEBUG=fatal-criticals make desktop-smoke` passed all three
fixtures: workspace 53.93s, controls 3.34s, custom extensions 0.93s. GTK layout
warnings were emitted, but the asserted compact/large workspace sizes passed.

The structural quality gate remains non-clean: comparison to `f83fdec` reports
10 gating findings (8 churn, 1 dispatcher complexity, 1 dispatcher verbosity),
plus 13 new-symbol findings, including test entry points classified as dead code.
Functional verification does not establish a clean structural gate.

## Provider-switch crash and model discovery — 19 October 2026

Reproduced a GTK invalid-object fault through real mouse provider selection.
GDB stopped in `gtk_single_selection_set_selected`: CUI's picker synchronously
replaced/freed the selection model from its own selection callback. The bundled
`cui-picker-lifetime.patch` defers acceptance until the native signal returns,
retains only the selected ID, and cancels pending acceptance on query/model
changes or closure. The adjacent CUI library and desktop executable were rebuilt.

Catalog discovery now resolves a saved profile's actual account name, avoiding
double-prefixed Cursor Native IDs and supporting renamed profiles. OpenRouter
uses the selected account's saved credentials, including after its environment
variable is unset. Missing explicit credentials fail without using another
account's key. Generic API profiles use the same environment precedence as
generation. Discovery keeps the saved model as the selection default and no
longer exports a form's API key into the shared environment. Empty/failed quick
catalog results are explained and are not cached as successful loads.

Verification:

- CUI search/feedback tests passed at normal and 4K sizes with
  `G_DEBUG=fatal-criticals`, including native selection and stale-click cancellation.
- Real X11 mouse provider switching, keyboard model/provider selection and clean
  shutdown passed with the same fatal checks. Before the fix, this path produced
  invalid-object notifications; after the fix it did not.
- The native race regression switches to an OpenRouter fixture, loads 512 models,
  searches/selects the last model, retains draft/history and handles canceled
  catalog requests. Shared CLI tests verify credential isolation, profile aliases,
  remembered model defaults and environment behavior.
- OpenRouter's unauthenticated public `/api/v1/models` endpoint returned 470 entries
  in 787,403 bytes. This response was replayed through a local fixture and rendered
  in the actual native picker. No generation or real account credentials were used.
  Endpoint reference: [OpenRouter models API](https://openrouter.ai/docs/api/api-reference/models/get-models).
- Full root race suite and vet, desktop race/vet, and both executable builds passed.
  Full native desktop workflow passed in 54.00 seconds; existing nonfatal GTK
  layout warnings remain. The rebuilt TUI passed PTY startup, command palette,
  resize at 80×24, 120×36, 160×48 and 60×20, and clean shutdown.
- Structural delta against `dcecce2` has three gating findings: increased size and
  complexity in the expanded native regression test, plus churn in `loadQuickModels`.
  This is not a clean structural gate. Native evidence covers Linux GTK, not macOS
  or Windows; the original screenshot's exact SIGSEGV had no saved core dump.

## Context, history, scoped skills, approvals and child models — 19 October 2026

- Native tabs now have individual close buttons and an always-visible History action.
  Sent conversations remain persisted; draft disposal retains its confirmation.
- The composer shows estimated context occupancy and exposes explicit summarization.
  Auto-compaction remains before-turn at 80% of a heuristic model window or 400 KiB
  of text. Catalog capacities improve the GUI meter when loaded; automatic thresholds
  currently use the model heuristic. Unknown capacities do not produce a percentage.
- Summarization has a bounded provider request and local transcript fallback. It only
  adopts a smaller context, retains source history, strips opaque checkpoints and
  converts retained tool calls to inert portable history. Source/fork turn locks,
  revision checks, interruption checks and cancellation prevent unsafe continuation.
  Cumulative turn usage is no longer mistaken for context occupancy.
- Installed skills expose project/global/built-in scopes, shadowing, source, revision
  and auto-load defaults. Changes are read on new turns, including daemon turns.
  Explicit empty project lists survive persistence; `--skill` remains an override.
  Existing task-local search/activation/resource tools remain model-directed. Remote
  catalog installation remains an explicit preview/install action.
- Native and Cursor Native agent commands support disabled/ask/rules/allow-all modes.
  GUI and TUI ask once for an exact command; pending approvals expire with the turn.
  Rules compare exact argv; shell strings do not receive implicit normalization.
  Vendor CLI and delegated vendor execution cannot claim Bifrost approval coverage.
  Headless and daemon runs reject unmatched commands when approval is unavailable.
- Sub-agent provider/model defaults are independent of the parent. Effective defaults
  are applied before scheduling so durable task records show the selected values.
  Explicit spawn overrides take precedence, and a different explicitly selected
  provider does not accidentally receive the default provider's model. Removing a
  referenced profile clears its child defaults.

Validation uses temporary projects/config/state and local provider fixtures:

- Root race suite and desktop race tests passed; root/desktop vet and both builds passed.
- Summarization tests inspect the next provider request, verify reduced context and
  original history, reject canceled summaries, and test automatic compaction/turn locks.
- Execution tests verify denial prevents filesystem side effects in both native exec
  and the Cursor bridge, and cancellation invalidates approval. TUI tests verify
  explicit approval keys and stale-request dismissal. No paid/live vendor calls.
- Native control test exercises close/history, scoped skills and persistence, command
  selection, sub-agent defaults and 800×720 rendering. Screenshots in README are
  actual native captures. The complete existing native workflow passed in 53.99s.
- The rebuilt TUI passed PTY startup, command palette, resizing at 80×24, 120×36,
  160×48 and 60×20, and clean shutdown. GTK/Xvfb still emits known layout/focus warnings.
- The structural delta against `2c4128c` remains non-clean: 81 findings, including
  31 minor and 24 new-symbol findings. Its 26 gating findings comprise one summary
  complexity regression, nine duplication groups, nine reused-helper clone groups,
  and seven churn findings. Functional coverage does not establish a clean
  structural gate or live vendor qualification.



## Independent quick provider/model controls — 19 October 2026

Desktop's composer now exposes separate searchable Provider and Model controls,
including when only one saved provider is available. Ctrl+Alt+P and Ctrl+Alt+M
open the respective picker; bare `:model` opens the same model picker. Provider
entries identify accounts without combining their model into the label. Model
catalogs load asynchronously with cancellation, stale-result checks, a per-profile
cache, explicit refresh, and manual model-ID entry via the existing command line.
The controls wrap on narrow layouts. Defaults remain workspace-wide and changes
are blocked while any conversation tab is running.

The shared model-switch implementation now prepares the requested runtime before
persisting the provider's model. Invalid or unsupported switches leave the old
model and conversation intact. Switching away and back restores the remembered
model while preserving conversation identity and readable history. The TUI keeps
its existing account/model picker and receives the same shared-runtime fix.

Verification (temporary profiles/state; model-catalog fixtures, no paid inference):

- Full root race suite passed, including new per-provider model memory, history,
  invalid input, and failed-switch persistence regressions.
- Native picker test passed: independent labels, model search/accept, provider
  return, draft retention, cache reuse, stale discovery cancellation, busy guards,
  and narrow rendering. Actual native captures are linked from the README.
- Real X11 Ctrl+Alt+M/P input searches and accepts a model/provider, restores the
  remembered model, and closes cleanly. The rebuilt TUI starts/resizes at 80×24,
  120×36, 160×48 and 60×20, opens its command palette and exits cleanly.
- Complete native desktop workflow passed (53.97 seconds). Desktop race tests,
  root/desktop vet and both executable builds passed.
- Structural delta remains non-clean: two gating churn findings in displaySnapshot
  and refreshBusy, four minor findings and seven new-symbol findings (including
  test size/complexity). No claim of a clean structural gate.

Live external provider model catalogs and paid generation were not exercised.

## File explorer and completed-tool stall — 18 October 2026

The native Files sidebar now has folders-first sorting, compact reusable rows,
original vector type icons, root/parent/refresh controls, a location header,
full-path/type tooltips, selected-file highlighting, filtering and keyboard row
navigation. Its scroll area leaves workspace navigation fixed. File names and
extensions are classified independently of their preserved backend paths. The
1,000-entry bound and stale directory completion checks remain in place. The
browser still navigates one directory at a time; it is not an expandable tree.
Normal, narrow and light native captures were inspected; the README includes the
new normal capture. No CUI source or TUI presentation changes were required.

The reported explanation stall had a concrete protocol defect: Cursor execution
results (including shell exit) were sent without the separate `stream_close`
control message. The installed Cursor 3.23.23 controlled executor explicitly
sends that marker after results. Bifrost now does likewise for unary and streaming
executions, sends execution heartbeats every three seconds during local work,
and serializes all outgoing sequence numbers. Append calls are bounded to 30
seconds and are not retried after an uncertain outcome. A matching execution ID
is required for aborts. Response-body errors are propagated rather than ignored.

Verification with temporary state/files and local HTTP fixtures, no paid calls:

- Full root race suite passed. Focused Cursor/CLI/TUI race regressions passed
  again after the final handshake helper refactor.
- A server fixture waits for each execution close before issuing its next tool
  and explanation; the old implementation stalls there. Heartbeat identity,
  shutdown ordering and cancellation on append failure are covered.
- Full native CUI workflow passed (53.98 seconds), including parallel chat,
  walkthrough, editor, terminal, project and marketplace flows. A dedicated
  explorer native test covers icons, folder/open/parent actions, filter identity,
  row reuse, normal/narrow and light rendering.
- Real X11 keyboard input (Ctrl+P, filter, Down, Enter) opens the expected source
  file and highlights its row; WM close exits cleanly. The rebuilt TUI starts,
  resizes at 80×24, 120×36, 160×48 and 60×20, opens its command palette and exits.
- Root/desktop vet and both builds passed; the TUI remains CGO-independent.
- Ripwire against the preceding commit reports two gating churn findings
  (`Run`, `buildSidebar`), one minor finding and ten new-symbol findings, mostly
  integration-test size/complexity and vector artwork length. The gate is not
  clean; the shared Run function's complexity/length no longer regress.

Live Cursor service acceptance has not been retested. This fixes the reproduced
execution-stream handshake, not every possible upstream stall. A previously
stuck process must be stopped/restarted with the rebuilt binary to use the fix.

## Startup project choice and Cursor-native fixes — 18 October 2026

Starting `bifrost-desktop` without an explicit root/project now opens a native
project chooser. It searches the saved registry, retains every project's linked
repositories, selects configured defaults without silently opening them, and offers
native folder selection/current-directory opening. Registry and workspace loading
run off the UI thread; failed opens return to the chooser. Explicit selectors and
CLI/TUI startup behavior are preserved. The Open action stays in a fixed footer at
640×560; normal and narrow GTK captures are in the README and desktop guide.

The screenshot-reported Cursor-native issues had distinct causes: model steps
were used as a raw tool-call cap, regular-expression patterns were sent to a
literal search API, thinking fragments had no stable identity, the aggregate final
replayed all previous streamed text, and external-runner filtering prevented
viewer/skill tool attachment. These are corrected at the adapter boundary. The
MCP bridge now accepts nested protobuf JSON arguments for existing callbacks.
Search uses bounded rooted regex/glob traversal; stream readers are cancelled on
an early return and limits are checked before the next local execution.

Verification (isolated temporary projects/state; no paid inference):

- Full root race suite (`go test -race -count=1 -timeout=90s ./...`) and desktop
  race tests pass. Root/desktop vet and both builds pass, including CGO-disabled TUI.
- Protocol regression fixtures cover regex/glob/file scoping, escaping paths,
  invalid patterns, cancellation, binary skipping, pre-execution budgets,
  MCP argument round trips, tool errors and walkthrough callback attachment.
- Shared adapter and TUI regressions verify streamed replies are not replayed and
  stable activity updates retain each reply. The real TUI binary starts/resizes at
  80×24, 120×36, 160×48 and 60×20, opens its command palette and exits cleanly.
  Opt-in PTY tests, including full-screen Vim, pass.
- The complete CUI workflow smoke passes (53.85 seconds), including parallel chats,
  editor/review, terminals, settings, catalogs and guided walkthrough controls.
  A separate chooser test checks project/repository selection, filtering, empty
  projects, folder opening and normal/narrow rendering. Actual X11 keyboard input
  exercises recovery from a missing saved path, startup search/Enter, workspace
  handoff, tab shortcuts and WM close.
- Native captures are Linux GTK/Xvfb evidence. Existing non-failing GTK focus and
  layout warnings remain; no claim of macOS/Windows runtime qualification.
- Structural quality remains non-clean: the pre-commit delta reports three gating
  findings (Cursor Run complexity/length and viewer-tool churn), five minor findings
  and 31 new-symbol findings, including test/callback detection and new search/
  protocol helper complexity. Functional checks do not erase these findings.

Cursor service acceptance is still unverified live. Regex search is RE2, not full
ripgrep/PCRE compatibility; context-line/sort options, ignore rules and the full
file-type registry remain outside this increment. Arbitrary MCP resources/servers,
Cursor-native desktop/browser/SSH integration, and vendor-CLI bridges are not
advertised as implemented by these changes.

## Parallel desktop conversations — 18 October 2026

The native CUI desktop now has a visible tab strip and independent running turns.
Tabs retain drafts, transcript/activity, reading position and unread completion
state. Ctrl+N opens a tab, Ctrl+W closes an idle tab, and Ctrl+Tab / Ctrl+Shift+Tab
cycles. Stop/Escape is scoped to the selected tab; application shutdown cancels
all turns. Saved running conversations focus their existing tab. Overflow arrows
keep the selected tab reachable at 640×600. Account/model changes remain exclusive;
external vendor CLI per-account locks still apply. Tabs share workspace files,
not separate worktrees. The open-tab arrangement is not restored after restart.

`DesktopWorkspace.RunConversation` copies turn configuration and owns separate
session state while retaining existing session locks, provider binding and
revision-safe persistence. The task monitor tracks multiple runtimes. A regression
caught and fixed a recovery/registration race during concurrent scheduler startup.
The CUI compatibility patch now also normalizes GTK ISO_Left_Tab for window
commands; actual keyboard input exposed this difference from ordinary Tab.

Validation uses temporary projects/state, a loopback streaming provider and Xvfb,
with no paid inference or real accounts:

- Full root race suite, desktop race tests, both vet checks and both executable
  builds pass, including the CGO-disabled TUI.
- 100 repetitions of the two concurrency regressions passed after the scheduler
  fix. They cover overlapping provider calls, isolated cancellation and history,
  follow-up continuity, duplicate-session rejection and shutdown cancellation.
- The native fixture checks two overlapping HTTP streams through real Send/tab
  controls, independent Stop, idle-tab close while another runs, unread badges,
  reopening running sessions, actual keyboard shortcuts, 640×600 overflow and
  scroll/draft restoration. Captures are in the README and desktop guide.
- Ripwire's Git-HEAD delta against `4627001a6` remains non-clean: 12 gating
  duplication/clone findings in thin adapters and tab cycling, plus minor and
  new-symbol findings. Functional test success is not a clean structural gate.
  The growing native parallel test cases were separated into their own helper;
  unrelated adapters were not refactored solely to remove syntactic matches.
- The bundled CUI patch passes reverse-apply verification against the local
  toolkit. Existing GTK measurement/focus warnings still appear in the full
  fixture; the tested native interactions and requested window sizes pass.

Live vendor interoperability and macOS/Windows runtime behavior were not tested.


## Guided explanations, task-aware skills and terminal identity — 17 October 2026

Both frontends now accept `:explain TOPIC` to prepare a short, editable request.
`viewer_flow` supports code, concept and supplied-diff steps, with separate
explanations and bounded Back/Next navigation. Native controls provide a
scrollable explanation panel; TUI `p/n/x`, clickable controls and wheel scrolling
provide navigation and a full explanation view. Presentations are in-memory.
They do not approve PRs, stage files, or persist review state. A normal file-open
clears the old walkthrough. Unsent composer drafts are preserved.

Four original embedded skills cover code exploration, debugging, change review
and walkthroughs. Native agents see descriptions, choose skills with
`skills_search`/`skills_activate`, then read referenced text through
`read_skill_resource`. The previous blanket injection of discovered bodies has
been removed. Explicit `--skill` activation still works. Project/global skills
can override built-ins; selected resources are revision checked. No skill load
executes scripts or enables permissions. Ripwire is optional and is not bundled.
Vendor-managed backends cannot call these native tools; do not claim interactive
walkthrough or model-driven Bifrost skill-tool coverage for those backends.

The TUI includes a Unicode violet/cyan bridge mark, a compact header/welcome
variant, and an ASCII fallback. Render geometry covers 32/40-column narrow views,
80×24, 120×36 and 160×48 with dark/light/plain themes. The README adds two native
explanation captures and two ANSI-rendered terminal fixtures.

Verification:

- Full root race suite passed; affected UI/CLI/skill race tests were rerun after
  the final refactors. Root and desktop vet pass; desktop race tests pass.
- The real GTK/Xvfb native fixture passed the full workspace sequence plus
  concept/code/diff, Back/Next boundaries, return navigation, End walkthrough,
  draft preservation and preparing a request without sending.
- A live PTY smoke with disposable state/config confirmed `:explain` prepares an
  editable request in the rebuilt TUI without calling a provider. Its first
  assertion expected the beginning of a long draft, which the composer correctly
  scrolled out of view; the request was shortened and the smoke rerun.
- Regression coverage includes invalid/oversized walkthroughs, code highlights,
  mouse coordinates, explanation scrolling, skill search without activation,
  activation/resource ordering, path traversal, selected revision replacement,
  default-skill precedence and preservation of explicit activation.
- All four SKILL.md files pass the skill frontmatter validator. README image
  references resolve. Both binaries rebuilt, including `CGO_ENABLED=0` TUI.
- `ripwire . --quality-delta` against `ffeab30c7` has **0 gating regressions**,
  **21 minor preexisting-worse findings** and **22 non-gating new-symbol findings**.
  This is a Git-baseline delta, not evidence that historical repository debt is
  resolved. Earlier counts below used a broader historical sidecar baseline.

No paid/live-model skill-selection evaluation, vendor login, remote machine or
production workspace was exercised. The tests demonstrate tool contracts and
UI behavior with deterministic/disposable fixtures.

## Native CUI desktop increment — 17 October 2026

The separate `desktop/` Go module builds `bin/bifrost-desktop`, a native Go+CUI
frontend covering the implemented TUI workflows. Both frontends share runtime,
accounts, settings, project/catalog/task callbacks and rooted filesystem services.
This increment adds all TUI command handlers/aliases, durable editor drafts,
shared Vim/search/undo, viewer highlights/walkthroughs, conversation tabs,
mentions, colored multi-PTY terminals and command-output tabs, persisted layouts,
interactive Markdown links/tables, and project/GitHub/Notion detail views.
The original executable still builds with `CGO_ENABLED=0`; its module has no CUI
imports or GTK dependency. Viewer tools are now available in both frontends.

The design retains a bounded reading width, charcoal/violet hierarchy, cyan
focus, optional resizable context, a growing composer and adaptive compact
layouts. Sources, launch instructions, screenshots and a workflow coverage table
are in [desktop.md](desktop.md).

The screenshot-driven workspace redesign adds persistent Project/Tasks/Skills/
MCP/Accounts/Settings navigation, a bounded sidebar and workspace reading width,
a compact editor toolbar with overflow actions, task search/status filters and
separate results/execution details. Project now opens a multi-repository overview;
common runtime/tool settings have native form fields alongside advanced JSON.
Both catalogs expose source previews and installation scope directly.
The chat follow-up's desktop race tests, native interaction suite (including
800×720 and 150% text), both vet checks, shared CLI/UI race tests, native rebuild
and CGO-disabled TUI build pass. The structural gate still reports 87 historical
major findings; this remains separate from functional verification.
The chat follow-up marks user prompts as outgoing and labels their violet
bubbles, groups adjacent execution events behind one native disclosure, and
removes the duplicate activity log panel. Group actions retain complete source
text and event identity; the shared session/TUI model is unchanged. Centering
uses an overlay so fixed gutters no longer prevent narrow-window resizing. The design
rationale and primary references are recorded in [desktop.md](desktop.md#design-choices-and-research).

The 17 October full GUI review additionally fixes streaming/status interleaving,
shared account labels across tabs, per-run command-output identity (including
background chats), stale mention suggestions, failed recovery-write retries,
reload failure retention and selection, and editor search at the current cursor.
Settings now use compact scalar inputs and boolean toggles; Save validates all
edited values and retains them across field changes. Unsent chat drafts and
repository-form edits participate in close protection. Superseded catalog
requests are cancelled.

The final native race fixture passed in 47.18 seconds (48.318 seconds including
race teardown). The full root race suite, focused shared CLI/UI tests, both vet
checks and desktop race tests passed; both binaries were rebuilt. Initial review
runs exposed a malformed new command-output fixture and a real pending-settings
loss on field selection; both were corrected and the complete suite rerun.
The structural check remains non-clean: 87 historical gating majors, 4 minor
findings and 1304 non-gating new-symbol findings against the existing sidecar.
No live vendor login, paid inference or real user workspace was used.

The README contains a native screenshot tour and a generated Bifrost logo.
`patches/cui-desktop.patch` preserves the adjacent toolkit edits required by this
snapshot, with its base revision and fresh-checkout instructions. This makes the
native dependency reproducible without changing the TUI's dependency graph.

Verification for this increment:

- The parent full `go test -race -count=1 -timeout=90s ./...` passed. The shared
  CLI/UI packages were race-tested again after the final adapter changes.
  Parent and desktop `go vet ./...` pass; desktop unit/race tests pass.
- The native race fixture uses actual GTK/CUI controls under Xvfb, a local
  streaming server, temporary config/state/files and disposable Git repositories.
  It covers send/stop, session creation/resume, stale reads, command completion/
  help/invalid-input retention, palette focus, file save/conflicts, settings
  save/conflicts/themes, account switching, catalog preview/install scope,
  task cancellation and multi-repository linking/defaults.
- Expanded cases cover real XTest Vim input, per-file undo/search, disk draft
  recovery, viewer walkthrough/highlight/focus, background chat stream isolation,
  independent PTYs, real Left/Delete/Return/Ctrl+C shell input, mention expansion,
  project connector detail views, persisted presets/proportions, composer row
  bounds and command-output tab eviction. The command coverage test checks every
  canonical command and alias against the shared TUI registry. Physical Tab/Shift+Tab
  events exercise the same completion cycle, including reverse cycling; adapter
  tests cover catalog IDs and scope completion. Agent-opened files can enter the
  rooted editor directly.
- The redesign suite adds real Ctrl+F/Escape focus checks, separate Skills/MCP
  navigation, MCP preview/install ID and scope, category switching during an
  active install, task search/status/details, refresh-safe repository drafts and
  common/advanced form validation. It passes with the race detector. The parent
  race suite, both vet checks, desktop race unit tests, native build and
  CGO-disabled TUI build also pass after the redesign.
- Captures are from the running native app at 2560×1440, 1440×920, 800×720, 640×600,
  640×480 for the palette, and 1200×900 with 150% text. Compact dimensions are
  asserted after asynchronous GTK font/layout reflow. Xvfb uses software
  rendering and logs Mesa DRI3 warnings; GTK emits transient sizing/focus warnings.
- CUI's Code widget now actually honors editable/read-only state, including
  printable normal-mode keys. GTK tests assert the editable property and real
  input behavior. Navigation/control forwarding and text ranges have regression
  coverage. Binding coverage and all 631 C/Rust ABI checks pass. macOS/Windows
  mapping changes are source-only evidence.
- Both binaries are rebuilt. `make desktop` rebuilds the adjacent CUI archive
  and forces relinking the native executable; the TUI build stays CGO-disabled.
- The structural quality gate is still non-clean against its historical sidecar:
  87 preexisting-worse major findings, plus non-gating new-symbol findings
  including complexity/length findings in desktop presentation builders. The
  redesign does not make this historical gate clean.
  Functional success must not be described as a clean structural gate.

No paid inference, real OAuth account, user repository mutation or real VM was
used for these checks. Linux native fixture coverage does not establish other OS
runtimes, real vendor/SSH GUI behavior, accessibility or IME qualification. Shared
TUI roadmap gaps (approval/MCP OAuth forms, public skill federation, LSP overlays,
hunk staging and terminal pixel images) remain outside the implemented baseline.

This continuation adds onboarding and configuration, native providers, monitored sub-agents, daemon execution, guest workers, desktop controls, GitHub workflows, inline editing, expanded MCP, LSP/hooks, and multi-file transactions. The latest increment adds named subscription accounts, portable conversations, decision routing, a skill marketplace, and configurable terminal layouts. It uses Go with ten direct modules. Rich chat adds Goldmark v1.8.5 (no third-party dependencies); no JavaScript/TypeScript runtime is required.

The previous increment's evidence is preserved in [local-workflow-build.md](local-workflow-build.md). The broader [backlog](implementation-backlog.md) remains open where its acceptance scope exceeds the capabilities below.

## Interactive workspace refinement

The composer expands with wrapping and newlines, defaults to Shift+Enter for a
newline, and keeps Alt+Enter/Ctrl+J fallbacks. Two blank rows separate the last
transcript line from the message box. Sending a message leaves the cursor in
the composer. `ui.composer_max_rows` and
`ui.newline_key` are persisted options. A searchable `:settings` / `:config`
editor exposes every configurable field, stages validated edits, saves atomically
with a revision check, and applies appearance changes live. Runtime changes need
a restart. Full field help and structured JSON edits are supported.

Colon commands now complete names, arguments, and known task IDs. Invalid input
stays open with usage help; `:help COMMAND` and `COMMAND --help` provide examples.
`?` opens a help modal over the workspace. Launching the TUI resumes the newest
saved conversation for this workspace and account. A later empty session does
not hide one that already has messages. `:history` still browses the rest, and
`:new` starts a fresh conversation.
The terminal is a tab in the viewer. It stays hidden until `t`, `T`, `:terminal`,
or an agent command opens one. Opening it does not take the keyboard. Click the
shell, or press `i` or Enter on that tab, to type. Esc returns to the message
box. `T` and the `+` on the viewer tab strip add another shell. Hiding a shell
tab does not stop its process.
Mouse dragging previews
column sizes and persists on release,
with cancellation and rollback on save failures. A left click focuses the pane
under the pointer. A click on a viewer tab selects that tab without entering
terminal input. The focused frame uses a heavier border and a cyan title in
color themes.

The chat account pill and model switcher show the provider name (OpenRouter)
instead of a repeated profile id. A distinct account name stays in parentheses.
The context badge uses a context length advertised by the model catalog,
including `context_length`, `context_window`, `max_context_window`,
`input_token_limit`, and OpenRouter `top_provider` lengths. A later connection
event does not replace that window with a name-based guess. When no catalog or
provider length exists, the badge shows the used token count without a
denominator. Reported session tokens are shown as-is; the rendered transcript
is used only when the session has not reported a count. A count above a known
window is shown above 100%.

The marketplace now uses a responsive one-to-four-column card grid with live
search. Each card shows its name and short description; a click or Enter opens
a centered, scrollable detail modal over the dimmed grid. Installation has
explicit project/global buttons, progress, and result/error feedback. Closing the
modal restores the search and selection. Keyboard navigation follows grid rows
and columns, and mouse hitboxes follow the rendered geometry.

`:skill browse` opens the grid; `:skill install ID [--scope project|global]`
installs directly with catalog-ID/scope completion and usage help. It resolves the
entry before installing and honors cancellation between callbacks. Installation
does not activate the skill. No dependencies were added.

Validation: full `go test -race ./...`, `go vet ./...`, and a CGO-disabled build
passed. After the final rendering fixes, the affected UI/config/CLI race tests
were rerun. A live Bubble Tea process under a real PTY accepted Kitty CSI-u
Shift+Enter as `Hello\nworld`, completed `:lay<Tab><Tab>`, saved a mocked settings
change, and received SGR mouse drag events with a changed persisted split. The
fixture used temporary state without paid inference or real account changes.
A separate marketplace PTY smoke test opened the real catalog, clicked a card
using SGR mouse events, verified modal install actions, returned to the grid with
Esc, and checked malformed install-command help. Card and modal renderings were
also inspected at 120×36, 80×24, and 36×24.
Rendered fixtures are in `internal/ui/testdata/snapshots`, including
`composer-expanded`, `settings`, `commands`, and `marketplace` screens.

The structural quality check remains non-clean: `ripwire . --quality-delta`
reports 66 major findings against the existing broad sidecar baseline, plus
new-symbol complexity/verbosity findings. One introduced join-renderer clone was
removed. This is not a claim that the structural quality gate passed.

## Implemented integration

- Setup: a six-step Bubble Tea wizard, offline mode, provider/model/environment-key references, multi-repository project registration, permissions, skills/MCP/browser choices, strict private config, custom-profile preservation and import preview/apply.
- Providers: native Responses, Anthropic and Gemini adapters, compatible Chat Completions, OpenRouter, streaming, bounded image transport, native continuation data, tool names and usage accounting. Session fingerprints reject accidental crossing of providers/environments; explicit TUI account switching rebinds the same conversation with portable history.
- Accounts: named ChatGPT OAuth identities with validated signed identity tokens and serialized rotating refresh; isolated official Cursor/Claude CLI logins; a separate native Cursor client (`cursor-native`) with browser challenge login, API-key exchange, refresh, and local execution of Cursor's file, search, and shell requests; OpenRouter environment-key references. Switching preserves the conversation ID and readable history while dropping provider-owned continuation data. Native parents can delegate to children using other native or external profiles.
- Routing: one bounded TypeSafe Jev/OpenRouter decision per parent turn, allowlisted profile candidates, confidence threshold, selected-profile fallback, and reported decision-token usage. Capability filtering keeps external backends from silently replacing turns that need native-only tools.
- Marketplace: a small built-in catalog of three pinned official skills, custom HTTPS catalogs, source metadata previews, explicit project/global installation, and project-over-global resolution. Installation does not activate skills.
- Sub-agents: model-facing spawn/status/wait/cancel, separate writable worktrees, readonly children without exec, checked provider/model overrides, bounded concurrency/queue/depth/children/steps/output, capacity yielding during child waits, durable snapshots/events, live usage and cross-process cancellation. The parent appears in the same graph. Mutable browser/MCP/hook sessions are not shared with children.
- TUI: inline editor with revision-safe saves, drafts/search/undo/redo, palette/help/theme choices, task monitoring with timed refresh, guest file trees/review/editor/terminal support. Studio, Conversation, Review, and Focus presets, pane percentages, and terminal visibility persist. The dark studio treatment adds role hierarchy, syntax color, provider/account/model/location segments, and restrained focus markers. Render fixtures cover 80×24, 120×36, and 160×48 cells.
- Daemon: private authenticated Unix socket, singleton handling, detached start, bounded jobs, event replay, cancellation, interrupted recovery, and session-preserving daemon chat. Local TUI writes use transient workspace ownership in daemon mode.
- Editing: bounded JSON patch plans for create/replace/delete/move, before-images, prepared/applying/committed journals, revision rechecks, no-replace destinations, partial-crash recovery, selective conflict-preserving undo. The original conversation is retained when explicitly compacting; imports never execute stored tools.
- Guest execution: versioned stdio worker and SSH transport with explicit root/state/capabilities, cancellation and concurrency limits. File, process, patch, review, browser and desktop RPCs remain guest-bound. SSH retains host-key verification and uses batch authentication.
- Browser/desktop: isolated Chromium accessibility actions and screenshots, observation-bound X11/Wayland tools and leases, capability reporting, bounded model image transport, guest artifact cleanup.
- Git/GitHub: status/diff/stage/unstage/commit, worktree management, env/gh authentication, project repository linking, PR/check queries and draft PR creation. Nothing pushes or merges automatically.
- MCP: stdio/HTTP tools, environment-referenced auth headers, resources/templates/prompts, subscription and elicitation callback APIs, explicit endpoint CRUD, rooted native stdio server.
- LSP/hooks: explicit language-server processes, handshake/versioned documents/diagnostics API/hover/definition; trusted bounded argv hooks at turn/tool boundaries.
- VMs: fixed QMP/VBox lifecycle and snapshot operations, managed QEMU launch configuration and optional guest SSH workers. Snapshot jobs distinguish submission from guest readiness.

## Validation

The prior full integration increment passed the following checks:

```sh
BIFROST_BROWSER_TEST=1 BIFROST_TERMINAL_TEST=1 BIFROST_QEMU_INTEGRATION=1 \
  go test -race -count=1 -timeout=90s ./...
go vet ./...
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o bin/bifrost ./cmd/bifrost
```

In this environment, Go caches were set to `/tmp/bifrost-go-cache`, `/tmp/bifrost-go-mod`, and `/tmp/bifrost-go`. Socket/PTY/Chromium/QEMU fixtures ran with the permissions needed for local test processes. The command `make integration` enables the same opt-in probes; `make test`, `make vet`, and `make release` cover the usual checks.

A separate executable smoke test used a temporary project/state directory, a loopback SSE provider, and a 120×36 PTY. It completed first-run onboarding, edited and saved a file in the inline editor, spawned and waited on a child agent, read completed task snapshots, ran two daemon-backed chat turns in the same conversation, opened the task monitor, exited the TUI, and stopped the daemon. Six fixture model requests were served. No API credentials or paid services were used.

That increment's stripped Linux amd64 executable was statically linked and 13,861,024 bytes. Linux arm64 and Windows amd64 cross-builds also passed; these are compilation checks, not runtime validation. These size and cross-build measurements precede the account/UI increment. The structural quality gate did not pass; its existing baseline comparison reports complexity and other regressions, including test complexity. That separate maintenance result is not included in the passing functional checks above.

The account/UI increment has additional race-test coverage for OAuth identity validation, refresh rotation, replacement/logout binding, external process cancellation, portable conversation switching, mixed-backend children, typed routing, marketplace catalog/install validation, immutable SHA installation, layout persistence, and source-preserving syntax coloring. A separate fake-Claude PTY smoke test switched two accounts within one session, verified that the second client received portable history without an old vendor resume ID, and exercised layout/resize/terminal persistence. An actual public, pinned marketplace skill was downloaded into temporary state without activation. No paid inference or real subscription login was performed.

Final account/UI checks passed: `go test -race ./...`, `go vet ./...`, and `CGO_ENABLED=0 go build -trimpath ./cmd/bifrost`. These tests used isolated temporary config/state and the Go caches above. The final structural comparison still exits nonzero with 60 major regressions against its older sidecar baseline; it is a remaining maintenance limitation, not a passing check.

Review found and corrected a queued-task cancellation/shutdown race, a daemon startup recursion bug, daemon flag/session loss, a TUI/daemon workspace-lock conflict, MCP/app config path confusion, task event cursor aggregation errors, native image/opaque-message loss, and partial patch recovery/destination-overwrite hazards. Regression tests exercise these paths. Failed smoke-test daemons were cleaned up.

## Actual limits

- Linux amd64 is the runtime tested here. Cross-compilation is build evidence only; Windows terminal and socket ownership limitations remain explicit. macOS desktop automation is not implemented.
- Native providers and GitHub have local fixture coverage, not paid-account/live-repository validation. Desktop actions use injected command fixtures; real X11/Wayland/graphical guests were not controlled. QEMU integration is a disposable diskless process. No user VM/disk was changed.
- SSH sub-agent isolation and daemon-backed SSH chat are explicitly unsupported. Direct SSH file/command/browser/desktop work is implemented. The remote terminal needs guest tools installed; the application does not provision an operating system or install guest binaries automatically.
- MCP OAuth login UI and TUI elicitation forms remain pending. SDK callback APIs exist; an uncertain mutating tool is not blindly replayed to continue it.
- LSP is available through explicit CLI/library calls, not editor diagnostics overlays. Hunk-level staging, background PTY reattachment, inline pixel graphics, and the full approval UI remain roadmap work.
- Cursor/Claude subscription turns use installed vendor clients and their tools; they do not expose Bifrost's native browser/desktop/MCP/SSH tool loop. Cursor multi-account isolation is supported on Linux and explicitly unavailable on macOS because of the vendor client's shared keychain namespace. Live vendor compatibility remains unverified.
- Native Cursor (`account login cursor-native`) is a separate account from `cursor-agent`. It speaks the Cursor 3.23.23 Connect agent protocol over HTTP/1 RunSSE, sends the CLI client version and type on each request, and executes rooted file, search, shell, shell-stream, background-shell, git-diff, and HTTP fetch requests when those permissions are enabled. Approval prechecks follow the same permissions. Attached Bifrost viewer/skill tools are now available through the MCP bridge described above. Diagnostics, unrelated MCP servers/resources, hooks, computer use, screen recording, and sub-agents return a completed protocol result that names the missing capability. A new turn sends an empty conversation state. A resumed turn sends the saved checkpoint together with the conversation blobs Cursor stored on the client. A checkpoint whose blobs were never stored starts a fresh Cursor conversation from the readable transcript. Project instructions travel in the user message; the team-only system-prompt option is left unset. A request for workspace context is answered with the workspace path, shell, and git branch when one is visible. A stream failure includes the server's error detail when Cursor sends one. Login, refresh, and model discovery are covered by a local fake server. Live Cursor subscription acceptance was not tested.
- Compaction is explicit and summary-driven. No automatic provider retries are performed. Session snapshots resume from the last complete turn, not an arbitrary interrupted tool instruction.
- Worktrees start from a committed ref. Dirty parent edits are not copied or automatically merged. Children retain separate results and worktree paths for review.
- Task aggregate events are a latest-window view; stable replay cursors are per-run. State/artifact lifecycle quotas and garbage collection need further work for long-running installations.
- The structural quality comparison still reports complexity/verbosity and test-entrypoint/clone findings against the existing sidecar baseline. The agent tool-batch execution was separated from the model loop; large CLI/protocol/state dispatchers remain maintenance work. No clean structural-quality-gate claim is made.

The README is the operational guide. Phase-wide production acceptance in the original plan has not been claimed.

## Rich chat and interactive folds

Chat now uses Goldmark v1.8.5 for CommonMark/GFM parsing and the existing ANSI/Lip Gloss renderer for headings, inline formatting, lists, quotes, tables, code panels, and Go/diff colors. It preserves literal code and source history, caches complete messages, and bounds the reading column. The composer supports multiline paste and grapheme-aware emoji deletion. Unicode wrapping is tested with skin tones, flags, variation selectors, and joined emoji.

Provider icons are configurable with `ui.icons`: Unicode, verified Nerd Font brand marks, or ASCII labels. OpenAI and Claude logos require matching Nerd Font glyphs; fonts are not installed or changed automatically.

Tool calls, provider-supplied thinking summaries, and other activity have mouse-clickable disclosures, with Tab/Shift-Tab and Enter/Space equivalents. Assistant replies can also be folded. Wheel scrolling and hit testing account for pane layout, margins, and viewport position. Tool updates retain the same fold and expanded state. Native, external, daemon, and session-history bridges carry bounded details. Thinking is shown only when explicitly returned by a provider; redacted blocks and continuation signatures are not rendered. Thinking folds can arrive after the model step completes and are not restored from opaque session state.

The full race suite passed, followed by affected-package race checks after final UI refinements; vet and the Linux build passed. Screenshot fixtures show the real renderer, including an expanded thinking-summary example. The Linux CGO-disabled, unstripped build grew from 21,138,065 to 22,174,885 bytes during this increment (rendering, folds, logos, and event plumbing combined). The structural baseline check remains nonzero as described above.

A real 120×36 PTY smoke sent SGR mouse press/release events to a collapsed tool row: details were absent initially, visible after the first click, and hidden after the second. Wheel input remained stable and quitting exited with status 0. The temporary smoke program was removed.

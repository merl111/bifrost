<p align="center">
  <img src="docs/images/bifrost-logo.png" alt="Bifrost — violet and cyan bridge logo" width="720">
</p>

# Bifrost

**Your code, conversations and tools in one native workspace.**

Bifrost is a Go AI development harness with a native CUI desktop app and a terminal UI.
Chat with models, edit source, run a real shell, review changes and follow agent
work across repositories. Both interfaces share the same sessions, accounts,
permissions and workspace services.

[Desktop quick start](#desktop-quick-start) · [Screenshot tour](#screenshot-tour) ·
[Terminal quick start](#terminal-quick-start) · [Configuration](docs/configuration.md) ·
[Build evidence](docs/current-build.md)

![Two conversations running in parallel in the native CUI desktop](docs/images/desktop-parallel-tabs.png)

- **Conversations you can follow.** Your prompts have distinct bubbles; execution
  activity folds away, and visible tabs run conversations concurrently while retaining
  their own drafts, history and reading position.
- **A project can contain several repositories.** Inspect branches and changes,
  open repository workspaces, and set project-specific provider and tool defaults.
- **Code stays close to the conversation.** Revision-checked editing, undo/redo,
  recovery drafts, Vim-style movement, file search, review and guided source views.
- **Tools have a place.** Independent terminal tabs, task monitoring, and separate
  Skills and MCP catalogs with source previews and installation scopes.
- **Choose the interface and provider.** Native APIs and supported vendor adapters;
  desktop controls or keyboard-driven TUI. No JavaScript or Node frontend runtime.

This is an actively developed **Linux preview**. GTK/X11 has native interaction
evidence; other desktop platforms and live vendor/OAuth integrations need further
qualification. [Implemented coverage and limits](docs/desktop.md#implemented-tui-workflow-coverage)
separate working features from the broader roadmap.

## Desktop quick start

Keep the CUI source checkout beside Bifrost. For a fresh checkout, use the
[pinned CUI revision and included compatibility patch](patches/README.md):

```text
source/
├── bifrost/
└── cui/
```

Install Go 1.26+, CMake, a C compiler, pkg-config, GTK 4.6+ development libraries
with X11 support, and Xext development libraries. On Debian/Ubuntu, the native
packages are `libgtk-4-dev` and `libxext-dev`.

```sh
make desktop
./bin/bifrost-desktop
```

Startup opens a searchable **project chooser**. Select a saved project and one of
its linked repositories, or choose **Open a folder**. An explicit
`--root /path/to/repository` or `--project NAME` opens that workspace directly.

![Native project chooser with linked repository selection](docs/images/desktop-project-picker.png)

Open **Accounts** to connect a provider. Without an account, files, editing,
review and the local shell remain available. Existing CLI configuration and
sessions work in the desktop too:

```sh
./bin/bifrost-desktop --project myapp --repository api --profile work
```

| Desktop shortcut | Action |
| --- | --- |
| `Enter` / `Shift+Enter` | Send / add a newline |
| `Ctrl+N` / `Ctrl+W` | New / close conversation tab |
| `Ctrl+Tab` / `Ctrl+Shift+Tab` | Next / previous conversation tab |
| `Ctrl+K` | Search commands, files and conversations |
| `Ctrl+Alt+P` / `Ctrl+Alt+M` | Choose provider / model |
| `Ctrl+P` | Browse workspace files |
| `Ctrl+L` | Focus the composer |
| `Ctrl+S` | Save the current file or settings |
| `Ctrl+F` in the editor | Find in file |
| `Ctrl+T` | Open the terminal |
| `Ctrl+B` / `Ctrl+J` outside the composer | Toggle sidebar / context pane |
| `Escape` during generation | Stop the selected tab’s turn |

See [the desktop guide](docs/desktop.md) for custom CUI paths, prerequisites,
permissions, all workflows and the test commands. The desktop lives in a separate
Go module; the TUI still builds with `CGO_ENABLED=0`.

Open a tab with **+** or **Ctrl+N**, send a message, then open another tab and
send there too. Running tabs keep streaming in the background. A dot marks an
unread result; Stop/Escape affects the selected tab only. Account/model changes
wait until all turns finish. Vendor CLI adapters retain their per-account lock;
native API and Cursor-native turns have independent conversation state.

## Quick provider and model selection

The composer has separate **Provider** and **Model** controls. Choose a configured
provider account, then choose one of its models without creating another profile.
Both lists are searchable; each provider remembers its last selected model.

- **Ctrl+Alt+P:** choose a provider/account.
- **Ctrl+Alt+M** or **`:model`:** choose a model for the current provider.
- **Refresh model list** reloads its catalog; **Enter model ID** supports unlisted models.

Drafts and conversation history survive switching. Selections apply to the workspace's
chat tabs, and switching is disabled until running turns finish or are stopped.

![Separate provider and model controls in the native composer](docs/images/desktop-provider-model-controls.png)

## Conversation controls and context

Every open tab has a **×** close button; **Ctrl+W** closes the current tab. Closing
keeps sent messages in **History**, available on the tab bar even when the sidebar
is hidden. Unsent drafts require confirmation before being discarded.

A subtle **Context ~N%** control below the composer estimates occupied context.
Hover for token counts and capacity; unknown capacities show **Context —**.
Click to summarize. The original conversation remains in History, and the new
conversation uses the summary plus recent whole turns when they fit. Automatic
summarization runs before a turn at 80% of a known model window or 400 KiB of text.
Provider summaries have a 30-second deadline; unavailable providers use a local
condensed transcript. Compaction only succeeds when estimated context shrinks.

![Native tabs, close controls and conversation history](docs/images/desktop-context-tabs-history.png)

## Installed skills and sub-agent defaults

Open **Installed skills** to inspect **Project**, **Global**, and **Built-in**
entries, including source, revision, shadowed copies and auto-load state. Project
copies take precedence. Configure project/global auto-load from the selected
skill; named projects can inherit global defaults or keep their own list, including
an empty list. Changes apply on the next turn. An explicit `--skill` overrides
auto-load defaults.

The AI can use `skills_search`, `skills_activate`, and `read_skill_resource` to
find and load an available skill for the current task. This does not change your
saved auto-load settings or run scripts. **Discover skills** opens the catalog
for preview and installation; automatic remote installation is not enabled.

![Scoped installed skills and auto-load controls](docs/images/desktop-installed-skills.png)

Open **Sub-agents** to choose their default provider and model independently of
the main conversation. Load the provider's model catalog or type an ID. Empty
values inherit the parent provider or selected profile's model. Explicit spawn
choices override defaults; a different explicit provider uses its own model.
Settings apply to new turns and are also available in TUI **`:settings`** under
`subagents.provider` and `subagents.model`.

![Independent sub-agent provider and model defaults](docs/images/desktop-subagent-defaults.png)

## Command permissions

The composer's **Commands** picker offers **disabled**, **ask every time**,
**rules**, and **allow all**. These are persisted defaults. Ask mode prompts for
the exact command before execution. In the TUI, **Ctrl+Y** allows once and
**Ctrl+N / Escape** denies; cancellation invalidates pending approvals.

Rules mode allows exact argv matches from `execution.allow`, plus `pwd`, `ls`,
`ls -la`, and `rg --files`. Everything else asks. Shell scripts such as `sh -c …`
need their own exact rule or approval; rules do not interpret shell syntax.
Unmatched commands in headless/daemon runs fail closed. Existing configurations
keep their previous behavior unless a mode is selected.

This policy covers Bifrost's native agent exec and Cursor Native shell bridge,
including inherited child-task policy. Vendor CLI backends manage their own tools
and cannot use Bifrost approval modes. Manually entered terminal commands, MCP
servers, browser tools and configured hooks retain their own capability controls;
this is not an OS sandbox.

![Compact composer with command policy and context](docs/images/desktop-workspace-controls-narrow.png)

## File explorer

Open **Files** (Ctrl+P) for a compact, folders-first directory browser. Native vector
icons distinguish source, Markdown, Git, Docker, environment/configuration files,
images and archives, including names such as `go.mod` and `Dockerfile.gateway`.
The location header, root/parent buttons, full-path tooltips and selected-file
highlight keep your place. Filter locally, use Down to enter the results, arrow
keys to move, and Enter to open. The file list scrolls independently of navigation.

![Native file explorer with file-type icons](docs/images/desktop-file-explorer.png)

## Step-by-step explanations and reviews

Use `:explain how requests reach the database` in either UI, or click **Understand**
on the desktop welcome screen. This prepares an editable request; press Send when
ready. For a review, try `:explain the changes in this PR against main` and specify
the actual PR or comparison base.

With a native tool-capable provider, the agent inspects the source and presents a
walkthrough combining concept explanations, highlighted code and actual diffs.
Desktop offers Previous/Next and End walkthrough. In the TUI, focus the viewer
and press `p` / `n`; `x` switches between code and the full scrollable explanation.
The controls also accept mouse clicks and the mouse wheel scrolls the viewer.
Navigation stops at the first and last step. `:walkthrough next|prev|explanation|code|close`
provides the same actions through the shared command line. Viewing a step does
not edit, stage, approve or publish changes. Walkthroughs currently live in the
open workspace; they are not saved as reusable courses or PR review state.

![Native code explanation with progress and navigation](docs/images/desktop-explanation-code.png)

![Step-by-step review of an illustrative fixture diff](docs/images/desktop-explanation-review.png)

## Skills selected for the task

Four original skills are embedded in Bifrost: **code-explore**, **debug-trace**,
**change-review**, and **code-walkthrough**. They work without downloads and guide
bounded code exploration, diagnosis, review and explanation. Where useful, they
recommend inspecting the installed Ripwire CLI and using its structural tools.
Ripwire remains optional; file/symbol searches are the fallback. Its executable
and upstream skill collection are not bundled.

Native agents see skill descriptions and choose relevant instructions with
`skills_search` and `skills_activate`. `read_skill_resource` reads referenced text
only after selection or explicit activation. Bodies are no longer all injected
on every workspace turn. Selection does not install software, run scripts,
enable write/exec permissions, or change persistent project defaults. Project and
global installed skills take precedence over built-ins. `bifrost skill list` and
`bifrost skill show code-explore` inspect what ships; `--skill NAME` remains an
explicit activation option.

Discovery currently includes up to 256 available local/installed skills, including
built-ins, with a bounded metadata prompt and paginated search. Public marketplace
search federation remains separate work. Vendor-managed backends use their own
tools: they cannot call these Bifrost skill or viewer tools, and explanations fall
back to numbered chat steps. Tests use deterministic provider fixtures, not paid
live-model evaluation of skill selection.

## The terminal gets the same identity

The TUI welcome screen now renders a violet/cyan cell-art version of the bridge
logo. Small panes use a compact mark; `ui.icons ascii` uses `|>|`. Plain and light
themes are preserved, with no image protocol or patched font requirement.
These two terminal images are renders of actual ANSI fixtures at 160×48.

![Bifrost terminal logo and welcome](docs/images/tui-brand-160x48.png)

![Explanation and highlighted code in the terminal](docs/images/tui-explanation-160x48.png)

## Screenshot tour

These are **screenshots of the running native application**, captured by the
GTK/X11 test suite on 17 October 2026. Conversations, catalogs, accounts and tasks
use controlled test fixtures; repository, editor and terminal checks use disposable
local workspaces. No private account data or production inference is pictured.
Images link to the original capture files in this repository.

### Chat that keeps your messages visible

User prompts sit on the right, replies on the left. Consecutive tool and reasoning
events share an expandable Activity row; its full text remains accessible.

![Expanded activity within the native conversation](docs/images/desktop-chat-activity.png)

### One project, multiple repositories

The Project overview groups linked repositories, runtime choices and tools.
Select a repository to inspect its branch and change counts or open its workspace.

![Multi-repository Project overview on a wide desktop](docs/images/desktop-project-wide.png)

Common project settings have ordinary form controls. Advanced JSON exposes the
full shared schema without replacing the everyday workflow.

![Project configuration with provider, environment, skills and permission controls](docs/images/desktop-project-configuration.png)

### Skills and MCP have their own catalogs

Search, inspect the source, then install for this project or globally. Skill
installation and activation are separate. Public skill API federation remains
planned; the existing built-in/configured catalogs and MCP registry use the shared
backend.

![Skills catalog with source preview and installation scope](docs/images/desktop-marketplace.png)

![MCP catalog with package details and installation controls](docs/images/desktop-mcp.png)

### Follow work and investigate failures

Task filters help find running, completed or failed work. Results and activity
are separate from execution IDs, worktrees and budgets.

![Task monitor showing a failed fixture task and its error](docs/images/desktop-task-failure.png)

<details>
<summary><strong>Source editing, guided walkthroughs and a real terminal — 3 screenshots</strong></summary>

The source pane uses the shared editor engine for revision-checked saves,
undo/redo, search and optional Vim modes. Recovery drafts survive closing the app.

![Native source editor with Vim controls and recovered draft](docs/images/desktop-editor-vim.png)

Viewer tools can open a sequence of files and highlighted lines alongside chat.

![Guided source walkthrough with previous and next controls](docs/images/desktop-walkthrough.png)

The terminal is a real PTY. Shells remain alive when hidden; command output also
has bounded read-only tabs.

![Native terminal pane with an interactive shell](docs/images/desktop-terminal.png)

</details>

<details>
<summary><strong>Commands, accounts and settings — 3 screenshots</strong></summary>

The command palette searches actions, files and conversations. All canonical
TUI colon commands have desktop handlers, completion and shared help.

![Searchable native command palette](docs/images/desktop-commands.png)

Saved provider profiles can be connected or changed while preserving readable
conversation history. Other tabs show the current shared runtime account.

![Provider account management and model controls](docs/images/desktop-accounts.png)

Simple settings use compact inputs and booleans use toggles. Structured values
retain a larger editor. Ctrl+S validates and saves every edited value; revision
conflicts keep the unsaved changes.

![Reviewed native Settings screen in the light theme](docs/images/desktop-settings-reviewed.png)

</details>

<details>
<summary><strong>Compact windows and larger text — 2 screenshots</strong></summary>

Optional panes collapse on narrow windows. Navigation remains available from the
toolbar, while chat and the composer keep their own space.

![Organized conversation at 800 by 720](docs/images/desktop-chat-organized-narrow.png)

Text scaling is exercised independently of window size.

![Native conversation with text enlarged to 150 percent](docs/images/desktop-chat-organized-large-text.png)

</details>

More captures, design references and reproducible verification commands are in
the [desktop guide](docs/desktop.md#verification).

## Terminal quick start

```sh
go build -o bin/bifrost ./cmd/bifrost
./bin/bifrost --root /path/to/repo
```

Go 1.26 or later is required. The first interactive launch opens onboarding: choose a provider or offline mode, enter your model ID and credential **environment-variable name**, register a project, select permissions, and optionally enable browser tools, MCP, and skills. Nothing is downloaded and no model call occurs during setup. Run `bifrost setup` explicitly to configure before launching; replacement requires `setup --force`.

For scripts:

```sh
bifrost --root /path/to/repo setup --non-interactive \
  --name myapp --provider responses --model YOUR_MODEL \
  --api-key-env OPENAI_API_KEY
bifrost --root /path/to/repo tui
```

Configuration lives at `$XDG_CONFIG_HOME/bifrost/config.json`; state lives at `$XDG_STATE_HOME/bifrost`. `--config` overrides the settings path. An explicit `--state-dir` also isolates settings under that directory unless `--config` is supplied. Keep state outside the workspace. API keys are resolved from environment variables, never accepted as literal CLI flags or stored in settings.

## Terminal workspace

The TUI remains a standalone native Go interface. Its shortcuts follow terminal
conventions; use the [desktop shortcuts](#desktop-quick-start) in the GUI.

| Key | Action |
|---|---|
| `Ctrl-w`, then `h/k` or `j/l` | Previous / next visible pane (currently cyclic) |
| `j/k`, `Enter` | Navigate files or scroll/open a file |
| `c`, `Enter` | Compose / send a chat prompt |
| `Shift-Enter` | Insert a prompt newline (`Alt-Enter` / `Ctrl-j` fallback) |
| `Ctrl-c` | Cancel active chat |
| `Tab` / `Shift-Tab` in chat | Select next / previous fold |
| `Enter` / `Space` in chat navigation | Expand / collapse selected fold |
| Click disclosure row / mouse wheel | Toggle details / scroll chat |
| `E` | Edit the selected file inside Bifrost |
| `e` | Use the configured external editor |
| `r` | Load Git review |
| `:` / `Ctrl-p` | Open the command palette |
| `Tab` / `Shift-Tab` in palette | Complete commands and arguments |
| `:help COMMAND` / `COMMAND --help` | Show command usage and examples |
| `:settings` | Search and edit all configuration options |
| Drag pane dividers | Resize columns or terminal height; release saves |
| `:tasks` | Show the live task monitor |
| `?` | Keyboard help |
| `t`, then `Ctrl-]` | Enter terminal input / return to navigation |
| `q` | Quit in navigation mode |

The inline editor provides normal/insert modes, `hjkl`, word/line movement, search, undo/redo, `Ctrl-s` save, and draft recovery. Saves use the same revision checks as agent edits. Themes are `midnight`, `light`, and `plain`; configure them with `config set ui.theme light`. This is a small Vim-like editor, not a Vim implementation.

Development handoff: [AGENTS.md](AGENTS.md) maps the implementation, constraints,
and validation workflow. The [navigation design brief](docs/navigation.md)
separates today's bindings from the proposed navigation improvements.
The [public skill API brief](docs/marketplace-sources.md) records the pending
marketplace source integrations.

The midnight theme uses charcoal surfaces, violet headings, cyan focus indicators, syntax colors, and a segmented connection bar showing provider, account, model, and workspace as space permits. Provider marks default to portable Unicode. Set `bifrost config set ui.icons nerd` for verified OpenAI/Claude logos with a compatible Nerd Font, or `ui.icons ascii` for text labels. See [provider icon compatibility](docs/provider-icons.md). Four layouts can be changed while working:

| Command | Arrangement |
|---|---|
| `:layout studio` | Files, editor/review, chat, and terminal |
| `:layout conversation` | Large central chat, files left, inspector right |
| `:layout review` | Large editor/review with smaller files and chat panes |
| `:layout focus` | Chat and composer only |

Use `:resize chat 60`, `:resize viewer 50`, or `:resize terminal 20` to adjust percentages, and `:terminal on` / `:terminal off` to toggle the terminal pane. Layout, proportions, and terminal visibility persist. Hiding a pane retains its state. `bifrost config set ui.layout conversation` sets a preset outside the TUI.

You can also drag the vertical pane dividers or the horizontal border above the terminal. Sizes preview while dragging and save on release; `Esc` cancels. Colon commands offer completion and keep invalid commands open with usage help so you can correct them.

The message box expands for wrapped text and explicit newlines, up to eight rows by default, then follows the cursor. Configure `ui.composer_max_rows` (1–20, or 0 for the default) and `ui.newline_key` (`shift+enter`, `alt+enter`, or `ctrl+j`). Shift+Enter requires a terminal that reports modified Enter; Alt+Enter and Ctrl+J remain available as fallbacks.

Open `:settings` (or `:config`) to search every configuration field, edit strings/numbers/booleans or structured JSON, and save with `Ctrl-S`. `?` shows complete help for the selected field. Appearance changes apply immediately after save; provider and runtime changes require restarting Bifrost. Settings use the existing JSON file, with validation, atomic writes, and conflict detection. Find it with `bifrost config path`; see [configuration reference](docs/configuration.md) and [example configuration](examples/config.json).

Chat renders headings, emphasis, nested/task lists, quotes, links, tables, and language-labeled code blocks. Go code and diffs are colored, emoji sequences wrap by displayed width, and long replies use a centered reading column. The composer shows multiple lines and keeps the insertion point visible. Completed messages are cached; source history remains unchanged. Tool calls, thinking summaries, and activity start collapsed; assistant messages can also be folded. Click a disclosure row or use Tab then Enter/Space. Tool events update the same section as work progresses. Thinking content is shown only when explicitly supplied by the provider, and may become available after that model step completes. Hold Shift for native terminal text selection in terminals that support that convention.

For a denser workspace, use a larger terminal grid, such as 160 columns by 48 rows; decrease the terminal application's font size if needed. Bifrost adapts to available cells and does not change your terminal font. Pixel graphics and inline VM/browser image previews are not implemented. See [design references and rendering guidance](docs/tui-design.md).

## Providers and configuration

```sh
bifrost config show
bifrost config set providers.fast '{"kind":"chat-completions","base_url":"http://localhost:11434/v1","model":"YOUR_LOCAL_MODEL","api_key_env":"LOCAL_MODEL_KEY"}'
bifrost config set active_provider fast
bifrost config set defaults.max_agents 3
bifrost config set projects.myapp '{"provider":"fast","skills":["coding"]}'
bifrost config import reviewed-settings.json          # preview
bifrost config import reviewed-settings.json --apply
bifrost run --profile fast 'Explain this repository'
bifrost run --allow-write --allow-exec 'Fix the failing test and run it'
```

Provider kinds are `responses`, `anthropic`, `gemini`, `chat-completions`, `chatgpt`, `cursor`, `claude`, and `openrouter`. Native providers have their usual service URL defaults. Model IDs are supplied explicitly. CLI overrides take precedence over environment variables and saved provider values. `BIFROST_MODEL`, `BIFROST_BASE_URL`, `BIFROST_PROVIDER`, and `BIFROST_API_KEY` remain available; compatible OpenAI names are supported too. Named accounts ignore generic provider environment overrides so another account's credentials or endpoint cannot replace their binding. Only loopback endpoints may use plain HTTP.

File changes and host commands are disabled until enabled in setup/config or with flags. Use `--allow-write=false` / `--allow-exec=false` to override saved permissions. Commands run with your account's permissions; this is not an OS sandbox. MCP/browser/desktop activation grants their own capabilities independently.

## Typed decision routing

The standard-library `internal/decision` client can route a bounded state to one named option with TypeSafe Jev or OpenRouter's Decisions API. It sends the same typed `choice` question to either service and validates that the returned choice is allowlisted, confidence is finite and in `0..1`, probabilities cover exactly the requested options, and their sum is one.

```go
client, err := decision.New(decision.Config{
    Kind:   decision.KindTypeSafe, // or decision.KindOpenRouter
    APIKey: os.Getenv("TYPESAFE_API_KEY"),
})
result, err := client.Choose(ctx, state, map[string]string{
    "native":   "Use the native provider",
    "external": "Use an installed external agent",
})
```

Defaults are `https://api.typesafe.ai/v1/systemone` with model `jev-latest` for TypeSafe, and `https://openrouter.ai/api/alpha/decisions` with model `~typesafe/jev-latest` for OpenRouter. Set `BaseURL` to a complete endpoint when using a fixture or a compatible deployment. State is limited to 8 KiB; public endpoints require HTTPS, while loopback HTTP is allowed for local fixtures. See the [TypeSafe API reference](https://docs.typesafe.ai/api) and [OpenRouter Decisions API](https://openrouter.ai/openapi.json) for the service contracts.

The CLI enables routing independently of the selected generation provider. The candidate keys must already be configured provider profile names; this example uses the `fast` and `careful` profiles above:

```sh
export OPENROUTER_API_KEY='…'
bifrost config set decision '{"enabled":true,"kind":"openrouter","model":"~typesafe/jev-latest","api_key_env":"OPENROUTER_API_KEY","min_confidence":0.8,"candidates":{"fast":"short routine tasks","careful":"complex or risky changes"}}'
```

Use `"kind":"typesafe"`, `"model":"jev-latest"`, and `"api_key_env":"TYPESAFE_API_KEY"` for TypeSafe. A decision is made once per parent turn; its reported input/output tokens are included in the turn usage. Unavailable or low-confidence decisions keep the selected profile. Reported token usage depends on the service; Bifrost does not calculate dollar charges, guarantee savings, or perform automatic quota rotation. Routing can select native or external profiles where their capabilities fit the turn, and a mixed child may still select an explicit native or Cursor/Claude profile under the parent's workspace and permission limits.

## Subscription and named accounts

Bifrost can keep several named identities in its local account store. Log in with one of the supported backends:

```sh
bifrost account login KIND NAME \
  [--model MODEL] [--api-key-env ENV] [--executable PATH]
# KIND is one of: chatgpt, cursor, cursor-native, claude, openrouter

bifrost account list
bifrost account use PROFILE
bifrost account status PROFILE
bifrost account logout PROFILE
bifrost account models PROFILE
```

`NAME` is the local alias you choose, such as `work` or `personal`. Login creates a stable provider profile ID in the form `kind-NAME` (for example, `chatgpt-work`); use that profile ID with `--profile`, `account use`, `account status`, `account logout`, and `account models`. `account use` makes the profile active for the current project/defaults. `account list` prints the saved account metadata, while secrets remain in the local account store. `account models` discovers the ChatGPT catalog and, for `cursor-native`, Cursor's usable models. The `cursor` and Claude accounts use their vendor model catalogs.

ChatGPT uses native browser OAuth and local token refresh, with model discovery available through `account models PROFILE`. Its native Responses route can receive Bifrost's local tools when those tools are enabled for the run. The account flow does not ask for an API key. `cursor-native` is a separate Cursor account that signs in through Cursor's browser challenge (or exchanges `CURSOR_API_KEY`) and talks to Cursor's agent service directly. It runs file, search, and shell requests inside the Bifrost workspace when those permissions are on, and it declines computer use, MCP, sub-agents, fetch, and screen recording. The `cursor` kind still uses the installed `cursor-agent` CLI; pass `--executable PATH` when that client is elsewhere. Claude uses the installed `claude` CLI. Those CLI backends own login, status, model selection, and tool behavior. They do not add native Bifrost `--ssh`, `--browser`, `--desktop`, or `--mcp` support to an external-client turn.

OpenRouter uses an API key from an environment variable and does not perform OAuth. The default variable is `OPENROUTER_API_KEY`; override it with `--api-key-env ENV`. OpenRouter usage is billed separately under that key. Keep the variable in the environment rather than putting the key in shell history or settings.

External account turns are serialized per account. A login, logout, or identity replacement cannot race an active turn. Type `:accounts` to open the account selector. Selecting an account rebinds the current conversation to that profile while retaining its conversation ID and readable history; provider-owned continuation data and executable tool calls are converted to portable transcript entries before the switch. The selector refuses to switch while a chat is running. A replaced external login cannot resume stale provider-owned continuation state.

Cursor account isolation is supported on Linux. Isolated Cursor accounts are explicitly unavailable on macOS because the vendor client uses a shared keychain namespace.

Useful vendor documentation: [ChatGPT sign-in for open-source clients](https://developers.openai.com/siwc/token-sharing-open-source), [Cursor ACP](https://cursor.com/docs/cli/acp), [Claude Code](https://code.claude.com/docs/en/headless), and [OpenRouter](https://openrouter.ai/docs).

The repository has fixture coverage for account storage, OAuth, and backend wiring. No live paid login or paid inference is exercised by the test suite; complete the vendor login in your own environment before using an account.

## Sub-agents and monitoring

Agents can use `spawn_agent`, `task_status`, `task_wait`, and `task_cancel`. Default configuration allows three child workers; `--agents N` adjusts the limit and `--agents 0` disables delegation. A child can select a saved provider profile or a model override, with credentials resolved locally. Children receive their task prompt and project instructions, not the entire parent conversation.

Readonly children have no write/exec tools. Writing children require the parent's write permission and run in preserved Git worktrees. Worktrees start from a Git ref (HEAD by default); uncommitted parent changes are not copied. They are retained for review and are never automatically merged. A repository needs a commit before a writing worktree can be created. Browser, desktop, MCP, and hook sessions are not shared between children.

Children without a profile override inherit the routed parent provider and model. A child with an explicit saved profile can use a different native provider or an external Cursor/Claude backend; the child still receives the parent's workspace boundary, permissions, and budgets. External children use their vendor client's tools and protocol; native children use Bifrost's tool loop.

Concurrency, queue length, nesting depth, child count, timeout, steps, output, and event retention are bounded. Waiting parents yield worker capacity. Cancellation propagates down the task tree; crashed tasks are marked interrupted on recovery and never replayed automatically.

```sh
bifrost tasks list
bifrost tasks tree TASK_ID
bifrost tasks status TASK_ID
bifrost tasks events
bifrost tasks events --run TURN_ID CURSOR LIMIT
bifrost tasks wait TASK_ID
bifrost tasks cancel TASK_ID
```

The `:tasks` view refreshes every second and shows parent/child IDs, status, provider/model, worktree, elapsed time, steps, tools, tokens, errors, and recent events. `:tasks cancel TASK_ID` also works across local clients. Task snapshots and journals are stored under the selected state directory.

Without `--run`, events show the latest bounded window across runs. Stable replay cursors require `--run TURN_ID`; use the run directory ID under `<state>/tasks` and start with cursor `0`.

## Daemon and sessions

```sh
bifrost daemon start
bifrost run --daemon --profile fast 'Inspect the repository'  # returns job ID
bifrost daemon jobs
bifrost daemon attach JOB_ID
bifrost daemon cancel JOB_ID
bifrost tui --daemon
bifrost daemon stop

bifrost session list
bifrost run --session SESSION_ID 'Continue'
bifrost session export SESSION_ID > conversation.json
bifrost --root /original/workspace session import conversation.json
bifrost session compact SESSION_ID reviewed-summary.txt 2
```

The local daemon uses a private authenticated Unix socket, bounded concurrent jobs, durable job events and replay cursors. Daemon chat retains one session across turns; explicit chat cancellation cancels its job. Shutdown cancels active work. Sessions preserve complete tool chains, visual observations, native opaque continuation data, and interruption status. Imports validate the snapshot and workspace binding and create a new ID. Compaction creates a new session from a reviewed summary plus complete recent turns; the original remains intact. Interrupted tool actions are never automatically replayed.

## Projects, GitHub, and editing

```sh
bifrost project add myapp --root /path/to/api --alias api
bifrost project add-repo myapp --root /path/to/web --alias web
bifrost --project myapp --repository api tui
bifrost --project myapp --repository api github link OWNER/REPO
bifrost github auth-status
bifrost github pr list OWNER/REPO
bifrost github pr create --title 'Change title' --head feature --base main OWNER/REPO

bifrost git status
bifrost git diff
bifrost git stage -- path/to/file
bifrost git unstage -- path/to/file
bifrost git commit --message 'Reviewed change'
bifrost review
bifrost search 'TODO'
bifrost file read README.md --json
bifrost file patch plan.json
bifrost file undo-patch PATCH_ID
```

GitHub uses `GH_TOKEN`, `GITHUB_TOKEN`, or explicitly available `gh` authentication. PR creation creates a draft. Linking updates project metadata; it does not push or overwrite Git remotes. No operation pushes or merges automatically.

Native coding tools include rooted reads/listing/search, exact edits, writes, recoverable multi-file JSON patches, Git review, and bounded argv commands. Existing-file mutations require content revisions. Patch operations are `create`, `replace`, `delete`, and `move`; all are prevalidated and recorded with before-images. Recovery and undo preserve conflicting manual edits. Individual writes are atomic; this is not filesystem-wide atomicity against unrelated external writers. `.git` and `.bifrost` metadata are protected.

```json
{"operations":[
  {"kind":"replace","path":"main.go","expected_revision":"SHA256_FROM_READ","content":"replacement text"},
  {"kind":"create","path":"new.txt","content":"hello\n"}
]}
```

## Skills, MCP, hooks, and language servers

```sh
bifrost --project myapp skill install /path/to/skill
bifrost --project myapp skill install-git https://github.com/owner/repo.git main skills/example
bifrost --project myapp tui --skill example

bifrost mcp config add --mcp-config /path/to/mcp.json --name local -- /path/to/server
bifrost mcp config list --mcp-config /path/to/mcp.json
bifrost tui --mcp-config /path/to/mcp.json
bifrost mcp resources list --url https://server.example/mcp
bifrost mcp prompts list --url https://server.example/mcp
bifrost --root /path/to/repo --state-dir /path/to/state mcp serve --allow-write

bifrost --root /path/to/repo lsp check -- gopls
bifrost --root /path/to/repo lsp hover main.go 0 0 -- gopls
bifrost --root /path/to/repo lsp definition main.go 0 0 -- gopls
```

MCP supports stdio and Streamable HTTP, environment-referenced auth headers, tools, resources/templates, prompts, subscription APIs, and explicit elicitation callbacks. `--header Authorization=TOKEN_ENV` binds a header to an environment variable; the variable holds the complete header value. The app settings file and MCP endpoint file are separate. OAuth login UI and elicitation forms are not yet integrated into the TUI; pending input is surfaced without blindly repeating a tool.

Skills can be project scoped or global, with snapshots and Git provenance. Project skills override global skills of the same name. Hooks are explicit machine-local argv commands for before/after-turn and before/after-tool events, with time/output limits; activating hooks requires command permission. Set the `hooks` settings array using `config set` or a reviewed config import. Language servers are started only from explicit argv commands. LSP positions are zero-based UTF-16 coordinates.

## Skill marketplace

Type `:marketplace` to browse and search the catalog, inspect source metadata, and install with `i` for the project or `g` globally. Installation does not activate a skill or run its instructions.

The marketplace opens as a responsive grid of cards, each with a name and short
description. Click the search field or press `/`; results filter locally as you
type or paste, across names, descriptions, IDs, and sources. All search words must
match. `Enter` leaves search mode. Arrow keys or `hjkl` move across the grid;
the mouse wheel and PgUp/PgDn move through larger catalogs.

Click a card or press `Enter` to open a centered detail modal. The background grid
stays visible. Read the description and source metadata, then click **Install to
project** / **Install globally**, or use `i` / `g`. Arrow keys, the mouse wheel,
and PgUp/PgDn scroll the details. `Esc` (or the modal's back link) returns to the
same search and selection; `r` refreshes the catalog from the grid.

Install directly from the colon command palette:

```text
:skill browse
:skill install openai-skill-creator
:skill install openai-cli-creator --scope global
:help skill
```

Project scope is the default. `Tab` completes IDs from the loaded catalog and
scope options. Invalid commands show usage help. The direct install command
resolves the catalog entry before installing, and shows progress and results
in the modal. Installation does not activate the skill.

```sh
bifrost skill market list
bifrost skill market show openai-cli-creator
bifrost skill market install openai-cli-creator --scope project
bifrost skill market sources
bifrost config set marketplace.catalog_url https://example.com/skills.json
```

The built-in starter catalog contains three official OpenAI skills pinned to an immutable Git revision. A custom catalog has the shape `{"entries":[{"id":"team-coding","name":"Team coding","description":"Team conventions","repo":"https://github.com/owner/repo.git","ref":"FULL_COMMIT_SHA","path":"skills/coding"}]}`. Use an HTTPS catalog URL; the CLI also accepts `--catalog URL` per marketplace command. Explicit activation uses `--skill NAME` or the project's configured skills; native workspace agents can also select available installed/bundled skills on demand through the skill tools above.

## Browser, desktop, and VM guests

```sh
bifrost tui --browser
bifrost desktop capabilities
bifrost tui --desktop
bifrost vm qemu status /path/to/qmp.sock
bifrost vm virtualbox list

bifrost config set ssh.dev '{"target":"user@vm","root":"/home/user/repo","state_dir":"/home/user/.local/state/bifrost-worker","executable":"/usr/local/bin/bifrost"}'
bifrost remote dev inspect
bifrost remote dev exec -- go test ./...
bifrost tui --ssh dev --allow-write --allow-exec --browser
```

Install the same Bifrost binary in the guest and use existing SSH authentication/host-key verification. File tools, review, inline saves, commands, the terminal, and enabled browser/desktop tools target the guest. Worker capabilities are explicit; an unavailable guest feature never falls back to host control. The guest needs Chromium for browser automation and its display tools for desktop control. Remote sub-agents and daemon-backed SSH chat currently report unsupported rather than running against the host.

Browser automation uses isolated Chromium, accessibility references, and bounded model-visible screenshots. Linux desktop capture/input uses detected X11 or Wayland tools, an interaction lease, and fresh observation IDs. See [desktop compatibility](docs/desktop-compatibility.md). Hypervisor control and guest readiness are separate: see [VM setup](docs/vm-setup.md) for lifecycle, managed launch, and snapshot commands.

## Validation and remaining scope

See [current-build.md](docs/current-build.md) for reproducible tests and the precise supported matrix. Native providers, GitHub and desktop adapters have fixture coverage; real paid accounts and a real graphical guest were not exercised. Linux is the validated runtime. The larger roadmap still includes integrated approval/MCP OAuth forms, full LSP/editor diagnostics UI, advanced hunk review, automatic provider retry/compaction policies, guest sub-agent isolation, and broader OS qualification.

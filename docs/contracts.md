# Bifrost contracts and configuration

These are target contracts, not a current implementation inventory. The initial
prototype used smaller in-process interfaces as recorded in
[initial-build.md](initial-build.md). Daemon and worker integrations have since
landed; consult [current-build.md](current-build.md) and source/tests for their
actual framing, capabilities, and remaining gaps. The full target envelopes and
phase-wide guarantees below must not be assumed implemented.

This specification accompanies the [development plan](development-plan.md) and [implementation backlog](implementation-backlog.md). Names below are proposed Bifrost contracts, not claims about existing code. Implement them with Go structs and explicit validation. Interface sketches define responsibilities; request/result types are elaborated in the adjacent sections.

## 1. Stable identifiers and entity relationships

Use opaque random IDs generated with `crypto/rand`, prefixed by kind: `prj_`, `repo_`, `task_`, `ses_`, `turn_`, `call_`, `op_`, `env_`, `proc_`, `artifact_`, `change_`, and `approval_`. Display names and filesystem paths are not stable IDs. Validate IDs before using them in filenames. Timestamps are UTC RFC3339Nano; local time is presentation only.

Entities and required data:

- Project: ID, name, manifest location, repository IDs, environment bindings, skill bindings, MCP bindings, model defaults, policy reference, creation/update revisions.
- Repository: ID, project ID, alias, remote URL, optional GitHub host/owner/name, default branch, and named environment-local checkout bindings.
- Task: ID, project ID, title, objective, environment ID, repository worktree records, session IDs, status, policy revision, active mutation owner, process IDs, change IDs, and PR links.
- Worktree: repository ID, environment ID, canonical root, branch, base commit, mode (`isolated` or `existing`), initial dirty/index snapshot reference, and lifecycle state.
- Session: ID, task ID, optional parent session and fork event, model reference, ordered turn IDs, compaction references, and history revision.
- Turn: ID, session ID, input message, context manifest, provider/model identity, state, tool calls, usage, budget, start/end time, and termination reason.
- ToolCall: ID, turn ID, stable tool ID, tool schema revision, canonical arguments, environment/repository, approval reference, operation ID, state, result/artifact references, and timestamps.
- Process: ID, task ID, worker boot ID, command/PTY specification, opaque worker handle, state, exit code/signal, output offsets, terminal dimensions, and interactive controller lease.
- ChangeSet: ID, operation ID, task/turn/tool origin, file operations, before/after revisions, before-image artifacts, journal status, and formatter relationships.
- Artifact: ID, content hash, byte size, MIME type, provenance, local/remote storage reference, retention class, and redaction/export flags.
- Environment: ID, kind, host/guest identity, transport, worker boot/version, capability set, root bindings, connection state, and credential references.

One task can reference many repositories; every file reference is qualified by environment and repository. An application event always includes sufficient IDs to route it without inferring the currently focused pane.

## 2. Concurrency and lifecycle invariants

1. One active turn per session. A session's next turn waits until tool results or cancellation have resolved the prior turn's state.
2. One mutation owner per task worktree set initially. Read-only sessions can observe; writing child tasks require their own worktrees or a coordinated ownership transfer.
3. Single durable writer per daemon store and worker journal. Other goroutines submit operations to owners rather than mutate shared maps opportunistically.
4. One interactive controller per terminal or desktop. Controller expiry/disconnect releases control, not the underlying process.
5. Approval and authorization are evaluated against canonical inputs and their target. Reusing a call ID does not grant permission for different arguments.
6. A worker reports its boot ID. Process handles and connection-local state from an earlier boot cannot be reused.
7. A reconnect never silently repeats a potentially mutating operation.
8. Closing a pane is a presentation operation; stopping a process/task is an explicit command.

Turn states and allowed transitions:

```text
queued -> building_context -> streaming
streaming -> waiting_tools -> streaming
streaming/waiting_tools -> waiting_input -> prior resumable state
streaming/waiting_tools -> waiting_approval -> prior resumable state
streaming -> completed
any active state -> cancelling -> cancelled
any active state -> failed
active state + lost execution certainty -> interrupted
```

Resuming an interrupted turn first reconciles known operations. A continuation can create a new turn linked to the interrupted one; never fabricate the missing final tool result.

Task states: creating, ready, active, waiting, interrupted, completed, archived, failed. Archive hides the task but does not discard artifacts or delete worktrees. Deletion is a separate operation with explicit retained-data choices.

## 3. Domain interfaces

Use consumer-owned interfaces and concrete structs. Keep framework and SDK types inside their adapters. Do not create one environment interface with dozens of mandatory methods.

```go
// Signatures are architectural contracts; domain request/result types live
// with the consuming package and contain the fields specified below.
type Provider interface {
    Capabilities(context.Context, ModelRef) (ModelCapabilities, error)
    Stream(context.Context, GenerateRequest, func(ModelEvent) error) error
}

type Tool interface {
    Definition() ToolDefinition
    Execute(context.Context, ToolContext, json.RawMessage) (ToolResult, error)
}

type FileService interface {
    Read(context.Context, ReadRequest) (ReadResult, error)
    List(context.Context, ListRequest) (ListResult, error)
    Search(context.Context, SearchRequest) (SearchResult, error)
    Apply(context.Context, ChangeRequest) (ChangeResult, error)
}

type ProcessService interface {
    Start(context.Context, ProcessSpec) (ProcessHandle, error)
    Status(context.Context, ProcessHandle) (ProcessState, error)
    ReadOutput(context.Context, OutputRequest) (OutputChunk, error)
    WriteInput(context.Context, InputRequest) error
    Resize(context.Context, ResizeRequest) error
    Stop(context.Context, StopRequest) (ProcessState, error)
}

type VMManager interface {
    List(context.Context) ([]VMInfo, error)
    Inspect(context.Context, VMRef) (VMInfo, error)
    Act(context.Context, VMAction) (VMActionResult, error)
}

type BrowserService interface {
    Observe(context.Context, BrowserObserveRequest) (BrowserObservation, error)
    Act(context.Context, BrowserAction) (BrowserActionResult, error)
}

type DesktopService interface {
    Capture(context.Context, CaptureRequest) (DisplayObservation, error)
    Act(context.Context, DesktopAction) (DesktopActionResult, error)
}

type CredentialStore interface {
    Get(context.Context, CredentialRef) (Credential, error)
    Put(context.Context, CredentialRef, Credential) error
    Delete(context.Context, CredentialRef) error
}
```

`ToolContext` contains immutable task/project/environment/repository identity, dispatcher-authorized scope, operation ID, artifact writer, and event sink. Tool implementations receive scoped services, not arbitrary access to global application state.

Provider `GenerateRequest` contains the resolved model, ordered messages/content parts, selected tool definitions, provider options, output budget, and opaque adapter continuation. Model events are a tagged union: text delta, tool start/argument delta/end, usage, provider metadata, finish, and failure. A completed tool call has a validated name and complete argument object before it is executable.

The callback may return an error for cancellation/backpressure. Provider adapters must stop reading and close response bodies when cancelled. Normalized usage includes a `source` field (`reported`, `estimated`, or `unavailable`).

For background processes, `ProcessService.Start` uses its request context only for launch/acknowledgment. A successful launch transfers lifecycle ownership to the task/worker process manager. Closing an RPC connection or cancelling `process.wait` must not accidentally terminate the child. Stopping requires `process.stop`, task shutdown policy, or worker shutdown.

## 4. Local client protocol

Use versioned JSON-RPC 2.0 messages over a permission-restricted Unix socket on Unix. Windows uses an authenticated platform transport selected in its milestone, preferably a named pipe with appropriate ACLs. Remote worker stdio is tunneled through SSH; public TCP listeners are not a default.

The daemon starts with a lock, atomic socket publication, and a startup handshake. Clients never delete another live daemon's socket based only on a failed first connection. Validate ownership and use a bounded startup race protocol. On Windows, loopback fallback requires a random per-user secret and explicit lifecycle handling.

Handshake request:

```json
{
  "jsonrpc": "2.0",
  "id": "request-1",
  "method": "client.hello",
  "params": {
    "protocol_major": 1,
    "protocol_minor": 0,
    "client_id": "client-local-1",
    "capabilities": ["events", "approval_ui", "terminal_control"]
  }
}
```

Reply includes server instance/boot ID, negotiated version/capabilities, and event cursor. Major incompatibility is a clear error; unknown optional fields in compatible minor versions are ignored. Bound message sizes; large data is retrieved through artifact/output operations. JSON framing must handle partial reads, embedded escaped newlines, and malformed/oversized records.

Core methods:

- `project.list/get/create/update`, `repository.add/remove`, `environment.inspect`.
- `task.create/get/list/archive`, `session.create/fork/get`, `turn.start/steer/cancel`.
- `events.subscribe` with cursor, task filter, and state snapshot fallback.
- `approval.respond`, `input.respond`, `tool.inspect`, `artifact.read`.
- `file.read/list`, `change.apply/undo`, `git.status/diff/stage/unstage`.
- `process.start/status/read/write/resize/stop`, `terminal.claim/release`.
- `skill.list/install/update/remove`, `mcp.list/add/connect/disconnect/authenticate`.

Each mutation takes an operation ID and relevant expected configuration/entity revision. Retransmission with the same ID and canonical request returns the stored result or pending status; same ID with different arguments is a conflict. This is application deduplication, not a promise of exactly-once execution against external services.

Event envelope:

```json
{
  "schema_version": 1,
  "sequence": 143,
  "event_id": "event-143",
  "time": "2026-10-15T12:00:00Z",
  "type": "change.applied",
  "project_id": "prj_example",
  "task_id": "task_example",
  "session_id": "ses_example",
  "turn_id": "turn_example",
  "operation_id": "op_example",
  "payload": {"change_id": "change_example", "files_changed": 2}
}
```

Sequences are monotonic per daemon store; worker sequence/boot identity is recorded separately in worker-origin payloads. Clients deduplicate by event identity and request snapshots if a cursor has expired. Required event families include turn/message/tool/approval/input/process/change/git/diagnostics/browser/desktop/MCP/environment updates.

Durable lifecycle events cannot be dropped. High-volume stream deltas can be coalesced for display; durable message chunks and process artifacts remain recoverable. A slow client receives a resync marker instead of unbounded buffering. Never place screenshot bytes or entire command logs in every state update.

## 5. Worker protocol and capabilities

Workers advertise supported protocol versions, binary build information, boot ID, platform/architecture, allowed roots, and capabilities such as `files.read`, `files.mutate`, `process.exec`, `process.pty`, `git`, `browser.cdp`, and `desktop.input`.

Worker launch authenticates through SSH and a scoped initialization request. The worker cannot widen the root list from later untrusted tool arguments. Stdio is protocol-only; diagnostics go to stderr. Configuration fixes whether stdio MCP processes launch on the host or this worker.

Host-to-worker operations carry operation ID, task identity, scope/policy revision, method, payload, and deadline. Worker state queries reconcile in-flight operations. A worker journals mutation intent before changing files or starting an owned process, then records completion when known. Gaps between an external side effect and its receipt are represented as unknown; idempotency cannot eliminate all such gaps.

Files stream in bounded chunks. Artifact downloads are hash-verified. Transfers have cancellation and quotas. The protocol does not expose a generic unauthenticated file server. Path meaning always belongs to the worker platform.

## 6. Native tool catalog

All tools return a common envelope with call/operation IDs, status, summary, structured data, diagnostics, artifacts, and optional continuation cursor. Status is one of success, failed, cancelled, approval-required, input-required, partial, or outcome-unknown. Invalid arguments are distinguished from operation failure. Provider-facing schemas are generated from reviewed definitions and tested against their actual parser.

File tools:

- `file.read`: repository, relative path, optional expected revision, start line/count or byte window. Returns revision, encoding, total size, selected content, and truncation metadata.
- `file.list`: repository, directory, depth/page limit, ignore mode. Returns qualified entries, types, sizes when cheap, and cursor.
- `file.find`: repository set, filename pattern, engine, limit. Define glob semantics including recursive `**`; do not use `filepath.Match` as if it implements that syntax.
- `file.search`: repository set, query, literal/regex mode, path filters, context lines, result limit. Returns file revisions and match ranges.
- `file.edit`: path, expected revision, old text, new text, replacement count/all flag. Reject zero or unexpected multiple matches.
- `file.patch`: repository scope, patch text, expected revision map, new-file preconditions. Supports add/update/delete/move through the journal.
- `file.write`: expected revision or `must_not_exist`, content, optional creation mode. No accidental unguarded overwrite.
- `file.move` / `file.delete`: expected source revision and destination preconditions; use the same change service as patches.
- `change.inspect` / `change.undo`: change ID and expected current revisions. Undo conflicts are data, not a reason to force-reset.

Execution tools:

- `process.exec`: environment, repository/root, argv or explicit shell/script, cwd, allowed environment references, deadline, output budget, foreground/background choice.
- `process.status`: process ID, no implicit wait.
- `process.output`: process ID, stream and byte cursor, bounded length; distinguish merged PTY stream.
- `process.input`: process ID, input bytes/text, controller authorization.
- `process.wait`: process ID, bounded deadline; cancellation stops waiting unless stop was separately requested.
- `process.stop`: process ID, graceful timeout and escalation behavior.

Git tools:

- `git.status`, `git.diff`, `git.log`, and `git.show` return structured repository-qualified results.
- `git.stage` / `git.unstage` use file/hunk IDs tied to current index/worktree revisions.
- `git.commit` uses explicit repository and selected staged state; commit hooks can run and their output is visible.
- `github.pr.prepare` builds a reviewable title/body/branch target; `github.pr.publish` and other remote mutations follow explicit policy.

Code intelligence tools:

- `code.diagnostics`, `code.definition`, `code.references`, and `code.symbols` use a configured language server with document version and root identity.
- `check.run` executes a named project check definition and returns exit status plus parsed diagnostics where supported.
- Formatters are configured commands whose edit results are recorded as a separate change transaction.

Other tools:

- `skill.list` / `skill.load`, `user.question`, `task.plan` and bounded `task.delegate`.
- `web.fetch` for bounded HTTP retrieval; optional search-provider adapters for `web.search` rather than assuming a free search backend exists.
- `browser.observe`, `browser.navigate`, `browser.act`, `browser.tabs`, `browser.capture`, and `browser.downloads`.
- `desktop.capture` / `desktop.act`, `vm.inspect` / `vm.act`.
- MCP discovery/activation/resource/prompt bridge plus namespaced external tool calls.

`task.delegate` is an in-product capability implemented after single-task correctness. It creates a child session with objective, context references, tool/model budget, and explicit read/write scope. Writes require isolated child worktrees. Parent cancellation propagates, and results identify changes and remaining errors; no recursive unbounded spawning.

## 7. Revisions, changes, and recovery

`FileRef` includes environment ID, repository ID, and normalized relative path. `FileRevision` includes content digest and relevant metadata; absence is an explicit state. Line ranges are one-based for display; internal byte offsets are zero-based. Preserve encoding and line endings; reject unknown binary encodings for text tools until an explicit conversion is chosen.

Changes include operation type, source/destination, expected revision, before-image reference, after revision, and status. Journal states: prepared, applying, committed, partial, reverted, conflicted. Each file operation records its result so recovery does not rely on a single final transaction bit.

Crash cases to cover:

1. Intent persisted, no file written: safe to report unapplied; never claim committed.
2. Some files replaced: compare disk revisions to before/after records and expose partial status.
3. All files replaced, final journal record missing: reconcile exact after-images before marking recovered completion.
4. External edits after interruption: preserve them and report conflict.
5. Response lost after commit: operation lookup returns the committed result without applying again.

No multi-file transaction claim crosses separate filesystems or Git repositories. Same-directory temporary replacement provides per-file atomic replacement where the platform supports it; durability and directory flush requirements are platform-specific. File ownership and symlink policy require tests.

## 8. Project and local configuration

Configuration precedence, lowest to highest: built-in defaults, user defaults, portable project manifest, locally trusted project overrides, explicit command-line/session overrides. Scalar values override; named maps merge by key; arrays replace unless a field explicitly defines set merging. A removal marker is explicit. Reject unknown keys in authored config unless they belong to a versioned extension field. Show every effective value's source.

Security-sensitive repository settings never silently broaden trusted local policy. Manifest changes that add executable hooks, MCP commands, credential helpers, or environment bindings must be reviewed under the existing trust policy before activation.

The following project manifest illustrates schema version 1. URLs and repositories are examples. `coding` is a user-configured model alias, not an invented provider model ID.

```json
{
  "schema_version": 1,
  "name": "storefront",
  "repositories": [
    {"alias": "web", "remote": "git@github.com:example/web.git", "default_branch": "main"},
    {"alias": "api", "remote": "git@github.com:example/api.git", "default_branch": "main"},
    {"alias": "infra", "remote": "git@github.com:example/infra.git", "default_branch": "main"}
  ],
  "default_model_ref": "coding",
  "default_environment_ref": "local",
  "skills": [
    {"name": "go-development", "source": "https://github.com/example/skills.git", "path": "go-development", "ref": "v1.0.0"}
  ],
  "mcp_servers": {
    "project-docs": {
      "transport": "stdio",
      "location": "environment",
      "command": "docs-mcp",
      "args": ["--stdio"],
      "environment_refs": {"DOCS_TOKEN": "env:DOCS_TOKEN"},
      "enabled": true,
      "policy_ref": "project-tools"
    },
    "remote-tools": {
      "transport": "streamable_http",
      "url": "https://mcp.example.test/mcp",
      "credential_ref": "keyring:storefront/remote-tools",
      "enabled": false,
      "policy_ref": "external-tools"
    }
  },
  "checks": {
    "api-test": {"repository": "api", "argv": ["go", "test", "./..."], "timeout_seconds": 120}
  },
  "lsp": {
    "go": {"command": "gopls", "args": [], "repositories": ["api"], "enabled": true}
  }
}
```

Project manifests use aliases; the local registry assigns stable IDs. Machine bindings supply actual paths and connection details, allowing the portable manifest to be shared without assuming a user's home directory:

```json
{
  "schema_version": 1,
  "project_ref": "storefront",
  "environments": {
    "local": {
      "kind": "local",
      "repositories": {"web": "/work/web", "api": "/work/api", "infra": "/work/infra"}
    },
    "qemu-dev": {
      "kind": "qemu",
      "hypervisor_host": "local",
      "qmp_socket": "/run/user/1000/bifrost/ubuntu-dev.qmp",
      "guest_transport": {"kind": "ssh", "host_alias": "ubuntu-dev", "worker": "bifrost"},
      "repositories": {"web": "/home/dev/work/web", "api": "/home/dev/work/api", "infra": "/home/dev/work/infra"}
    },
    "vbox-test": {
      "kind": "virtualbox",
      "hypervisor_host": "local",
      "vm_name": "bifrost-test",
      "guest_transport": {"kind": "ssh", "host_alias": "vbox-test", "worker": "bifrost"},
      "repositories": {"web": "/home/dev/work/web", "api": "/home/dev/work/api", "infra": "/home/dev/work/infra"}
    }
  }
}
```

The user provider registry stores endpoint/provider family, credential reference, model aliases with actual model IDs, capability overrides, and optional pricing. Provider setup validates an alias before it is selected. A custom endpoint is not assumed to support all capabilities of another endpoint with similar JSON.

`skills.lock.json` records source URL, subdirectory, requested ref, resolved commit, content digest, and enabled project binding. It contains no executable installation commands or secrets. A manifest change and its lock update are reviewed together.

## 9. MCP contracts

Internal server identity is a stable ID plus configuration revision. Display name is not enough to key credentials or cached tool catalogs. The launch identity includes location/environment, executable/arguments or URL, protocol preference, credential reference, policy, and trust decision.

An MCP tool is represented internally by server ID, original name, schema hash, catalog revision, annotations, and capabilities. Its model-facing alias uses a deterministic bounded encoding/hash compatible with the selected provider; keep a reversible lookup and detect collisions. Display the original names to users.

Connection readiness requires completed version/capability discovery according to the selected protocol. Do not impose legacy initialize/initialized or session-ID semantics on newer protocols. Implement compatibility behavior through the SDK and contract fixtures. Per-call capabilities and continuation/input-required behavior must remain in the adapter.

Tool catalog discovery is paginated and bounded. Keep native tool names separate from external aliases. Loading a server with thousands of tools must not inject all schemas into every prompt. Offer a small discovery tool that searches cached metadata, activates selected schemas for the next model request, and preserves stable references for pending calls.

Map content blocks individually: text, image, structured content, embedded resources, links, and other negotiated media. Unsupported media remains an artifact with a clear status. An MCP `isError` result is a failed tool result, not necessarily a broken connection. Transport errors and protocol errors are distinct.

Authentication-required state blocks dependent calls and offers login. Form elicitation never asks the model to invent the user's answer. URL elicitation opens only an identified user-approved flow. Legacy sampling routes through a separately budgeted child request and does not automatically include the entire conversation or secrets. Never advertise an unimplemented capability.

When a call times out or loses connection, return cancelled/failed only if that outcome is known; otherwise use outcome-unknown. Cancellation is best effort and does not prove the external action was rolled back. Long-running task extensions have separate remote task IDs and lifecycle state; ordinary local process IDs are not substitutes.

Server process logs have byte limits and secret redaction. Configured third-party runtimes are optional prerequisites. No automatic `npx`, `pip`, shell installer, or package-manager execution occurs simply because a repository contains an MCP definition.

## 10. LSP and diagnostics

Use JSON-RPC over server stdio with proper Content-Length framing, independent from Bifrost's JSONL transport. Maintain initialization state, server capabilities, workspace folders, document versions, URI mapping, negotiated position encoding, and cancellation. Do not assume all servers use byte offsets; correctly translate UTF-16/UTF-8 positions.

Start one server per compatible project/repository/environment grouping, with isolation where configuration differs. Open/change/save/close notifications follow Bifrost's file revision state. An unsaved editor buffer is authoritative for its language-server document, while disk tool edits must coordinate with that buffer.

Diagnostics include URI, repository/environment, range, severity, source, message, code, related information, and observed document version. Discard or label stale results; never block forever waiting for diagnostics. CLI checks produce parallel diagnostics with command/process provenance. Formatters and code actions produce revision-checked changes rather than direct hidden writes.

## 11. Artifact and state layout

Use platform config/state/cache directory conventions. Proposed Unix state structure:

```text
config/bifrost/config.json
config/bifrost/keymap.json
config/bifrost/projects/<project-id>/local.json
state/bifrost/registry.json
state/bifrost/projects/<project-id>/tasks/<task-id>/task.json
state/bifrost/projects/<project-id>/tasks/<task-id>/sessions/<session-id>/events.jsonl
state/bifrost/projects/<project-id>/tasks/<task-id>/sessions/<session-id>/snapshot.json
state/bifrost/operations/<operation-id>.json
state/bifrost/artifacts/sha256/<digest>
cache/bifrost/skills/<source-id>/<commit>/
runtime/bifrost/daemon.sock
```

Directory prefixes are resolved from OS conventions; these are relative examples, not hard-coded absolute paths. Worktrees live in configured storage on the execution environment, not inside the artifact cache. Remote worker state uses an equivalent worker-owned directory and stores only its own operations/artifacts.

Artifacts have explicit retention. Referenced before-images for undo are not garbage-collected while the change is retained. Apply disk quotas with visible failures and retention choices; do not silently delete active terminal output. A snapshot contains its journal sequence and checksum. Migration backs up prior metadata and commits an atomic version switch.

## 12. CLI surface

Proposed commands; no command below is implemented yet:

```text
bifrost                              open picker/current project
bifrost open <project>
bifrost serve                        run local daemon
bifrost attach [task]
bifrost run --project <name> --prompt <text> --output json
bifrost project add|list|edit|remove
bifrost repo add|list|remove --project <name>
bifrost task new|list|resume|archive
bifrost provider add|list|login|test
bifrost model list|set
bifrost skills add|list|inspect|update|remove
bifrost mcp add|list|inspect|connect|disable|restart|login|import
bifrost mcp serve --stdio             optional outgoing server mode
bifrost env add|list|inspect|connect|provision
bifrost vm list|inspect|start|stop|snapshot
bifrost worker --stdio               remote worker mode
bifrost config show --effective
bifrost doctor [--environment <name>]
bifrost session export|import
```

CLI and TUI invoke the same service methods. Implement flag parsing with the standard library unless complexity demonstrates a need for another library. JSON output is versioned and never interleaves status prose on stdout; logs use stderr. Headless input/approval requirements return a structured waiting/error result unless an explicit response channel is available. Exit codes distinguish success, task failure, configuration error, and required user interaction.

## 13. Error and observability contract

Use typed errors with stable codes: invalid-argument, not-found, conflict, permission-denied, approval-required, unsupported-capability, authentication-required, rate-limited, cancelled, deadline-exceeded, disconnected, outcome-unknown, corruption, and internal. Include a concise user message, retry classification, safe details, and operation IDs. Never put secrets in errors.

Use `log/slog` with correlated project/task/turn/tool/worker identifiers. Local debug logging is opt-in; model payload tracing is separately opt-in and redacted. Show useful error details in the affected pane with a retry/reconnect action where appropriate. An unsupported capability is an actionable state rather than a panic.

Keep traces and records local by default. Developer diagnostics include queue sizes, active goroutines/processes, event lag, render timing, and cache/artifact sizes. Metrics support benchmarks and `doctor`; no telemetry backend is required.

## 14. Initial operational defaults

These are configurable starting limits, to be checked against P0/P11 workloads. They are not claims about measured performance or protocol-wide maxima. Validate configuration ranges and expose the effective limits in `doctor`.

- One active turn per session, one mutation owner per task, four concurrent independent read-only native calls per task, and one interaction stream per browser tab or desktop.
- A turn stops resumably after 64 model/tool-loop iterations by default. Time/spend ceilings are configurable; unknown pricing must not be treated as zero cost.
- Ordinary foreground command deadline: 120 seconds. Background commands have an explicit retained process handle instead of an infinite synchronous tool call. Graceful stop allowance: 5 seconds before configured force termination.
- MCP connection/initial discovery deadline: 15 seconds. Ordinary call deadline: 60 seconds, overridable per server/tool; negotiated asynchronous tasks use their own tracked lifecycle. Retry backoff starts at 1 second, caps at 30 seconds, includes jitter, and stops after five consecutive connection failures until user action or a later scheduled health check. Mutating calls are never covered by automatic replay.
- Model/tool-visible text result budget: 32 KiB per result by default, with head/tail or selected-window presentation and a continuation/artifact reference. Limit truncation on valid character boundaries.
- File read default: 200 lines within the text budget. Search default: 200 matches with a continuation cursor. Directory listing default: 500 entries per page.
- Internal RPC frame cap: 8 MiB; larger artifacts use 256 KiB chunks. External MCP/model protocol limits are separately governed by their adapters. Oversized data is an explicit error or artifact result, not a truncated invalid JSON message.
- Live process-output ring: 1 MiB per process. Disk spool default: 100 MiB per process, with a visible quota state and explicit retention/rotation choices. Reserve capacity for lifecycle records so full output storage cannot prevent recording cancellation or completion.
- Session snapshots: after 500 durable lifecycle/message records or 5 MiB of additional journal data, whichever occurs first. Completed mutation records are flushed independently of snapshot cadence.
- UI render coalescing: at most 30 updates per second while streaming, with immediate handling of focus/cancellation state. Terminal bytes are processed correctly even when visual repaint is coalesced.
- No idle daemon shutdown while a task, terminal, MCP request, or owned background process is active. Detached but active tasks remain visible through status/list commands.

Timeouts, output limits, and parallelism must be included in fixture tests. A limit reached is a named result with a continuation or recovery path, never an unexplained disappearance of data.

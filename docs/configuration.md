# Configuration

Bifrost stores user configuration as private JSON. The default path is the
platform user configuration directory under `bifrost/config.json`; set
`BIFROST_CONFIG` or pass `--config PATH` to choose another file. Configuration
is validated before it is written, unknown JSON fields are rejected, and the
file is replaced atomically while a sibling lock prevents concurrent Bifrost
writes.

Use the same settings from the CLI or the TUI settings editor:

```text
bifrost config show
bifrost config set ui.theme light
bifrost config set defaults.max_steps 12
bifrost config set providers.local '{"kind":"chat-completions","base_url":"http://localhost:11434/v1","model":"my-model"}'
```

Inside the TUI, press `:settings` to open the searchable editor. Press `/` to
filter fields, `Enter` to stage an edit, and `Ctrl-S` to persist the staged
settings. Appearance and layout changes are applied after saving; provider,
permissions, SSH, hooks, decision, and marketplace changes take effect after
restarting the runtime. A concurrent file change produces a revision conflict
and leaves the on-disk configuration untouched.

`Enter` opens the selected field and stages its value; `Space` toggles a boolean.
`Ctrl-U` clears the editor, arrow keys move the cursor, and Shift+Enter inserts a
newline in JSON. Press `?` outside the value editor for full field help and its
current value. `Esc` cancels an edit; closing a dirty settings screen requires
a second `Esc` to discard. Configuration edits never silently overwrite a file
that changed while the editor was open.

On Linux the default location is `~/.config/bifrost/config.json` (or
`$XDG_CONFIG_HOME/bifrost/config.json`). `bifrost config path` prints the exact
resolved path, including `--config` and `--state-dir` overrides. The
[example file](../examples/config.json) illustrates the schema; replace its
placeholder models, project/skill names, SSH host, and catalog URL before use.

Scalar strings are entered as plain values. Booleans and numbers use their
usual literal forms. Maps, arrays, hooks, and complete struct values use one
strict JSON value; structured values cannot contain unknown fields. The
editor exposes scalar leaves such as `defaults.max_steps` and
`ui.theme`. `providers`, `projects`, and `ssh` are available as complete JSON
maps, and each existing entry is also available as a complete JSON value and
as individual leaves such as `providers.local.model`.

The top-level areas are:

| Area | Purpose |
| --- | --- |
| `active_provider` | Provider profile used when no command-line provider is selected. |
| `providers` | Named native or subscription provider profiles. Each profile requires `kind` and `model`; credentials are environment-variable names. |
| `defaults` | Project, provider, skills, MCP configuration, permissions, browser access, and execution limits. |
| `projects` | Per-project provider, skills, MCP configuration, and environment choices. |
| `ssh` | Named guest-worker targets with target, absolute guest roots, state directory, and optional identity/executable settings. |
| `ui` | Theme, icons, external editor argv, layout, pane proportions, terminal visibility, terminal size, and composer newline/height behavior. TUI layout changes take effect immediately and are persisted; provider and runtime changes take effect on the next run. |
| `decision` | Optional routing policy. When enabled it requires a supported kind, model, credential environment, confidence threshold, and at least two known provider candidates. |
| `marketplace` | Optional skills catalog URL. |
| `hooks` | Trusted before/after-turn and before/after-tool argv hooks. Hooks are arrays of argv strings and never shell snippets. |

`schema_version` is required and currently must be `1`; it is maintained by
Bifrost and is not an editable setting. Secret values such as API keys do not
belong in this file. Put credentials in the environment named by `api_key_env`
or use the account login flow for subscription providers.

The setup command writes a complete initial file:

```text
bifrost setup --non-interactive --provider responses --model MODEL --name PROJECT
```

To review a replacement before applying it, use `config import FILE` and then
`config import FILE --apply`. A stale TUI or service save is rejected when the
file revision changed since it was loaded; reload the settings and apply the
edits again.

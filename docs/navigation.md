# Bifrost navigation: current behavior and development target

Status: documented 2026-10-15, with one slice landed on 2026-10-16. A left click
focuses the pane under the pointer and does not enter terminal input
(`t` still does that, inside the viewer). The focused frame uses a heavier
border and, in color themes, a cyan title. The rest of this scheme is still a
design brief, not a list of available shortcuts.

## Current behavior and conflicts

- `Ctrl+W`, then `h`/Left/`k`/Up calls `moveFocus(-1)`; `j`/Down/`l`/Right
  calls `moveFocus(1)`. This cycles visible panes in layout order; it does not
  move geometrically. Studio order is files/viewer/chat; Conversation uses
  files/chat/viewer. Focus has chat only. The terminal is a viewer tab, not a
  separate pane.
- Unmodified `h`/Left and `l`/Right in ordinary navigation jump to files/chat,
  whereas the marketplace uses them to move within its card grid.
- Tab/Shift+Tab currently select chat folds. Enter/Space toggles them. This must
  migrate before Tab can consistently navigate panes.
- `:` and Ctrl+P both open the command palette. Ctrl+P is not a file finder yet.
- `?` opens a help modal over the workspace. Esc closes it. The viewer stays
  on the file or terminal tab underneath.
- `c` composes chat; Enter sends; Shift+Enter adds a newline with Alt+Enter and
  Ctrl+J fallbacks. The inline editor has its own normal/insert modes.
- `t` opens the shell as a viewer tab and leaves the keyboard on Bifrost.
  `T` adds another shell tab. Click the shell, or press `i` or Enter while
  that tab is focused, to type into it. Esc returns to the message box. A
  click on another pane leaves the shell too. Ctrl+W still cycles panes.
- Marketplace supports live `/` search, spatial arrow/hjkl card navigation,
  click/Enter detail modals, pinned install buttons, scrolling and Esc/back.
- Pane dividers already resize with mouse drag. Layout/proportions/visibility
  persist, and hiding the terminal does not stop its process.
- A left click focuses the pane under the pointer. Clicks on a divider still
  resize. Clicking a viewer tab selects it without entering terminal input.

## Proposed navigation defaults — pending

These bindings apply to Bifrost browsing/navigation. Text fields, editors,
dialogs and terminal passthrough have explicit local behavior.

| Action | Proposed binding | Migration note |
| --- | --- | --- |
| Next/previous visible pane | Tab / Shift+Tab | Replace chat-fold Tab ownership |
| Focus neighbor in direction | Ctrl+W, then arrows or hjkl | Replace cyclic directional handling |
| Previous focused pane | Ctrl+W, then p | New focus history |
| Zoom pane / restore arrangement | Ctrl+W, then z | New temporary presentation state |
| Move within lists/cards | Arrows or hjkl | Preserve spatial card behavior; stop global h/l jumps |
| Open selected item | Enter | Contextual, not a global pane jump |
| Expand/collapse selected block | Space | Chat disclosure selection must become explicit |
| Search active view | / | Keep local search input distinct from commands |
| Find a file | Ctrl+P | Reassign current palette alias and update help |
| Find/execute an action | : | Search commands by name/description; retain colon syntax/completion |
| Context help | ? | Show focused view's bindings, not only a global dump |
| Return one level | Esc | Close local layer and restore focus/position |
| Leave the shell | Esc | Return to the message box. The shell keeps running |

Dialogs use Tab/Shift+Tab between their controls, with keyboard access to the
same buttons the mouse can use. Typing ordinary characters or moving the caret
must not trigger navigation commands. While the shell is taking keys, Esc
returns to the message box and Ctrl+W still cycles panes. Other keys go to
the shell.

## Interaction rules

1. **Real spatial focus.** Derive neighbors from visible pane rectangles after
   layout/resizing. Move toward the nearest relevant pane in that direction;
   do not wrap across the opposite edge. Prefer overlap along the perpendicular
   axis and deterministic ties. Tab separately cycles reading order and wraps.
2. **Visible focus and mode.** Distinguish active pane borders/titles from selected
   rows/cards. Show readable context such as `CHAT · WRITING` or `FILES · BROWSING`.
   Use text/symbols as well as color; preserve monochrome readability.
3. **Discoverability.** Show contextual footer actions and make them clickable.
   After Ctrl+W, show a compact prefix menu for directions, previous pane, zoom
   and resizing. Escape cancels the prefix. Unsupported keys must not accidentally
   fall through into unrelated actions. Keep help generated from binding metadata.
4. **Stable context.** Closing a modal returns to the initiating pane/control,
   search, selection and scroll position. Layout changes and resize clamp focus
   to visible panes without discarding state. Streaming never steals focus.
5. **Mouse parity.** Click to focus or activate a visible control; hover alone
   does not change keyboard focus. Wheel scrolls the pane under the pointer;
   modals block background actions. Measure hitboxes from the rendered geometry.
6. **Chat browsing.** Provide a selected message/tool block with j/k or arrows
   and Space/Enter folding; keep transcript scrolling accessible via page keys
   and wheel. Moving between blocks must reveal the selected block. Preserve
   fold state when a streaming tool updates and preserve drafts on exit/re-entry.
7. **Temporary zoom.** Expand a pane without changing saved layout percentages
   or restarting its process. Restore the previous arrangement and sane focus;
   explicitly handle terminal resize and window-size changes while zoomed.
8. **Configurable bindings.** Add validated action/context mappings through the
   existing settings system, with defaults and reset. Detect conflicting bindings
   within a context. Do not conflate configurable keys with text being typed.
   This is a target; the current `UISettings` has no general keymap field.

## Implementation entry points

| Concern | Start here |
| --- | --- |
| Key routing, normal actions, prefix handling | `internal/ui/model.go`: `updateKey`, `handleNormalKey`, `handlePrefixKey`, `handleNormalAction`, `handleNormalNavigation` |
| Visibility and focus traversal | `internal/ui/layout.go`: `visibleFocuses`, `normalizeFocus`, `moveFocus` |
| Layout geometry / drag behavior | `internal/ui/model.go`, `internal/ui/pane_drag.go`, `internal/ui/chat_folds.go` |
| Fold selection / hit testing | `internal/ui/chat_folds.go`: `handleChatFoldKey`, `chatPaneBounds` |
| Composer and editor key ownership | `internal/ui/chat.go`, `internal/ui/inline_editor.go` |
| Palette registry, usage, completion | `internal/ui/command_palette.go`; palette editing in `inline_editor.go` |
| Footer, colors and status | `internal/ui/connection.go`, `internal/ui/styles.go`, `internal/ui/model.go` |
| Settings schema / editor / persistence wiring | `internal/config/settings.go`, `internal/config/editor.go`, `internal/ui/settings.go`, `internal/cli/settings_ui.go` |
| Marketplace navigation reference | `internal/ui/marketplace.go`, `marketplace_grid.go`, `skill_command.go` |

Reuse geometry and the existing callback boundaries. Do not create a second
layout model whose coordinates drift from rendering. Introduce action/context
binding metadata when implementing consistent remapping/help, rather than adding
another growing set of unrelated key switches. Preserve cancellation and stale
response guards throughout modal transitions.

## Suggested implementation order and acceptance

1. Geometric focus and pane Tab traversal; migrate chat block navigation together
   to resolve the Tab conflict. Cover all four layouts and narrow active-pane views.
2. Previous-pane restoration, mode/focus styling, contextual footer and Ctrl+W
   help menu. Keyboard and mouse must invoke the same actions.
3. Zoom/restore with correct PTY resize and intact drafts/history/processes.
4. File finder and action search; change Ctrl+P help and documentation atomically.
5. Validated configurable bindings, settings integration and conflict feedback.

For each increment, test editor/composer/search/dialog/passthrough key ownership,
hidden panes, layout changes, edge directions, resize during navigation, stale
async results, focus restoration and scrolling. Verify actual render dimensions
and hit targets at 80×24, 120×36, 160×48, narrow sizes, plain theme and Unicode.
A live PTY smoke should include terminal passthrough and returning to navigation.
Update README/help, this document's status and current-build evidence when each
behavior lands. Do not claim the entire scheme after implementing only focus.

## Design references

- [Lazygit keybindings](https://github.com/jesseduffield/lazygit/blob/master/docs/keybindings/Keybindings_en.md): contextual actions, search, drill-in and return.
- [Yazi navigation](https://yazi-rs.github.io/docs/quick-start/): arrows and Vim-style movement through structured content.
- [Zellij presets](https://zellij.dev/documentation/keybinding-presets): explicit modes and separation from embedded application shortcuts.

These inform the design; Bifrost's target table above is our proposal, not a
statement that those applications share identical bindings.

# Desktop compatibility

`internal/desktop` exposes native desktop control only when the current Linux
display session and its explicit adapter tools are detected. It never claims
that a screenshot implies input support, and it does not bypass compositor or
OS permission prompts.

## Host Linux

### X11

The X11 adapter captures the root display with ImageMagick `import` and reads
the display geometry with `xdotool getdisplaygeometry`. Pointer, key, and text
actions use argument-safe `xdotool` invocations. The `DISPLAY` environment and
the current X11 authorization must already be available to the process.

### Wayland

The Wayland adapter captures with the compositor-supported `grim` command and
reads dimensions from the resulting PNG. Text entry is separately advertised
when `wtype` is available. Pointer and key actions are advertised only when
`ydotool` is installed and configured for the session. There is no universal
Wayland input route in this package: portals and compositor-specific APIs are
not guessed or silently substituted.

Capabilities are returned individually through `Manager.Capabilities`, with
human-readable limitations in `Details`. Missing tools therefore produce a
feature-specific error from `Capture`, `Move`, `Click`, `KeyPress`, or `Type`.

## Observation and lease rules

`Manager.Acquire` serializes one interaction stream. `Capture` writes a bounded
PNG artifact under the configured artifact directory and returns its path,
timestamp, backend, display, dimensions, and observation ID. Every action must
include the latest observation ID and exact display name, and coordinates must
fall within that observation's pixel bounds. An observation is consumed after
an action, so callers capture again after each action or reconnect/resize.

The package does not attempt live desktop interactions in its tests. Tests use
injected command runners that inspect argv and create fixture PNG headers. A
real desktop qualification still requires a manual session for the target
X11 compositor or Wayland compositor and its normal permission setup.

## Virtual machines

QEMU screenshot/input should use its existing QMP adapter where supported.
VirtualBox management APIs expose screenshot and keyboard operations only
where the installed `VBoxManage` capability is verified. VirtualBox pointer
input is not claimed through an unsupported management command; richer guest
desktop control belongs in a guest worker reached through the existing worker
transport/SSH path.

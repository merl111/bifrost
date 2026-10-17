# CUI compatibility patch

Bifrost's native desktop was verified with
[`merl111/cui`](https://github.com/merl111/cui) at commit
`1c05d5c43077af7524a171a127e60b3740c48775`, plus
[`cui-desktop.patch`](cui-desktop.patch).

The patch preserves the local toolkit changes used by this Bifrost build:
editable Code controls, native key and modifier forwarding, text ranges and
related regression tests. It contains the complete diff from that published revision to the local
CUI tree used by this snapshot, including local Windows memory-test additions. The separate CUI repository is not modified by committing
this patch to Bifrost.

For a fresh checkout, run from the Bifrost root:

```sh
git clone https://github.com/merl111/cui.git ../cui
git -C ../cui checkout 1c05d5c43077af7524a171a127e60b3740c48775
git -C ../cui apply ../bifrost/patches/cui-desktop.patch
make desktop
```

The relative patch path assumes this repository directory is named `bifrost`.
If CUI is already checked out, inspect its local edits before applying anything.
Use `git -C ../cui apply --reverse --check ../bifrost/patches/cui-desktop.patch`
to check whether this patch is already present. Do not apply it twice.

The GUI links this adjacent checkout through `desktop/go.mod`; the terminal
build has no CUI dependency. See [desktop setup](../docs/desktop.md) for native
system packages and verification commands.

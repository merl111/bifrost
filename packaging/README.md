# Linux release packaging

The GUI and TUI stay separate. `packaging/Dockerfile` builds on Ubuntu 24.04
with Go 1.26.0, checks out the pinned CUI commit from `patches/README.md`, and
applies both compatibility patches. It never uses a developer's adjacent CUI tree.
The Go dependency checksums are checked by the toolchain. Packaging tool downloads
are pinned and SHA-256 verified before execution. Update pins deliberately.

```sh
docker build --progress=plain --target verified -f packaging/Dockerfile -t bifrost-release .
container=$(docker create bifrost-release)
mkdir -p dist
docker cp "$container:/out/." dist/
docker rm "$container"
(cd dist && sha256sum --check SHA256SUMS)
```

Build output:

- `bifrost-VERSION-linux-x86_64`: stripped, CGO-disabled CLI/TUI executable.
- `Bifrost-VERSION-x86_64.AppImage`: CUI GUI, GTK schemas/modules/themes and native
  dependencies. The host supplies glibc 2.39+, X11/XWayland and graphics drivers.
- Third-party notices, version, release notes and `SHA256SUMS`.

Both frontends pass race/vet checks. Native GTK tests run in separate processes
under Xvfb. The final Docker stage contains no system GTK, compiler or checkout;
it runs the actual TUI in a PTY at four sizes and opens/closes both the AppImage
project chooser and workspace. FUSE mounting is not available in CI; the smoke
uses the runtime's extract-and-run path, also supported on user machines.
Real provider/OAuth/SSH/VM sessions are outside these packaging tests.

The custom AppRun preserves the caller's working directory and does not add
bundled libraries to `LD_LIBRARY_PATH` or bundled utilities to `PATH`. Relative
ELF RPATHs load GUI libraries without affecting external Git/vendor/shell tools.
The pinned GTK hook selects X11; native Wayland is not qualified. New repository windows relaunch the original AppImage so each owns its mount.
For the optional detached daemon, set `BIFROST_DAEMON_EXECUTABLE` to your downloaded
TUI binary before launching the GUI with `--daemon`. The daemon needs the CLI
entry point; ordinary GUI chat does not need the TUI binary. No auto-updater
or desktop integration is installed. Download a newer AppImage to upgrade.

`.github/workflows/release.yml` checks pull requests, main and manual runs and
retains the tested artifacts. Only `v*` tag runs can publish, in a separate job
with write permission. That job checks the tag against the embedded version and
checksums, uploads to a draft, then publishes it. A published release is never
replaced on rerun. A failed build or smoke test cannot reach publication.

To release: update `internal/cli.Version`, write `docs/releases/vVERSION.md`, get a
passing main build, then create and push that version tag. Keep README and
`docs/current-build.md` limitations aligned with the tested platform. Container
base tags and Ubuntu security packages can move; this is a repeatable build recipe,
not a claim of byte-for-byte reproducibility. The notes archive preserves upstream
notices and records the full Ubuntu package/source-version manifest.

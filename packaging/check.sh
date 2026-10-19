#!/usr/bin/env bash
set -euo pipefail
go test -race -timeout=120s ./...
go vet ./...
(
    cd desktop
    go test -race -timeout=120s ./...
    go vet ./...
    # Each GTK application must own the main OS thread of a fresh process.
    for test in TestNativeWorkspace TestNativeWorkspaceControls TestNativeCustomExtensions \
        TestNativeProjectPicker TestNativeFileExplorer TestNativeIndependentProviderAndModel; do
        BIFROST_NATIVE_TEST=1 GDK_BACKEND=x11 GSK_RENDERER=cairo \
            dbus-run-session -- xvfb-run -a -s '-screen 0 3840x2160x24' \
            go test -race -run "^${test}$" -count=1 -timeout=120s -v
    done
)

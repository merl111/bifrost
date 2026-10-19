#!/usr/bin/env bash
# Run inside packaging/Dockerfile, or an equivalent Ubuntu 24.04 build host.
set -euo pipefail
[[ "$(uname -m)" == x86_64 ]] || { echo "This release target requires Linux x86-64" >&2; exit 1; }
out="$(mkdir -p "${1:?output directory}" && realpath "$1")"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$work/bifrost" ./cmd/bifrost
version="$("$work/bifrost" --version | awk '{print $NF}')"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Invalid release version' >&2; exit 1; }
cp "$work/bifrost" "$out/bifrost-$version-linux-x86_64"
file "$work/bifrost" | grep -q 'statically linked'
(cd desktop && go build -trimpath -buildvcs=false -ldflags='-s -w' -o "$work/bifrost-desktop" .)
[[ "$("$work/bifrost-desktop" --version)" == "bifrost-desktop $version" ]]
fetch() {
    curl --fail --silent --show-error --location --retry 4 --connect-timeout 30 --max-time 300 "$1" -o "$2"
    printf '%s  %s\n' "$3" "$2" | sha256sum --check --status
}
fetch https://github.com/linuxdeploy/linuxdeploy/releases/download/1-alpha-20251107-1/linuxdeploy-x86_64.AppImage \
    "$work/linuxdeploy.AppImage" c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d
fetch https://raw.githubusercontent.com/linuxdeploy/linuxdeploy-plugin-gtk/7a3fbc31a9e5075073ff8790f26effbac5f84453/linuxdeploy-plugin-gtk.sh \
    "$work/linuxdeploy-plugin-gtk.sh" b0f4cbc684a0103a9651f0955b635eaea0096b3a66c0f5a2c2aa337960375171
fetch https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64 \
    "$work/runtime-x86_64" 2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d
chmod +x "$work/linuxdeploy.AppImage" "$work/linuxdeploy-plugin-gtk.sh"
(cd "$work" && ./linuxdeploy.AppImage --appimage-extract >/dev/null)
export PATH="$work:$PATH" DEPLOY_GTK_VERSION=4
appdir="$work/Bifrost.AppDir"
mkdir -p "$appdir/usr/share/bifrost"
python3 packaging/notices.py "$appdir/usr/share/bifrost/notices"
# linuxdeploy's desktop baseline excludes these, but a GTK-free host may lack
# them. Explicit library arguments override that exclusion list.
libraries=()
for name in libharfbuzz.so.0 libfribidi.so.0 libwayland-client.so.0 \
    libgmp.so.10 libgpg-error.so.0 libcom_err.so.2; do
    libraries+=(--library "/usr/lib/x86_64-linux-gnu/$name")
done
"$work/squashfs-root/AppRun" --appdir "$appdir" \
    --executable "$work/bifrost-desktop" --desktop-file packaging/linux/bifrost-desktop.desktop \
    --icon-file packaging/linux/bifrost-desktop.svg --custom-apprun packaging/linux/AppRun --plugin gtk "${libraries[@]}"
# Supply the pinned runtime explicitly; never fetch a mutable continuous runtime.
ARCH=x86_64 "$work/squashfs-root/plugins/linuxdeploy-plugin-appimage/usr/bin/appimagetool" \
    --mksquashfs-opt -processors --mksquashfs-opt 4 \
    --runtime-file "$work/runtime-x86_64" "$appdir" "$out/Bifrost-$version-x86_64.AppImage"
tar -czf "$out/bifrost-$version-notices.tar.gz" -C "$appdir/usr/share/bifrost" notices
printf '%s\n' "$version" > "$out/VERSION"
cp "docs/releases/v$version.md" "$out/RELEASE_NOTES.md"
(cd "$out" && sha256sum bifrost-* Bifrost-*.AppImage VERSION RELEASE_NOTES.md > SHA256SUMS)

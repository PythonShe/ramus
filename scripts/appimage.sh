#!/bin/sh
# Packs the linux/amd64 build of `mygo build` into an AppImage, the one
# Linux package mygo does not write. It downloads appimagetool and the
# AppImage runtime, each pinned by its checksum, so it runs in CI.
#
#   scripts/appimage.sh <the build's linux-amd64 folder> <out folder>
set -eu
build=$1
out=$2
root=$(cd "$(dirname "$0")/.." && pwd)
version=$(sed -n 's/^  "version": *"\([^"]*\)".*/\1/p' "$root/mygo.json")

tool_url=https://github.com/AppImage/appimagetool/releases/download/1.9.1/appimagetool-x86_64.AppImage
tool_sum=ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0
runtime_url=https://github.com/AppImage/type2-runtime/releases/download/20251108/runtime-x86_64
runtime_sum=2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fetch() {
	curl -fsSL -o "$2" "$1"
	echo "$3  $2" | sha256sum -c -
}
fetch "$tool_url" "$work/appimagetool" "$tool_sum"
fetch "$runtime_url" "$work/runtime" "$runtime_sum"
chmod +x "$work/appimagetool"

app=$work/Ramus.AppDir
mkdir -p "$app/usr/lib/ramus"
cp "$build/ramus" "$build/icon.png" "$build/.mygo-files" "$app/usr/lib/ramus/"
cp "$build/ramus.desktop" "$app/ramus.desktop"
cp "$build/ramus-256x256.png" "$app/ramus.png"
cat >"$app/AppRun" <<'RUN'
#!/bin/sh
exec "$(dirname "$(readlink -f "$0")")/usr/lib/ramus/ramus" "$@"
RUN
chmod +x "$app/AppRun"

mkdir -p "$out"
# The tool is an AppImage itself; extracting it needs no FUSE.
ARCH=x86_64 APPIMAGE_EXTRACT_AND_RUN=1 "$work/appimagetool" --no-appstream \
	--runtime-file "$work/runtime" "$app" "$out/Ramus-$version-x86_64.AppImage"

#!/usr/bin/env bash

set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
version=${1:?usage: build-binary-release.sh VERSION [ARCH]}
arch=${2:-x86_64}
stage="$repo_root/dist/regalia-$version-$arch"
archive="$repo_root/dist/regalia-$version-$arch.tar.zst"
payload="$stage/payload"

case "$arch" in
    x86_64) go_arch=amd64 ;;
    aarch64) go_arch=arm64 ;;
    *) echo "unsupported architecture: $arch" >&2; exit 1 ;;
esac
[[ "$version" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] || {
    echo "invalid release version: $version" >&2
    exit 1
}
command -v go >/dev/null 2>&1 || {
    echo "Go is required to build a Regalia release." >&2
    exit 1
}

mkdir -p "$repo_root/dist"
rm -rf -- "$stage"
rm -f -- "$archive" "$archive.sha256"
mkdir -p "$payload/usr/bin" \
    "$payload/usr/lib/regalia" \
    "$payload/usr/lib/systemd/system" \
    "$payload/usr/lib/systemd/user" \
    "$payload/usr/share/polkit-1/rules.d"

(
    cd "$repo_root"
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
        go build -trimpath -ldflags '-s -w' \
        -o "$payload/usr/bin/regalia" ./cmd/regalia
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
        go build -trimpath -ldflags '-s -w' \
        -o "$payload/usr/bin/regaliad" ./cmd/regaliad
    CGO_ENABLED=0 GOOS=linux GOARCH="$go_arch" \
        go build -trimpath -ldflags '-s -w' \
        -o "$payload/usr/lib/regalia/regalia-engine" ./cmd/regalia-engine
)

install -Dm644 "$repo_root/packaging/systemd/regalia-engine@.service" \
    "$payload/usr/lib/systemd/system/regalia-engine@.service"
install -Dm644 "$repo_root/packaging/systemd/user/regaliad.service" \
    "$payload/usr/lib/systemd/user/regaliad.service"
install -Dm644 "$repo_root/packaging/polkit/50-regalia-engine.rules" \
    "$payload/usr/share/polkit-1/rules.d/50-regalia-engine.rules"
install -Dm755 "$repo_root/installer/install-release-payload.sh" \
    "$stage/install.sh"
printf '%s\n' "$version" >"$stage/VERSION"

tar --zstd -C "$stage" -cf "$archive" .
sha256sum "$archive" >"$archive.sha256"
printf '%s\n' "$archive"

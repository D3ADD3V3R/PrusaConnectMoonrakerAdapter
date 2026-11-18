#!/usr/bin/env bash
set -euo pipefail
root="${BUILD_ROOT:-$(cd "$(dirname "$0")/.." && pwd)}"
source "$root/versions.env"

if [[ "$(uname -s)" != Linux || "$(uname -m)" != aarch64 ]]; then
    echo 'This toolchain is pinned for Linux aarch64.' >&2
    exit 1
fi
for command in curl tar xz sha256sum git make python3 flock; do
    command -v "$command" >/dev/null || { echo "Missing command: $command" >&2; exit 1; }
done
mkdir -p "$root/.tools" "$root/.cache/downloads" "$root/.cache/tmp" "$root/bin" "$root/upstream"
if [[ ! -d "$root/upstream/prusa-connect/.git" ]]; then
    git clone --depth 1 --branch v0.1.0-beta.1 https://github.com/prusa3d/prusa-connect.git "$root/upstream/prusa-connect"
fi
actual=$(git -C "$root/upstream/prusa-connect" rev-parse HEAD)
[[ "$actual" == "$UPSTREAM_COMMIT" ]] || { echo 'Prusa source revision differs from the pinned release.' >&2; exit 1; }
bash "$root/scripts/apply-upstream.sh"

download() {
    local url=$1 archive=$2 checksum=$3
    if [[ ! -f "$archive" ]]; then
        curl --fail --location --retry 3 --output "$archive.part" "$url"
        mv -- "$archive.part" "$archive"
    fi
    printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status
}

go_dir="$root/.tools/go$GO_VERSION"
if [[ ! -x "$go_dir/bin/go" ]]; then
    archive="$root/.cache/downloads/go$GO_VERSION.linux-arm64.tar.gz"
    download "https://go.dev/dl/go$GO_VERSION.linux-arm64.tar.gz" "$archive" "$GO_SHA256"
    staging=$(mktemp -d "$root/.tools/go-stage.XXXXXX")
    tar -xzf "$archive" -C "$staging" --strip-components=1
    mv -- "$staging" "$go_dir"
fi
"$go_dir/bin/go" version | grep -F "go$GO_VERSION linux/arm64"

node_dir="$root/.tools/node$NODE_VERSION"
if [[ ! -x "$node_dir/bin/node" ]]; then
    archive="$root/.cache/downloads/node-v$NODE_VERSION-linux-arm64.tar.xz"
    download "https://nodejs.org/dist/v$NODE_VERSION/node-v$NODE_VERSION-linux-arm64.tar.xz" "$archive" "$NODE_SHA256"
    staging=$(mktemp -d "$root/.tools/node-stage.XXXXXX")
    tar -xJf "$archive" -C "$staging" --strip-components=1
    mv -- "$staging" "$node_dir"
fi
export PATH="$node_dir/bin:$go_dir/bin:$PATH"
[[ "$(node --version)" == "v$NODE_VERSION" ]]

pnpm_bin="$root/.tools/pnpm/node_modules/.bin/pnpm"
if [[ ! -x "$pnpm_bin" ]]; then
    npm install --prefix "$root/.tools/pnpm" --no-audit --no-fund --ignore-scripts "pnpm@$PNPM_VERSION"
fi
[[ "$("$pnpm_bin" --version)" == "$PNPM_VERSION" ]]
printf 'Build tools ready: Go %s, Node %s, pnpm %s\n' "$GO_VERSION" "$NODE_VERSION" "$PNPM_VERSION"

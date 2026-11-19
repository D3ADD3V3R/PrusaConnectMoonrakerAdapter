#!/usr/bin/env bash
set -euo pipefail
root="${BUILD_ROOT:?BUILD_ROOT is required}"
source "$root/versions.env"
core="$root/upstream/prusa-connect"
composition="$root/.cache/composition"
[[ "$(git -C "$core" rev-parse HEAD)" == "$UPSTREAM_COMMIT" ]]
cd "$core"
go run ./cmd/compose-build --config "$root/build.yaml" --work "$composition" --print
cd "$composition"
go mod tidy
tags=$(cd "$core" && go run ./cmd/detect-build-tags --config "$core/config/config.yaml" --variant full --extra "${RELEASE_EXTRA_TAGS//,/ }")
printf 'Building %s with tags %s\n' "$BUILD_VERSION" "$tags"
go build -trimpath -tags "$tags" \
    -ldflags "-X github.com/prusa3d/prusa-connect/internal/kernel/version.stamped=$BUILD_VERSION -X github.com/prusa3d/prusa-connect/internal/kernel/version.docsURL=$DOCS_URL -X github.com/prusa3d/prusa-connect/internal/upgrade.officialPublicKeys=$UPGRADE_PUBLIC_KEYS" \
    -o "$root/bin/prusa-connect-pcma.new" .
mv -- "$root/bin/prusa-connect-pcma.new" "$root/bin/prusa-connect-pcma"
cd "$root/adapter"
go build -trimpath -o "$root/bin/pcma-prepare" ./cmd/pcma-prepare
go build -trimpath -o "$root/bin/pcma-profile" ./cmd/pcma-profile
go build -trimpath -o "$root/bin/moonraker-sim" ./cmd/moonraker-sim
cd "$root/bin"
sha256sum prusa-connect-pcma pcma-prepare pcma-profile moonraker-sim > SHA256SUMS
go version -m prusa-connect-pcma > BUILDINFO.txt
printf '%s\n' "$BUILD_VERSION" > VERSION
cat SHA256SUMS

#!/usr/bin/env bash
set -euo pipefail
root="${BUILD_ROOT:?BUILD_ROOT is required}"
core="$root/upstream/prusa-connect"
embed="$core/internal/transport/http/httpapi/appdist"
source_fingerprint() {
    find "$core/frontend" "$core/packages" \
        \( -name node_modules -o -name dist -o -name .git -o -name .cache \) -prune \
        -o -type f ! -name '*.tsbuildinfo' -print0 \
        | sort -z | xargs -0 sha256sum | sha256sum | cut -d ' ' -f 1
}
fingerprint=$(source_fingerprint)
record="$root/.cache/frontend.fingerprint"
if [[ -f "$record" ]] && [[ "$(cat "$record")" == "$fingerprint" ]] \
    && { [[ -f "$embed/index.html.gz" ]] || [[ -f "$embed/index.html" ]]; }; then
    echo 'Embedded frontend is up to date.'
    exit 0
fi
make -j1 -C "$core" embed-app AUTO_INSTALL_PNPM=0
source_fingerprint > "$record"

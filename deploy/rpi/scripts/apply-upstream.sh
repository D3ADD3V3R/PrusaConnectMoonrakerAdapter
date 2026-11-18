#!/usr/bin/env bash
set -euo pipefail
root="${BUILD_ROOT:?BUILD_ROOT is required}"
core="$root/upstream/prusa-connect"
patch="$root/upstream.patch"
record="$root/.cache/applied-upstream.patch"
[[ -f "$patch" ]] || { echo 'Missing upstream.patch.' >&2; exit 1; }
if git -C "$core" apply --reverse --check "$patch" 2>/dev/null; then
    cp -- "$patch" "$record"
    exit 0
fi
if [[ -f "$record" ]] && ! cmp -s "$patch" "$record"; then
    git -C "$core" apply --reverse --check "$record"
    git -C "$core" apply --reverse "$record"
fi
git -C "$core" apply --check "$patch"
git -C "$core" apply "$patch"
cp -- "$patch" "$record"
echo 'Applied the Moonraker setup frontend patch.'

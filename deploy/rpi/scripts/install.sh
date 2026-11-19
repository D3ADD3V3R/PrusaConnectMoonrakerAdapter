#!/usr/bin/env bash
set -euo pipefail
exec python3 "${BUILD_ROOT:?BUILD_ROOT is required}/scripts/install.py"

#!/usr/bin/env bash
# Issue #1528's planted violations, at the path the matrix and the gate name.
# The controls are scripts/retire/selftest.py; `exec`, so its exit status is
# the self-test's own.
set -euo pipefail
repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/retire/selftest.py" "$@"

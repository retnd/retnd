#!/usr/bin/env bash
# The suite itself is scripts/bdtools/tests/pin_on_main.py (issue #1074).
# This shim keeps the scripts/tests/*.test.sh shape scripts/ci-local.sh runs.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/pin_on_main.py" "$@"

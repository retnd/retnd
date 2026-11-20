#!/usr/bin/env bash
# How much scratch the architecture self-test holds at once (#1580): at most
# two tree copies alive, each removed right after its case, even when the case
# raises or is interrupted. The cases are
# scripts/bdtools/tests/architecture_selftest_scratch.py.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/architecture_selftest_scratch.py" "$@"

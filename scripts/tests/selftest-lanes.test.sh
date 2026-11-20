#!/usr/bin/env bash
# Which self-test controls a host can run, and what the gate hears about the
# rest (#1530, #1531). The cases are scripts/bdtools/tests/selftest_lanes.py.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/selftest_lanes.py" "$@"

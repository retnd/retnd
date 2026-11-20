#!/usr/bin/env bash
# The discovery readability probe's mutation self-test (#1541), at the path
# scripts/bdtools/selftest/check_anchors.py calls with --check-anchors.
#
# The controls themselves are scripts/bdtools/discovery/selftest.py, beside
# the other ported selftests.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/discovery/selftest.py" "$@"

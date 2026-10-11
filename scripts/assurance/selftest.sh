#!/usr/bin/env bash
# EPIC Q's assurance mutation self-test (#988), at the path everything names.
#
# The controls themselves are scripts/bdtools/assurance/selftest.py, beside
# the other ported selftests. This file is the runnable shim at the literal
# path scripts/ci-local.sh runs and scripts/bdtools/selftest/check_anchors.py
# calls with --check-anchors.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/assurance/selftest.py" "$@"

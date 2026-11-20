#!/usr/bin/env bash
# EPIC O's observability mutation self-test (#899), at the path the spec, the
# matrix and scripts/ci-local.sh name.
#
# The controls themselves are scripts/bdtools/observability/selftest.py,
# beside the other ported selftests. This file is the runnable shim at the
# literal path scripts/ci-local.sh runs and
# scripts/bdtools/selftest/check_anchors.py calls with --check-anchors.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/observability/selftest.py" "$@"

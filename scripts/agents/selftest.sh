#!/usr/bin/env bash
# EPIC U's falsifications (#1353), at the path everything names.
#
# The controls themselves are scripts/bdtools/agents/selftest.py. A PASS row in
# docs/conformance/epic-u-matrix.md cites one of its labels, and
# core/tests/agentgate refuses a PASS whose label is not in that file.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/agents/selftest.py" "$@"

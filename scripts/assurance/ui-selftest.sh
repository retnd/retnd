#!/usr/bin/env bash
# EPIC Q's browser-side mutation self-test (#988), at the path everything names.
#
# The controls themselves are scripts/bdtools/assurance/ui_selftest.py. This
# file is the runnable shim. Unlike the Go self-tests it needs node, the shared
# UI's dependencies (`cd ui/shared && npm ci`) and Playwright with a browser
# (`npm i --no-save @playwright/test` there), so scripts/ci-local.sh does not
# run it on every commit; the matrix cites it, and the `recovery-captures` job's
# arrangement is the one that has all three.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/assurance/ui_selftest.py" "$@"

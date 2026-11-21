#!/usr/bin/env bash
# EPIC Q's browser-side mutation self-test (#988), at the path everything names.
#
# The controls themselves are scripts/bdtools/assurance/ui_selftest.py. This
# file is the runnable shim. Unlike the Go self-tests it needs node, the shared
# UI's dependencies (`cd ui/shared && npm ci`) and Playwright with a browser
# (`npm i --no-save @playwright/test` there), so scripts/ci-local.sh runs it in
# the Chromium lane (scripts/e2e/ui-browser.sh, the ui-browser shard, #1532),
# which brings all three in the pinned Playwright image. `--check-anchors` needs
# none of them, and scripts/selftest/check-anchors.sh runs it with the static
# checks.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/assurance/ui_selftest.py" "$@"

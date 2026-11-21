#!/usr/bin/env bash
# The Chromium lane's entry point (#1532, #1537). The lane itself is
# scripts/bdtools/e2e/ui_browser.py, which says what it runs and how it is
# bounded.
#
#   bash scripts/e2e/ui-browser.sh specs ui/shared/e2e/playwright.config.ts [playwright args...]
#   bash scripts/e2e/ui-browser.sh run bash scripts/assurance/ui-selftest.sh
#
# A file and not a symlink, and scripts/ci-local.sh calls this path rather
# than python3, for the reason scripts/e2e/run-tests-repo-gate.sh gives:
# scripts/tests/ci-local-gate.test.sh puts stand-ins at this literal path.
# `exec`, so the lane's 0, 1 or 3 reaches the gate untouched.
set -euo pipefail
exec python3 "$(cd "$(dirname "$0")/../.." && pwd)/scripts/bdtools/e2e/ui_browser.py" "$@"

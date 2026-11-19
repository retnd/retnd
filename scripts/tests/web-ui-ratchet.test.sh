#!/usr/bin/env bash
# The suite itself is scripts/bdtools/tests/web_ui_ratchet.py (#1260). This
# file exists at the path scripts/ci-local.sh runs and
# scripts/tests/ci-local-gate.test.sh fabricates a stand-in at, like every
# other *.test.sh shim here.
#
# `exec`, so the exit status and the checks/failures tally are the suite's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/web_ui_ratchet.py" "$@"

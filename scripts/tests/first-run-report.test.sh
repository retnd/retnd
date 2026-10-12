#!/usr/bin/env bash
# A skipped first-run spec fails the gate (retnd#1049). The suite is
# scripts/bdtools/tests/first_run_report.py; this file is the entry point
# scripts/ci-local.sh runs, like every other scripts/tests/*.test.sh.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/first_run_report.py" "$@"

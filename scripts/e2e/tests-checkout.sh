#!/usr/bin/env bash
# The pinned retnd-tests checkout for a gate shard's self-tests (#1548). The
# logic is scripts/bdtools/e2e/tests_checkout.py, which says where the checkout
# comes from and why no credential is involved. Prints the checkout's path on
# its last line.
#
# A file and not a symlink, and scripts/ci-local.sh calls this path rather than
# python3, for the reason scripts/e2e/run-tests-repo-gate.sh gives. `exec`, so
# the helper's 0, 1 or 3 reaches the gate untouched.
set -euo pipefail
exec python3 "$(cd "$(dirname "$0")/../.." && pwd)/scripts/bdtools/e2e/tests_checkout.py" "$@"

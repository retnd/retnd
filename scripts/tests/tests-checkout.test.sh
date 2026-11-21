#!/usr/bin/env bash
# The pinned retnd-tests checkout for the self-test shards (#1548). The cases
# are scripts/bdtools/tests/tests_checkout.py.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/tests/tests_checkout.py" "$@"

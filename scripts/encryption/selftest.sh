#!/usr/bin/env bash
# EPIC N and EPIC T's encryption falsifications (#998, #1320), at the path
# everything names.
#
# The controls themselves are scripts/bdtools/encryption/selftest.py. A PASS
# row in docs/conformance/epic-n-matrix.md or epic-t-matrix.md cites one of
# its labels, and core/tests/encryptiongate refuses a PASS whose label is not
# in that file. scripts/bdtools/selftest/check_anchors.py calls this with
# --check-anchors.
#
# `exec`, so the exit status is the self-test's own.
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
exec python3 "$repo_root/scripts/bdtools/encryption/selftest.py" "$@"

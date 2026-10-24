#!/usr/bin/env bash
# The release gate's check, at a path three callers share (#1260): the
# require-release-gate job in .github/workflows/release.yml, the hosted dry
# run (PUBLISH=false, report only), and scripts/release/record-local-gate.sh,
# which runs it over the record it has just written (--uncommitted). The
# check itself is scripts/bdtools/release/verify_local_gate.py; its header
# has the design.
#
# `exec`, so the exit status is the check's own: 0 allowed, 1 refused, 2 a
# command line or checkout it cannot work from.
#
# No `cd`, deliberately: the check reads the record off the commit named
# on its command line in the CURRENT checkout, which is what lets
# scripts/tests/release-refuses-an-ungated-publish.test.sh point it at a
# throwaway repository.
set -euo pipefail

exec python3 "$(cd "$(dirname "$0")/../.." && pwd)/scripts/bdtools/release/verify_local_gate.py" "$@"

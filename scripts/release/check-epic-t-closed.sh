#!/usr/bin/env bash
# The release's check that EPIC T (encryption suite agility, #1310) is closed
# (#1320). Run by the require-epic-t-closed job in .github/workflows/release.yml,
# and by hand with the same command:
#
#     bash scripts/release/check-epic-t-closed.sh
#
# It refuses, naming each failure, unless all of this holds:
#
#   1. the canonical deployment is an EMPTY diff against the commit the epic
#      started from: container/compose.yaml, the compose files under
#      apps/*/compose/, apps/unraid/template/, apps/truenas/catalog/, the four
#      distribution/packaging/*.json and apps/common/platform/capabilities/.
#      A suite is a property of the bytes a copy holds, so nothing this epic
#      ships changes how a container is run, and an untracked file in one of
#      those places counts as a change;
#   2. the epic added 0020_encryption_key_suites.sql (T1.2, the key's suite)
#      and no other migration, and nothing else under core/migrations/ moved;
#   3. the closing mode of core/tests/encryptiongate passes for EPIC T's
#      matrix: no BLOCKED row in docs/conformance/epic-t-matrix.md, every proof
#      it cites resolves, every PASS row cites a mutation that exists.
#
# It needs a checkout with history back to EPIC_T_BASE.
# scripts/tests/epic-t-closure.test.sh drives this script in throwaway
# repositories, once per failure and once for a clean run.
#
#   EPIC_T_ROOT     repository to check (default: the one this script is in)
#   EPIC_T_BASE     the commit the epic started from (default: 477aec5a, the
#                   merge of #1302 that put all of EPIC N on main, which is the
#                   tree the EPIC T specification was measured on)
#   EPIC_T_SKIP_GO  1 leaves out check 3's Go run (the self-test sets it; the
#                   Go checker has its own lie table)
set -uo pipefail

root="${EPIC_T_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
base="${EPIC_T_BASE:-477aec5a}"
cd "$root" || exit 2

fails=0
bad() { fails=$((fails + 1)); printf 'epic-t-closed: FAIL %s\n' "$1" >&2; }
ok() { printf 'epic-t-closed: ok   %s\n' "$1"; }

if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "epic-t-closed: the base commit $base is not in this checkout, so nothing can be compared to it (fetch the full history)" >&2
  exit 2
fi

# 1. The canonical deployment.
paths=(
  container/compose.yaml
  ':(glob)apps/*/compose/**'
  apps/unraid/template
  apps/truenas/catalog
  distribution/packaging/canonical.json
  distribution/packaging/compliance.json
  distribution/packaging/conformance.json
  distribution/packaging/submission.json
  apps/common/platform/capabilities
)
changed="$(git diff --name-only "$base" -- "${paths[@]}"; git ls-files --others --exclude-standard -- "${paths[@]}")"
if [ -n "$changed" ]; then
  bad "the canonical container contract moved since $base:$(printf '\n         %s' $changed)"
else
  ok "the canonical container contract is an empty diff against $base"
fi

# 2. The epic's one migration, and no other.
added="$(git diff --name-only --diff-filter=A "$base" -- core/migrations; git ls-files --others --exclude-standard -- core/migrations)"
touched="$(git diff --name-only --diff-filter=MDRT "$base" -- core/migrations)"
want20="core/migrations/0020_encryption_key_suites.sql"
unexpected="$(printf '%s\n' "$added" | grep -vxF -e "$want20" | grep -v '^$' || true)"
if printf '%s\n' "$added" | grep -qxF "$want20" && [ -z "$unexpected" ] && [ -z "$touched" ]; then
  ok "core/migrations gained only the epic's own 0020 and nothing else moved"
else
  bad "core/migrations must gain exactly $want20 and nothing else, and change nothing. Added: [$(printf '%s' "$added" | tr '\n' ' ')] Not allowed: [$(printf '%s' "$unexpected" | tr '\n' ' ')] Changed or removed: [$(printf '%s' "$touched" | tr '\n' ' ')]"
fi

# 3. The matrix.
if [ "${EPIC_T_SKIP_GO:-0}" = "1" ]; then
  echo "epic-t-closed: skipped the Go closing check (EPIC_T_SKIP_GO=1)"
else
  if (cd core && GOWORK=off RETND_EPIC_T_CLOSURE=1 go test -count=1 -run 'TestEpicTIsClosed' ./tests/encryptiongate/ -v 2>&1 | tee /dev/stderr | grep -q -- '--- PASS: TestEpicTIsClosed'); then
    ok "no BLOCKED row in the EPIC T matrix, and every proof resolves"
  else
    bad "the EPIC T closing check in core/tests/encryptiongate did not pass (output above)"
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "epic-t-closed: $fails failure(s). EPIC T is not closed, so this release does not go out." >&2
  exit 1
fi
echo "epic-t-closed: ok"

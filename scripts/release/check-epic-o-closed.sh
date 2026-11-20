#!/usr/bin/env bash
# The check that EPIC O (observability integrations, #898) is closed (#903).
# Run by hand with the same command the closing gate uses:
#
#     bash scripts/release/check-epic-o-closed.sh
#
# It refuses, naming each failure, unless all of this holds:
#
#   1. the canonical deployment is an EMPTY diff against the commit the epic
#      started from: container/compose.yaml, the compose files under
#      apps/*/compose/, apps/unraid/template/, apps/truenas/catalog/, the four
#      distribution/packaging/*.json and apps/common/platform/capabilities/.
#      /metrics is on the listener the engine already had, the bundle is an
#      example under container/observability/, and nothing this epic ships
#      changes how a container is run (matrix row O-15). A release version
#      bump alone is not a move (contract-unchanged.sh holds that rule for
#      every closing check);
#   2. core/migrations/ did not move at all: EPIC O stores nothing in the
#      journal (the scrape credential is a file), so the directory still ends
#      at 0022_agent_access.sql and nothing was added, changed or removed
#      (matrix row O-21);
#   3. no row of docs/conformance/epic-o-matrix.md is BLOCKED, in any of its
#      three tables (the detailed O-nn rows, the O1.n exit gate, the OV.n
#      ledger), named here row by row; and the closing mode of
#      core/tests/observabilitygate passes: every proof a row cites resolves,
#      every PASS is dated, every exit-gate or ledger PASS quotes a planted
#      violation that is really in a self-test, and no exit-gate row claims more
#      than the detailed rows it rests on;
#   4. every box under "## Phase 1 exit gate" in
#      docs/EPIC-O-observability-integrations.md is ticked. core/tests/compat
#      holds a box ticked exactly when its O1.n row is PASS, so this is the
#      refusal that makes a PARTIAL exit-gate line stop a close.
#
# What it does NOT prove: it re-runs no falsification. Whether a plant a PASS
# row quotes goes red is scripts/observability/selftest.sh's question (a gate
# step), and a BY HAND detailed row is accepted as long as it says who looked
# and when. PARTIAL and BY HAND detailed rows do not stop a close on their own;
# the exit-gate box they feed does.
#
# It needs a checkout with history back to EPIC_O_BASE.
# scripts/tests/epic-o-closure.test.sh drives this script in throwaway
# repositories, once per failure and once for a clean run.
#
#   EPIC_O_ROOT     repository to check (default: the one this script is in)
#   EPIC_O_BASE     the commit the epic started from (default: 89af3d93, the
#                   merge-base the spec records in its exit gate line 12)
#   EPIC_O_TIP      the commit to compare the migrations at (default: the work
#                   tree). Once EPIC O is closed, a later epic's own migration
#                   is that epic's business: set this to the merge that closed
#                   EPIC O, the way EPIC_N_TIP does for EPIC N
#   EPIC_O_SKIP_GO  1 leaves out check 3's Go run (the self-test sets it; the
#                   Go checker has its own lie table)
set -uo pipefail

root="${EPIC_O_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
base="${EPIC_O_BASE:-89af3d935a8b7667600cf52cbdbbea4f4fee9961}"
tip="${EPIC_O_TIP:-}"
here="$(cd "$(dirname "$0")" && pwd)"
cd "$root" || exit 2

fails=0
bad() { fails=$((fails + 1)); printf 'epic-o-closed: FAIL %s\n' "$1" >&2; }
ok() { printf 'epic-o-closed: ok   %s\n' "$1"; }

if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "epic-o-closed: the base commit $base is not in this checkout, so nothing can be compared to it (fetch the full history)" >&2
  exit 2
fi

# 1. The canonical container contract.
moved="$(bash "$here/contract-unchanged.sh" "$base")"
if [ -n "$moved" ]; then
  bad "the canonical container contract moved since $base (a release version bump alone is not a move):$(printf '\n         %s' $moved)"
else
  ok "the canonical container contract is an empty diff against $base, apart from the release version"
fi

# 2. No migration, added, changed or removed.
if [ -n "$tip" ]; then
  changed="$(git diff --name-status "$base" "$tip" -- core/migrations)"
else
  changed="$(git diff --name-status "$base" -- core/migrations; git ls-files --others --exclude-standard -- core/migrations | sed 's/^/??\t/')"
fi
if [ -z "$changed" ]; then
  ok "core/migrations did not move since $base (EPIC O stores nothing in the journal)"
else
  bad "core/migrations must not move for EPIC O, which stores nothing in the journal. Moved since $base: [$(printf '%s' "$changed" | tr '\t\n' '  ')]"
fi

# 3. The matrix. First the plain reading, so a BLOCKED row is named without a
# Go toolchain; then the Go checker, which also holds every proof and every PASS.
# The outcome is the first cell after the id that starts with an outcome word,
# because the detailed rows keep it second and the gate and ledger rows fifth.
matrix=docs/conformance/epic-o-matrix.md
if [ ! -f "$matrix" ]; then
  bad "$matrix does not exist"
else
  read -r rows blocked < <(awk -F'|' '
    function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
    {
      id = trim($2)
      if (id !~ /^(O-[0-9][0-9]|O1\.[0-9]+|OV\.[0-9]+)$/) next
      n++
      for (i = 3; i < NF; i++) {
        c = trim($i)
        if (c ~ /^(PASS|PARTIAL|BLOCKED|BY HAND)/) { if (c ~ /^BLOCKED/) b = b id ","; break }
      }
    }
    END { printf "%d %s\n", n + 0, (b == "" ? "-" : b) }
  ' "$matrix")
  if [ "${rows:-0}" -eq 0 ]; then
    bad "$matrix has no EPIC O row (O-nn, O1.n or OV.n) at all, so there is nothing to read as clean"
  elif [ "$blocked" != "-" ]; then
    bad "$matrix still has BLOCKED rows, and this epic does not close with one: $(printf '%s' "${blocked%,}" | tr ',' ' ')"
  else
    ok "no row of $matrix is BLOCKED ($rows rows read)"
  fi
fi

if [ "${EPIC_O_SKIP_GO:-0}" = "1" ]; then
  echo "epic-o-closed: skipped the Go closing check (EPIC_O_SKIP_GO=1)"
else
  # No `grep -q`: under pipefail it quits at the first match, tee takes SIGPIPE on the
  # rest of the output and a passing run reads as a failure.
  if (cd core && GOWORK=off RETND_EPIC_O_CLOSURE=1 go test -count=1 -run 'TestEpicOIsClosed' ./tests/observabilitygate/ -v 2>&1 | tee /dev/stderr | grep -- '--- PASS: TestEpicOIsClosed' >/dev/null); then
    ok "every proof the EPIC O matrix cites resolves, every PASS is dated and names its plant, and no gate row outruns its detailed rows"
  else
    bad "the EPIC O closing check in core/tests/observabilitygate did not pass (output above)"
  fi
fi

# 4. The spec's exit gate: every box ticked.
spec=docs/EPIC-O-observability-integrations.md
if [ ! -f "$spec" ]; then
  bad "$spec does not exist"
else
  boxes="$(awk '
    /^## Phase 1 exit gate[ \t]*$/ { inside = 1; next }
    inside && /^#/ { exit }
    inside && /^- \[[ x]\]/ { print }
  ' "$spec")"
  unticked="$(grep -E '^- \[ \]' <<<"$boxes" | cut -c1-120 || true)"
  if [ -z "$boxes" ]; then
    bad "$spec has no checkbox line under \"## Phase 1 exit gate\", so there is no exit gate to read as finished"
  elif [ -n "$unticked" ]; then
    bad "$spec has unticked exit-gate boxes, and this epic does not close with one:$(printf '\n         %s' "$unticked")"
  else
    ok "every box under \"## Phase 1 exit gate\" in $spec is ticked ($(grep -c . <<<"$boxes") lines)"
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "epic-o-closed: $fails failure(s). EPIC O is not closed." >&2
  exit 1
fi
echo "epic-o-closed: ok"

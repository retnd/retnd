#!/usr/bin/env bash
# The check that EPIC U (an MCP server for agents, #1343) is closed (#1353).
# Run by hand with the same command the closing gate uses:
#
#     bash scripts/release/check-epic-u-closed.sh
#
# It refuses, naming each failure, unless all of this holds:
#
#   1. the canonical deployment is an EMPTY diff against the commit the epic
#      started from: container/compose.yaml, the compose files under
#      apps/*/compose/, apps/unraid/template/, apps/truenas/catalog/, the four
#      distribution/packaging/*.json and apps/common/platform/capabilities/.
#      An agent is a client of /api/v1 and the engine is the same container
#      with the same mounts, so nothing this epic ships changes how a container
#      is run, and an untracked file in one of those places counts as a change;
#   2. the epic added exactly one migration, 0022_agent_access.sql (the three
#      agent tables), and nothing else under core/migrations/ moved;
#   3. no row of docs/conformance/epic-u-matrix.md is BLOCKED (named here, row
#      by row), and the closing mode of core/tests/agentgate passes: every proof
#      a row cites resolves, every PASS is dated and names a mutation that
#      exists;
#   4. every gifs[].name in docs/site/tools/agents-captures.json exists as a
#      .gif under docs/site/screens/ (the same check EPIC N holds for its
#      pictures), so the four agent clips cannot be missing at a release.
#
# It needs a checkout with history back to EPIC_U_BASE.
# scripts/tests/epic-u-closure.test.sh drives this script in throwaway
# repositories, once per failure and once for a clean run.
#
#   EPIC_U_ROOT     repository to check (default: the one this script is in)
#   EPIC_U_BASE     the commit the epic started from (default: 9cd53e1f, the
#                   merge of #1330, which is the tree the EPIC U specification
#                   was measured on)
#   EPIC_U_SKIP_GO  1 leaves out check 3's Go run (the self-test sets it; the
#                   Go checker has its own lie table)
set -uo pipefail

root="${EPIC_U_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
base="${EPIC_U_BASE:-9cd53e1f}"
cd "$root" || exit 2

fails=0
bad() { fails=$((fails + 1)); printf 'epic-u-closed: FAIL %s\n' "$1" >&2; }
ok() { printf 'epic-u-closed: ok   %s\n' "$1"; }

if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "epic-u-closed: the base commit $base is not in this checkout, so nothing can be compared to it (fetch the full history)" >&2
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
want="core/migrations/0022_agent_access.sql"
unexpected="$(printf '%s\n' "$added" | grep -vxF -e "$want" | grep -v '^$' || true)"
if printf '%s\n' "$added" | grep -qxF "$want" && [ -z "$unexpected" ] && [ -z "$touched" ]; then
  ok "core/migrations gained only the epic's own 0022 and nothing else moved"
else
  bad "core/migrations must gain exactly $want and nothing else, and change nothing. Added: [$(printf '%s' "$added" | tr '\n' ' ')] Not allowed: [$(printf '%s' "$unexpected" | tr '\n' ' ')] Changed or removed: [$(printf '%s' "$touched" | tr '\n' ' ')]"
fi

# 3. The matrix. First the plain reading, so a BLOCKED row is named without a
# Go toolchain; then the Go checker, which also holds every proof and every PASS.
matrix=docs/conformance/epic-u-matrix.md
if [ ! -f "$matrix" ]; then
  bad "$matrix does not exist"
else
  blocked="$(awk -F'|' '
    $2 ~ /^ *(U[12]\.[0-9]+|UC-[0-9]+|UV\.[0-9]+) *$/ && $6 ~ /^ *BLOCKED/ { gsub(/ /, "", $2); printf "%s ", $2 }
  ' "$matrix")"
  if [ -n "$blocked" ]; then
    bad "$matrix still has BLOCKED rows, and this epic does not close with one: $blocked"
  else
    ok "no row of $matrix is BLOCKED"
  fi
fi

if [ "${EPIC_U_SKIP_GO:-0}" = "1" ]; then
  echo "epic-u-closed: skipped the Go closing check (EPIC_U_SKIP_GO=1)"
else
  if (cd core && GOWORK=off RETND_EPIC_U_CLOSURE=1 go test -count=1 -run 'TestEpicUIsClosed' ./tests/agentgate/ -v 2>&1 | tee /dev/stderr | grep -q -- '--- PASS: TestEpicUIsClosed'); then
    ok "every proof the EPIC U matrix cites resolves, and every PASS is dated and names a mutation"
  else
    bad "the EPIC U closing check in core/tests/agentgate did not pass (output above)"
  fi
fi

# 4. The captures the manifest promises.
manifest=docs/site/tools/agents-captures.json
if [ ! -f "$manifest" ]; then
  bad "$manifest does not exist"
else
  names="$(jq -r '.gifs[].name' "$manifest" 2>/dev/null)"
  if [ -z "$names" ]; then
    bad "$manifest lists no gifs[].name, so there is nothing to hold the site to"
  else
    missing=""
    for n in $names; do
      [ -f "docs/site/screens/$n.gif" ] || missing="$missing $n.gif"
    done
    if [ -n "$missing" ]; then
      bad "docs/site/screens/ is missing clips that $manifest lists:$missing"
    else
      ok "every clip in $manifest exists under docs/site/screens/"
    fi
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "epic-u-closed: $fails failure(s). EPIC U is not closed, so this release does not go out." >&2
  exit 1
fi
echo "epic-u-closed: ok"

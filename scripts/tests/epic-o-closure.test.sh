#!/usr/bin/env bash
# The EPIC O closing check can fail (#903): a mutation self-test with a
# negative control for each guard in scripts/release/check-epic-o-closed.sh.
#
# Same shape as scripts/tests/epic-u-closure.test.sh, on purpose: every case
# plants one violation in a throwaway repository, runs the real script against
# it and asserts the distinct message, not only the exit status. The controls
# are the clean repository and a change beside the guarded places. There is one
# case per refusal the spec's exit gate line 19 names (a moved canonical
# deployment, a migration, a BLOCKED row) plus the one this epic adds (an
# exit-gate box in the spec left unticked), so removing any refusal turns its
# case red.
#
# The Go half (the matrix in closing mode, core/tests/observabilitygate) is not
# run here (EPIC_O_SKIP_GO=1); that package has its own lie table.
#
# Run directly (`bash scripts/tests/epic-o-closure.test.sh`) or let the gate run
# it: scripts/ci-local.sh invokes it. No Docker, no network, a few seconds.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="$REPO_ROOT/scripts/release/check-epic-o-closed.sh"

echo "==> EPIC O closing check: each guard fires on its violation and stays quiet on the control"

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1)); failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  [ $# -gt 1 ] && printf '%s\n' "$2" | sed 's/^/         | /' >&2
}

[ -f "$CHECK" ] || { fail "scripts/release/check-epic-o-closed.sh exists"; echo "epic-o-closure: $checks checks, $failures failures"; exit 1; }

SANDBOX="$(mktemp -d "${TMPDIR:-/tmp}/epic-o-closure.XXXXXX")"
case "$SANDBOX" in
  /*/epic-o-closure.*) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
trap 'rm -rf "$SANDBOX"' EXIT

# The matrix has three tables that carry an outcome, in two shapes: the detailed
# O-nn rows (`# | Outcome | Certifies | Where | Falsification`), and the exit gate
# and ledger (`ID | Claim | Executable proof | Falsification | Status | Waits on`).
# $1 is the outcome of the detailed row, $2 of the gate row, $3 of the ledger row.
matrix() {
  printf '%s\n' \
    '| # | Outcome | Certifies | Where | Falsification |' \
    '| --- | --- | --- | --- | --- |' \
    "| O-01 | $1 | a claim | \`pkg/a_test.go\` | a plant, run 2026-11-20 |" \
    '' \
    '| ID | Claim | Executable proof | Falsification | Status | Waits on |' \
    '|---|---|---|---|---|---|' \
    "| O1.1 | a claim (O-01) | \`pkg:TestReal\` | a plant, run 2026-11-20 | $2 | - |" \
    "| OV.1 | a guard | \`pkg:TestReal\` | a plant, run 2026-11-20 | $3 | - |"
}

# $1 is the box of the spec's one exit-gate line.
spec() {
  printf '%s\n' '# spec' '' '## Phase 1 exit gate' '' "- $1 **A claim.**" '' '## Sub-issues' '' '- [ ] not an exit-gate box'
}

# A repository with every guarded place present and the migrations up to 0022
# in it, committed as the base. $1 names it. Prints its path.
make_repo() {
  local dir="$SANDBOX/$1"
  mkdir -p "$dir" && cd "$dir" || return 1
  git init -q . && git config user.name t && git config user.email t@example.invalid
  mkdir -p container apps/a/compose apps/b/compose apps/unraid/template apps/truenas/catalog \
    distribution/packaging apps/common/platform/capabilities core/migrations apps/a/docs docs/conformance
  echo "services: {}" > container/compose.yaml
  echo "a: 1" > apps/a/compose/compose.yaml
  echo "b: 1" > apps/b/compose/compose.yaml
  echo "<x/>" > apps/unraid/template/retnd.xml
  echo "name: x" > apps/truenas/catalog/app.yaml
  for f in canonical compliance conformance submission; do echo "{}" > "distribution/packaging/$f.json"; done
  echo "package capabilities" > apps/common/platform/capabilities/capabilities.go
  echo "-- 21" > core/migrations/0021_encryption_default_suite.sql
  echo "-- 22" > core/migrations/0022_agent_access.sql
  echo "readme" > apps/a/docs/README.md
  git add -A && git commit -q -m base
  git rev-parse HEAD > "$SANDBOX/$1.base"
  matrix PASS PASS PASS > docs/conformance/epic-o-matrix.md
  spec "[x]" > docs/EPIC-O-observability-integrations.md
  git add -A && git commit -q -m epic
  cd "$SANDBOX" || return 1
  printf '%s' "$dir"
}

run_check() { (cd "$1" && EPIC_O_ROOT="$1" EPIC_O_BASE="${BASE_OVERRIDE:-$(cat "$SANDBOX/$(basename "$1").base")}" EPIC_O_SKIP_GO=1 bash "$CHECK" 2>&1); }

expect_pass() { # name repo
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -eq 0 ] && grep -q 'epic-o-closed: ok$' <<<"$out"; then pass "$1"; else fail "$1" "$out"; fi
}
expect_fail() { # name repo message
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -ne 0 ] && grep -qF "$3" <<<"$out"; then pass "$1"; else fail "$1" "exit $rc, wanted: $3
$out"; fi
}

# Control: no migration, a matrix with no BLOCKED row, every box ticked.
r="$(make_repo control)"
expect_pass "control: no migration, no BLOCKED row and every exit-gate box ticked pass" "$r"

# Control: PARTIAL and BY HAND are outcomes the epic may close with (the check
# refuses BLOCKED, and only the spec box says whether a line is finished).
r="$(make_repo partial)"
matrix "BY HAND" PASS PASS > "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_pass "control: a BY HAND detailed row is not refused by the closing check" "$r"

# Control: a change outside every guarded place.
r="$(make_repo outside)"
echo "more" >> "$r/apps/a/docs/README.md"; (cd "$r" && git commit -qam "docs")
expect_pass "control: a change beside the guarded places does not fire the guard" "$r"

# Control: a box outside the exit gate is not the exit gate's business.
r="$(make_repo other-box)"
printf '%s\n' '' '- [ ] a later section box' >> "$r/docs/EPIC-O-observability-integrations.md"; (cd "$r" && git commit -qam x)
expect_pass "control: an unticked box after the exit gate's section is not read as an exit-gate box" "$r"

# Guard 1, the canonical deployment is an empty diff. One violation per group.
msg='the canonical container contract moved'
r="$(make_repo g1-compose)"; echo "ports: ['9100:8080']" >> "$r/container/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: a port added to container/compose.yaml is refused" "$r" "$msg"
r="$(make_repo g1-app-compose)"; echo "c: 1" >> "$r/apps/b/compose/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit under apps/*/compose is refused, and named" "$r" "apps/b/compose/compose.yaml"
r="$(make_repo g1-new-compose)"; echo "n: 1" > "$r/apps/a/compose/compose.observability.yaml"
expect_fail "the canonical deployment: a new, untracked file under apps/*/compose is refused" "$r" "apps/a/compose/compose.observability.yaml"
r="$(make_repo g1-unraid)"; echo "<y/>" >> "$r/apps/unraid/template/retnd.xml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to the Unraid template is refused" "$r" "apps/unraid/template/retnd.xml"
r="$(make_repo g1-truenas)"; echo "v: 2" >> "$r/apps/truenas/catalog/app.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to the TrueNAS catalog is refused" "$r" "apps/truenas/catalog/app.yaml"
for f in canonical compliance conformance submission; do
  r="$(make_repo "g1-$f")"; echo '{"x":1}' > "$r/distribution/packaging/$f.json"; (cd "$r" && git commit -qam x)
  expect_fail "the canonical deployment: an edit to packaging/$f.json is refused" "$r" "distribution/packaging/$f.json"
done
r="$(make_repo g1-caps)"; echo "// x" >> "$r/apps/common/platform/capabilities/capabilities.go"; (cd "$r" && git commit -qam x)
expect_fail "the capability contract: an edit under apps/common/platform/capabilities is refused" "$r" "apps/common/platform/capabilities/capabilities.go"

# Guard 2, no migration: EPIC O stores nothing in the journal.
msg='core/migrations must not move'
r="$(make_repo g2-added)"; echo "-- 23" > "$r/core/migrations/0023_scrape_tokens.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a new migration is refused, and named" "$r" "0023_scrape_tokens.sql"
r="$(make_repo g2-untracked)"; echo "-- 23" > "$r/core/migrations/0023_scrape_tokens.sql"
expect_fail "migrations: a new, untracked migration is refused" "$r" "$msg"
r="$(make_repo g2-edit)"; echo "-- changed" >> "$r/core/migrations/0022_agent_access.sql"; (cd "$r" && git commit -qam x)
expect_fail "migrations: an edit to an earlier migration is refused, and named" "$r" "0022_agent_access.sql"

# Guard 3, the matrix: a BLOCKED row in any of the three tables is named, an
# absent matrix is refused.
r="$(make_repo g3-detail)"; matrix "BLOCKED (#903)" PASS PASS > "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a BLOCKED detailed row is refused, and named" "$r" "BLOCKED rows, and this epic does not close with one: O-01"
r="$(make_repo g3-gate)"; matrix PASS "BLOCKED (#903)" PASS > "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a BLOCKED exit-gate row is refused, and named" "$r" "O1.1"
r="$(make_repo g3-ledger)"; matrix PASS PASS "BLOCKED (#903)" > "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a BLOCKED ledger row is refused, and named" "$r" "OV.1"
r="$(make_repo g3-absent)"; rm "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a missing matrix file is refused" "$r" "docs/conformance/epic-o-matrix.md does not exist"
r="$(make_repo g3-empty)"; printf '| # | Outcome |\n|---|---|\n' > "$r/docs/conformance/epic-o-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a matrix with no row at all is refused rather than read as clean" "$r" "no EPIC O row"

# Guard 4, the spec's exit gate: every box ticked, and a gate with no box is
# not read as a finished one.
r="$(make_repo g4-unticked)"; spec "[ ]" > "$r/docs/EPIC-O-observability-integrations.md"; (cd "$r" && git commit -qam x)
expect_fail "the spec: an unticked exit-gate box is refused, and quoted" "$r" "A claim."
r="$(make_repo g4-no-boxes)"; printf '%s\n' '## Phase 1 exit gate' '' '1. **A claim.**' > "$r/docs/EPIC-O-observability-integrations.md"; (cd "$r" && git commit -qam x)
expect_fail "the spec: an exit gate with no checkbox line is refused" "$r" "no checkbox line"

# The base commit.
r="$(make_repo nobase)"
out="$(BASE_OVERRIDE=0000000000000000000000000000000000000000 run_check "$r")"; rc=$?
if [ "$rc" -eq 2 ] && grep -q 'is not in this checkout' <<<"$out"; then pass "a base commit that is not in the checkout is refused, not skipped"; else fail "a missing base is refused" "exit $rc: $out"; fi

echo "epic-o-closure: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

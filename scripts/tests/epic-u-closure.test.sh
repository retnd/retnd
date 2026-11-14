#!/usr/bin/env bash
# The EPIC U closing check can fail (#1353): a mutation self-test with a
# negative control for each guard in scripts/release/check-epic-u-closed.sh.
#
# Same shape as scripts/tests/epic-t-closure.test.sh, on purpose: every case
# plants one violation in a throwaway repository, runs the real script against
# it and asserts the distinct message, not only the exit status. The controls
# are the clean repository and a change beside the guarded places. There is one
# case per refusal the issue names (a moved compose file, a second migration, a
# BLOCKED row) and one clean case, so removing any refusal turns its case red.
#
# The Go half (the matrix in closing mode) is not run here (EPIC_U_SKIP_GO=1);
# core/tests/agentgate has its own lie table for it.
#
# Run directly (`bash scripts/tests/epic-u-closure.test.sh`) or let the gate run
# it: scripts/ci-local.sh invokes it. No Docker, no network, a few seconds.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="$REPO_ROOT/scripts/release/check-epic-u-closed.sh"

echo "==> EPIC U closing check: each guard fires on its violation and stays quiet on the control"

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1)); failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  [ $# -gt 1 ] && printf '%s\n' "$2" | sed 's/^/         | /' >&2
}

[ -f "$CHECK" ] || { fail "scripts/release/check-epic-u-closed.sh exists"; echo "epic-u-closure: $checks checks, $failures failures"; exit 1; }

SANDBOX="$(mktemp -d "${TMPDIR:-/tmp}/epic-u-closure.XXXXXX")"
case "$SANDBOX" in
  /*/epic-u-closure.*) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
trap 'rm -rf "$SANDBOX"' EXIT

matrix_table() { # $1 = status cell of the one exit-gate row
  printf '%s\n' \
    '| ID | Claim | Executable proof | Falsification | Status | Waits on |' \
    '|---|---|---|---|---|---|' \
    "| U1.1 | a claim | \`pkg:TestReal\` | a mutation, run 2026-11-11 | $1 | - |" \
    '| UV.1 | a guard | `pkg:TestReal` | a mutation | PASS | - |'
}

# A repository with every guarded place present and the migrations up to 0021
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
  echo "-- 15" > core/migrations/0015_retained_copy_encryption.sql
  echo "-- 21" > core/migrations/0021_encryption_default_suite.sql
  echo "readme" > apps/a/docs/README.md
  git add -A && git commit -q -m base
  git rev-parse HEAD > "$SANDBOX/$1.base"
  matrix_table "PASS" > docs/conformance/epic-u-matrix.md
  mkdir -p docs/site/tools docs/site/screens
  echo '{"gifs":[{"name":"agents-mint"},{"name":"agents-audit"}]}' > docs/site/tools/agents-captures.json
  : > docs/site/screens/agents-mint.gif; : > docs/site/screens/agents-audit.gif
  cd "$SANDBOX" || return 1
  printf '%s' "$dir"
}

run_check() { (cd "$1" && EPIC_U_ROOT="$1" EPIC_U_BASE="${BASE_OVERRIDE:-$(cat "$SANDBOX/$(basename "$1").base")}" EPIC_U_SKIP_GO=1 bash "$CHECK" 2>&1); }
add_0022() { echo "-- 22" > "$1/core/migrations/0022_agent_access.sql"; (cd "$1" && git add -A && git commit -q -m "epic"); }

expect_pass() { # name repo
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'epic-u-closed: ok$'; then pass "$1"; else fail "$1" "$out"; fi
}
expect_fail() { # name repo message
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "$3"; then pass "$1"; else fail "$1" "exit $rc, wanted: $3
$out"; fi
}

# Control: the epic's one migration, a matrix with no BLOCKED row, nothing else.
r="$(make_repo control)"; add_0022 "$r"
expect_pass "control: the new migration 0022 and a matrix with no BLOCKED row pass" "$r"

# Control: a change outside every guarded place.
r="$(make_repo outside)"; add_0022 "$r"
echo "more" >> "$r/apps/a/docs/README.md"; (cd "$r" && git commit -qam "docs")
expect_pass "control: a change beside the guarded places does not fire the guard" "$r"

# Guard 1, the canonical deployment is an empty diff. One violation per group.
msg='the canonical container contract moved'
r="$(make_repo g1-compose)"; add_0022 "$r"; echo "ports: ['9999:9999']" >> "$r/container/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to container/compose.yaml is refused" "$r" "$msg"
r="$(make_repo g1-app-compose)"; add_0022 "$r"; echo "c: 1" >> "$r/apps/b/compose/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to a file under apps/*/compose is refused" "$r" "apps/b/compose/compose.yaml"
r="$(make_repo g1-new-compose)"; add_0022 "$r"; echo "n: 1" > "$r/apps/a/compose/extra.yaml"
expect_fail "the canonical deployment: a new, untracked file under apps/*/compose is refused" "$r" "apps/a/compose/extra.yaml"
r="$(make_repo g1-unraid)"; add_0022 "$r"; echo "<y/>" >> "$r/apps/unraid/template/retnd.xml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to the Unraid template is refused" "$r" "apps/unraid/template/retnd.xml"
r="$(make_repo g1-truenas)"; add_0022 "$r"; echo "v: 2" >> "$r/apps/truenas/catalog/app.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical deployment: an edit to the TrueNAS catalog is refused" "$r" "apps/truenas/catalog/app.yaml"
for f in canonical compliance conformance submission; do
  r="$(make_repo "g1-$f")"; add_0022 "$r"; echo '{"x":1}' > "$r/distribution/packaging/$f.json"; (cd "$r" && git commit -qam x)
  expect_fail "the canonical deployment: an edit to packaging/$f.json is refused" "$r" "distribution/packaging/$f.json"
done
r="$(make_repo g1-caps)"; add_0022 "$r"; echo "// x" >> "$r/apps/common/platform/capabilities/capabilities.go"; (cd "$r" && git commit -qam x)
expect_fail "the capability contract: an edit under apps/common/platform/capabilities is refused" "$r" "apps/common/platform/capabilities/capabilities.go"

# Guard 1 and a release cut (#1407). Cutting a release moves the image tag in
# every guarded file and canonical.json's release record, and none of that
# changes how a container is run, so a cut alone passes and a real change hidden
# inside a cut is still refused (the same rule the N and T checks hold).
seed_release() { # repo version published -> the guarded files carry that release
  local v="$2" pub="$3"
  printf 'services:\n  retnd:\n    image: ${IMAGE:-ghcr.io/retnd/retnd:%s}\n    ports: ["8080:8080"]\n' "$v" > "$1/container/compose.yaml"
  printf 'services:\n  retnd:\n    image: ghcr.io/retnd/retnd:%s\n' "$v" > "$1/apps/a/compose/compose.yaml"
  printf 'IMAGE=ghcr.io/retnd/retnd:%s\n' "$v" > "$1/apps/b/compose/.env"
  printf '<x>\n  <Repository>ghcr.io/retnd/retnd:%s</Repository>\n</x>\n' "$v" > "$1/apps/unraid/template/retnd.xml"
  printf '{\n  "image": {\n    "registry": "ghcr.io",\n    "repository": "retnd/retnd",\n    "tag": "%s",\n    "reference": "ghcr.io/retnd/retnd:%s",\n    "published": %s,\n    "note": ["%s is the release this names, published=%s"]\n  },\n  "listenPort": 8080\n}\n' "$v" "$v" "$pub" "$v" "$pub" > "$1/distribution/packaging/canonical.json"
}
make_cut_repo() { # name -> a repository whose base carries 0.7.2, with the epic's migration, cut to 0.8.0
  local r
  r="$(make_repo "$1")"
  seed_release "$r" 0.7.2 true
  (cd "$r" && git add -A && git commit -q -m "0.7.2" && git rev-parse HEAD > "$SANDBOX/$1.base")
  add_0022 "$r"
  seed_release "$r" 0.8.0 false
  printf '%s' "$r"
}
r="$(make_cut_repo cut-control)"; (cd "$r" && git commit -qam "cut 0.8.0")
expect_pass "control: a release version bump alone is not a move" "$r"
r="$(make_cut_repo cut-uncommitted)"
expect_pass "control: the same cut, not yet committed, passes" "$r"
r="$(make_cut_repo cut-port)"; sed -i.bak 's/8080:8080/9999:8080/' "$r/container/compose.yaml" && rm -f "$r/container/compose.yaml.bak"; (cd "$r" && git commit -qam x)
expect_fail "a release cut that also moves a port in container/compose.yaml is refused, and the file named" "$r" "container/compose.yaml"
r="$(make_cut_repo cut-latest)"; printf 'services:\n  retnd:\n    image: ghcr.io/retnd/retnd:latest\n' > "$r/apps/a/compose/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "an image tag moved off a release version to latest is refused" "$r" "apps/a/compose/compose.yaml"
r="$(make_cut_repo cut-field)"; sed -i.bak 's/"listenPort": 8080/"listenPort": 8081/' "$r/distribution/packaging/canonical.json" && rm -f "$r/distribution/packaging/canonical.json.bak"; (cd "$r" && git commit -qam x)
expect_fail "canonical.json: a cut that also changes a field outside the release record is refused" "$r" "distribution/packaging/canonical.json"
r="$(make_cut_repo cut-removed)"; (cd "$r" && git rm -qf apps/unraid/template/retnd.xml && git commit -qam x)
expect_fail "a cut that removes a guarded file is refused" "$r" "apps/unraid/template/retnd.xml"

# Guard 2, the migration claim: 0022 and nothing else.
msg='core/migrations must gain exactly'
r="$(make_repo g2-none)"
expect_fail "migrations: an epic that added no 0022 is refused" "$r" "$msg"
r="$(make_repo g2-second)"; add_0022 "$r"; echo "-- 23" > "$r/core/migrations/0023_extra.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a second new migration is refused, and named" "$r" "0023_extra.sql"
r="$(make_repo g2-edit-earlier)"; add_0022 "$r"; echo "-- changed" >> "$r/core/migrations/0021_encryption_default_suite.sql"; (cd "$r" && git commit -qam x)
expect_fail "migrations: an edit to an earlier migration is refused" "$r" "0021_encryption_default_suite.sql"
r="$(make_repo g2-wrong-name)"; echo "-- 22" > "$r/core/migrations/0022_agents.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a 0022 under another name is refused" "$r" "$msg"

# Guard 3, the matrix: a BLOCKED row is named, an absent matrix is refused.
r="$(make_repo g3-blocked)"; add_0022 "$r"
matrix_table "BLOCKED (#1343)" > "$r/docs/conformance/epic-u-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a BLOCKED row is refused, and named" "$r" "BLOCKED rows, and this epic does not close with one: U1.1"
r="$(make_repo g3-blocked-ledger)"; add_0022 "$r"
{ matrix_table "PASS"; echo '| UV.7 | a guard | `pkg:TestReal` | a mutation | BLOCKED (#1343) | #1353 |'; } > "$r/docs/conformance/epic-u-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a BLOCKED row in the violation ledger is refused, and named" "$r" "UV.7"
r="$(make_repo g3-absent)"; add_0022 "$r"; rm "$r/docs/conformance/epic-u-matrix.md"; (cd "$r" && git commit -qam x)
expect_fail "the matrix: a missing matrix file is refused" "$r" "docs/conformance/epic-u-matrix.md does not exist"

# Guard 4, the captures: a clip the manifest lists but the screens folder lacks.
r="$(make_repo g4-missing-clip)"; add_0022 "$r"; rm "$r/docs/site/screens/agents-audit.gif"
expect_fail "the captures: a manifest clip missing from docs/site/screens is refused, and named" "$r" "missing clips"
r="$(make_repo g4-no-manifest)"; add_0022 "$r"; rm "$r/docs/site/tools/agents-captures.json"
expect_fail "the captures: a missing manifest is refused" "$r" "agents-captures.json does not exist"

# The base commit.
r="$(make_repo nobase)"
out="$(BASE_OVERRIDE=0000000000000000000000000000000000000000 run_check "$r")"; rc=$?
if [ "$rc" -eq 2 ] && printf '%s' "$out" | grep -q 'is not in this checkout'; then pass "a base commit that is not in the checkout is refused, not skipped"; else fail "a missing base is refused" "exit $rc: $out"; fi

echo "epic-u-closure: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

#!/usr/bin/env bash
# The EPIC N closing check can fail (#998): a mutation self-test with a
# negative control for each guard in scripts/release/check-epic-n-closed.sh.
#
# A guard that only ever passes is as useless as one that never does, and an
# assertion that something did NOT change is the shape that rots silently. So
# every case below plants one violation in a throwaway repository, runs the
# real script against it, and asserts the distinct message rather than only the
# exit status. The controls are the clean repository (the script must pass) and
# changes OUTSIDE the guarded places (the script must not fire on them).
#
# The Go half of the closing check (the matrix, the capture manifest) is not run
# here (EPIC_N_SKIP_GO=1); core/tests/encryptiongate has its own self-test.
#
# Run directly (`bash scripts/tests/epic-n-closure.test.sh`) or let the gate run
# it: scripts/ci-local.sh invokes it. No Docker, no network, a few seconds.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
CHECK="$REPO_ROOT/scripts/release/check-epic-n-closed.sh"

echo "==> EPIC N closing check: each guard fires on its violation and stays quiet on the control"

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1)); failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  [ $# -gt 1 ] && printf '%s\n' "$2" | sed 's/^/         | /' >&2
}

[ -f "$CHECK" ] || { fail "scripts/release/check-epic-n-closed.sh exists"; echo "epic-n-closure: $checks checks, $failures failures"; exit 1; }

SANDBOX="$(mktemp -d "${TMPDIR:-/tmp}/epic-n-closure.XXXXXX")"
case "$SANDBOX" in
  /*/epic-n-closure.*) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
trap 'rm -rf "$SANDBOX"' EXIT

# A repository with every guarded place present, committed as the base.
# $1 names it. Prints its path; the base commit is in $BASE_SHA.
make_repo() {
  local dir="$SANDBOX/$1"
  mkdir -p "$dir" && cd "$dir" || return 1
  git init -q . && git config user.name t && git config user.email t@example.invalid
  mkdir -p container apps/a/compose apps/b/compose apps/unraid/template apps/truenas/catalog \
    distribution/packaging apps/common/platform/capabilities core/migrations docs apps/a/docs
  echo "services: {}" > container/compose.yaml
  echo "a: 1" > apps/a/compose/compose.yaml
  echo "b: 1" > apps/b/compose/compose.yaml
  echo "<x/>" > apps/unraid/template/retnd.xml
  echo "name: x" > apps/truenas/catalog/app.yaml
  for f in canonical compliance conformance submission; do echo "{}" > "distribution/packaging/$f.json"; done
  echo "package capabilities" > apps/common/platform/capabilities/capabilities.go
  echo "-- 1" > core/migrations/0001_init.sql
  echo "-- 16" > core/migrations/0016_copy_assurance.sql
  echo "readme" > apps/a/docs/README.md
  git add -A && git commit -q -m base
  BASE_SHA="$(git rev-parse HEAD)"
  printf '%s' "$BASE_SHA" > "$SANDBOX/$1.base"
  cd "$SANDBOX" || return 1
  printf '%s' "$dir"
}

run_check() { (cd "$1" && EPIC_N_ROOT="$1" EPIC_N_BASE="${BASE_SHA:-$(cat "$SANDBOX/$(basename "$1").base")}" EPIC_N_SKIP_GO=1 bash "$CHECK" 2>&1); }
add_0015() { echo "-- 15" > "$1/core/migrations/0015_retained_copy_encryption.sql"; (cd "$1" && git add -A && git commit -q -m "epic"); }

expect_pass() { # name repo
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'epic-n-closed: ok$'; then pass "$1"; else fail "$1" "$out"; fi
}
expect_fail() { # name repo message
  local out rc
  out="$(run_check "$2")"; rc=$?
  if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "$3"; then pass "$1"; else fail "$1" "exit $rc, wanted: $3
$out"; fi
}

# Control: the epic's one migration and nothing else.
r="$(make_repo control)"; add_0015 "$r"
expect_pass "control: one new migration 0015 and nothing guarded touched passes" "$r"

# Control: a change outside every guarded place.
r="$(make_repo outside)"; add_0015 "$r"
echo "more" >> "$r/apps/a/docs/README.md"; (cd "$r" && git commit -qam "docs")
expect_pass "control: a change beside the guarded places does not fire the guard" "$r"

# Guard 1, one violation per group.
msg='the canonical container contract moved'
r="$(make_repo g1-compose)"; add_0015 "$r"; echo "ports: ['9999:9999']" >> "$r/container/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical container contract: an edit to container/compose.yaml is refused" "$r" "$msg"
r="$(make_repo g1-app-compose)"; add_0015 "$r"; echo "c: 1" >> "$r/apps/b/compose/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical container contract: an edit to a file under apps/*/compose is refused" "$r" "apps/b/compose/compose.yaml"
r="$(make_repo g1-new-compose)"; add_0015 "$r"; echo "n: 1" > "$r/apps/a/compose/extra.yaml"
expect_fail "the canonical container contract: a new, untracked file under apps/*/compose is refused" "$r" "apps/a/compose/extra.yaml"
r="$(make_repo g1-unraid)"; add_0015 "$r"; echo "<y/>" >> "$r/apps/unraid/template/retnd.xml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical container contract: an edit to the Unraid template is refused" "$r" "apps/unraid/template/retnd.xml"
r="$(make_repo g1-truenas)"; add_0015 "$r"; echo "v: 2" >> "$r/apps/truenas/catalog/app.yaml"; (cd "$r" && git commit -qam x)
expect_fail "the canonical container contract: an edit to the TrueNAS catalog is refused" "$r" "apps/truenas/catalog/app.yaml"
for f in canonical compliance conformance submission; do
  r="$(make_repo "g1-$f")"; add_0015 "$r"; echo '{"x":1}' > "$r/distribution/packaging/$f.json"; (cd "$r" && git commit -qam x)
  expect_fail "the canonical container contract: an edit to packaging/$f.json is refused" "$r" "distribution/packaging/$f.json"
done
r="$(make_repo g1-caps)"; add_0015 "$r"; echo "// x" >> "$r/apps/common/platform/capabilities/capabilities.go"; (cd "$r" && git commit -qam x)
expect_fail "the capability contract: an edit under apps/common/platform/capabilities is refused" "$r" "apps/common/platform/capabilities/capabilities.go"

# Guard 2, the migration claim.
msg='core/migrations must gain exactly'
r="$(make_repo g2-none)"
expect_fail "migrations: an epic that added no 0015 is refused" "$r" "$msg"
r="$(make_repo g2-extra)"; add_0015 "$r"; echo "-- 19" > "$r/core/migrations/0019_extra.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a second new migration is refused" "$r" "0019_extra.sql"
r="$(make_repo g2-edit)"; add_0015 "$r"; echo "-- changed" >> "$r/core/migrations/0001_init.sql"; (cd "$r" && git commit -qam x)
expect_fail "migrations: an edit to an existing migration is refused" "$r" "0001_init.sql"
r="$(make_repo g2-wrong-number)"; echo "-- 17" > "$r/core/migrations/0017_retained_copy_encryption.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a new migration with another number is refused" "$r" "$msg"

# The base commit.
r="$(make_repo nobase)"; BASE_SHA=0000000000000000000000000000000000000000
out="$(run_check "$r")"; rc=$?
if [ "$rc" -eq 2 ] && printf '%s' "$out" | grep -q 'is not in this checkout'; then pass "a base commit that is not in the checkout is refused, not skipped"; else fail "a missing base is refused" "exit $rc: $out"; fi

echo "epic-n-closure: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

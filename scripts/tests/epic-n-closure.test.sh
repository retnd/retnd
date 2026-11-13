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

run_check() { (cd "$1" && EPIC_N_ROOT="$1" EPIC_N_BASE="${BASE_SHA:-$(cat "$SANDBOX/$(basename "$1").base")}" EPIC_N_TIP="${TIP_SHA:-WORKTREE}" EPIC_N_SKIP_GO="${SKIP_GO:-1}" PATH="${STUB_BIN:+$STUB_BIN:}$PATH" bash "$CHECK" 2>&1); }
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

# Guard 1 and a release cut (#1331). Cutting a release moves the image tag in
# every guarded file and canonical.json's release record (tag, reference,
# published, note), and none of that changes how a container is run. A guard
# that read file names failed every cut, so the cut alone is the control here,
# and each case after it hides one real contract change inside a cut.
seed_release() { # repo version published -> the guarded files carry that release
  local v="$2" pub="$3"
  printf 'services:\n  retnd:\n    image: ${IMAGE:-ghcr.io/retnd/retnd:%s}\n    ports: ["8080:8080"]\n' "$v" > "$1/container/compose.yaml"
  printf 'services:\n  retnd:\n    image: ghcr.io/retnd/retnd:%s\n' "$v" > "$1/apps/a/compose/compose.yaml"
  printf 'IMAGE=ghcr.io/retnd/retnd:%s\n' "$v" > "$1/apps/b/compose/.env"
  printf '<x>\n  <Repository>ghcr.io/retnd/retnd:%s</Repository>\n</x>\n' "$v" > "$1/apps/unraid/template/retnd.xml"
  printf '{\n  "image": {\n    "registry": "ghcr.io",\n    "repository": "retnd/retnd",\n    "tag": "%s",\n    "reference": "ghcr.io/retnd/retnd:%s",\n    "published": %s,\n    "note": ["%s is the release this names, published=%s"]\n  },\n  "listenPort": 8080\n}\n' "$v" "$v" "$pub" "$v" "$pub" > "$1/distribution/packaging/canonical.json"
}
make_cut_repo() { # name -> a repository whose base carries 0.7.2, with the epic's migrations, cut to 0.8.0
  local r
  r="$(make_repo "$1")"
  seed_release "$r" 0.7.2 true
  (cd "$r" && git add -A && git commit -q -m "0.7.2" && git rev-parse HEAD > "$SANDBOX/$1.base")
  add_0015 "$r"
  seed_release "$r" 0.8.0 false
  printf '%s' "$r"
}
r="$(make_cut_repo cut-control)"; (cd "$r" && git commit -qam "cut 0.8.0")
expect_pass "control: a release cut that moves only the image tag and canonical.json's release record passes" "$r"
r="$(make_cut_repo cut-uncommitted)"
expect_pass "control: the same cut, not yet committed, passes" "$r"
r="$(make_cut_repo cut-port)"; sed -i.bak 's/8080:8080/9999:8080/' "$r/container/compose.yaml" && rm -f "$r/container/compose.yaml.bak"; (cd "$r" && git commit -qam x)
expect_fail "a release cut that also moves a port in container/compose.yaml is refused, and the file named" "$r" "container/compose.yaml"
r="$(make_cut_repo cut-latest)"; printf 'services:\n  retnd:\n    image: ghcr.io/retnd/retnd:latest\n' > "$r/apps/a/compose/compose.yaml"; (cd "$r" && git commit -qam x)
expect_fail "an image tag moved off a release version to latest is refused" "$r" "apps/a/compose/compose.yaml"
r="$(make_cut_repo cut-registry)"; sed -i.bak 's#"registry": "ghcr.io"#"registry": "docker.io"#' "$r/distribution/packaging/canonical.json" && rm -f "$r/distribution/packaging/canonical.json.bak"; (cd "$r" && git commit -qam x)
expect_fail "canonical.json: a cut that also changes the registry is refused" "$r" "distribution/packaging/canonical.json"
r="$(make_cut_repo cut-field)"; sed -i.bak 's/"listenPort": 8080/"listenPort": 8081/' "$r/distribution/packaging/canonical.json" && rm -f "$r/distribution/packaging/canonical.json.bak"; (cd "$r" && git commit -qam x)
expect_fail "canonical.json: a cut that also changes a field outside the release record is refused" "$r" "distribution/packaging/canonical.json"
r="$(make_cut_repo cut-other-image)"; printf 'IMAGE=ghcr.io/someone/else:0.8.0\n' > "$r/apps/b/compose/.env"; (cd "$r" && git commit -qam x)
expect_fail "a cut that points an image at another repository is refused" "$r" "apps/b/compose/.env"
r="$(make_cut_repo cut-removed)"; (cd "$r" && git rm -qf apps/unraid/template/retnd.xml && git commit -qam x)
expect_fail "a cut that removes a guarded file is refused" "$r" "apps/unraid/template/retnd.xml"

# Guard 2, the migration claim.
msg='core/migrations must gain exactly'
r="$(make_repo g2-none)"
expect_fail "migrations: an epic that added no 0015 is refused" "$r" "$msg"
r="$(make_repo g2-extra)"; add_0015 "$r"; echo "-- 19" > "$r/core/migrations/0019_extra.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a second new migration is refused" "$r" "0019_extra.sql"
r="$(make_repo g2-0019)"; add_0015 "$r"; echo "-- 19" > "$r/core/migrations/0019_encrypt_existing_consents.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_pass "migrations: 0015 plus 0019_encrypt_existing_consents is the epic's whole list and passes" "$r"
r="$(make_repo g2-0020)"; add_0015 "$r"; echo "-- 19" > "$r/core/migrations/0019_encrypt_existing_consents.sql"; echo "-- 20" > "$r/core/migrations/0020_extra.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a 0020 beside 0015 and 0019 is refused, and named" "$r" "0020_extra.sql"
r="$(make_repo g2-0019-only)"; echo "-- 19" > "$r/core/migrations/0019_encrypt_existing_consents.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: 0019 without 0015 is refused" "$r" "$msg"
r="$(make_repo g2-edit)"; add_0015 "$r"; echo "-- changed" >> "$r/core/migrations/0001_init.sql"; (cd "$r" && git commit -qam x)
expect_fail "migrations: an edit to an existing migration is refused" "$r" "0001_init.sql"
r="$(make_repo g2-wrong-number)"; echo "-- 17" > "$r/core/migrations/0017_retained_copy_encryption.sql"; (cd "$r" && git add -A && git commit -qm x)
expect_fail "migrations: a new migration with another number is refused" "$r" "$msg"

# The tip (#1320): a later epic's migration after EPIC N's last commit is that
# epic's business, an extra migration inside EPIC N's range is still refused,
# and an edit to an existing migration is refused whenever it happened.
r="$(make_repo tip-later)"; add_0015 "$r"; tip="$(cd "$r" && git rev-parse HEAD)"
echo "-- 20" > "$r/core/migrations/0020_encryption_key_suites.sql"; (cd "$r" && git add -A && git commit -qm "epic t")
out="$(TIP_SHA="$tip" run_check "$r")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'epic-n-closed: ok$'; then pass "tip: a later epic's 0020 after EPIC N's tip passes"; else fail "tip: a later epic's 0020 after EPIC N's tip passes" "$out"; fi
r="$(make_repo tip-inside)"; add_0015 "$r"; echo "-- 20" > "$r/core/migrations/0020_extra.sql"; (cd "$r" && git add -A && git commit -qm x); tip="$(cd "$r" && git rev-parse HEAD)"
out="$(TIP_SHA="$tip" run_check "$r")"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "0020_extra.sql"; then pass "tip: an extra migration inside EPIC N's range is refused, and named"; else fail "tip: an extra migration inside EPIC N's range is refused" "exit $rc: $out"; fi
r="$(make_repo tip-edit)"; add_0015 "$r"; tip="$(cd "$r" && git rev-parse HEAD)"
echo "-- changed" >> "$r/core/migrations/0001_init.sql"; (cd "$r" && git commit -qam x)
out="$(TIP_SHA="$tip" run_check "$r")"; rc=$?
if [ "$rc" -ne 0 ] && printf '%s' "$out" | grep -qF "0001_init.sql"; then pass "tip: an edit to an existing migration after the tip is still refused"; else fail "tip: an edit after the tip is refused" "exit $rc: $out"; fi
r="$(make_repo tip-missing)"; add_0015 "$r"
out="$(TIP_SHA=1111111111111111111111111111111111111111 run_check "$r")"; rc=$?
if [ "$rc" -eq 2 ] && printf '%s' "$out" | grep -q 'tip commit .* is not in this checkout'; then pass "a tip commit that is not in the checkout is refused, not skipped"; else fail "a missing tip is refused" "exit $rc: $out"; fi


# A stub `go` that prints what `go test -v` prints, a delay apart: the test's
# own PASS line, then (after the test binary exits) the go command's `ok` line.
# Under pipefail a `grep -q` after `tee` quits at the first line, tee takes
# SIGPIPE on the second, and the passing run reads as a failure (#1407).
make_stub_go() { # $1 = test name it passes
  mkdir -p "$SANDBOX/stubbin"
  printf '#!/bin/sh\nprintf "=== RUN   %s\\n--- PASS: %s (0.00s)\\nPASS\\n"\nsleep 0.5\nprintf "ok  \\tretnd/tests\\t0.5s\\n"\n' "$1" "$1" > "$SANDBOX/stubbin/go"
  chmod +x "$SANDBOX/stubbin/go"
}
make_stub_go TestEpicNIsClosed
r="$(make_repo go-slow-ok)"; add_0015 "$r"; mkdir -p "$r/core"
out="$(SKIP_GO=0 STUB_BIN="$SANDBOX/stubbin" run_check "$r")"; rc=$?
if [ "$rc" -eq 0 ] && printf '%s' "$out" | grep -q 'epic-n-closed: ok$'; then pass "the Go run: a passing test whose ok line comes after a delay passes (#1407)"; else fail "the Go run: a slow ok line passes" "exit $rc: $out"; fi

# The base commit.
r="$(make_repo nobase)"; BASE_SHA=0000000000000000000000000000000000000000
out="$(run_check "$r")"; rc=$?
if [ "$rc" -eq 2 ] && printf '%s' "$out" | grep -q 'is not in this checkout'; then pass "a base commit that is not in the checkout is refused, not skipped"; else fail "a missing base is refused" "exit $rc: $out"; fi

echo "epic-n-closure: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

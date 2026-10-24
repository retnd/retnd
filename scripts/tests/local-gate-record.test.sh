#!/usr/bin/env bash
# The local-gate recorder's refusals, and the record it writes (#1260).
#
# scripts/release/record-local-gate.sh is the one step in release prep
# that turns "the full gate was green" into something a later job can
# check: it runs scripts/ci-local.sh in a fresh detached worktree at HEAD
# and writes provenance/local-gate.json and provenance/local-gate.log.
# require-release-gate in .github/workflows/release.yml then reads that
# record off the commit it is about to publish and refuses without it.
# With ci.yml gone there is no other gate in front of a signed image on a
# public registry, so a recorder that could be talked into writing a
# record for a run that was not full, or not on this tree, would be the
# whole chain's weakest link.
#
# So every refusal is driven here, against the real script, in throwaway
# git repositories, one per case, asserting the exit code AND the distinct
# message. All of the refusals exit 2, so an exit-code-only assertion
# cannot tell "the tree is dirty" from "the hook is not installed", and
# those are fixed by different people doing different things.
#
# The gate itself is a stub committed into each throwaway repository,
# because the recorder runs the gate of the tree it records (from inside
# the worktree it creates), not the gate of the repository this test lives
# in. The stub prints the same last line the real gate prints, and it
# prints the CI variable it sees, which is how the "the recorder sets CI=1
# itself" half of #1260's item 7 is measured rather than assumed.
#
# Eleven behaviours:
#
#    1. a clean checkout with a green gate writes a record whose gate_tree
#       is what scripts/release/gate-tree.sh says, with an empty ledger, a
#       log, the log's hash, and the tool versions;
#    2. the worktree it ran in is gone afterwards, and the checkout it
#       was run from is untouched;
#    3. a dirty working tree is refused, because ci-local.sh tests the
#       working tree rather than the index, so a run in a dirty tree
#       describes no commit at all;
#    4. CI_LOCAL_FAST and every CI_LOCAL_SKIP_* variable are refused, even
#       set to 0, because the record claims a FULL run and the only
#       honest way to make that claim is with none of them in the
#       environment;
#    5. a clone without the pre-commit hook installed is refused and told
#       how to install it;
#    6. a gate that ends INCOMPLETE writes nothing and exits 3;
#    7. a gate that FAILS writes nothing and exits 1;
#    8. gate-tree.sh leaves out exactly the record and its log, so
#       committing them does not move the hash, and a product change does;
#    9. the record the recorder writes is accepted by the require-release-gate
#       script pulled out of release.yml, which is the end-to-end proof
#       that the writer and the reader agree;
#   10. the gate is run with CI=1 in its environment;
#   11. the record is written only after the gate, from the gate's own
#       verdict line, so a gate that printed ok and then died still
#       writes nothing.
#
# Run directly (`bash scripts/tests/local-gate-record.test.sh`) or let the
# gate run it: scripts/ci-local.sh invokes it in a non-FAST run.
set -uo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"
RECORDER="$SCRIPTS_DIR/release/record-local-gate.sh"
GATE_TREE="$SCRIPTS_DIR/release/gate-tree.sh"
WORKFLOW="$REPO_ROOT/.github/workflows/release.yml"

echo "==> local-gate recorder: only a full green run on a clean tree becomes a record"

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  if [ $# -gt 1 ] && [ -n "$2" ]; then
    printf '%s\n' "$2" | sed 's/^/         | /' >&2
  fi
}

# Inside the checkout and gitignored, like every other sandbox here:
# nothing in this repository should need a recursive delete outside the
# working tree to clean up after itself.
SANDBOX="$REPO_ROOT/.local-gate-record-test"
case "$SANDBOX" in
  /*/.local-gate-record-test) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
rm -rf "$SANDBOX"
mkdir -p "$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

for f in "$RECORDER" "$GATE_TREE"; do
  if [ ! -f "$f" ]; then
    fail "$f exists" "every case below drives it, and a missing script fails them all for one reason"
  else
    pass "$(basename "$f") is where this expects it"
  fi
done

# --- fixtures ------------------------------------------------------------

# The git this test shells out to, with the hook-time variables scrubbed so
# a run of this suite from inside the pre-commit hook (which is how the
# gate runs it) does not have its throwaway repositories resolved against
# the real index.
git_in() { # <repo> <args...>
  local repo="$1"; shift
  env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX git -C "$repo" "$@"
}

# The stub gate every throwaway repository commits. It takes its verdict
# from a file the case writes BEFORE the recorder runs, so the same
# committed stub can play all three outcomes without the tree changing
# between cases (the tree is what the record is about, so it must not).
# The verdict file lives outside the worktree the recorder makes, which
# is the point: a stub that read it from the tree would need a different
# commit per outcome.
STUB_GATE='#!/usr/bin/env sh
echo "==> stub gate: CI-ENV=[${CI:-unset}] FAST=[${CI_LOCAL_FAST:-unset}]"
echo "==> stub gate: running in $(pwd)"
# What the real e2e gate leaves behind for the record (#1260): the browser
# lane result, in the worktree the gate ran in.
mkdir -p .e2e-gate
printf '"'"'{"pin": "9699ec5118a0d7748e988346192be37c7bbb5ec7", "passed": 213, "failed": 33, "skipped": 95, "listed_failures": 33, "judged": "ok"}\n'"'"' >.e2e-gate/web-ui-lane.json
verdict="$(cat "$LOCAL_GATE_STUB_VERDICT" 2>/dev/null || echo ok)"
case "$verdict" in
  ok)
    echo "==> ci-local: ok"
    exit 0
    ;;
  incomplete)
    echo "==> ci-local: INCOMPLETE. This run is not merge evidence. These checks did not run:"
    echo "        - the browser e2e suite (CI_LOCAL_SKIP_E2E=1)"
    exit 3
    ;;
  ok-then-die)
    echo "==> ci-local: ok"
    exit 1
    ;;
  *)
    echo "==> ci-local: FAILED (core/ go build)" >&2
    exit 1
    ;;
esac
'

# new_repo <name> -> path. A throwaway repository with the stub gate, a
# product file, a hook directory and the hook installed in its config, so
# that the positive control passes every guard and each refusal case can
# break exactly one thing.
new_repo() {
  local repo="$SANDBOX/$1"
  mkdir -p "$repo"
  git_in "$repo" init -q -b main
  git_in "$repo" config user.email t@example.invalid
  git_in "$repo" config user.name t
  mkdir -p "$repo/scripts/release" "$repo/core" "$repo/.husky" "$repo/provenance"
  printf '%s' "$STUB_GATE" >"$repo/scripts/ci-local.sh"
  chmod +x "$repo/scripts/ci-local.sh"
  cp "$GATE_TREE" "$repo/scripts/release/gate-tree.sh" 2>/dev/null || :
  printf '#!/usr/bin/env sh\nexit 0\n' >"$repo/.husky/pre-commit"
  printf '#!/usr/bin/env sh\nexit 0\n' >"$repo/.husky/prepare-commit-msg"
  chmod +x "$repo/.husky/pre-commit" "$repo/.husky/prepare-commit-msg"
  printf 'package main\n' >"$repo/core/main.go"
  # The toolchain the release builds with, which the reader compares the
  # recorded Go against at major.minor: the real one, so the round trip
  # below is about a real record.
  cp "$REPO_ROOT/core/go.mod" "$repo/core/go.mod"
  printf '{"not": "the record"}\n' >"$repo/provenance/release-provenance.json"
  git_in "$repo" add -A
  git_in "$repo" commit -qm base
  git_in "$repo" config core.hooksPath .husky
  printf 'ok\n' >"$SANDBOX/$1.verdict"
  printf '%s\n' "$repo"
}

OUT=""
STATUS=0

# run_recorder <repo> [VAR=VALUE ...] runs the real recorder from inside
# the throwaway repository, with every CI_LOCAL_* variable scrubbed first
# so the developer's own shell cannot decide what a case measures, then
# with the case's own variables applied on top.
run_recorder() {
  local repo="$1"; shift
  OUT="$(
    cd "$repo" && env -u CI_LOCAL_FAST -u CI_LOCAL_SKIP_JS -u CI_LOCAL_SKIP_DOCKER \
      -u CI_LOCAL_SKIP_E2E -u CI_LOCAL_SKIP_TWO_MACHINE -u CI_LOCAL_SELFTEST -u CI \
      -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX \
      LOCAL_GATE_STUB_VERDICT="$repo.verdict" \
      "$@" bash "$RECORDER" 2>&1
  )"
  STATUS=$?
}

expect() { # <name> <want status> <substring>
  local name="$1" want_status="$2" want_text="$3"
  if [ "$STATUS" -ne "$want_status" ]; then
    fail "$name: exit ${STATUS}, want ${want_status}" "$OUT"
    return
  fi
  case "$OUT" in
    *"$want_text"*) pass "$name" ;;
    *) fail "$name: the exit code is right and the message is not; want a mention of [${want_text}]" "$OUT" ;;
  esac
}

no_record() { # <name> <repo>
  if [ -e "$2/provenance/local-gate.json" ] || [ -e "$2/provenance/local-gate.log" ]; then
    fail "$1: a record was written anyway" "$(ls "$2/provenance")"
  else
    pass "$1"
  fi
}

json_field() { # <file> <python expression over d>
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$1" "$2" 2>/dev/null
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# --- 1 and 2: the positive control -----------------------------------------
#
# Everything below is a refusal, and a recorder that refused every input
# would pass every one of them. This is the case that keeps them honest.
repo="$(new_repo green)"
head_before="$(git_in "$repo" rev-parse HEAD)"
run_recorder "$repo"
expect "a clean checkout with a green gate writes the record" 0 "local-gate: recorded"
record="$repo/provenance/local-gate.json"
log="$repo/provenance/local-gate.log"
if [ -f "$record" ]; then
  pass "provenance/local-gate.json exists"
  want_tree="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>/dev/null)"
  got_tree="$(json_field "$record" 'd["gate_tree"]')"
  if [ -n "$want_tree" ] && [ "$want_tree" = "$got_tree" ]; then
    pass "gate_tree is what gate-tree.sh computes for HEAD ($got_tree)"
  else
    fail "gate_tree is what gate-tree.sh computes for HEAD" "want [$want_tree], got [$got_tree]"
  fi
  [ "$(json_field "$record" 'd["schema"]')" = "retnd.local-gate/1" ] \
    && pass "the schema is retnd.local-gate/1" \
    || fail "the schema is retnd.local-gate/1" "$(cat "$record")"
  [ "$(json_field "$record" 'd["result"]')" = "ok" ] \
    && pass "the result is ok" || fail "the result is ok" "$(cat "$record")"
  [ "$(json_field "$record" 'd["ledger"]')" = "[]" ] \
    && pass "the ledger is empty" || fail "the ledger is empty" "$(cat "$record")"
  [ "$(json_field "$record" 'd["head"]')" = "$head_before" ] \
    && pass "the record names the commit it ran from" || fail "the record names the commit it ran from" "$(cat "$record")"
  case "$(json_field "$record" 'd["tools"]["go"]')" in
    go1.*) pass "the record carries the Go version in go's own form" ;;
    *) fail "the record carries the Go version in go's own form" "$(cat "$record")" ;;
  esac
  for tool in node golangci_lint docker; do
    [ -n "$(json_field "$record" "d['tools']['$tool']")" ] \
      && pass "the record carries a $tool version" || fail "the record carries a $tool version" "$(cat "$record")"
  done
  [ "$(json_field "$record" 'int(d["duration_s"]) >= 0')" = "True" ] \
    && pass "the record carries the run's duration" || fail "the record carries the run's duration" "$(cat "$record")"
  [ "$(json_field "$record" 'd["web_ui_lane"]["listed_failures"]')" = "33" ] \
    && pass "the record carries the browser lane's result from the worktree the gate ran in" \
    || fail "the record carries the browser lane's result from the worktree the gate ran in" "$(cat "$record")"
  if [ -f "$log" ]; then
    pass "provenance/local-gate.log exists"
    [ "$(json_field "$record" 'd["log_sha256"]')" = "$(sha256_of "$log")" ] \
      && pass "log_sha256 is the hash of that log" || fail "log_sha256 is the hash of that log" "$(cat "$record")"
    case "$(cat "$log")" in
      *'CI-ENV=[1]'*) pass "the gate ran with CI=1 in its environment" ;;
      *) fail "the gate ran with CI=1 in its environment" "$(cat "$log")" ;;
    esac
    case "$(cat "$log")" in
      *'==> ci-local: ok'*) pass "the log is the gate's own output, verdict line included" ;;
      *) fail "the log is the gate's own output, verdict line included" "$(cat "$log")" ;;
    esac
  else
    fail "provenance/local-gate.log exists"
  fi
else
  fail "provenance/local-gate.json exists" "$OUT"
fi
# 2. The run happened in a worktree of its own and left nothing behind.
case "$OUT" in
  *'worktree'*) pass "the recorder says it ran in a detached worktree" ;;
  *) fail "the recorder says it ran in a detached worktree" "$OUT" ;;
esac
worktrees="$(git_in "$repo" worktree list | wc -l | tr -d ' ')"
[ "$worktrees" = 1 ] && pass "the worktree is removed afterwards" \
  || fail "the worktree is removed afterwards" "$(git_in "$repo" worktree list)"
[ "$(git_in "$repo" rev-parse HEAD)" = "$head_before" ] && pass "HEAD of the checkout is unchanged" \
  || fail "HEAD of the checkout is unchanged"
dirty="$(git_in "$repo" status --porcelain | grep -v '^?? provenance/local-gate' || true)"
[ -z "$dirty" ] && pass "nothing but the record and its log is new in the checkout" \
  || fail "nothing but the record and its log is new in the checkout" "$dirty"

# --- 3: a dirty tree -------------------------------------------------------
repo="$(new_repo dirty)"
printf '// edited\n' >>"$repo/core/main.go"
run_recorder "$repo"
expect "a dirty working tree is refused" 2 "working tree is dirty"
expect "and the refusal says why a dirty tree cannot be recorded" 2 "tests the working tree"
no_record "and no record is written for it" "$repo"

repo="$(new_repo untracked)"
printf 'stray\n' >"$repo/core/stray.go"
run_recorder "$repo"
expect "an untracked file is a dirty tree too" 2 "working tree is dirty"

# --- 4: the skip variables -------------------------------------------------
for var in CI_LOCAL_FAST CI_LOCAL_SKIP_JS CI_LOCAL_SKIP_DOCKER CI_LOCAL_SKIP_E2E CI_LOCAL_SKIP_TWO_MACHINE; do
  repo="$(new_repo "skip-$var")"
  run_recorder "$repo" "$var=1"
  expect "$var=1 is refused" 2 "$var"
  no_record "and no record is written under $var" "$repo"
done
repo="$(new_repo skip-zero)"
run_recorder "$repo" CI_LOCAL_FAST=0
expect "CI_LOCAL_FAST=0 is refused too: the variable has to be unset, not false" 2 "unset"

# --- 5: the hook is not installed --------------------------------------------
repo="$(new_repo no-hook)"
git_in "$repo" config --unset core.hooksPath
run_recorder "$repo"
expect "a clone without the hook installed is refused" 2 "core.hooksPath"
expect "and the refusal names the installer" 2 "scripts/install-hooks.sh"
no_record "and no record is written for it" "$repo"

repo="$(new_repo wrong-hook)"
git_in "$repo" config core.hooksPath /somewhere/else
run_recorder "$repo"
expect "a hooksPath pointing elsewhere is a missing hook" 2 "core.hooksPath"

# --- 6 and 7: the gate's own verdict -----------------------------------------
repo="$(new_repo incomplete)"
printf 'incomplete\n' >"$SANDBOX/incomplete.verdict"
run_recorder "$repo"
expect "an INCOMPLETE gate run is not recorded, and keeps its own exit status" 3 "INCOMPLETE"
expect "and the refusal repeats what did not run" 3 "the browser e2e suite"
no_record "and no record is written for it" "$repo"

repo="$(new_repo failed)"
printf 'failed\n' >"$SANDBOX/failed.verdict"
run_recorder "$repo"
expect "a FAILED gate run is not recorded" 1 "FAILED"
no_record "and no record is written for it" "$repo"

# --- 11: the verdict line is read, not only the exit status ------------------
#
# The record is a claim about the gate's last line. A gate that printed ok
# and then died (a trap that failed, a tee that broke) has an exit status
# that says one thing and a log that says another, and a recorder that
# read either alone could be made to write a record for the other.
repo="$(new_repo ok-then-die)"
printf 'ok-then-die\n' >"$SANDBOX/ok-then-die.verdict"
run_recorder "$repo"
if [ "$STATUS" -ne 0 ]; then
  pass "a gate whose exit status and verdict line disagree is not recorded"
else
  fail "a gate whose exit status and verdict line disagree is not recorded" "$OUT"
fi
no_record "and no record is written for it" "$repo"

# --- 8: gate-tree.sh ---------------------------------------------------------
repo="$(new_repo tree)"
if [ -f "$GATE_TREE" ]; then
  t0="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  case "$t0" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]*) pass "gate-tree.sh prints a hash for HEAD" ;;
    *) fail "gate-tree.sh prints a hash for HEAD" "$t0" ;;
  esac
  [ "${#t0}" = 40 ] && pass "and it is a full 40-character tree hash" || fail "and it is a full 40-character tree hash" "$t0"
  t0_by_sha="$(cd "$repo" && bash "$GATE_TREE" "$(git_in "$repo" rev-parse HEAD)" 2>&1)"
  [ "$t0" = "$t0_by_sha" ] && pass "HEAD and its sha give the same hash" || fail "HEAD and its sha give the same hash" "$t0 vs $t0_by_sha"
  raw_tree="$(git_in "$repo" rev-parse 'HEAD^{tree}')"
  [ "$t0" = "$raw_tree" ] && pass "with no record in the tree, the gate tree is the commit's own tree" \
    || fail "with no record in the tree, the gate tree is the commit's own tree" "$t0 vs $raw_tree"
  printf '{"schema":"retnd.local-gate/1"}\n' >"$repo/provenance/local-gate.json"
  printf 'a log\n' >"$repo/provenance/local-gate.log"
  git_in "$repo" add -A
  git_in "$repo" commit -qm "record the gate"
  t1="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  [ "$t0" = "$t1" ] && pass "committing the record and its log does not move the gate tree" \
    || fail "committing the record and its log does not move the gate tree" "$t0 vs $t1"
  [ "$t1" != "$(git_in "$repo" rev-parse 'HEAD^{tree}')" ] && pass "and the commit's own tree did move, so the exclusion is doing the work" \
    || fail "and the commit's own tree did move, so the exclusion is doing the work"
  printf '// product change\n' >>"$repo/core/main.go"
  git_in "$repo" commit -qam "product change"
  t2="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  [ "$t0" != "$t2" ] && pass "a product change moves the gate tree" || fail "a product change moves the gate tree" "$t0 vs $t2"
  # The other provenance files are NOT left out: install_docker_host.py
  # and the manifest are code and data the release ships, and a wider
  # exclusion would let them change after the gate ran (#1260, item 2).
  printf '{"changed": true}\n' >"$repo/provenance/release-provenance.json"
  git_in "$repo" commit -qam "provenance change"
  t3="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  [ "$t2" != "$t3" ] && pass "a change to another provenance file moves the gate tree: only the record is left out" \
    || fail "a change to another provenance file moves the gate tree: only the record is left out" "$t2 vs $t3"
  index_leftovers="$(ls "$(git_in "$repo" rev-parse --git-dir)" | grep -c 'gate-tree' || true)"
  [ "$index_leftovers" = 0 ] && pass "gate-tree.sh leaves no temporary index behind" \
    || fail "gate-tree.sh leaves no temporary index behind" "$(ls "$(git_in "$repo" rev-parse --git-dir)")"
fi

# --- 9: the reader accepts what the writer wrote ------------------------------
#
# scripts/release/verify-local-gate.sh is what the job runs, so this is
# the real check reading a real record, in a repository where the record
# is at the published commit.
repo="$(new_repo roundtrip)"
run_recorder "$repo"
if [ "$STATUS" -eq 0 ] && [ -f "$repo/provenance/local-gate.json" ]; then
  git_in "$repo" add provenance/local-gate.json provenance/local-gate.log
  git_in "$repo" commit -qm "Record the local gate"
  cp "$REPO_ROOT/core/go.mod" "$repo/core/go.mod"
  SCRIPT="$SCRIPTS_DIR/release/verify-local-gate.sh"
  summary="$SANDBOX/roundtrip.summary"
  : >"$summary"
  OUT="$(
    cd "$repo" && env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX \
      PUBLISH=true GITHUB_STEP_SUMMARY="$summary" \
      bash "$SCRIPT" "$(git_in "$repo" rev-parse HEAD)" 2>&1
  )"
  STATUS=$?
  expect "require-release-gate accepts the record the recorder wrote" 0 "release gate: PASSED"
  case "$OUT" in
    *OVERRIDDEN*) fail "and it publishes on the record, not on an override" "$OUT" ;;
    *) pass "and it publishes on the record, not on an override" ;;
  esac
else
  fail "the round trip could not start: the recorder did not write a record" "$OUT"
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "==> local-gate recorder: FAILED (${failures} failing assertion(s) of ${checks})" >&2
  exit 1
fi
echo "==> local-gate recorder: ok (${checks} checks)"

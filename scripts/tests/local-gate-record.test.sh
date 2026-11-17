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
# Eleven behaviours (plus the signing cases of #1373: the record is signed and
# the signature verifies, a missing key, an impostor's key and a key-less
# allowed_signers are refused):
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

# Throwaway ed25519 keys (#1373): the owner's stand-in and an impostor's.
ssh-keygen -q -t ed25519 -N '' -C test -f "$SANDBOX/owner" >/dev/null
ssh-keygen -q -t ed25519 -N '' -C test -f "$SANDBOX/impostor" >/dev/null
export RETND_GATE_KEY="$SANDBOX/owner"

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
# What the real gate prints at its start (#1260): the toolchain it used,
# which is what the record carries rather than whatever the recorder'"'"'s own
# shell happens to find.
echo "==> toolchain: go go1.27.0 darwin/arm64"
echo "==> toolchain: golangci-lint 2.15.0 built with go1.27.0"
echo "==> toolchain: node v26.12.0"
echo "==> toolchain: docker 29.0.0"
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
  ok-with-paths)
    echo "==> building in $HOME/workspace/retnd-lanes/x-wt and /Users/somebody/workspace/retnd"
    echo "==> ci-local: ok"
    exit 0
    ;;
  ok-with-secret)
    echo "Authorization: Bearer ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
    echo "==> ci-local: ok"
    exit 0
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
  # And the pinned linter the reader compares the recorded one against.
  mkdir -p "$repo/scripts/ci"
  cp "$REPO_ROOT/scripts/ci/pins.env" "$repo/scripts/ci/pins.env"
  printf '{"not": "the record"}\n' >"$repo/provenance/release-provenance.json"
  printf 'retnd-release namespaces="retnd-local-gate,retnd-e2e-attest" %s\n' "$(cut -d' ' -f1,2 "$SANDBOX/owner.pub")" >"$repo/provenance/allowed_signers"
  git_in "$repo" add -A
  git_in "$repo" commit -qm base
  # The trust reference the verifier compares allowed_signers with (origin/main
  # in the real repository).
  git_in "$repo" update-ref refs/remotes/origin/main HEAD
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
      -u CI_LOCAL_SKIP_E2E -u CI_LOCAL_SKIP_TWO_MACHINE -u CI_LOCAL_SELFTEST -u CI_LOCAL_TIER -u CI \
      -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX -u DOCKER_DEFAULT_PLATFORM \
      LOCAL_GATE_STUB_VERDICT="$repo.verdict" \
      "$@" bash "$RECORDER" ${RECORDER_ARGS:-} 2>&1
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
  if [ -e "$2/provenance/local-gate.json" ] || [ -e "$2/provenance/local-gate.log" ] || [ -e "$2/provenance/local-gate.json.sig" ]; then
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
  # The versions the GATE printed, not the recorder's shell: the gate may
  # run under a GOTOOLCHAIN of its own, and the record is about the run.
  [ "$(json_field "$record" 'd["tools"]["go"]')" = "go1.27.0" ] \
    && pass "the record carries the Go the gate said it used" \
    || fail "the record carries the Go the gate said it used" "$(cat "$record")"
  [ "$(json_field "$record" 'd["tools"]["golangci_lint"]')" = "2.15.0" ] \
    && pass "the record carries the golangci-lint the gate said it used" \
    || fail "the record carries the golangci-lint the gate said it used" "$(cat "$record")"
  [ "$(json_field "$record" 'd["tools"]["node"]')" = "v26.12.0" ] \
    && pass "the record carries the node the gate said it used" \
    || fail "the record carries the node the gate said it used" "$(cat "$record")"
  [ "$(json_field "$record" 'd["tools"]["docker"]')" = "29.0.0" ] \
    && pass "the record carries the docker the gate said it used" \
    || fail "the record carries the docker the gate said it used" "$(cat "$record")"
  [ "$(json_field "$record" 'int(d["duration_s"]) >= 0')" = "True" ] \
    && pass "the record carries the run's duration" || fail "the record carries the run's duration" "$(cat "$record")"
  [ "$(json_field "$record" 'd["web_ui_lane"]["listed_failures"]')" = "33" ] \
    && pass "the record carries the browser lane's result from the worktree the gate ran in" \
    || fail "the record carries the browser lane's result from the worktree the gate ran in" "$(cat "$record")"
  if [ -f "$record.sig" ] && ssh-keygen -Y verify -f "$repo/provenance/allowed_signers" -I retnd-release \
      -n retnd-local-gate -s "$record.sig" <"$record" >/dev/null 2>&1; then
    pass "provenance/local-gate.json.sig exists and verifies against allowed_signers"
  else
    fail "provenance/local-gate.json.sig exists and verifies against allowed_signers" "$(ls "$repo/provenance")"
  fi
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

# --- signing (#1373) -------------------------------------------------------
repo="$(new_repo no-key-file)"
run_recorder "$repo" RETND_GATE_KEY="$SANDBOX/does-not-exist"
expect "a missing signing key is refused before the gate runs" 2 "signing key"
no_record "and no record is written for it" "$repo"

repo="$(new_repo impostor-key)"
run_recorder "$repo" RETND_GATE_KEY="$SANDBOX/impostor"
if [ "$STATUS" -ne 0 ]; then
  pass "a record signed with a key that is not in allowed_signers is not accepted"
else
  fail "a record signed with a key that is not in allowed_signers is not accepted" "$OUT"
fi
no_record "and no record is left behind for it" "$repo"

repo="$(new_repo no-signers)"
printf '# no key yet\n' >"$repo/provenance/allowed_signers"
git_in "$repo" commit -qam "empty signers"
run_recorder "$repo"
expect "an allowed_signers with no key is refused with a clear message" 1 "has no key in it"
no_record "and no record is left behind for it" "$repo"

repo="$(new_repo swapped-signers)"
printf 'retnd-release namespaces="retnd-local-gate,retnd-e2e-attest" %s\n' "$(cut -d' ' -f1,2 "$SANDBOX/impostor.pub")" >"$repo/provenance/allowed_signers"
git_in "$repo" commit -qam "swap the signers"
run_recorder "$repo" RETND_GATE_KEY="$SANDBOX/impostor"
expect "a record signed by a key that only the same commit's allowed_signers trusts is refused" 1 "differs from"
no_record "and no record is left behind for it" "$repo"

repo="$(new_repo default-key-path)"
mkdir -p "$SANDBOX/home/.ssh"
run_recorder "$repo" RETND_GATE_KEY= HOME="$SANDBOX/home"
expect "the default signing key is ~/.ssh/retnd-release-attest" 2 "retnd-release-attest"

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

# --- 4b: anything that narrows the run (#1261 review, item 4) --------------------
#
# The five names above were a hard-coded list, and GOFLAGS=-run=XXX turned
# every Go suite into "ok [no tests to run]" with the gate still printing
# ok. So: any CI_LOCAL_* at all, and a GOFLAGS that narrows (-run, -skip,
# -short, -count=0, -failfast and the like), are refused.
for var in GOFLAGS=-run=XXX GOFLAGS=-short "GOFLAGS=-count=0" GOFLAGS=-skip=Test CI_LOCAL_SELFTEST=1 CI_LOCAL_LOCK=0 CI_LOCAL_SOMETHING_NEW=1; do
  repo="$(new_repo "env-$(printf '%s' "$var" | tr -c 'A-Za-z0-9' '_')")"
  run_recorder "$repo" "$var"
  expect "$var is refused" 2 "${var%%=*}"
  no_record "and no record is written under $var" "$repo"
done
repo="$(new_repo env-goflags-harmless)"
run_recorder "$repo" "GOFLAGS=-mod=mod"
expect "a GOFLAGS that narrows nothing is allowed" 0 "local-gate: recorded"

# DOCKER_DEFAULT_PLATFORM forced to a non-amd64 platform is refused (#1410);
# unset, or naming amd64, is not.
repo="$(new_repo env-platform-arm64)"
run_recorder "$repo" DOCKER_DEFAULT_PLATFORM=linux/arm64
expect "DOCKER_DEFAULT_PLATFORM=linux/arm64 is refused" 2 "unset DOCKER_DEFAULT_PLATFORM"
no_record "and no record is written under a forced arm64 platform" "$repo"
repo="$(new_repo env-platform-amd64)"
run_recorder "$repo" DOCKER_DEFAULT_PLATFORM=linux/amd64
expect "DOCKER_DEFAULT_PLATFORM=linux/amd64 is allowed" 0 "local-gate: recorded"

# The record carries the environment the gate ran under, and the REAL
# ledger read off the log, not a hard-coded empty list.
repo="$(new_repo env-recorded)"
run_recorder "$repo" "GOFLAGS=-mod=mod"
record="$repo/provenance/local-gate.json"
[ "$(json_field "$record" 'd["env"]["GOFLAGS"]')" = "-mod=mod" ] \
  && pass "the record carries the environment the gate ran under" \
  || fail "the record carries the environment the gate ran under" "$(cat "$record" 2>/dev/null)"
[ "$(json_field "$record" 'd["env"]["CI"]')" = "1" ] \
  && pass "including CI=1" || fail "including CI=1" "$(cat "$record" 2>/dev/null)"
[ "$(json_field "$record" 'd["ledger_source"]')" = "log" ] \
  && pass "the ledger is read off the log, not assumed" \
  || fail "the ledger is read off the log, not assumed" "$(cat "$record" 2>/dev/null)"

# --- 9: the published log is scrubbed (#1261 review, item 9) -------------------------
#
# The log goes verbatim into a public repository and onto the GitHub
# Release. Home paths and anything secret-shaped are scrubbed before it is
# written, and a run whose log still matches a secret shape afterwards is
# not recorded at all.
repo="$(new_repo scrubbed)"
printf 'ok-with-paths\n' >"$SANDBOX/scrubbed.verdict"
run_recorder "$repo"
expect "a log with home paths is still recorded" 0 "local-gate: recorded"
log="$repo/provenance/local-gate.log"
# "$HOME/" and not "$HOME": in the pinned CI image HOME is /ci, and every
# "scripts/ci-local.sh" in the log contains that (#1331).
case "$(cat "$log" 2>/dev/null)" in
  *"$HOME/"*) fail "the published log carries no home path" "$(grep -n "$HOME/" "$log" | head -3)" ;;
  *) pass "the published log carries no home path" ;;
esac
assert_scrubbed() { case "$(cat "$log" 2>/dev/null)" in *"$1"*) fail "the published log does not carry $2" ;; *) pass "the published log does not carry $2" ;; esac; }
assert_scrubbed "/Users/somebody" "another user's home path"
case "$(cat "$log" 2>/dev/null)" in
  *'~/'*) pass "and the scrubbed path reads as a home" ;;
  *) fail "and the scrubbed path reads as a home" "$(head -5 "$log")" ;;
esac

repo="$(new_repo secret)"
printf 'ok-with-secret\n' >"$SANDBOX/secret.verdict"
run_recorder "$repo"
expect "a log that still matches a secret shape is not recorded" 1 "secret"
no_record "and no record is written for it" "$repo"

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
  printf 'a signature\n' >"$repo/provenance/local-gate.json.sig"
  git_in "$repo" add -A
  git_in "$repo" commit -qm "record the gate"
  t1="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  [ "$t0" = "$t1" ] && pass "committing the record, its log and its signature does not move the gate tree" \
    || fail "committing the record, its log and its signature does not move the gate tree" "$t0 vs $t1"
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
  # Exact paths, not prefixes (#1261 review, item 8): a directory that
  # happens to carry the record's name is content, and must move the hash.
  git_in "$repo" rm -q provenance/local-gate.log
  mkdir -p "$repo/provenance/local-gate.log"
  printf 'content hiding under the record name\n' >"$repo/provenance/local-gate.log/nested.txt"
  git_in "$repo" add -A
  git_in "$repo" commit -qm "a directory named like the log"
  t4="$(cd "$repo" && bash "$GATE_TREE" HEAD 2>&1)"
  [ "$t3" != "$t4" ] && pass "a directory named like the excluded log is not excluded with it" \
    || fail "a directory named like the excluded log is not excluded with it" "$t3 vs $t4"
  index_leftovers=0
  for leftover in "$(git_in "$repo" rev-parse --absolute-git-dir)"/*gate-tree*; do
    [ -e "$leftover" ] && index_leftovers=$((index_leftovers + 1))
  done
  [ "$index_leftovers" = 0 ] && pass "gate-tree.sh leaves no temporary index behind" \
    || fail "gate-tree.sh leaves no temporary index behind" "$(ls "$(git_in "$repo" rev-parse --absolute-git-dir)")"
fi

# --- 9: the reader accepts what the writer wrote ------------------------------
#
# scripts/release/verify-local-gate.sh is what the job runs, so this is
# the real check reading a real record, in a repository where the record
# is at the published commit.
repo="$(new_repo roundtrip)"
run_recorder "$repo"
if [ "$STATUS" -eq 0 ] && [ -f "$repo/provenance/local-gate.json" ]; then
  git_in "$repo" add provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
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

# --- sharded gate (#1455 item D) --------------------------------------------
#
# `record-local-gate.sh --shards` records a gate that ran as shards: one
# signed record per shard under <git-common-dir>/local-gate/shards/<gate_tree>/,
# resumable, and one aggregate written as schema retnd.local-gate/2. The shard
# runner is a stub here (RETND_GATE_SHARD_RUNNER); the real one is
# scripts/release/run-gate-shard.sh. The manifest is
# scripts/release/gate-shards.txt: one shard name per line, first column only,
# blank lines and # comments ignored, order is the order of the log.
STUB_RUNNER="$SANDBOX/stub-runner.sh"
cat >"$STUB_RUNNER" <<'STUB'
#!/usr/bin/env bash
shard="$1"; shift
host=auto
[ "${1:-}" = "--host" ] && host="$2"
echo "$shard $host" >>"$STUB_DIR/calls"
echo "==> toolchain: go go1.27.0 darwin/arm64"
echo "==> toolchain: golangci-lint 2.15.0 built with go1.27.0"
echo "==> toolchain: node v26.12.0"
echo "==> toolchain: docker 29.0.0"
echo "==> stub shard $shard running in $(pwd) with CI=[${CI:-unset}]"
[ "$shard" = "e2e" ] && echo '==> web-ui-lane: {"pin": "9699ec5", "passed": 213, "failed": 33, "skipped": 95, "listed_failures": 33, "judged": "ok"}'
mode="$(cat "$STUB_DIR/mode-$shard" 2>/dev/null || echo ok)"
if [ "$mode" = fail-once ]; then
  if [ -e "$STUB_DIR/seen-$shard" ]; then mode=ok; else : >"$STUB_DIR/seen-$shard"; mode=fail; fi
fi
case "$mode" in
  ok) echo "==> ci-local: ok (shard $shard)"; exit 0 ;;
  fail)
    echo "--- FAIL: TestTheSourceCapsConnections (0.4s)"
    echo "--- FAIL: TestLoginUnder502 (0.1s)"
    echo "==> ci-local: FAILED (shard $shard)" >&2; exit 1 ;;
  build-error) echo "core/x.go:1: syntax error"; echo "==> ci-local: FAILED (shard $shard)" >&2; exit 1 ;;
  incomplete)
    echo "==> ci-local: INCOMPLETE. This run is not merge evidence. These checks did not run:"
    echo "        - the browser e2e suite (CI_LOCAL_SKIP_E2E=1)"
    exit 3 ;;
  secret) echo "Authorization: Bearer ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"; echo "==> ci-local: ok (shard $shard)"; exit 0 ;;
  wrong-verdict) echo "==> ci-local: ok"; exit 0 ;;
esac
STUB
chmod +x "$STUB_RUNNER"

SHARD_NAMES="static core-a e2e"
new_shard_repo() { # <name> -> path; a repo with the manifest committed
  local repo
  repo="$(new_repo "$1")"
  printf '# the shards, in log order\nstatic\n\ncore-a   # go test\ne2e\n' >"$repo/scripts/release/gate-shards.txt"
  git_in "$repo" add -A
  git_in "$repo" commit -qm "manifest"
  git_in "$repo" update-ref refs/remotes/origin/main HEAD
  mkdir -p "$SANDBOX/stub-$1"
  printf '%s\n' "$repo"
}
shards_dir_of() { # <repo>
  printf '%s/.git/local-gate/shards/%s\n' "$1" "$(cd "$1" && bash "$GATE_TREE" HEAD 2>/dev/null)"
}
run_shards() { # <name> <recorder args> [VAR=VALUE ...]
  local name="$1" args="$2"; shift 2
  RECORDER_ARGS="$args" run_recorder "$SANDBOX/$name" RETND_GATE_SHARD_RUNNER="$STUB_RUNNER" STUB_DIR="$SANDBOX/stub-$name" "$@"
}
calls_of() { grep -c "^$2 " "$SANDBOX/stub-$1/calls" 2>/dev/null || true; }

# 1. the positive control
repo="$(new_shard_repo sh-green)"
run_shards sh-green "--shards --jobs 1"
expect "a sharded run with every shard green writes the record" 0 "local-gate: recorded"
record="$repo/provenance/local-gate.json"
if [ -f "$record" ]; then
  [ "$(json_field "$record" 'd["schema"]')" = "retnd.local-gate/2" ] \
    && pass "the aggregate's schema is retnd.local-gate/2" || fail "the aggregate's schema is retnd.local-gate/2" "$(head -c 600 "$record")"
  [ "$(json_field "$record" '[s["name"] for s in d["shards"]]')" = "['static', 'core-a', 'e2e']" ] \
    && pass "the shards are the manifest's lines, in order, comments and blanks dropped" \
    || fail "the shards are the manifest's lines, in order" "$(json_field "$record" '[s["name"] for s in d["shards"]]')"
  [ "$(json_field "$record" 'd["manifest_sha256"]')" = "$(sha256_of "$repo/scripts/release/gate-shards.txt")" ] \
    && pass "manifest_sha256 is the hash of the manifest" || fail "manifest_sha256 is the hash of the manifest"
  [ "$(json_field "$record" 'd["tools"]["go"]')" = "go1.27.0" ] \
    && pass "the tools come from the shard logs" || fail "the tools come from the shard logs"
  [ "$(json_field "$record" 'd["web_ui_lane"]["listed_failures"]')" = "33" ] \
    && pass "the browser lane is lifted from the e2e shard's log" || fail "the browser lane is lifted from the e2e shard's log"
  [ "$(json_field "$record" 'd["result"]') $(json_field "$record" 'd["ledger"]')" = "ok []" ] \
    && pass "result ok, ledger empty" || fail "result ok, ledger empty"
  [ "$(json_field "$record" 'd["log_sha256"]')" = "$(sha256_of "$repo/provenance/local-gate.log")" ] \
    && pass "log_sha256 is the hash of the concatenated log" || fail "log_sha256 is the hash of the concatenated log"
  [ "$(json_field "$record" 'all(__import__("hashlib").sha256(open(sys.argv[1].replace("local-gate.json","local-gate.log"),"rb").read()[s["log_offset"]:s["log_offset"]+s["log_length"]]).hexdigest()==s["log_sha256"] for s in d["shards"])')" = "True" ] \
    && pass "every shard's log slice hashes to its log_sha256" || fail "every shard's log slice hashes to its log_sha256"
  if ssh-keygen -Y verify -f "$repo/provenance/allowed_signers" -I retnd-release -n retnd-local-gate -s "$record.sig" <"$record" >/dev/null 2>&1; then
    pass "the aggregate is signed and verifies against allowed_signers"
  else
    fail "the aggregate is signed and verifies against allowed_signers"
  fi
  [ "$(json_field "$record" 'all(s["signature"].startswith("-----BEGIN SSH SIGNATURE-----") and s["record"] for s in d["shards"])')" = "True" ] \
    && pass "each shard's signed record and signature travel in the aggregate" || fail "each shard's signed record and signature travel in the aggregate"
else
  fail "the aggregate record exists" "$OUT"
fi
sd="$(shards_dir_of "$repo")"
for s in $SHARD_NAMES; do
  if [ -f "$sd/$s.json" ] && ssh-keygen -Y verify -f "$repo/provenance/allowed_signers" -I retnd-release -n retnd-local-gate -s "$sd/$s.json.sig" <"$sd/$s.json" >/dev/null 2>&1; then
    pass "shard $s has a signed record under the git dir"
  else
    fail "shard $s has a signed record under the git dir" "$(ls "$sd" 2>&1)"
  fi
done
case "$(cat "$repo/provenance/local-gate.log" 2>/dev/null)" in
  *'==> shard core-a on '*'CI=[1]'*) pass "the log has a header per shard and the shards ran with CI=1" ;;
  *) fail "the log has a header per shard and the shards ran with CI=1" "$(head -20 "$repo/provenance/local-gate.log" 2>/dev/null)" ;;
esac
[ "$(git_in "$repo" worktree list | wc -l | tr -d ' ')" = 1 ] && pass "the worktree is removed afterwards" || fail "the worktree is removed afterwards"

# 2. a failed shard writes no aggregate, keeps its log, and resume skips what is signed
repo="$(new_shard_repo sh-resume)"
printf 'fail\n' >"$SANDBOX/stub-sh-resume/mode-core-a"
run_shards sh-resume "--shards --jobs 1"
expect "one failing shard fails the run" 1 "core-a"
no_record "and no aggregate is written" "$repo"
sd="$(shards_dir_of "$repo")"
[ -f "$sd/static.json" ] && [ ! -f "$sd/core-a.json" ] && pass "the green shards are signed and the red one is not" || fail "the green shards are signed and the red one is not" "$(ls "$sd" 2>&1)"
[ -f "$sd/core-a.attempt-1.log" ] && pass "the failed attempt's log is kept" || fail "the failed attempt's log is kept" "$(ls "$sd" 2>&1)"
printf 'ok\n' >"$SANDBOX/stub-sh-resume/mode-core-a"
run_shards sh-resume "--shards --jobs 1"
expect "rerunning the same command finishes the run" 0 "local-gate: recorded"
[ "$(calls_of sh-resume static)" = 1 ] && [ "$(calls_of sh-resume e2e)" = 1 ] && [ "$(calls_of sh-resume core-a)" = 2 ] \
  && pass "resume ran only the shard that had not passed" \
  || fail "resume ran only the shard that had not passed" "$(cat "$SANDBOX/stub-sh-resume/calls")"
record="$repo/provenance/local-gate.json"
[ "$(json_field "$record" '[a["exit"] for s in d["shards"] if s["name"]=="core-a" for a in s["attempts"]]')" = "[1, 0]" ] \
  && pass "the record keeps both attempts of core-a, the failure first" \
  || fail "the record keeps both attempts of core-a, the failure first" "$(json_field "$record" '[s["attempts"] for s in d["shards"]]')"
[ "$(json_field "$record" '[s["attempts"][0]["failed_tests"] for s in d["shards"] if s["name"]=="core-a"][0]')" = "['TestLoginUnder502', 'TestTheSourceCapsConnections']" ] \
  && pass "and the failed attempt names the tests that failed" || fail "and the failed attempt names the tests that failed"
case "$(head -30 "$repo/provenance/local-gate.log")" in
  *'core-a'*'TestTheSourceCapsConnections'*) pass "the log opens with the attempts table" ;;
  *) fail "the log opens with the attempts table" "$(head -12 "$repo/provenance/local-gate.log")" ;;
esac

# 3. --only reruns one shard whatever its state; --force reruns all
repo="$(new_shard_repo sh-only)"
run_shards sh-only "--shards --jobs 1"
rm -f "$repo/provenance/local-gate.json" "$repo/provenance/local-gate.log" "$repo/provenance/local-gate.json.sig"
run_shards sh-only "--shards --only e2e"
expect "--only reruns the one shard and records" 0 "local-gate: recorded"
[ "$(calls_of sh-only e2e)" = 2 ] && [ "$(calls_of sh-only static)" = 1 ] && [ "$(calls_of sh-only core-a)" = 1 ] \
  && pass "--only ran e2e again and nothing else" || fail "--only ran e2e again and nothing else" "$(cat "$SANDBOX/stub-sh-only/calls")"
rm -f "$repo/provenance/local-gate.json" "$repo/provenance/local-gate.log" "$repo/provenance/local-gate.json.sig"
run_shards sh-only "--shards --force --jobs 1"
[ "$(calls_of sh-only static)" = 2 ] && [ "$(calls_of sh-only core-a)" = 2 ] && [ "$(calls_of sh-only e2e)" = 3 ] \
  && pass "--force reruns every shard" || fail "--force reruns every shard" "$(cat "$SANDBOX/stub-sh-only/calls")"
rm -f "$repo/provenance/local-gate.json" "$repo/provenance/local-gate.log" "$repo/provenance/local-gate.json.sig"
run_shards sh-only "--shards --only nosuchshard"
expect "--only with a name the manifest does not have is refused" 2 "nosuchshard"

repo="$(new_shard_repo sh-only-red)"
printf 'fail\n' >"$SANDBOX/stub-sh-only-red/mode-core-a"
run_shards sh-only-red "--shards --only static"
expect "--only static on a run that is not complete says what is still unsigned and writes nothing" 0 "core-a"
no_record "and no aggregate is written while a shard is unsigned" "$repo"

# 4. --retry-once
repo="$(new_shard_repo sh-retry)"
printf 'fail-once\n' >"$SANDBOX/stub-sh-retry/mode-core-a"
run_shards sh-retry "--shards --jobs 1"
expect "a flaky shard fails the run without --retry-once" 1 "core-a"
rm -rf "$SANDBOX/stub-sh-retry/seen-core-a" "$repo/.git/local-gate"
: >"$SANDBOX/stub-sh-retry/calls"
run_shards sh-retry "--shards --retry-once --jobs 1"
expect "--retry-once reruns the failed shard once and records" 0 "local-gate: recorded"
[ "$(calls_of sh-retry core-a)" = 2 ] && pass "core-a ran twice" || fail "core-a ran twice" "$(cat "$SANDBOX/stub-sh-retry/calls")"
[ "$(json_field "$repo/provenance/local-gate.json" '[len(s["attempts"]) for s in d["shards"]]')" = "[1, 2, 1]" ] \
  && pass "the first failure stays in the record" || fail "the first failure stays in the record"

repo="$(new_shard_repo sh-retry-twice)"
printf 'fail\n' >"$SANDBOX/stub-sh-retry-twice/mode-core-a"
run_shards sh-retry-twice "--shards --retry-once --jobs 1"
expect "a shard that fails twice stops the run" 1 "core-a"
[ "$(calls_of sh-retry-twice core-a)" = 2 ] && pass "and it was tried exactly twice" || fail "and it was tried exactly twice"

repo="$(new_shard_repo sh-retry-incomplete)"
printf 'incomplete\n' >"$SANDBOX/stub-sh-retry-incomplete/mode-core-a"
run_shards sh-retry-incomplete "--shards --retry-once --jobs 1"
expect "an INCOMPLETE shard keeps exit 3" 3 "INCOMPLETE"
[ "$(calls_of sh-retry-incomplete core-a)" = 1 ] && pass "and is never retried" || fail "and is never retried"

repo="$(new_shard_repo sh-retry-build)"
printf 'build-error\n' >"$SANDBOX/stub-sh-retry-build/mode-core-a"
run_shards sh-retry-build "--shards --retry-once --jobs 1"
[ "$(calls_of sh-retry-build core-a)" = 1 ] && pass "a build error (no failed tests) is not retried" || fail "a build error (no failed tests) is not retried"

# 5. a shard whose verdict line is not its own, or whose log has a secret
repo="$(new_shard_repo sh-wrong-verdict)"
printf 'wrong-verdict\n' >"$SANDBOX/stub-sh-wrong-verdict/mode-core-a"
run_shards sh-wrong-verdict "--shards --jobs 1"
[ "$STATUS" -ne 0 ] && pass "a shard whose log says a full run passed is not a shard that passed" || fail "a shard whose log says a full run passed is not a shard that passed" "$OUT"
no_record "and no aggregate is written for it" "$repo"
repo="$(new_shard_repo sh-secret)"
printf 'secret\n' >"$SANDBOX/stub-sh-secret/mode-core-a"
run_shards sh-secret "--shards --jobs 1"
[ "$STATUS" -ne 0 ] && pass "a shard log with a secret shape is not recorded" || fail "a shard log with a secret shape is not recorded" "$OUT"
no_record "and no aggregate is written for it" "$repo"

# 6. a new tree is a new key: every shard runs again
repo="$(new_shard_repo sh-stale)"
run_shards sh-stale "--shards --jobs 1"
rm -f "$repo/provenance/local-gate.json" "$repo/provenance/local-gate.log" "$repo/provenance/local-gate.json.sig"
printf '// product change\n' >>"$repo/core/main.go"
git_in "$repo" commit -qam "product change"
run_shards sh-stale "--shards --jobs 1"
[ "$(calls_of sh-stale static)" = 2 ] && [ "$(calls_of sh-stale core-a)" = 2 ] && [ "$(calls_of sh-stale e2e)" = 2 ] \
  && pass "a changed tree reruns every shard" || fail "a changed tree reruns every shard" "$(cat "$SANDBOX/stub-sh-stale/calls")"

# 7. a signed shard that was touched is not trusted
tamper_case() { # <name> <description> <function run inside the shard dir>
  local name="$1" what="$2" action="$3"
  repo="$(new_shard_repo "$name")"
  run_shards "$name" "--shards --jobs 1"
  rm -f "$repo/provenance/local-gate.json" "$repo/provenance/local-gate.log" "$repo/provenance/local-gate.json.sig"
  sd="$(shards_dir_of "$repo")"
  (cd "$sd" && "$action")
  run_shards "$name" "--shards --jobs 1"
  [ "$(calls_of "$name" core-a)" = 2 ] && [ "$(calls_of "$name" static)" = 1 ] \
    && pass "$what: that shard reruns and the others are reused" \
    || fail "$what: that shard reruns and the others are reused" "$(cat "$SANDBOX/stub-$name/calls")"
}
tamper_forge() {
  cp core-a.json forged.json
  ssh-keygen -q -Y sign -n retnd-local-gate -f "$SANDBOX/impostor" forged.json </dev/null >/dev/null 2>&1
  mv forged.json.sig core-a.json.sig && rm -f forged.json
}
tamper_record() { sed 's/"result": "ok"/"result": "ok "/' core-a.json >core-a.json.new && mv core-a.json.new core-a.json; }
tamper_log() { printf 'extra\n' >>core-a.log; }
tamper_nosig() { rm -f core-a.json.sig; }
tamper_case sh-forged-sig "a shard signed with another key" tamper_forge
tamper_case sh-edited-record "a shard record edited after signing" tamper_record
tamper_case sh-edited-log "a shard log edited after signing" tamper_log
tamper_case sh-no-sig "a shard with no signature" tamper_nosig

# 8. refusals before anything runs
repo="$(new_shard_repo sh-no-manifest)"
git_in "$repo" rm -q scripts/release/gate-shards.txt
git_in "$repo" commit -qm "no manifest"
run_shards sh-no-manifest "--shards"
expect "a tree with no manifest is refused" 2 "gate-shards.txt"
repo="$(new_shard_repo sh-dup)"
printf 'static\nstatic\n' >"$repo/scripts/release/gate-shards.txt"
git_in "$repo" commit -qam "dup"
run_shards sh-dup "--shards"
expect "a manifest that names a shard twice is refused" 2 "twice"
repo="$(new_shard_repo sh-no-runner)"
run_shards sh-no-runner "--shards" RETND_GATE_SHARD_RUNNER="$SANDBOX/no-such-runner"
expect "a runner that does not exist is refused" 2 "RETND_GATE_SHARD_RUNNER"
repo="$(new_shard_repo sh-dirty)"
printf '// edited\n' >>"$repo/core/main.go"
run_shards sh-dirty "--shards"
expect "--shards refuses a dirty tree like the serial path" 2 "working tree is dirty"
repo="$(new_shard_repo sh-skipvar)"
run_shards sh-skipvar "--shards" CI_LOCAL_SKIP_E2E=1
expect "--shards refuses CI_LOCAL_* like the serial path" 2 "CI_LOCAL_SKIP_E2E"
repo="$(new_shard_repo sh-badflag)"
run_shards sh-badflag "--shards --nonsense"
expect "an unknown option is refused" 2 "--nonsense"
repo="$(new_shard_repo sh-only-serial)"
run_shards sh-only-serial "--only e2e"
expect "--only without --shards is refused" 2 "--shards"

# 9. the serial path is still the default and still schema 1
repo="$(new_shard_repo sh-serial)"
run_shards sh-serial "--serial"
expect "--serial is the serial path" 0 "local-gate: recorded"
[ "$(json_field "$repo/provenance/local-gate.json" 'd["schema"]')" = "retnd.local-gate/1" ] \
  && pass "and it writes a schema 1 record" || fail "and it writes a schema 1 record"

# 10. a sharded record is committed and the release check reads it
repo="$(new_shard_repo sh-roundtrip)"
run_shards sh-roundtrip "--shards --jobs 2"
if [ "$STATUS" -eq 0 ]; then
  git_in "$repo" add provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
  git_in "$repo" commit -qm "Record the local gate"
  cp "$REPO_ROOT/core/go.mod" "$repo/core/go.mod"
  OUT="$(cd "$repo" && env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE PUBLISH=true GITHUB_STEP_SUMMARY=/dev/null \
    bash "$SCRIPTS_DIR/release/verify-local-gate.sh" "$(git_in "$repo" rev-parse HEAD)" 2>&1)"
  STATUS=$?
  expect "require-release-gate accepts the sharded record, shards run two at a time" 0 "release gate: PASSED"
else
  fail "the sharded round trip could not start" "$OUT"
fi
unset RECORDER_ARGS

echo
if [ "$failures" -gt 0 ]; then
  echo "==> local-gate recorder: FAILED (${failures} failing assertion(s) of ${checks})" >&2
  exit 1
fi
echo "==> local-gate recorder: ok (${checks} checks)"

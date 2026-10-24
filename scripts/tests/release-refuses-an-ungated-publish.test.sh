#!/usr/bin/env bash
# A publish the local gate never recorded is refused (issue #575, rewired
# by #1260).
#
# .github/workflows/release.yml publishes a signed image to a public
# registry on every push to `release`. The only gate in front of that is
# the pre-commit hook running scripts/ci-local.sh, and a hook is a thing a
# clone may not have, a rebase never runs, and GitHub's merge commit never
# meets. So release prep writes a record of one full green run
# (scripts/release/record-local-gate.sh, which produces
# provenance/local-gate.json and provenance/local-gate.log), and the
# `require-release-gate` job reads that record off the commit it is about
# to publish, recomputes the tree it describes with
# scripts/release/gate-tree.sh, and refuses `publish` unless the two agree.
# This drives that job.
#
# It does not pattern-match the YAML. It pulls the job's own script out of
# the workflow and runs it inside a throwaway git repository, once per
# outcome worth having, for the reason release_publish_gate_test.go gives
# about the `decide` script next door: a guard asserted by grep survives
# being rewritten into something that no longer works. GitHub Actions is
# the only thing that ever runs this workflow, so a test that executes the
# script is the only place these refusals are ever performed.
#
# Thirteen behaviours, and the shape of the workflow around them:
#
#    1. a commit carrying a record whose gate_tree matches publishes;
#    2. a commit with no record does not;
#    3. a record for a different tree does not, and the refusal names
#       both hashes, because "main moved after the record was written"
#       is the ordinary way this happens and the fix is a re-run;
#    4. a record of an INCOMPLETE run (a non-empty ledger, or a result
#       that is not ok) does not, however it got written;
#    5. a record whose Go differs from the Go release.yml builds with does
#       not, because the run that was green is not the run that ships;
#    6. a record with a schema this job does not read does not, by name;
#    7. a record that is not JSON does not;
#    8. a record whose log hash does not match the committed log does not,
#       so a forged record has to forge the whole log too;
#    9. `Release-Gate-Override: <why>` publishes anyway and says so;
#   10. the same trailer with no reason refuses, because the reason is
#       the whole audit trail;
#   11. a run that publishes nothing does not read anything at all;
#   12. a publish decision that is neither true nor false refuses;
#   13. a stale override trailer on a commit whose record matches is
#       noise, not the reason it published.
#
# Run directly (`bash scripts/tests/release-refuses-an-ungated-publish.test.sh`)
# or let the gate run it: scripts/ci-local.sh invokes it in every run.
set -uo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"
WORKFLOW="$REPO_ROOT/.github/workflows/release.yml"
GATE_TREE="$SCRIPTS_DIR/release/gate-tree.sh"
JOB="require-release-gate"

echo "==> release publish gate: only a commit the local gate recorded publishes"

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
SANDBOX="$REPO_ROOT/.release-gate-test"
case "$SANDBOX" in
  /*/.release-gate-test) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
rm -rf "$SANDBOX"
mkdir -p "$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

# --- the script -----------------------------------------------------------
#
# scripts/release/verify-local-gate.sh is what the job runs, so it is what
# this runs: the same bytes, from the checkout, pointed at a throwaway
# repository through the working directory. The wiring check at the bottom
# is what holds the job to calling it.
SCRIPT="$REPO_ROOT/scripts/release/verify-local-gate.sh"
if [ ! -f "$SCRIPT" ]; then
  fail "the check script exists at scripts/release/verify-local-gate.sh" "every case below runs it"
  echo "==> release publish gate: FAILED (the script under test does not exist)" >&2
  exit 1
fi
pass "the check script is at scripts/release/verify-local-gate.sh"

# --- the throwaway repositories ------------------------------------------
#
# One per case. Each carries a product file, the real gate-tree.sh (the
# job runs it from the checkout, so the checkout has to have it), and a
# copy of release.yml for the Go pin the job reads. The record is written
# by this test rather than by the recorder, because the cases here are
# about what the READER does with a record of each shape, and most of
# those shapes are ones the recorder refuses to write.

git_in() { # <repo> <args...>
  local repo="$1"; shift
  env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX git -C "$repo" "$@"
}

REPO=""

# new_case <name> [commit message] starts a repository with one product
# commit. The message is what the override cases read.
new_case() {
  REPO="$SANDBOX/repo-$1"
  mkdir -p "$REPO/core" "$REPO/scripts/release" "$REPO/provenance"
  git_in "$REPO" init -q -b release
  git_in "$REPO" config user.email t@example.invalid
  git_in "$REPO" config user.name t
  printf 'package main\n' >"$REPO/core/main.go"
  cp "$GATE_TREE" "$REPO/scripts/release/gate-tree.sh" 2>/dev/null || printf '#!/usr/bin/env bash\nexit 1\n' >"$REPO/scripts/release/gate-tree.sh"
  cp "$REPO_ROOT/core/go.mod" "$REPO/core/go.mod"
  mkdir -p "$REPO/scripts/ci"
  cp "$REPO_ROOT/scripts/ci/pins.env" "$REPO/scripts/ci/pins.env" 2>/dev/null || :
  git_in "$REPO" add -A
  git_in "$REPO" commit -qm "${2:-Release 0.7.2}"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

# release_go reads the Go core/go.mod builds with, so the green record is
# right whatever the pin is, and the wrong-Go case is wrong whatever it is.
release_go() {
  sed -n 's/^go[[:space:]]*\([0-9][0-9.]*\)[[:space:]]*$/\1/p' "$REPO_ROOT/core/go.mod" | head -1
}

# write_record [field=value ...] writes a record for HEAD's gate tree and
# a log, then commits both with the message given in COMMIT_MESSAGE (so an
# override trailer can sit on the commit that carries the record). Fields
# override the green defaults: tree=, result=, ledger=, schema=, go=,
# log_sha256=, raw= (the literal file body, for the not-JSON case).
write_record() {
  local tree result ledger schema go raw log_sha log_bytes lint arg
  tree="$(cd "$REPO" && bash scripts/release/gate-tree.sh HEAD 2>/dev/null || echo 0000000000000000000000000000000000000000)"
  result=ok; ledger='[]'; schema='retnd.local-gate/1'; go="go$(release_go)"; raw=""; log_sha=""; log_bytes=""
  lint="$(sed -n 's/^GOLANGCI_LINT_VERSION=//p' "$REPO_ROOT/scripts/ci/pins.env" 2>/dev/null)"
  for arg in "$@"; do
    case "$arg" in
      log_bytes=*) log_bytes="${arg#log_bytes=}" ;;
      tree=*) tree="${arg#tree=}" ;;
      result=*) result="${arg#result=}" ;;
      ledger=*) ledger="${arg#ledger=}" ;;
      schema=*) schema="${arg#schema=}" ;;
      go=*) go="${arg#go=}" ;;
      lint=*) lint="${arg#lint=}" ;;
      raw=*) raw="${arg#raw=}" ;;
      log_sha256=*) log_sha="${arg#log_sha256=}" ;;
    esac
  done
  if [ -n "$log_bytes" ]; then
    # Raw bytes, written by python so the shell never decodes them: the
    # real log carries carriage returns from progress output and can
    # carry a byte that is not UTF-8, and the hash is over bytes.
    python3 -c 'import sys; open(sys.argv[1], "wb").write(eval(sys.argv[2]))' "$REPO/provenance/local-gate.log" "$log_bytes"
  else
    printf '==> stub gate log\n==> ci-local: ok\n' >"$REPO/provenance/local-gate.log"
  fi
  [ -n "$log_sha" ] || log_sha="$(sha256_of "$REPO/provenance/local-gate.log")"
  if [ -n "$raw" ]; then
    printf '%s' "$raw" >"$REPO/provenance/local-gate.json"
  else
    python3 - "$REPO/provenance/local-gate.json" "$tree" "$result" "$ledger" "$schema" "$go" "$log_sha" "$lint" <<'PY'
import json, sys
path, tree, result, ledger, schema, go, log_sha, lint = sys.argv[1:]
json.dump({
    "schema": schema,
    "gate_tree": tree,
    "head": "0" * 40,
    "result": result,
    "ledger": json.loads(ledger),
    "started_at": "2026-10-21T09:12:03Z",
    "duration_s": 1534,
    "tools": {"go": go, "node": "v26.12.0", "golangci_lint": lint, "docker": "29.0.0"},
    "log_sha256": log_sha,
    "env": {"CI": "1"},
}, open(path, "w"), indent=2)
PY
  fi
  git_in "$REPO" add provenance/local-gate.json provenance/local-gate.log
  git_in "$REPO" commit -qm "${COMMIT_MESSAGE:-Record the local gate}"
}

OUT=""
STATUS=0
SUMMARY=""

# run_gate <publish> [sha] runs the extracted script the way Actions would:
# from the checkout, with GITHUB_SHA naming the commit being published
# (HEAD unless a case says otherwise).
run_gate() {
  local sha="${2:-$(git_in "$REPO" rev-parse HEAD)}"
  SUMMARY="$SANDBOX/summary"
  : > "$SUMMARY"
  OUT="$(
    cd "$REPO" && env -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_OBJECT_DIRECTORY \
      -u GIT_COMMON_DIR -u GIT_PREFIX \
      PUBLISH="$1" \
      GITHUB_REPOSITORY="retnd/retnd" \
      GITHUB_SHA="$sha" \
      GITHUB_STEP_SUMMARY="$SUMMARY" \
      bash "$SCRIPT" "$sha" 2>&1
  )"
  STATUS=$?
}

# expect <name> <want status> <substring> asserts the exit code and the
# distinct message, never the exit code alone: every refusal here exits 1,
# so a status-only assertion cannot tell "no record" from "the record is
# for another tree", and those are fixed by different people doing
# different things.
expect() {
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

refute_text() {
  case "$OUT" in
    *"$2"*) fail "$1: the output says [$2] and should not" "$OUT" ;;
    *) pass "$1" ;;
  esac
}

# 1. The release that is supposed to work. This is the positive control for
#    everything below it: without it, a script that refused every input
#    would pass every other case in this file.
new_case green
write_record
run_gate true
expect "a commit carrying a matching record publishes" 0 "release gate: PASSED"
refute_text "and it publishes because the record matches, not because anything was overridden" "OVERRIDDEN"
expect "and the report names the tree it checked" 0 "gate_tree"

# 2. The case the job exists for: nothing recorded anything.
new_case missing
run_gate true
expect "a commit with no record does not publish" 1 "nothing checked the commit this run would publish"
expect "and it says which file it looked for" 1 "provenance/local-gate.json"
expect "and it says how to get one" 1 "record-local-gate.sh"

# 3. The record is real, and main moved after it was written.
new_case moved
write_record
printf '// a change after the record\n' >>"$REPO/core/main.go"
git_in "$REPO" commit -qam "One more change"
run_gate true
expect "a record for a different tree does not publish" 1 "describes a different tree"
recorded="$(git_in "$REPO" show 'HEAD~1:provenance/local-gate.json' | python3 -c 'import json,sys; print(json.load(sys.stdin)["gate_tree"])')"
published="$(cd "$REPO" && bash scripts/release/gate-tree.sh HEAD)"
expect "and the refusal names the recorded hash" 1 "$recorded"
expect "and the refusal names the published hash" 1 "$published"

# 4. INCOMPLETE, in both ways a record could carry it.
new_case ledger
write_record 'ledger=["the browser e2e suite (CI_LOCAL_SKIP_E2E=1)"]'
run_gate true
expect "a record with a non-empty ledger does not publish" 1 "ledger"
expect "and the refusal repeats what did not run" 1 "the browser e2e suite"

new_case incomplete-result
write_record result=incomplete
run_gate true
expect "a record whose result is not ok does not publish" 1 "result"

# 5. The wrong Go. Major and minor are what the comparison is about: a
#    patch release of the same Go is the same language and the same
#    standard library, and refusing on it would make every release wait
#    for a brew upgrade to match a pin to the digit.
new_case wrong-go
write_record go=go1.25.4
run_gate true
expect "a record from a different Go does not publish" 1 "go1.25.4"
expect "and the refusal names the Go core/go.mod builds with" 1 "$(release_go)"

new_case patch-go
write_record "go=go$(release_go | cut -d. -f1,2).99"
run_gate true
expect "a record from a patch release of the same Go publishes" 0 "release gate: PASSED"

# 5b. The wrong golangci-lint (#1261 review). The pin lives in
#     scripts/ci/pins.env, which the throwaway carries, and a record made
#     with another linter was cleaned against another linter's findings.
new_case wrong-lint
write_record lint=9.9.9
run_gate true
expect "a record from a golangci-lint other than the pinned one does not publish" 1 "9.9.9"
expect "and the refusal names the pin" 1 "$(sed -n 's/^GOLANGCI_LINT_VERSION=//p' "$REPO_ROOT/scripts/ci/pins.env")"

# 6. A schema this job does not read.
new_case schema
write_record schema=retnd.local-gate/2
run_gate true
expect "a record with an unknown schema does not publish" 1 "retnd.local-gate/2"
expect "and the refusal names the schema this job reads" 1 "retnd.local-gate/1"

# 7. Not JSON at all.
new_case malformed
write_record 'raw={"schema": "retnd.local-gate/1", "gate_tree": '
run_gate true
expect "a record that is not JSON does not publish" 1 "not JSON"

# 8a. The log is bytes (#1261 review). Progress output from npm, docker
#     and the Playwright list reporter writes bare carriage returns, a
#     tool can write \r\n, and a truncated multibyte character is a byte
#     that is not UTF-8. The recorder hashes the file's bytes; a reader
#     that decoded the log as text would hash something else (and die on
#     the stray byte), which is a genuine release refused on the day.
new_case log-bytes
write_record 'log_bytes=b"==> stub gate log\rprogress 10%\rprogress 100%\r\nline two\xff\n==> ci-local: ok\n"'
run_gate true
expect "a log with bare \\r, \\r\\n and a byte that is not UTF-8 still verifies" 0 "release gate: PASSED"
refute_text "and the reader does not crash on the byte" "Traceback"

# 8. The log hash. The record points at a log so a reader can see what
#    the run did; a record whose hash does not match the committed log is
#    a record somebody edited after the fact, or a log that is not the
#    run's.
new_case log-hash
write_record log_sha256=0000000000000000000000000000000000000000000000000000000000000000
run_gate true
expect "a record whose log hash does not match the committed log does not publish" 1 "log"
expect "and the refusal says the hash is the thing" 1 "log_sha256"

new_case log-missing
write_record
git_in "$REPO" rm -q provenance/local-gate.log
git_in "$REPO" commit -qm "Lose the log"
run_gate true
expect "a record whose log is not in the commit does not publish" 1 "provenance/local-gate.log"

# 9. The escape hatch.
new_case overridden "Cut 0.7.3
The recorder's host lost Docker for the day and this fixes a
data-losing bug.

Release-Gate-Override: Docker is down and 0.7.2 loses backups"
run_gate true
expect "a commit carrying the override trailer publishes" 0 "release gate: OVERRIDDEN"
expect "and the reason is in the output rather than only in somebody's head" 0 "Docker is down and 0.7.2 loses backups"
expect "and it is annotated on the run, not merely logged" 0 "::warning title=release gate overridden::"

# 10. The override with nothing behind it.
new_case override-without-reason "Cut 0.7.4

Release-Gate-Override: yes"
run_gate true
expect "an override with no reason behind it refuses" 1 "release gate override carries no reason"

# 11. A run that publishes nothing still CHECKS, and only reports (#1261
#     review). The runbook's dry run dispatches with publish=false, and a
#     dry run that skipped this check left the first real check to the
#     merge into release, which is the irreversible publish. So the dry
#     run says what it would have refused, and exits 0 either way.
new_case dry-run
write_record
printf '// a change after the record\n' >>"$REPO/core/main.go"
git_in "$REPO" commit -qam "One more change"
run_gate false
expect "a run that publishes nothing exits 0 whatever it finds" 0 "release gate: DRY RUN"
expect "and still says what a publishing run would have refused" 0 "describes a different tree"

new_case dry-run-clean
write_record
run_gate false
expect "a dry run over a matching record says so" 0 "release gate: DRY RUN"
expect "and says the record would have passed" 0 "would have passed"

# 12. The publish decision arriving as something this job does not
#     recognise. Reading it as "not publishing" is the shape of every
#     defect in this area: quiet, green, and looking at nothing.
new_case unknown-publish
run_gate ""
expect "a publish decision that is neither true nor false refuses" 1 "which is neither \`true\` nor \`false\`"

# 13. The stale trailer. A commit whose record matches publishes on the
#     record, so a trailer left behind from an earlier cut is noise rather
#     than a standing permission.
new_case stale-trailer
COMMIT_MESSAGE="Cut 0.7.5

Release-Gate-Override: this was true three releases ago" write_record
run_gate true
expect "a matching record publishes on the record even when a stale trailer is sitting there" 0 "release gate: PASSED"
refute_text "and a stale trailer is not read as an override" "OVERRIDDEN"

# 14. The summary is what a reader sees on the run page, so a refusal that
#     only reaches stdout is a refusal nobody reads.
new_case summary-written
run_gate true
if grep -q "release gate: REFUSED" "$SUMMARY" 2>/dev/null; then
  pass "the refusal reaches the run summary and not only the log"
else
  fail "the refusal never reached GITHUB_STEP_SUMMARY, so the run page says nothing about why the release stopped" "$(cat "$SUMMARY" 2>/dev/null)"
fi

# 15. The record is read off the commit being published, not off whatever
#     is in the working tree. GITHUB_SHA is the commit; a working tree
#     with a good record and a published commit without one is the shape
#     of a record somebody dropped in at the last minute.
new_case working-tree
sha_without="$(git_in "$REPO" rev-parse HEAD)"
write_record
git_in "$REPO" reset -q --soft "$sha_without"
run_gate true "$sha_without"
expect "a record only in the working tree does not publish the commit that lacks it" 1 "nothing checked the commit this run would publish"

# --- the wiring around the script ---------------------------------------
#
# The script above can be perfect and gate nothing if `publish` does not
# wait for it, which is one word to delete and invisible in a diff that
# also touches the job.
python3 - "$WORKFLOW" "$JOB" "$SCRIPT" <<'PY'
import os
import re
import sys

workflow, job, script = sys.argv[1], sys.argv[2], sys.argv[3]
text = open(workflow, encoding="utf-8").read()
lines = text.splitlines()

jobs = []
publish_needs = []
gate_permissions = []
id_token_jobs = []
checkout_jobs = []
current = None
in_jobs = False
bodies = {}

for line in lines:
    if re.match(r"^jobs:\s*$", line):
        in_jobs = True
        continue
    if in_jobs and line and not line.startswith(" ") and not line.startswith("#"):
        in_jobs = False
        continue
    if not in_jobs:
        continue
    m = re.match(r"^  ([A-Za-z0-9_.-]+):\s*$", line)
    if m:
        current = m.group(1)
        jobs.append(current)
        bodies[current] = []
        continue
    if current is None:
        continue
    bodies[current].append(line)
    if current == "publish":
        m = re.match(r"^    needs:\s*\[(.*)\]\s*$", line)
        if m:
            publish_needs = [n.strip() for n in m.group(1).split(",") if n.strip()]
    if current == job and re.match(r"^      (contents|checks|packages|id-token):", line):
        gate_permissions.append(line.strip())
    if re.match(r"^\s+id-token:\s*write\s*$", line):
        id_token_jobs.append(current)
    if "actions/checkout" in line:
        checkout_jobs.append(current)

failures = []
checks = 0


def check(ok, ok_text, bad_text):
    global checks
    checks += 1
    if ok:
        print("    ok   " + ok_text)
    else:
        failures.append(bad_text)
        print("    FAIL " + bad_text, file=sys.stderr)


check(
    len(jobs) >= 4 and "decide" in jobs and "publish" in jobs,
    f"the parser found {len(jobs)} jobs in release.yml, so it is reading the file",
    f"the parser found only {len(jobs)} jobs in release.yml ({jobs}): it has stopped matching, and every comparison below would pass against nothing",
)
check(
    job in jobs,
    f"{job} is one of them",
    f"{job} is not in release.yml. Nothing in this repository then checks that a commit reaching `release` was ever gated, and the only thing standing in front of a signed image is a branch protection setting no file here can read",
)
check(
    job in publish_needs,
    f"publish waits for {job}",
    f"publish does not wait for {job} (it needs {publish_needs}), so the gate check runs beside the publish instead of in front of it and a refusal arrives after the image is already in the registry",
)
check(
    "id-token: write" not in gate_permissions and "packages: write" not in gate_permissions,
    f"{job} holds no id-token and no packages scope, so it cannot publish or sign anything",
    f"{job} carries {gate_permissions}. The job that decides whether a release may publish must not be able to publish one",
)
check(
    id_token_jobs == ["publish"],
    "publish is still the only job that holds id-token: write, so the signing identity is unchanged",
    f"id-token: write is held by {id_token_jobs}. GitHub builds the Fulcio certificate SAN from the workflow and ref of the run that asks for the token, so a second job minting one signs under an identity the documented `cosign verify` command does not pin, which is #510",
)
check(
    job in checkout_jobs,
    f"{job} checks the repository out, which is where the record and gate-tree.sh are",
    f"{job} never checks the repository out, so it has no provenance/local-gate.json to read and no scripts/release/gate-tree.sh to run; every case above passed against a script that cannot run in the job it was extracted from",
)
check(
    "checks: read" not in gate_permissions,
    f"{job} no longer asks for checks: read, because there is no check run to read",
    f"{job} still holds checks: read ({gate_permissions}), which is the permission for the ci.yml check run this job no longer reads; a scope nobody uses is a scope nobody notices widening",
)

# The job reads the record off GITHUB_SHA and runs the one shared
# gate-tree.sh over it. Both names are literals in the script, and the
# recorder writes the same path, so a rename on either side is a refused
# release on the day of the release.
script_text = open(os.path.join(os.path.dirname(workflow), "..", "..", "scripts", "bdtools", "release", "verify_local_gate.py"), encoding="utf-8").read()
shim = "scripts/release/verify-local-gate.sh"
job_body = "\n".join(bodies.get(job, []))
check(
    shim in job_body and os.path.isfile(os.path.join(os.path.dirname(workflow), "..", "..", shim)),
    f"the job runs {shim}, which exists, so the dry run and the runbook can run the same check",
    f"the job does not run {shim} (or it does not exist); the check lives only inside release.yml and nothing but a publish can run it",
)
check(
    "provenance/local-gate.json" in script_text,
    "the check reads provenance/local-gate.json, the path the recorder writes",
    "the check never names provenance/local-gate.json, so it is not reading the recorder's record",
)
check(
    "scripts/release/gate-tree.sh" in script_text,
    "the check recomputes the tree with scripts/release/gate-tree.sh, the same script the recorder used",
    "the check never runs scripts/release/gate-tree.sh, so the writer and the reader compute the tree hash two different ways and will drift",
)
check(
    '"gh"' not in script_text and "gh api" not in script_text,
    "the job asks GitHub nothing: the record is in the commit",
    "the check still runs `gh`, which means it is still reading a check run that nothing produces any more",
)

# The log travels with the release. github-release attaches it, so the
# hash in the record points at something a reader can open.
gr = "\n".join(bodies.get("github-release", []))
check(
    "provenance/local-gate.log" in gr,
    "github-release attaches provenance/local-gate.log to the GitHub Release",
    "github-release never mentions provenance/local-gate.log, so the record's log_sha256 points at a file nobody can download",
)

# No ${{ }} expansion inside the job's script: the published commit's
# message is read with git, and a trailer is text, not shell.
in_run, indent, hit = False, -1, []
for line in bodies.get(job, []):
    if re.match(r"^\s*#", line):
        continue
    m = re.search(r"[^ ]", line)
    ind = m.start() if m else -1
    if in_run and m and ind <= indent:
        in_run = False
    if in_run and "${{" in line:
        hit.append(line.strip())
    if not in_run and re.match(r"^\s*run:\s*[|>]", line):
        in_run, indent = True, ind
check(
    not hit,
    f"{job} expands no ${{{{ }}}} expression into shell source",
    f"{job} pastes an expression into its script: {hit}",
)

print()
if failures:
    print(f"==> release publish gate wiring: FAILED ({len(failures)} of {checks} checks)", file=sys.stderr)
    sys.exit(1)
print(f"==> release publish gate wiring: ok ({checks} checks)")
PY
wiring=$?
if [ "$wiring" -ne 0 ]; then
  failures=$((failures + 1))
fi

echo
if [ "$failures" -gt 0 ]; then
  echo "==> release publish gate: FAILED (${failures} failing assertion(s) of ${checks})" >&2
  exit 1
fi
echo "==> release publish gate: ok (${checks} behaviour checks, plus the wiring above)"

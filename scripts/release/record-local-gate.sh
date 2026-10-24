#!/usr/bin/env bash
# Record one full, green run of the local gate on the commit at HEAD, as
# the thing a release can later check (#1260).
#
#   bash scripts/release/record-local-gate.sh
#
# GitHub Actions no longer gates anything here. The pre-commit hook running
# scripts/ci-local.sh is the only gate, for main and for releases, and a
# hook is a thing a clone may not have, a rebase never runs, and GitHub's
# merge commit never meets. So a release needs its own proof that the tree
# it publishes went through a full, complete run, and this is what writes
# it: provenance/local-gate.json, next to the release manifest and the
# provenance bundle, plus provenance/local-gate.log, the gate's own output.
# require-release-gate in .github/workflows/release.yml reads the record off
# the commit it is about to publish, recomputes the tree it describes with
# scripts/release/gate-tree.sh, and refuses `publish` unless they match.
#
# The run happens in a fresh detached worktree at HEAD, not here. That is
# the whole point rather than tidiness: ci-local.sh tests the WORKING TREE,
# not the index (its one index-aware line is the `git diff --cached` at
# the bottom), so a run in a checkout with an unstaged edit, an untracked
# test file or a stale node_modules describes no commit at all. A clean
# worktree checked out at exactly HEAD removes that gap by construction,
# and the record names the hash of that tree, with only itself left out.
#
# It refuses, exit 2, before it runs anything, when:
#
#   * the working tree is dirty, tracked or untracked, because HEAD and
#     "what you are looking at" are then two different trees and the
#     record would describe the one you are not looking at;
#   * CI_LOCAL_FAST or any CI_LOCAL_SKIP_* variable is in the environment
#     at all, even set to 0. The record claims a FULL run, and the only
#     way to make that claim honestly is with none of them present: a
#     FAST or SKIP run ends INCOMPLETE and this would refuse it anyway,
#     but twenty-five minutes later;
#   * the pre-commit hook is not installed (core.hooksPath is not .husky).
#     The hook cannot report its own absence, so this is one of the two
#     places that can; a clone without it has been committing ungated and
#     a release cut from it needs saying so before anything else.
#
# It writes nothing unless the gate exits 0 AND its last line says
# "ci-local: ok". Both, because the record is a claim about that line and
# an exit status that disagrees with it is a run whose output cannot be
# trusted. INCOMPLETE (exit 3) keeps its number and writes nothing; FAILED
# writes nothing and exits 1.
#
# CI=1 is set for the run, which is the one #1259 asked for: vitest,
# Playwright and several Go tests in this tree read CI, and the local gate
# had never set it, so a comment claiming "a failure under CI" described a
# run nobody ever made. The gate sets it for itself as well now; this sets
# it too so the record cannot depend on which gate version the worktree
# carries.
#
# Release prep runs this LAST, after record-release-hashes.sh and the
# provenance bundle, and commits the two files with the rest of the prep.
# Committing them does not move gate_tree, because gate-tree.sh leaves
# exactly those two paths out, and everything written before them is
# inside the hash, so nothing can change after the gate ran and still
# publish. docs/release-workflow.md has the order.
#
# What this proves, precisely: that somebody with push rights ran the full
# suite on these bytes and it passed. It is self-asserted. Anyone who can
# type a JSON file can type this one, the same way they could have
# dispatched the old ci.yml by hand. Review of the prep pull request is
# what stands against a forged record, and the log travelling with it
# (hashed here, attached to the GitHub Release by release.yml) is what
# makes forging it cost a twenty-five-minute log rather than one line.
# Commit signing is the upgrade path if that is ever not enough.
#
# No `cd` before the target is resolved, deliberately. Like
# record-release-hashes.sh, this resolves the repository from the CURRENT
# WORKING DIRECTORY, which is what lets scripts/tests/local-gate-record.test.sh
# point it at a throwaway repository. gate-tree.sh is taken from beside
# this script, because the two are one contract; the gate is taken from
# the worktree, because the gate that has to pass is the one in the tree
# being recorded.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GATE_TREE="$SCRIPT_DIR/gate-tree.sh"
RECORD_SCHEMA="retnd.local-gate/1"

refuse() { # <lines...>
  echo "local-gate: REFUSED. $1" >&2
  shift
  for line in "$@"; do echo "    $line" >&2; done
  exit 2
}

# --- the guards, before anything slow ----------------------------------------

unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

toplevel="$(git rev-parse --show-toplevel 2>/dev/null)" \
  || refuse "this is not inside a git checkout." "Run it from the checkout whose HEAD is the release candidate."
cd "$toplevel"

# Anything that could narrow the run is refused before it starts, and the
# list is not hard-coded (#1261 review): ANY CI_LOCAL_* name, because the
# five that existed were a list a sixth knob would have slipped past, and
# a GOFLAGS that selects, skips, shortens or zeroes tests, because
# GOFLAGS=-run=XXX makes every Go suite print "ok [no tests to run]" with
# the gate none the wiser. The environment the gate does run under is
# written into the record (below), so a reader can see what it was.
while IFS='=' read -r name _; do
  case "$name" in
    CI_LOCAL_*)
      refuse "$name is set (to '${!name}'), and the record claims a full, unmodified run." \
        "Every CI_LOCAL_* variable changes what the gate does, so none may be present when the record is made." \
        "Fix: unset $name and re-run." \
        "    unset $name"
      ;;
  esac
done < <(env)
if [ -n "${GOFLAGS+set}" ]; then
  case " $GOFLAGS " in
    *" -run"*|*" -run="*|*" -skip"*|*" -skip="*|*" -short"*|*" -count=0"*|*" -failfast"*|*" -test.run"*|*" -test.short"*|*" -bench"*|*" -fuzz"*)
      refuse "GOFLAGS is set to '$GOFLAGS', which narrows what go test runs." \
        "A Go suite under a -run, -skip, -short, -count=0 or -failfast flag prints ok having run less than it was" \
        "asked to, and the gate cannot see that. Fix: unset GOFLAGS (or set only flags that select nothing) and re-run." \
        "    unset GOFLAGS"
      ;;
  esac
fi

hooks_path="$(git config --get core.hooksPath || true)"
if [ "$hooks_path" != ".husky" ]; then
  refuse "the pre-commit hook is not installed in this clone." \
    "core.hooksPath is ${hooks_path:-unset} (expected: .husky), so commits here have not been gated, and a" \
    "release cut from an ungated clone needs the hook before it needs a record." \
    "Fix (once per clone; covers every worktree): bash scripts/install-hooks.sh"
fi

dirty="$(git status --porcelain)"
if [ -n "$dirty" ]; then
  refuse "the working tree is dirty." \
    "ci-local.sh tests the working tree rather than the index, so a run here would describe neither HEAD nor" \
    "any other commit. The record is about HEAD, and HEAD has to be what is on disk. Commit or stash" \
    "these first, then re-run:" \
    "$(printf '%s\n' "$dirty" | sed 's/^/        /')"
fi

[ -x "$GATE_TREE" ] || [ -f "$GATE_TREE" ] \
  || refuse "scripts/release/gate-tree.sh is not beside this script." "The record's gate_tree comes from it, and so does the release job's recomputation."

head="$(git rev-parse HEAD)"
gate_tree="$(bash "$GATE_TREE" "$head")"

# --- the worktree ---------------------------------------------------------------
#
# Inside the checkout and gitignored, like every sandbox in this
# repository: nothing here should need a delete outside the working tree
# to clean up after itself. A worktree left behind by an interrupted run
# is removed on the way in.
worktree="$toplevel/.local-gate-worktree"
if [ -e "$worktree" ]; then
  echo "local-gate: removing the worktree a previous run left at $worktree"
  git worktree remove --force "$worktree" >/dev/null 2>&1 || rm -rf "$worktree"
  git worktree prune
fi

# The log is written under the shared git dir while the run is in flight,
# so a FAILED run still leaves its log somewhere readable, and copied into
# provenance/ only once the run has earned it.
log_dir="$(git rev-parse --path-format=absolute --git-common-dir)/local-gate"
mkdir -p "$log_dir"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
log="$log_dir/$(date -u +%Y%m%dT%H%M%SZ)-${head:0:12}.log"

cleanup() {
  if [ -d "$worktree" ]; then
    git worktree remove --force "$worktree" >/dev/null 2>&1 || true
    git worktree prune >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

echo "local-gate: recording HEAD ${head} (gate tree ${gate_tree})"
echo "local-gate: running the full gate in a detached worktree at $worktree"
echo "local-gate: log at $log"
git worktree add --detach "$worktree" "$head" >/dev/null 2>&1

# The JS workspaces have to be installed in the worktree, because
# node_modules/ is gitignored and the gate refuses a tree with an
# uninstalled workspace (#160). Only the ones in the tree: an optional
# component that is not here is nothing to install.
for ws in ui/shared apps/common/tests apps/ugos/frontend/upk-proof; do
  if [ -f "$worktree/$ws/package.json" ]; then
    echo "local-gate: installing $ws"
    if [ -f "$worktree/$ws/package-lock.json" ]; then
      (cd "$worktree/$ws" && npm ci --silent)
    else
      (cd "$worktree/$ws" && npm install --silent)
    fi
  fi
done

# --- the run ---------------------------------------------------------------------
#
# `set +e` around the gate only: its exit status is a verdict this script
# has to READ, and `set -e` would end the run on it before anything here
# saw it. The pipe through tee is why the status is taken from
# PIPESTATUS rather than $?.
start_s="$(date +%s)"
set +e
(cd "$worktree" && CI=1 bash scripts/ci-local.sh) 2>&1 | tee "$log"
gate_status="${PIPESTATUS[0]}"
set -e
duration_s=$(( $(date +%s) - start_s ))

# The last verdict line the gate printed. gate_summary prints exactly one,
# and the exit trap prints one for a run that died, so the last
# "==> ci-local:" line is the run's own word on itself.
verdict="$(grep '^==> ci-local: ' "$log" | tail -1 || true)"

case "$gate_status" in
  0)
    if [ "$verdict" != "==> ci-local: ok" ]; then
      echo "local-gate: NOT RECORDED. The gate exited 0 and its last verdict line is [${verdict:-none}], not \"==> ci-local: ok\"." >&2
      echo "    An exit status and a verdict that disagree is a run whose output cannot be trusted. Log: $log" >&2
      exit 1
    fi
    ;;
  3)
    echo "" >&2
    echo "local-gate: NOT RECORDED. The gate ended INCOMPLETE, so this run is not merge evidence." >&2
    echo "    These checks did not run:" >&2
    sed -n '/^==> ci-local: INCOMPLETE/,/^==> For a run/p' "$log" | grep '^        - ' >&2 || true
    echo "    Fix whatever each one names (the gate said how), and re-run. Log: $log" >&2
    exit 3
    ;;
  *)
    echo "" >&2
    echo "local-gate: NOT RECORDED. The gate FAILED (exit ${gate_status}): ${verdict:-no verdict line}." >&2
    echo "    Log: $log" >&2
    exit 1
    ;;
esac

if [ "$verdict" = "==> ci-local: ok" ] && grep -q '^        - ' <(sed -n '/^==> ci-local: INCOMPLETE/,$p' "$log") 2>/dev/null; then
  echo "local-gate: NOT RECORDED. The log carries a skip ledger and an ok verdict at once." >&2
  exit 1
fi

# --- the record --------------------------------------------------------------------

# The tools are what the GATE said it used, read off its own log (the
# `==> toolchain:` lines ci-local.sh prints at its start), not whatever
# this shell finds: the gate runs under a GOTOOLCHAIN of its own, and the
# record is about the run. A gate too old to print them leaves the fields
# empty, and the release check refuses an empty Go.
toolchain_line() { # <tool> -> the rest of the gate's line, or empty
  sed -n "s/^==> toolchain: $1 //p" "$log" | head -n 1 | tr -d '\r'
}
go_version="$(toolchain_line go | awk '{print $1}')"
node_version="$(toolchain_line node)"
golangci_version="$(toolchain_line golangci-lint | awk '{print $1}')"
docker_version="$(toolchain_line docker)"

# The one hash both ends compute: the reader's own helper over the file's
# bytes, so a carriage return or a byte that is not UTF-8 in the log
# hashes here exactly as it does there.
sha256_of() {
  python3 "$SCRIPT_DIR/../bdtools/release/verify_local_gate.py" --log-sha256 "$1"
}

# The ledger, read off the log rather than assumed empty: the lines gate_summary
# prints under an INCOMPLETE verdict. On exit 0 with an ok verdict it is empty
# by construction, and the record says it read the log to know that.
ledger_json="$(sed -n '/^==> ci-local: INCOMPLETE/,/^==> For a run/p' "$log" | sed -n 's/^        - //p' | python3 -c 'import json,sys; print(json.dumps([l.rstrip("\n") for l in sys.stdin if l.strip()]))')"

# The log goes into a public repository and onto the GitHub Release
# verbatim (#1261 review, item 9), so before it is copied: every home
# path becomes `~`, and anything secret-shaped left afterwards (a bearer
# token, a GitHub or AWS key, a private key block, an Authorization
# header) means no record at all rather than a scrubbed one, because a
# scrubber that half-worked is how a secret gets published with a clean
# conscience.
mkdir -p provenance
python3 - "$log" provenance/local-gate.log <<'PY'
import re
import sys

src, dst = sys.argv[1], sys.argv[2]
data = open(src, "rb").read()
# Home paths: this user's, and anybody's under /Users or /home.
data = re.sub(rb"/Users/[^/\s\"']+", b"~", data)
data = re.sub(rb"/home/[^/\s\"']+", b"~", data)
secret_shapes = [
    (rb"ghp_[A-Za-z0-9]{20,}", "a GitHub personal access token"),
    (rb"github_pat_[A-Za-z0-9_]{20,}", "a GitHub fine-grained token"),
    (rb"gho_[A-Za-z0-9]{20,}", "a GitHub OAuth token"),
    (rb"AKIA[0-9A-Z]{16}", "an AWS access key id"),
    (rb"-----BEGIN [A-Z ]*PRIVATE KEY-----", "a private key block"),
    (rb"(?i)authorization:\s*(bearer|basic)\s+\S+", "an Authorization header"),
    (rb"(?i)x-api-key:\s*\S+", "an API key header"),
]
found = [what for shape, what in secret_shapes if re.search(shape, data)]
if found:
    print("local-gate: NOT RECORDED. The gate log still matches a secret shape after scrubbing: " + ", ".join(found) + ".", file=sys.stderr)
    print("    A log that may carry a secret is not published. Find the line in " + src + ", stop whatever printed it, and re-run.", file=sys.stderr)
    sys.exit(1)
open(dst, "wb").write(data)
PY
log_sha256="$(sha256_of provenance/local-gate.log)"

# The browser lane's result, as evidence (#1260). The e2e gate writes
# .e2e-gate/web-ui-lane.json in the worktree it ran in: how many passed,
# failed, were listed on scripts/e2e/known-red.txt and skipped, at which
# retnd-tests pin. It is carried into the record so a release says what
# its browser lane looked like, and absent (null) when the gate in the
# recorded tree predates the lane.
lane_file="$worktree/.e2e-gate/web-ui-lane.json"
[ -f "$lane_file" ] || lane_file=""

# The environment the gate ran under, as the record's `env`: the gate's
# own exports (CI, CI_LOCAL) and whatever Go, Node and Docker variables
# were present, which is what a reader needs to tell this run from one
# made elsewhere. Values, not just names, because GOFLAGS=-mod=mod and
# GOFLAGS=-run=X are different runs.
env_json="$(env | python3 -c '
import json, os, sys
keep = {}
for line in sys.stdin:
    name, sep, value = line.rstrip("\n").partition("=")
    if not sep:
        continue
    if name in ("CI", "CI_LOCAL") or name.startswith(("GO", "NODE_", "DOCKER_", "PLAYWRIGHT_", "VITEST_", "GATE_", "CI_LOCAL_")):
        keep[name] = value
keep["CI"] = "1"
keep["CI_LOCAL"] = "1"
print(json.dumps(keep, sort_keys=True))
')"

python3 - provenance/local-gate.json "$RECORD_SCHEMA" "$gate_tree" "$head" "$started_at" "$duration_s" \
  "$go_version" "$node_version" "$golangci_version" "$docker_version" "$log_sha256" "$lane_file" \
  "$ledger_json" "$env_json" <<'PY'
import json
import sys

(path, schema, gate_tree, head, started_at, duration_s,
 go, node, golangci, docker, log_sha256, lane_file, ledger_json, env_json) = sys.argv[1:]
lane = None
if lane_file:
    with open(lane_file, encoding="utf-8") as fh:
        lane = json.load(fh)
ledger = json.loads(ledger_json)
env = json.loads(env_json)
record = {
    "schema": schema,
    "gate_tree": gate_tree,
    "head": head,
    "result": "ok",
    "ledger": ledger,
    "ledger_source": "log",
    "started_at": started_at,
    "duration_s": int(duration_s),
    "tools": {
        "go": go,
        "node": node,
        "golangci_lint": golangci,
        "docker": docker,
    },
    "log_sha256": log_sha256,
    "web_ui_lane": lane,
    "env": env,
    "note": "Written by scripts/release/record-local-gate.sh. gate_tree is scripts/release/gate-tree.sh over the recorded commit: its tree with this file and local-gate.log left out. require-release-gate in .github/workflows/release.yml recomputes it on the commit it publishes and refuses a mismatch.",
}
with open(path, "w", encoding="utf-8") as fh:
    json.dump(record, fh, indent=2)
    fh.write("\n")
PY

# The reader's own check over what was just written, before this says
# "recorded": the record and the log from the working tree, the gate tree
# from HEAD, which is exactly what the release job will see once they are
# committed. A record this refuses is not one to commit.
echo "local-gate: running the release check over the record just written"
if ! PUBLISH=true bash "$SCRIPT_DIR/verify-local-gate.sh" --uncommitted HEAD; then
  echo "local-gate: NOT RECORDED. The release check refuses the record this run just wrote; its output above says why." >&2
  rm -f provenance/local-gate.json provenance/local-gate.log
  exit 1
fi

echo ""
echo "local-gate: recorded. The full gate passed on ${head} in ${duration_s}s, in a detached worktree, and scripts/release/verify-local-gate.sh accepts the record."
echo "    provenance/local-gate.json   gate_tree ${gate_tree}"
echo "    provenance/local-gate.log    sha256 ${log_sha256}"
echo "    Commit both with the release prep: they are what require-release-gate reads before publish may run."

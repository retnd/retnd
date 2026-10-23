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

for var in CI_LOCAL_FAST CI_LOCAL_SKIP_JS CI_LOCAL_SKIP_DOCKER CI_LOCAL_SKIP_E2E CI_LOCAL_SKIP_TWO_MACHINE; do
  if [ -n "${!var+set}" ]; then
    refuse "$var is set (to '${!var}'), and the record claims a full run." \
      "A run with any CI_LOCAL_FAST or CI_LOCAL_SKIP_* variable present is not merge evidence, and this" \
      "refuses it now rather than after the gate has run. Fix: unset $var and re-run." \
      "    unset $var"
  fi
done

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

tool_version() { # <command> <args...> -> first line, or empty when absent
  if command -v "$1" >/dev/null 2>&1; then
    "$@" 2>/dev/null | head -1 | tr -d '\r'
  fi
}
go_version="$(tool_version go version | sed -n 's/^go version \(go[^ ]*\).*/\1/p')"
node_version="$(tool_version node --version)"
golangci_version="$(tool_version golangci-lint --version | sed -n 's/.*version \(v\{0,1\}[0-9][^ ]*\).*/\1/p')"
docker_version="$(tool_version docker version --format '{{.Server.Version}}')"
[ -n "$docker_version" ] || docker_version="$(tool_version docker --version | sed -n 's/^Docker version \([^,]*\),.*/\1/p')"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

mkdir -p provenance
cp "$log" provenance/local-gate.log
log_sha256="$(sha256_of provenance/local-gate.log)"

# The browser lane's result, as evidence (#1260). The e2e gate writes
# .e2e-gate/web-ui-lane.json in the worktree it ran in: how many passed,
# failed, were listed on scripts/e2e/known-red.txt and skipped, at which
# retnd-tests pin. It is carried into the record so a release says what
# its browser lane looked like, and absent (null) when the gate in the
# recorded tree predates the lane.
lane_file="$worktree/.e2e-gate/web-ui-lane.json"
[ -f "$lane_file" ] || lane_file=""

python3 - provenance/local-gate.json "$RECORD_SCHEMA" "$gate_tree" "$head" "$started_at" "$duration_s" \
  "$go_version" "$node_version" "$golangci_version" "$docker_version" "$log_sha256" "$lane_file" <<'PY'
import json
import sys

(path, schema, gate_tree, head, started_at, duration_s,
 go, node, golangci, docker, log_sha256, lane_file) = sys.argv[1:]
lane = None
if lane_file:
    with open(lane_file, encoding="utf-8") as fh:
        lane = json.load(fh)
record = {
    "schema": schema,
    "gate_tree": gate_tree,
    "head": head,
    "result": "ok",
    "ledger": [],
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
    "env": {"CI": "1"},
    "note": "Written by scripts/release/record-local-gate.sh. gate_tree is scripts/release/gate-tree.sh over the recorded commit: its tree with this file and local-gate.log left out. require-release-gate in .github/workflows/release.yml recomputes it on the commit it publishes and refuses a mismatch.",
}
with open(path, "w", encoding="utf-8") as fh:
    json.dump(record, fh, indent=2)
    fh.write("\n")
PY

echo ""
echo "local-gate: recorded. The full gate passed on ${head} in ${duration_s}s, in a detached worktree."
echo "    provenance/local-gate.json   gate_tree ${gate_tree}"
echo "    provenance/local-gate.log    sha256 ${log_sha256}"
echo "    Commit both with the release prep: they are what require-release-gate reads before publish may run."

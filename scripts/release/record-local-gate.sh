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
# That upgrade has landed (#1373): the record is signed with
# `ssh-keygen -Y sign -n retnd-local-gate` and the signature travels as
# provenance/local-gate.json.sig. The owner's public key is the one line in
# provenance/allowed_signers, and scripts/bdtools/release/verify_local_gate.py
# refuses a missing or invalid signature. The private key (RETND_GATE_KEY,
# default ~/.ssh/retnd-release-attest) is only ever handed to ssh-keygen: this
# script never reads or prints it.
#
# SHARDS (#1455). `--shards [--only <shard>] [--host <host>] [--retry-once]
# [--force] [--jobs N]` records a gate that ran as the shards named in
# scripts/release/gate-shards.txt instead of one serial ci-local.sh run. Every
# shard runs through the shard runner (RETND_GATE_SHARD_RUNNER, default
# scripts/release/run-gate-shard.sh of the recorded tree) from the same clean
# worktree; the Mac signs one record per passing shard under
# <git-common-dir>/local-gate/shards/<gate_tree>/, a rerun reuses the shards
# already signed for this tree, and when all are signed the aggregate is
# written as schema retnd.local-gate/2. `--serial` is the default and the
# path described above. scripts/bdtools/release/gate_shards.py has the rest.
#
# THE FRONT DOOR (#1672). `--preflight [--shards ...]` runs only the checks
# below and exits 0 or 2: every guard above, plus origin/release and a v* tag,
# the release key in ssh-agent (the one the trust ref's allowed_signers names,
# and a test signature in namespace retnd-preflight), allowed_signers at HEAD
# equal to the trust ref's, and, when anything runs on this Mac, Node at the
# NODE_MAJOR scripts/ci/pins.env pins, gh auth, a docker that answers within
# GATE_DOCKER_INFO_TIMEOUT and builds a one-file image, and the Playwright lock
# when the browser steps run here. Every record run does the same checks
# first and names every refusal at once. One recorder runs per clone, under
# <git-common-dir>/local-gate/recorder.lock; a second one exits 2 and leaves
# the first one's worktree alone. `--status` prints HEAD, the gate tree and
# which shards are signed and reusable for it (or why not), read-only, and a
# sharded run prints the same table first. docs/reference/local-gate.md has
# the details.
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
GATE_KEY="${RETND_GATE_KEY:-$HOME/.ssh/retnd-release-attest}"

mode=serial
only="" host="" retry_once="" force="" jobs="" preflight_only="" status_only=""
while [ $# -gt 0 ]; do
  case "$1" in
    --serial) mode=serial ;;
    --shards) mode=shards ;;
    --preflight) preflight_only=1 ;;
    --status) status_only=1 ;;
    --only) [ $# -ge 2 ] || { echo "local-gate: REFUSED. --only needs a value" >&2; exit 2; }; only="$2"; shift ;;
    --host) [ $# -ge 2 ] || { echo "local-gate: REFUSED. --host needs a value" >&2; exit 2; }; host="$2"; shift ;;
    --jobs) [ $# -ge 2 ] || { echo "local-gate: REFUSED. --jobs needs a value" >&2; exit 2; }; jobs="$2"; shift ;;
    --retry-once) retry_once=1 ;;
    --force) force=1 ;;
    *) echo "local-gate: REFUSED. unknown option $1" >&2; exit 2 ;;
  esac
  shift
done

refuse() { # <lines...>
  echo "local-gate: REFUSED. $1" >&2
  shift
  for line in "$@"; do echo "    $line" >&2; done
  exit 2
}

if [ "$mode" != shards ] && [ -n "$only$host$jobs$retry_once$force" ]; then
  refuse "--only, --host, --jobs, --retry-once and --force only mean something with --shards."
fi
if [ -n "$status_only" ] && [ -n "$only$host$jobs$retry_once$force$preflight_only" ]; then
  refuse "--status takes no other option: it reports on HEAD and changes nothing."
fi

unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

toplevel="$(git rev-parse --show-toplevel 2>/dev/null)" \
  || refuse "this is not inside a git checkout." "Run it from the checkout whose HEAD is the release candidate."
cd "$toplevel"
common_dir="$(git rev-parse --path-format=absolute --git-common-dir)"
TRUST_REF="${RETND_TRUST_REF:-origin/main}"
GATE_DOCKER_INFO_TIMEOUT="${GATE_DOCKER_INFO_TIMEOUT:-120}"

# Everything a run leaves behind is undone here, on every exit path: the
# worktree (only by the run that holds the lock, below), the lock itself, and
# the scratch directories the preflight and --status read into.
worktree="$toplevel/.local-gate-worktree"
worktree_owned=0 lock_mine="" pf_tmp="" status_tmp=""
on_exit() {
  if [ "$worktree_owned" = 1 ] && [ -d "$worktree" ]; then
    git worktree remove --force "$worktree" >/dev/null 2>&1 || true
    git worktree prune >/dev/null 2>&1 || true
  fi
  lock_release
  [ -z "$pf_tmp" ] || rm -rf "$pf_tmp"
  [ -z "$status_tmp" ] || rm -rf "$status_tmp"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The one hash both ends compute: the reader's own helper over the file's bytes.
sha256_of() {
  python3 "$SCRIPT_DIR/../bdtools/release/verify_local_gate.py" --log-sha256 "$1"
}

# bounded <seconds> <command...>: the command's own status, or 124 when it
# outlives the limit (macOS has no timeout(1)).
bounded() {
  local limit="$1" ticks=0 pid
  shift
  "$@" &
  pid=$!
  while kill -0 "$pid" 2>/dev/null; do
    if [ "$ticks" -ge $((limit * 10)) ]; then
      kill -TERM "$pid" 2>/dev/null; sleep 0.2; kill -KILL "$pid" 2>/dev/null
      wait "$pid" 2>/dev/null
      return 124
    fi
    sleep 0.1
    ticks=$((ticks + 1))
  done
  wait "$pid"
}

# --- the recorder lock (#1672 P0.2) ------------------------------------------
#
# One recorder per clone. Two at once used to remove each other's
# .local-gate-worktree mid-run, so the second one is refused, exit 2, with the
# holder named, and never touches the worktree. Same shape as the gate lock in
# scripts/lib/ci-local-gate.sh: a symlink at <git-common-dir>/local-gate/recorder.lock
# pointing at a directory that is this holder's alone (pid, host, boot, where,
# since), written before the link exists. A holder that is dead on this host
# and this boot (a kill -9 runs no trap) is reclaimed; one that cannot be
# judged is refused with the command that breaks the lock. --status and
# --preflight never take it.
recorder_lock="$common_dir/local-gate/recorder.lock"

boot_id() {
  if [ -r /proc/sys/kernel/random/boot_id ]; then
    cat /proc/sys/kernel/random/boot_id
  elif command -v sysctl >/dev/null 2>&1 && sysctl -n kern.boottime >/dev/null 2>&1; then
    sysctl -n kern.boottime | tr -d ' ' | cut -c1-40
  else
    hostname
  fi
}
lock_holder_dir() {
  local target
  target="$(readlink "$recorder_lock" 2>/dev/null)" || return 1
  [ -n "$target" ] || return 1
  echo "$(dirname "$recorder_lock")/$target"
}
lock_describe() { # <holder dir>
  echo "pid $(cat "$1/pid" 2>/dev/null || echo '?') on $(cat "$1/host" 2>/dev/null || echo '?') ($(cat "$1/where" 2>/dev/null || echo 'unknown checkout')), since $(cat "$1/since" 2>/dev/null || echo '?')"
}
lock_holder_gone() { # <holder dir> -> 0 only when the holder is known to be dead
  [ -d "$1" ] || return 0
  local pid
  pid="$(cat "$1/pid" 2>/dev/null || true)"
  case "$pid" in ''|*[!0-9]*) return 1 ;; esac
  [ "$(cat "$1/host" 2>/dev/null)" = "$(hostname)" ] && [ "$(cat "$1/boot" 2>/dev/null)" = "$(boot_id)" ] || return 1
  if kill -0 "$pid" 2>/dev/null; then return 1; fi
  return 0
}
lock_refusal() { # <holder dir>
  refuse "another recorder is running: $(lock_describe "$1")." \
    "Two recorders in one clone remove each other's worktree, so one runs at a time. Wait for it to end;" \
    "bash scripts/release/record-local-gate.sh --status shows what it has signed so far." \
    "If that process is really gone (it ran on another machine, or before a reboot), break the lock:" \
    "    rm -r '$recorder_lock' '$1'"
}
# One attempt at the link. Plain `ln -s` onto a link that points at a
# directory creates the new link INSIDE that directory and reports success, so
# -n, and the result is read back: the lock is ours only when the path itself
# now points at our directory.
lock_try() {
  [ ! -e "$recorder_lock" ] && [ ! -L "$recorder_lock" ] || return 1
  ln -sn "$(basename "$lock_mine")" "$recorder_lock" 2>/dev/null || return 1
  [ "$(readlink "$recorder_lock" 2>/dev/null)" = "$(basename "$lock_mine")" ]
}
lock_take() {
  local tries=0 holder
  mkdir -p "$(dirname "$recorder_lock")"
  lock_mine="$recorder_lock.$$"
  rm -rf "$lock_mine"
  mkdir "$lock_mine"
  echo "$$" >"$lock_mine/pid"
  hostname >"$lock_mine/host"
  boot_id >"$lock_mine/boot"
  echo "$toplevel" >"$lock_mine/where"
  date -u +%Y-%m-%dT%H:%M:%SZ >"$lock_mine/since"
  while ! lock_try; do
    tries=$((tries + 1))
    holder="$(lock_holder_dir || true)"
    if [ -n "$holder" ] && ! lock_holder_gone "$holder"; then
      rm -rf "$lock_mine"
      lock_mine=""
      lock_refusal "$holder"
    fi
    if [ "$tries" -gt 20 ]; then
      rm -rf "$lock_mine"
      lock_mine=""
      refuse "could not take the recorder lock at $recorder_lock after 20 tries."
    fi
    if [ -n "$holder" ] && [ -d "$holder" ]; then
      # Exactly one reclaimer wins this rename, and only it removes the link.
      if mv "$holder" "$holder.reclaimed.$$" 2>/dev/null; then
        echo "local-gate: reclaiming the recorder lock from $(lock_describe "$holder.reclaimed.$$"), which no longer exists"
        rm -rf "$holder.reclaimed.$$"
        rm -f "$recorder_lock"
      fi
    elif [ -L "$recorder_lock" ]; then
      # A link with no holder directory: a reclaim in flight, or a holder
      # killed half way. Give a reclaimer a moment, then remove it.
      sleep 0.2
      if [ "$tries" -ge 10 ]; then
        echo "local-gate: removing a recorder lock that has no holder"
        rm -f "$recorder_lock"
      fi
    fi
  done
}
lock_release() {
  [ -n "$lock_mine" ] || return 0
  if [ "$(readlink "$recorder_lock" 2>/dev/null)" = "$(basename "$lock_mine")" ]; then
    rm -f "$recorder_lock"
  fi
  rm -rf "$lock_mine"
  lock_mine=""
}

# --- the shard table (#1672 P0.3) ----------------------------------------------
#
# shard_table <manifest> <manifest sha> <runner> <worktree> <signers>
# HEAD, the gate tree, the manifest and runner hashes, then one line per shard:
# signed and reusable, stale with valid_shard's own reason, or not run, with
# the host, attempts and time of what ran. Read-only: it reads the shards dir
# through gate_shards.py's valid_shard and writes nothing. When nothing is
# signed for this tree and another tree has signed shards, it names that tree's
# head and prints git diff --stat from there, which is what moved the gate tree.
shard_table() {
  python3 - "$SCRIPT_DIR/../bdtools/release" "$@" "$head" "$gate_tree" "$common_dir/local-gate/shards" <<'PY'
import argparse
import json
import subprocess
import sys
from pathlib import Path

lib, manifest, manifest_sha, runner, worktree, signers, head, gate_tree, root = sys.argv[1:]
sys.path.insert(0, lib)
import gate_shards as gs  # noqa: E402

names = gs.parse_manifest(Path(manifest).read_text(encoding="utf-8"))
args = argparse.Namespace(gate_tree=gate_tree, manifest_sha=manifest_sha, runner=runner, worktree=worktree)
d = Path(root) / gate_tree
print(f"local-gate: HEAD {head}")
print(f"    gate tree   {gate_tree}")
print(f"    manifest    scripts/release/gate-shards.txt, sha256 {manifest_sha}")
print(f"    runner      {runner}, sha256 {gs.runner_sha(args)}")
print(f"    {'shard':<22} {'state':<58} {'host':<6} {'attempts':>8} {'time':>7}")
reusable = 0
for name in names:
    attempts = []
    path = d / f"{name}.attempts.json"
    if path.is_file():
        try:
            attempts = [a for a in json.loads(path.read_text(encoding="utf-8")) if a.get("gate_tree") == gate_tree]
        except ValueError:
            attempts = []
    why = gs.valid_shard(d, name, args, Path(signers)) if d.is_dir() else "no record"
    if why is None:
        state = "signed, reusable"
        reusable += 1
    elif why == "no record":
        state = f"not signed (last attempt exit {attempts[-1].get('exit')})" if attempts else "not run"
    else:
        state = f"stale: {why}"
    where = str(attempts[-1].get("host", "?")) if attempts else "-"
    took = f"{sum(int(a.get('duration_s', 0)) for a in attempts)}s" if attempts else "-"
    print(f"    {name:<22} {state:<58} {where:<6} {len(attempts):>8} {took:>7}")
print(f"local-gate: {reusable} of {len(names)} shards signed and reusable for gate tree {gate_tree[:12]}")
if reusable == 0 and Path(root).is_dir():
    others = []
    for t in Path(root).iterdir():
        if not t.is_dir() or t.name == gate_tree:
            continue
        recs = [p for p in t.glob("*.json") if not p.name.endswith(".attempts.json") and Path(str(p) + ".sig").is_file()]
        if recs:
            others.append((max(p.stat().st_mtime for p in recs), t.name, recs))
    if others:
        _, tree, recs = max(others)
        try:
            other_head = str(json.loads(recs[0].read_text(encoding="utf-8")).get("head", ""))
        except ValueError:
            other_head = ""
        print(f"local-gate: nothing is signed for this tree. Gate tree {tree} (head {other_head or '?'}) has "
              f"{len(recs)} signed shard(s); what changed since that head:")
        diff = subprocess.run(["git", "diff", "--stat", other_head, head], capture_output=True, text=True) if other_head else None
        if diff is not None and diff.returncode == 0:
            for line in diff.stdout.splitlines():
                print(f"    {line}")
        else:
            print("    (that commit is not in this clone)")
PY
}

# --- status (#1672 P0.3): read-only, no worktree, no lock -----------------------
#
# The only supported way to ask which shards are still good for HEAD:
# git rev-parse HEAD^{tree} is not the gate tree (gate-tree.sh leaves the
# record files out). Everything is read with git show from HEAD into a scratch
# directory outside the checkout, so nothing in the clone or its git dir moves.
if [ -n "$status_only" ]; then
  [ -f "$GATE_TREE" ] || refuse "scripts/release/gate-tree.sh is not beside this script."
  head="$(git rev-parse HEAD)"
  gate_tree="$(bash "$GATE_TREE" "$head")"
  [ -z "$(git status --porcelain)" ] \
    || echo "local-gate: note: the working tree is dirty. This is about HEAD, not about what is on disk, and a record run refuses a dirty tree."
  status_tmp="$(mktemp -d "${TMPDIR:-/tmp}/retnd-gate-status.XXXXXX")"
  if ! git show "HEAD:scripts/release/gate-shards.txt" >"$status_tmp/gate-shards.txt" 2>/dev/null; then
    echo "local-gate: HEAD $head, gate tree $gate_tree"
    echo "    this tree has no scripts/release/gate-shards.txt, so it can only be recorded serially"
    exit 0
  fi
  status_runner="${RETND_GATE_SHARD_RUNNER:-}"
  if [ -z "$status_runner" ]; then
    status_runner="$status_tmp/run-gate-shard.sh"
    git show "HEAD:scripts/release/run-gate-shard.sh" >"$status_runner" 2>/dev/null || : >"$status_runner"
  fi
  mkdir -p "$status_tmp/tree/core"
  git show "HEAD:core/go.mod" >"$status_tmp/tree/core/go.mod" 2>/dev/null || true
  git show "HEAD:provenance/allowed_signers" >"$status_tmp/allowed_signers" 2>/dev/null || true
  shard_table "$status_tmp/gate-shards.txt" "$(sha256_of "$status_tmp/gate-shards.txt")" "$status_runner" \
    "$status_tmp/tree" "$status_tmp/allowed_signers"
  exit $?
fi

# --- the preflight (#1672 P0.1), before anything slow -----------------------------
#
# Every check here is something a later step already refuses (the gate, the
# shard runner, the signing, or the release check after the whole run), moved
# to the front so a run that cannot end in a record never starts. Each check
# prints its own line; every refusal is collected, so one preflight names all
# of them, and then the run exits 2 before it makes a worktree. --preflight
# runs only this and exits 0 when nothing was refused.
pf_refused=0
pf_refuse() { # <lines...>
  echo "local-gate: REFUSED. $1" >&2
  shift
  for line in "$@"; do echo "    $line" >&2; done
  pf_refused=$((pf_refused + 1))
}
pf_ok() { echo "local-gate: preflight: ok    $1"; }
pf_note() { echo "local-gate: preflight: note  $1"; }
pf_tmp="$(mktemp -d "${TMPDIR:-/tmp}/retnd-gate-preflight.XXXXXX")"

# Where the shards will run decides which local checks matter. --serial and any
# shard on the Mac need the local docker, Node and gh; a shard on mars or nas
# brings its own (scripts/ci-docker.sh), and the ui-browser shard goes to mars
# when no --host is given (run-gate-shard.sh).
mac_placed=1 remote_hosts="" browser_on_mac=0
if [ "$mode" = shards ]; then
  case "$host" in
    mars|nas) mac_placed=0; remote_hosts="$host" ;;
    "")
      if [ "$only" = ui-browser ]; then
        mac_placed=0; remote_hosts=mars
      elif [ -z "$only" ] && git show HEAD:scripts/release/gate-shards.txt 2>/dev/null | grep -q '^ui-browser'; then
        remote_hosts=mars
      fi
      ;;
  esac
fi
if [ "$mac_placed" = 1 ] && [ "${RETND_UI_BROWSER_ALLOW_LOCAL:-0}" = 1 ]; then
  if [ "$mode" = serial ]; then
    browser_on_mac=1
  elif [ "$host" = mac ] && { [ -z "$only" ] || [ "$only" = ui-browser ]; }; then
    browser_on_mac=1
  fi
fi

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
      pf_refuse "$name is set (to '${!name}'), and the record claims a full, unmodified run." \
        "Every CI_LOCAL_* variable changes what the gate does, so none may be present when the record is made." \
        "Fix: unset $name and re-run." \
        "    unset $name"
      ;;
  esac
done < <(env)
if [ -n "${GOFLAGS+set}" ]; then
  case " $GOFLAGS " in
    *" -run"*|*" -skip"*|*" -short"*|*" -count=0"*|*" -failfast"*|*" -test.run"*|*" -test.short"*|*" -bench"*|*" -fuzz"*)
      pf_refuse "GOFLAGS is set to '$GOFLAGS', which narrows what go test runs." \
        "A Go suite under a -run, -skip, -short, -count=0 or -failfast flag prints ok having run less than it was" \
        "asked to, and the gate cannot see that. Fix: unset GOFLAGS (or set only flags that select nothing) and re-run." \
        "    unset GOFLAGS"
      ;;
  esac
fi

# DOCKER_DEFAULT_PLATFORM forces every `docker run` and `docker build` onto one
# platform. The gate's fixture images are amd64, so a shell that exports
# linux/arm64 runs them in the wrong architecture and the record would claim a
# run it did not make (0.7.2 lessons, #1410). Refused unless it names amd64.
if [ -n "${DOCKER_DEFAULT_PLATFORM:-}" ]; then
  case "$DOCKER_DEFAULT_PLATFORM" in
    linux/amd64|linux/amd64/*) ;;
    *)
      pf_refuse "DOCKER_DEFAULT_PLATFORM is set to '$DOCKER_DEFAULT_PLATFORM', which forces every container onto that platform." \
        "The gate's fixture images are amd64 and break under a forced non-amd64 default, so the record would not describe a real run." \
        "Fix: unset DOCKER_DEFAULT_PLATFORM and re-run." \
        "    unset DOCKER_DEFAULT_PLATFORM"
      ;;
  esac
fi

hooks_path="$(git config --get core.hooksPath || true)"
if [ "$hooks_path" != ".husky" ]; then
  pf_refuse "the pre-commit hook is not installed in this clone." \
    "core.hooksPath is ${hooks_path:-unset} (expected: .husky), so commits here have not been gated, and a" \
    "release cut from an ungated clone needs the hook before it needs a record." \
    "Fix (once per clone; covers every worktree): bash scripts/install-hooks.sh"
fi

command -v python3 >/dev/null 2>&1 || pf_refuse "python3 is not on PATH, and the record, the shards and the release check are written in it."
have_keygen=1
command -v ssh-keygen >/dev/null 2>&1 \
  || { have_keygen=0; pf_refuse "ssh-keygen is not on PATH, so the record cannot be signed."; }
# Checked up front, not after the gate: a run that then cannot be signed is a
# wasted run. The private key is never read here; ssh-keygen gets its path.
have_key=1
[ -f "$GATE_KEY" ] \
  || { have_key=0; pf_refuse "the signing key $GATE_KEY does not exist." \
    "The record is signed with ssh-keygen -Y sign (namespace retnd-local-gate) and is useless unsigned." \
    "Fix: point RETND_GATE_KEY at the private key whose public half is in provenance/allowed_signers."; }

dirty="$(git status --porcelain)"
if [ -n "$dirty" ]; then
  pf_refuse "the working tree is dirty." \
    "ci-local.sh tests the working tree rather than the index, so a run here would describe neither HEAD nor" \
    "any other commit. The record is about HEAD, and HEAD has to be what is on disk. Commit or stash" \
    "these first, then re-run:" \
    "$(printf '%s\n' "$dirty" | sed 's/^/        /')"
fi

[ -f "$GATE_TREE" ] \
  || pf_refuse "scripts/release/gate-tree.sh is not beside this script." "The record's gate_tree comes from it, and so does the release job's recomputation."

# run-gate-shard.sh refuses a clone without these, today only after the
# worktree is made.
if git rev-parse -q --verify refs/remotes/origin/release >/dev/null; then
  pf_ok "origin/release is here"
else
  pf_refuse "this clone has no origin/release." "Fix: git fetch origin release"
fi
if [ -n "$(git tag -l 'v*' | head -n 1)" ]; then
  pf_ok "a v* tag is here"
else
  pf_refuse "this clone has no v* tag." "Fix: git fetch --tags origin"
fi

# The key that will sign has to be the one the trust reference names, it has
# to be in ssh-agent now (the owner unlocks it at the keyboard), and it has to
# sign. The test signature uses namespace retnd-preflight, which
# allowed_signers does not trust, so it is worth nothing to a verifier.
trusted_signers="$(git show "$TRUST_REF:provenance/allowed_signers" 2>/dev/null || true)"
if [ -z "$trusted_signers" ]; then
  pf_refuse "there is no trust reference: $TRUST_REF:provenance/allowed_signers cannot be read." \
    "The release check compares the commit's allowed_signers with it. Fix: git fetch origin main, or name another with RETND_TRUST_REF."
else
  if [ "$(git show HEAD:provenance/allowed_signers 2>/dev/null || true)" = "$trusted_signers" ]; then
    pf_ok "provenance/allowed_signers is the same as $TRUST_REF's"
  else
    pf_refuse "provenance/allowed_signers at HEAD differs from $TRUST_REF's." \
      "The release check refuses that after the whole run (a key swapped in the commit it signs), so it is refused now." \
      "Fix: record a HEAD whose allowed_signers is the one on $TRUST_REF, or fetch $TRUST_REF if it is stale."
  fi
  trusted_key="$(printf '%s\n' "$trusted_signers" | awk '$1 == "retnd-release" { for (i = 2; i < NF; i++) if ($i ~ /^(ssh-|ecdsa-|sk-)/) { print $i " " $(i + 1); exit } }')"
  if [ -z "$trusted_key" ]; then
    pf_refuse "$TRUST_REF:provenance/allowed_signers has no retnd-release key, so nothing can sign a record it trusts."
  elif [ "$have_key" = 1 ] && [ "$have_keygen" = 1 ]; then
    printf '%s\n' "$trusted_key" >"$pf_tmp/trusted.pub"
    trusted_fp="$(ssh-keygen -l -f "$pf_tmp/trusted.pub" </dev/null 2>/dev/null | awk '{print $2}')"
    key_pub="$GATE_KEY.pub"
    [ -f "$key_pub" ] || key_pub="$GATE_KEY"
    key_fp="$(ssh-keygen -l -f "$key_pub" </dev/null 2>/dev/null | awk '{print $2}' || true)"
    key_ok=1
    if [ -z "$key_fp" ] || [ "$key_fp" != "$trusted_fp" ]; then
      key_ok=0
      pf_refuse "RETND_GATE_KEY ($GATE_KEY, ${key_fp:-unreadable}) is not the key $TRUST_REF:provenance/allowed_signers trusts ($trusted_fp)." \
        "Fix: point RETND_GATE_KEY at the release key, ~/.ssh/retnd-release-attest by default."
    fi
    if ssh-add -L 2>/dev/null | awk '{print $1 " " $2}' | grep -qxF "$trusted_key"; then
      pf_ok "ssh-agent holds the release key ($trusted_fp)"
    else
      key_ok=0
      pf_refuse "the release key is not in ssh-agent, so no shard and no record could be signed." \
        "ssh-add -L does not list the key $TRUST_REF:provenance/allowed_signers trusts ($trusted_fp)." \
        "Fix (the owner, at the keyboard): ssh-add -t 12h $GATE_KEY"
    fi
    if [ "$key_ok" = 1 ]; then
      printf 'retnd recorder preflight %s\n' "$$" >"$pf_tmp/probe"
      printf 'retnd-release namespaces="retnd-preflight" %s\n' "$trusted_key" >"$pf_tmp/signers"
      if bounded 60 ssh-keygen -Y sign -n retnd-preflight -f "$GATE_KEY" "$pf_tmp/probe" </dev/null >/dev/null 2>&1 \
        && ssh-keygen -Y verify -f "$pf_tmp/signers" -I retnd-release -n retnd-preflight -s "$pf_tmp/probe.sig" <"$pf_tmp/probe" >/dev/null 2>&1; then
        pf_ok "a test signature with $GATE_KEY verifies"
      else
        pf_refuse "a test signature with $GATE_KEY failed (namespace retnd-preflight, worth nothing to a verifier)." \
          "Fix: ssh-add -D, then ssh-add -t 12h $GATE_KEY, and re-run."
      fi
    fi
  fi
fi

holder="$(lock_holder_dir || true)"
if [ -n "$holder" ] && ! lock_holder_gone "$holder"; then
  pf_refuse "another recorder is running: $(lock_describe "$holder")." \
    "Two recorders in one clone remove each other's worktree, so one runs at a time. Wait for it to end."
else
  pf_ok "no other recorder holds the lock"
fi

if [ "$mac_placed" = 1 ]; then
  node_pin="$(sed -n 's/^NODE_MAJOR=//p' scripts/ci/pins.env 2>/dev/null | head -n 1)"
  if [ -z "$node_pin" ]; then
    pf_note "scripts/ci/pins.env pins no NODE_MAJOR, so the Node on PATH is not checked"
  else
    node_version="$(node -v 2>/dev/null || true)"
    node_major="${node_version#v}"
    node_major="${node_major%%.*}"
    if [ "$node_major" = "$node_pin" ]; then
      pf_ok "Node $node_version is the pinned major $node_pin"
    else
      pf_refuse "Node on PATH is ${node_version:-missing}, and scripts/ci/pins.env pins Node $node_pin." \
        "The JS steps and the N2.4 control behave differently across Node majors (0.8.1 needed $node_pin)." \
        "Fix: put Node $node_pin first on PATH and re-run."
    fi
  fi
  if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
    pf_ok "gh auth status passes"
  else
    pf_refuse "gh auth status fails, and the gh-rows checks ask GitHub." "Fix: gh auth login"
  fi
  docker_status=0
  docker_arch="$(bounded "$GATE_DOCKER_INFO_TIMEOUT" docker info --format '{{.Architecture}}' 2>/dev/null)" || docker_status=$?
  if [ "$docker_status" = 124 ]; then
    pf_refuse "docker info did not answer within GATE_DOCKER_INFO_TIMEOUT (${GATE_DOCKER_INFO_TIMEOUT}s)." \
      "Fix: restart Docker Desktop, wait until docker info answers, and re-run."
  elif [ "$docker_status" != 0 ] || [ -z "$docker_arch" ]; then
    pf_refuse "the local docker daemon does not answer (docker info failed)." \
      "Fix: start (or restart) Docker Desktop, wait until docker info answers, and re-run."
  else
    pf_ok "docker answers ($docker_arch)"
    mkdir -p "$pf_tmp/build"
    printf 'FROM scratch\nCOPY probe /probe\n' >"$pf_tmp/build/Dockerfile"
    : >"$pf_tmp/build/probe"
    if bounded "$GATE_DOCKER_INFO_TIMEOUT" docker build -q -t "retnd-recorder-preflight:$$" "$pf_tmp/build" >/dev/null 2>&1; then
      docker image rm -f "retnd-recorder-preflight:$$" >/dev/null 2>&1 || true
      pf_ok "a one-file docker build works"
    else
      pf_refuse "a one-file docker build failed, which is how the buildx cache fault (\"layer does not exist\") shows" \
        "before core/tests/machines runs into it. Fix: docker buildx prune -f (or restart Docker Desktop) and re-run."
    fi
  fi
  if [ "$browser_on_mac" = 1 ]; then
    pw_lock="${RETND_PW_LOCK:-/Users/rom/workspace/xdg-pw-lock.sh}"
    if [ -f "$pw_lock" ] && [ -x "$pw_lock" ]; then
      pf_ok "the Playwright lock $pw_lock is executable"
    else
      pf_refuse "RETND_UI_BROWSER_ALLOW_LOCAL=1 runs the browser steps here only through the Playwright lock, and $pw_lock is not an executable file." \
        "Fix: point RETND_PW_LOCK at the lock script, or run the ui-browser shard on mars."
    fi
  fi
  # A warning, never a refusal: a loaded Mac is a reason to wait, not a defect.
  ncpu="$(getconf _NPROCESSORS_ONLN 2>/dev/null || nproc 2>/dev/null || echo 0)"
  load1="$(uptime 2>/dev/null | sed -n 's/.*load average[s]*: *\([0-9][0-9]*\).*/\1/p')"
  if [ -n "$load1" ] && [ "$ncpu" -gt 0 ] && [ "$load1" -gt "$ncpu" ]; then
    pf_note "load is about $load1 on $ncpu cpus; the load-sensitive tests (the mutation self-tests, tworoutes, alertrefresh) may fail for that alone"
  fi
  free_pct=""
  if sysctl -n kern.memorystatus_level >/dev/null 2>&1; then
    free_pct="$(sysctl -n kern.memorystatus_level)"
  elif [ -r /proc/meminfo ]; then
    free_pct="$(awk '/^MemTotal:/ {t = $2} /^MemAvailable:/ {a = $2} END {if (t) print int(a * 100 / t)}' /proc/meminfo)"
  fi
  if [ -n "$free_pct" ] && [ "$free_pct" -lt 15 ]; then
    pf_note "only about $free_pct% of memory is free"
  fi
fi

for remote in $remote_hosts; do
  if grep -q '"probe"' "$SCRIPT_DIR/../bdtools/release/gate_shards.py" 2>/dev/null; then
    if probe_out="$(python3 "$SCRIPT_DIR/../bdtools/release/gate_shards.py" probe "$remote" 2>&1)"; then
      pf_ok "$remote: $probe_out"
    else
      pf_refuse "$remote does not answer the probe: $probe_out"
    fi
  else
    pf_note "$remote: not probed here (gate_shards.py has no probe yet); run-gate-shard.sh checks ssh and docker there when its shard starts"
  fi
done

if [ "$pf_refused" -gt 0 ]; then
  echo "local-gate: REFUSED. The preflight found $pf_refused problem(s), each above with its fix; nothing was started." >&2
  exit 2
fi

head="$(git rev-parse HEAD)"
gate_tree="$(bash "$GATE_TREE" "$head")"

if [ -n "$preflight_only" ]; then
  echo "local-gate: preflight: ok. HEAD $head (gate tree $gate_tree) can be recorded."
  exit 0
fi

# The environment the gate ran under, as the record's `env` (both modes).
gate_env_json() {
  env | python3 -c '
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
'
}

# The JS workspaces have to be installed in the worktree, because
# node_modules/ is gitignored and the gate refuses a tree with an
# uninstalled workspace (#160). Only the ones in the tree: an optional
# component that is not here is nothing to install. The serial path always
# needs them; the sharded path needs them when a shard runs on the Mac
# (#1672 P0.4: js exited 1 in 0 s twice without them). A shard on mars or nas
# gets its own install from scripts/ci-docker.sh --deps.
install_js_workspaces() { # <worktree>
  local ws
  for ws in ui/shared apps/common/tests apps/ugos/frontend/upk-proof; do
    if [ -f "$1/$ws/package.json" ]; then
      echo "local-gate: installing $ws"
      if [ -f "$1/$ws/package-lock.json" ]; then
        (cd "$1/$ws" && npm ci --silent)
      else
        (cd "$1/$ws" && npm install --silent)
      fi
    fi
  done
}

# --- the worktree ---------------------------------------------------------------
#
# Inside the checkout and gitignored, like every sandbox in this
# repository: nothing here should need a delete outside the working tree
# to clean up after itself. Only the holder of the recorder lock touches it,
# so a worktree found here was left by a run that is gone, and is removed.
lock_take
if [ -e "$worktree" ]; then
  echo "local-gate: removing the worktree a previous run left at $worktree"
  git worktree remove --force "$worktree" >/dev/null 2>&1 || rm -rf "$worktree"
  git worktree prune
fi
worktree_owned=1

# The log is written under the shared git dir while the run is in flight,
# so a FAILED run still leaves its log somewhere readable, and copied into
# provenance/ only once the run has earned it.
log_dir="$common_dir/local-gate"
mkdir -p "$log_dir"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
log="$log_dir/$(date -u +%Y%m%dT%H%M%SZ)-${head:0:12}.log"

if [ "$mode" = shards ]; then
  echo "local-gate: recording HEAD ${head} as shards (gate tree ${gate_tree})"
  git worktree add --detach "$worktree" "$head" >/dev/null 2>&1
  manifest="$worktree/scripts/release/gate-shards.txt"
  [ -f "$manifest" ] \
    || refuse "scripts/release/gate-shards.txt is not in this tree." "A sharded record names its shards from that manifest."
  python3 "$SCRIPT_DIR/../bdtools/release/gate_shards.py" manifest "$manifest" >/dev/null || exit 2
  runner="${RETND_GATE_SHARD_RUNNER:-$worktree/scripts/release/run-gate-shard.sh}"
  [ -f "$runner" ] \
    || refuse "the shard runner $runner does not exist." "Set RETND_GATE_SHARD_RUNNER, or record a tree that has scripts/release/run-gate-shard.sh."
  shards_dir="$(git rev-parse --path-format=absolute --git-common-dir)/local-gate/shards/$gate_tree"
  mkdir -p "$shards_dir"
  signers="$shards_dir/allowed_signers"
  git show "HEAD:provenance/allowed_signers" >"$signers" 2>/dev/null \
    || refuse "provenance/allowed_signers is not in HEAD, so a shard signature cannot be checked."
  manifest_sha="$(sha256_of "$manifest")"
  shard_args=(--manifest "$manifest" --manifest-sha "$manifest_sha" --dir "$shards_dir" --gate-tree "$gate_tree"
    --head "$head" --worktree "$worktree" --signers "$signers")
  shard_table "$manifest" "$manifest_sha" "$runner" "$worktree" "$signers" || true
  if [ "$mac_placed" = 1 ]; then
    install_js_workspaces "$worktree"
  fi
  set +e
  python3 "$SCRIPT_DIR/../bdtools/release/gate_shards.py" run "${shard_args[@]}" --runner "$runner" --key "$GATE_KEY" \
    ${host:+--host "$host"} ${only:+--only "$only"} ${force:+--force} ${retry_once:+--retry-once} ${jobs:+--jobs "$jobs"}
  run_status=$?
  set -e
  case "$run_status" in
    0) ;;
    4) exit 0 ;;
    *) exit "$run_status" ;;
  esac
  mkdir -p provenance
  rm -f provenance/local-gate.json.sig
  python3 "$SCRIPT_DIR/../bdtools/release/gate_shards.py" aggregate "${shard_args[@]}" --runner "$runner" --out provenance \
    --env-json "$(gate_env_json)" || { rm -f provenance/local-gate.json provenance/local-gate.log; exit 1; }
  if ! ssh-keygen -Y sign -n retnd-local-gate -f "$GATE_KEY" provenance/local-gate.json >/dev/null; then
    rm -f provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
    echo "local-gate: NOT RECORDED. ssh-keygen could not sign the record with $GATE_KEY." >&2
    exit 1
  fi
  echo "local-gate: running the release check over the record just written"
  if ! PUBLISH=true bash "$SCRIPT_DIR/verify-local-gate.sh" --uncommitted HEAD; then
    echo "local-gate: NOT RECORDED. The release check refuses the record this run just wrote; its output above says why." >&2
    rm -f provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
    exit 1
  fi
  echo ""
  echo "local-gate: recorded. Every shard passed on ${head}, each signed on this machine, and scripts/release/verify-local-gate.sh accepts the record."
  echo "    provenance/local-gate.json   gate_tree ${gate_tree}  (schema retnd.local-gate/2)"
  echo "    provenance/local-gate.log    sha256 $(sha256_of provenance/local-gate.log)"
  echo "    provenance/local-gate.json.sig   signed with ${GATE_KEY}"
  echo "    Commit all three with the release prep: they are what require-release-gate reads before publish may run."
  exit 0
fi

echo "local-gate: recording HEAD ${head} (gate tree ${gate_tree})"
echo "local-gate: running the full gate in a detached worktree at $worktree"
echo "local-gate: log at $log"
git worktree add --detach "$worktree" "$head" >/dev/null 2>&1

install_js_workspaces "$worktree"

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
import os
import re
import sys

src, dst = sys.argv[1], sys.argv[2]
data = open(src, "rb").read()
# Home paths: this user's, and anybody's under /Users or /home. This user's
# first and by value, because it is not always under either: the pinned CI
# image runs the gate with HOME=/ci (#1331).
home = os.environ.get("HOME", "").rstrip("/")
if home:
    data = re.sub(re.escape(home.encode()) + rb"(?=/|[\s\"']|$)", b"~", data)
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
env_json="$(gate_env_json)"

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

# Sign the record's exact bytes. ssh-keygen writes provenance/local-gate.json.sig
# next to it; a stale one is removed first so a failed signing cannot leave an
# old signature standing for a new record.
rm -f provenance/local-gate.json.sig
if ! ssh-keygen -Y sign -n retnd-local-gate -f "$GATE_KEY" provenance/local-gate.json >/dev/null; then
  rm -f provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
  echo "local-gate: NOT RECORDED. ssh-keygen could not sign the record with $GATE_KEY." >&2
  exit 1
fi

# The reader's own check over what was just written, before this says
# "recorded": the record and the log from the working tree, the gate tree
# from HEAD, which is exactly what the release job will see once they are
# committed. A record this refuses is not one to commit.
echo "local-gate: running the release check over the record just written"
if ! PUBLISH=true bash "$SCRIPT_DIR/verify-local-gate.sh" --uncommitted HEAD; then
  echo "local-gate: NOT RECORDED. The release check refuses the record this run just wrote; its output above says why." >&2
  rm -f provenance/local-gate.json provenance/local-gate.log provenance/local-gate.json.sig
  exit 1
fi

echo ""
echo "local-gate: recorded. The full gate passed on ${head} in ${duration_s}s, in a detached worktree, and scripts/release/verify-local-gate.sh accepts the record."
echo "    provenance/local-gate.json   gate_tree ${gate_tree}"
echo "    provenance/local-gate.log    sha256 ${log_sha256}"
echo "    provenance/local-gate.json.sig   signed with ${GATE_KEY}"
echo "    Commit all three with the release prep: they are what require-release-gate reads before publish may run."

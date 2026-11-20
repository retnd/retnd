#!/usr/bin/env bash
# The fast tier of the gate (#1361).
#
#   scripts/ci-fast.sh                    # the staged paths (what pre-commit runs)
#   scripts/ci-fast.sh --base <rev>       # everything changed since <rev>...HEAD
#   scripts/ci-fast.sh --paths-from FILE  # a list of paths, one per line
#   scripts/ci-fast.sh --root DIR         # run in another checkout of the repository
#
# scripts/ci-local.sh is the full gate: the Docker crash matrix, the
# cross-compiles, the -race suites, the browser lane. It takes the better
# part of an hour, so on every commit it got bypassed, and all 28 PRs of
# EPIC N and EPIC T reached main carrying `Local-Gate: unverified` (#1361).
# This is the tier that is small enough to leave switched on. It runs what
# the change can have broken and nothing else:
#
#   always        the mutation-anchor check and the /api/v1 contract drift
#                 check (seconds each, and the two that would have caught
#                 both headline breaks on main)
#   Go test files the former-name scan (scripts/rename/check-no-former-name.sh)
#                 on any commit that touches a *_test.go (#1618); it reads the
#                 staged blobs, not the working tree (#1622)
#   Go files      gofmt on the changed files, then go vet, golangci-lint
#                 and go test on the packages those files live in, per
#                 module. Nothing downstream of them is rebuilt. A
#                 go.mod/go.sum change vets the whole module and ledgers the
#                 rest, because a dependency bump can break any package
#   conformance   docs/conformance/ or an EPIC spec staged runs the compat
#                 package that reads them (core/tests/compat)
#   shell, python bash -n (and shellcheck, when it is installed) on changed
#                 shell, ruff on changed Python under scripts/
#   JS            only when a path under ui/shared or apps/common/tests is
#                 staged, and then only that workspace (typecheck, eslint,
#                 and for ui/shared its vitest suite). A fresh worktree with
#                 no node_modules therefore does not block a Go-only or
#                 docs-only commit; a JS commit in one is refused with the
#                 command that fixes it, as the full gate does (#160)
#
# It does NOT run, and the full gate on the release PR still does: the
# whole-module suites, -race, the cross-compiles, the production builds, the
# browser and two-machine lanes, the repository-structure proofs and the
# gate's own self-test. A fast run passing is therefore not merge evidence
# for a release, and the commit it gates is stamped `Local-Gate: fast`, never
# left unstamped (.husky/prepare-commit-msg).
#
# It exports CI_LOCAL_TIER=fast, which core/tests/compat reads to stand down
# the one test that asks GitHub for live issue state. That one belongs to the
# full run (docs/reference/local-gate.md, "Tiers").
#
# The verdicts are the full gate's, from the same library: exit 0 for
# "ci-local: ok", 3 for "ci-local: INCOMPLETE" (something that applied could
# not run: no golangci-lint, a Docker suite with no daemon, a skipped JS
# workspace), 1 for "ci-local: FAILED".
#
# CI_LOCAL_FAST_JS_INSTALL=1 installs an uninstalled JS workspace that a
# staged path needs, instead of refusing. scripts/merge-gate.sh sets it,
# because its worktree is always fresh.
#
# THE QUEUE (#1544). This clone has dozens of worktrees and every one runs
# this hook, so a few commits landing together used to be a few go vet,
# golangci-lint and go test builds on the same cores at once (the Mac sat at
# a 1-minute load of 40 to 137). The Go and JS sections now take turns: a
# run first waits while the 1-minute load is above the core count (at most
# CI_FAST_LOAD_WAIT seconds, default 600, then it goes ahead anyway), then
# takes ci-fast.lock in the git common dir, which every worktree shares, and
# runs those sections under nice 10 with GOMAXPROCS=4 (unless the caller set
# one) and go test -p 2. A second run says who it waits for. To see who
# holds it: readlink "$(git rev-parse --git-common-dir)/ci-fast.lock", and
# the pid and worktree are in the directory it points at. The lock is the
# full gate's symlink primitive (scripts/lib/ci-local-gate.sh) under its own
# name, so an hour-long full gate never blocks a commit. A wait past
# CI_FAST_LOCK_TIMEOUT (default 1800) goes ahead without the lock, out loud:
# a hook that can wait forever is a hook people --no-verify. The
# always-steps above the Go section take seconds and stay outside it, and so
# does the wait for the full gate's Docker lock before a Docker-backed go
# test: the run gives the fast lock back, takes the Docker lock, then takes
# the fast lock again (the order is always Docker, then fast; #1560).
# CI_FAST_LOCK=0 turns off the lock and the load wait, for a place with one
# run by construction.
#
# Every heavy command runs under a watchdog: no output for CI_FAST_STALL_MIN
# minutes (default 10), or a run past CI_FAST_CAP_MIN (default 30), stops it
# and everything under it and ends the run "ci-local: FAILED (STALLED:
# <step>, <why>)". 0 turns a limit off. The Go steps are quiet by nature
# (go vet and golangci-lint print nothing until they finish, go test nothing
# until a package does), so their quiet limit has a floor: 20 minutes for
# vet and lint, and for go test 2 minutes past its own -timeout (22, and 47
# for the Docker-backed packages, whose cap is at least 50), so a real hang
# ends in Go's timeout panic with its goroutine dump instead of a kill.
set -e

mode=staged
base=""
paths_file=""
root_arg=""
while [ $# -gt 0 ]; do
  case "$1" in
    --staged) mode=staged ;;
    --base)
      [ $# -ge 2 ] || { echo "ci-fast: --base needs a revision" >&2; exit 2; }
      mode=base
      base="$2"
      shift
      ;;
    --paths-from)
      [ $# -ge 2 ] || { echo "ci-fast: --paths-from needs a file" >&2; exit 2; }
      mode=list
      paths_file="$2"
      shift
      ;;
    --root)
      [ $# -ge 2 ] || { echo "ci-fast: --root needs a directory" >&2; exit 2; }
      root_arg="$2"
      shift
      ;;
    -h | --help)
      sed -n '2,/^set -e$/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "ci-fast: unknown argument [$1]. See --help." >&2
      exit 2
      ;;
  esac
  shift
done

here="$(cd "$(dirname "$0")" && pwd)"

# The index being committed (#1554). In a hook git points GIT_INDEX_FILE at
# it, and on a pathspec commit (git commit -- <paths>, which is what the
# shared commit helper runs) that is a temporary index holding exactly the
# commit, while the real index holds none of an unstaged path. Reading the
# real index scoped such a commit to 0 paths and checked nothing. So it is
# kept here, absolute, before any cd, and used for the staged list alone;
# the variable itself is still unset below, so no go test or npm run under
# this script can write into the commit's index.
commit_index=""
case "${GIT_INDEX_FILE:-}" in
  "") ;;
  /*) commit_index="$GIT_INDEX_FILE" ;;
  *) commit_index="$(pwd)/$GIT_INDEX_FILE" ;;
esac

if [ -n "$root_arg" ]; then
  cd "$root_arg"
fi

# A hook's git environment points at the commit in progress, and every git
# call below has to resolve the repository fresh (see scripts/ci-local.sh).
unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

# The same PATH rule as the full gate (#1442): Homebrew is appended when the
# caller's PATH doesn't have it, never moved ahead of what the caller set.
case ":$PATH:" in
  *:/opt/homebrew/bin:*) ;;
  *) PATH="$PATH:/opt/homebrew/bin" ;;
esac
export PATH
if [ -n "${GOLANGCI_LINT_DIR:-}" ]; then
  export PATH="$GOLANGCI_LINT_DIR:$PATH"
fi

export CI_LOCAL_TIER=fast
# shellcheck disable=SC2034  # read by gate_lint in the sourced lib below
REPO_ROOT="$(pwd)"

# shellcheck source=scripts/lib/ci-local-gate.sh
. "$here/lib/ci-local-gate.sh"

# ----------------------------------------------------------------- the queue
#
# See THE QUEUE in the header (#1544). Everything here is this script's own;
# the lock reuses the library's primitives (gate_lock_try, gate_lock_dead,
# gate_lock_identity, gate_lock_sweep_orphans) but keeps its own state, because
# the library's gate_lock_* state is the Docker-step lock and gate_step gives
# that one back at every step.

CI_FAST_LOAD_WAIT="${CI_FAST_LOAD_WAIT:-600}"
CI_FAST_LOAD_POLL="${CI_FAST_LOAD_POLL:-10}"
CI_FAST_LOCK_TIMEOUT="${CI_FAST_LOCK_TIMEOUT:-1800}"
CI_FAST_POLL="${CI_FAST_POLL:-1}"
CI_FAST_STALL_MIN="${CI_FAST_STALL_MIN:-10}"
CI_FAST_CAP_MIN="${CI_FAST_CAP_MIN:-30}"
CI_FAST_KILL_GRACE="${CI_FAST_KILL_GRACE:-10}"

fast_queued=0
fast_lock_at=""
fast_lock_path=""
fast_lock_mine=""
fast_run_pid=""
fast_tmp=""

# fast_seconds <minutes> -> whole seconds, or nothing for a value that is not
# a plain number of minutes (fractions are fine).
fast_seconds() {
  awk -v m="$1" 'BEGIN { if (m ~ /^[0-9]+(\.[0-9]+)?$/ || m ~ /^\.[0-9]+$/) printf "%d\n", m * 60 }'
}
fast_stall_s="$(fast_seconds "$CI_FAST_STALL_MIN")"
fast_cap_s="$(fast_seconds "$CI_FAST_CAP_MIN")"
if [ -z "$fast_stall_s" ] || [ -z "$fast_cap_s" ]; then
  echo "==> ci-local: FAILED (ci-fast watchdog). CI_FAST_STALL_MIN=[$CI_FAST_STALL_MIN] and CI_FAST_CAP_MIN=[$CI_FAST_CAP_MIN] have to be a number of minutes; 0 turns that limit off." >&2
  exit 2
fi

# fast_at_least <seconds> <floor> -> 0 stays 0 (the limit is off), anything
# else is raised to the floor.
fast_at_least() {
  if [ "$1" = 0 ] || [ "$1" -ge "$2" ]; then echo "$1"; else echo "$2"; fi
}

# fast_load1 -> the 1-minute load average, or nothing when it cannot be read.
# sysctl first (macOS); on Linux that key does not exist and /proc answers.
fast_load1() {
  fl1="$(sysctl -n vm.loadavg 2>/dev/null | tr -d '{}' | awk '{ print $1 }' || true)"
  if [ -z "$fl1" ] && [ -r /proc/loadavg ]; then
    fl1="$(cut -d' ' -f1 /proc/loadavg)"
  fi
  echo "$fl1"
}

fast_ncpu() {
  sysctl -n hw.ncpu 2>/dev/null || nproc 2>/dev/null || getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4
}

# fast_wait_for_load <max load> <seconds>
# Wait while the 1-minute load is above <max load>, for at most <seconds>,
# one line a minute. Returns 0 either way: the wait is a courtesy, never a
# refusal.
fast_wait_for_load() {
  [ "$2" -gt 0 ] || return 0
  fwl_began="$(date +%s)"
  fwl_said=-60
  while :; do
    fwl_load="$(fast_load1)"
    [ -n "$fwl_load" ] || return 0
    if ! awk -v l="$fwl_load" -v m="$1" 'BEGIN { exit !(l > m) }'; then
      return 0
    fi
    fwl_waited=$(( $(date +%s) - fwl_began ))
    if [ "$fwl_waited" -ge "$2" ]; then
      echo "==> ci-fast: the 1-minute load is still $fwl_load (over $1) after ${fwl_waited}s; going ahead anyway. CI_FAST_LOAD_WAIT=<seconds> changes the wait."
      return 0
    fi
    if [ $(( fwl_waited - fwl_said )) -ge 60 ]; then
      fwl_said="$fwl_waited"
      echo "==> ci-fast: waiting for the load to come down: 1-minute load $fwl_load on $1 cores, waited ${fwl_waited}s of at most ${2}s (#1544)"
    fi
    sleep "$CI_FAST_LOAD_POLL"
  done
}

# fast_lock_acquire <lock path>
# The library's lock shape (a symlink at <lock path> pointing at this
# holder's own directory beside it, see gate_lock_acquire), its dead-holder
# reclaim, and a wait that is capped and goes ahead without the lock at the
# cap instead of failing the commit.
#
# fast_lock_path is set before the first attempt, not after the loop: a
# signal can land between the `ln -s` and the next line, and release has to
# see the path then, or it deletes the holder directory and leaves the link
# pointing at nothing (#1552 review, B2). Release only removes a link that
# points at this run's own directory, so knowing the path early is safe.
#
# A link whose holder directory is gone has no holder to ask. It is only
# that way for an instant during a reclaim (the directory is renamed, then
# the link), so one still dangling at the same target after three polls was
# left by a run killed in that instant and is removed.
fast_lock_acquire() {
  fla_path="$1"
  fast_lock_path="$fla_path"
  fast_lock_mine="$fla_path.$(gate_lock_identity)"
  rm -rf "$fast_lock_mine"
  mkdir -p "$fast_lock_mine"
  echo "$$" >"$fast_lock_mine/pid"
  hostname >"$fast_lock_mine/host"
  gate_boot_id >"$fast_lock_mine/boot"
  pwd >"$fast_lock_mine/where"
  gate_lock_sweep_orphans "$fla_path"
  fla_began="$(date +%s)"
  fla_said=-60
  fla_waited=0
  fla_dangling=""
  fla_dangling_polls=0
  while ! gate_lock_try "$fla_path" "$(basename "$fast_lock_mine")"; do
    fla_holder=""
    fla_target="$(readlink "$fla_path" 2>/dev/null || true)"
    [ -z "$fla_target" ] || fla_holder="$(dirname "$fla_path")/$fla_target"
    if [ -n "$fla_holder" ] && [ ! -d "$fla_holder" ]; then
      if [ "$fla_target" = "$fla_dangling" ]; then
        fla_dangling_polls=$((fla_dangling_polls + 1))
      else
        fla_dangling="$fla_target"
        fla_dangling_polls=1
      fi
      if [ "$fla_dangling_polls" -ge 3 ] && mv "$fla_path" "$fla_path.deadlink.$$" 2>/dev/null; then
        if [ "$(readlink "$fla_path.deadlink.$$" 2>/dev/null)" = "$fla_target" ]; then
          rm -f "$fla_path.deadlink.$$"
          echo "==> ci-fast lock: reclaiming $fla_path, a link to $fla_target, which no longer exists (a run was killed while releasing or reclaiming it)"
        else
          # A fresh holder linked between the look and the rename: give its
          # link back. If yet another run got in first, that one holds it.
          mv -n "$fla_path.deadlink.$$" "$fla_path" 2>/dev/null || rm -f "$fla_path.deadlink.$$"
        fi
        fla_dangling=""
        fla_dangling_polls=0
        continue
      fi
    else
      fla_dangling=""
      fla_dangling_polls=0
    fi
    if [ -n "$fla_holder" ] && [ -d "$fla_holder" ] && gate_lock_dead "$fla_holder"; then
      fla_stale="$fla_holder.reclaimed.$$"
      if mv "$fla_holder" "$fla_stale" 2>/dev/null; then
        echo "==> ci-fast lock: reclaiming $fla_path, left behind by pid $(cat "$fla_stale/pid" 2>/dev/null || echo '?') ($(cat "$fla_stale/where" 2>/dev/null || echo 'unknown worktree')), which no longer exists"
        rm -rf "$fla_stale"
        if mv "$fla_path" "$fla_path.deadlink.$$" 2>/dev/null; then
          rm -f "$fla_path.deadlink.$$"
        fi
      fi
      continue
    fi
    fla_pid="$(cat "$fla_holder/pid" 2>/dev/null || echo '?')"
    fla_where="$(cat "$fla_holder/where" 2>/dev/null || echo "no holder directory at ${fla_target:-?}")"
    fla_waited=$(( $(date +%s) - fla_began ))
    if [ "$fla_waited" -ge "$CI_FAST_LOCK_TIMEOUT" ]; then
      echo "==> ci-fast lock: still held by pid $fla_pid ($fla_where) after ${fla_waited}s; going ahead without the lock, because a hook that waits forever gets bypassed. CI_FAST_LOCK_TIMEOUT=<seconds> changes the wait."
      rm -rf "$fast_lock_mine"
      fast_lock_mine=""
      fast_lock_path=""
      return 0
    fi
    if [ $(( fla_waited - fla_said )) -ge 60 ]; then
      fla_said="$fla_waited"
      echo "==> ci-fast lock: waiting for $fla_path, held by pid $fla_pid ($fla_where); one worktree's fast tier runs at a time (#1544). Waited ${fla_waited}s of at most ${CI_FAST_LOCK_TIMEOUT}s."
    fi
    sleep "$CI_FAST_POLL"
  done
  if [ "$fla_said" -ge 0 ]; then
    echo "==> ci-fast lock: acquired after ${fla_waited}s"
  else
    echo "==> ci-fast lock: held ($fla_path)"
  fi
}

# fast_lock_release: idempotent, and only if the lock still points at us.
fast_lock_release() {
  [ -n "$fast_lock_mine" ] || return 0
  if [ -n "$fast_lock_path" ] && [ "$(readlink "$fast_lock_path" 2>/dev/null)" = "$(basename "$fast_lock_mine")" ]; then
    rm -f "$fast_lock_path"
  fi
  rm -rf "$fast_lock_mine"
  fast_lock_mine=""
  fast_lock_path=""
}

# fast_enter_queue
# Called before the first heavy step, so a docs-only commit never queues.
# Nice and GOMAXPROCS apply even with CI_FAST_LOCK=0; only the lock and the
# load wait are skipped. Renicing this shell once covers every command it
# starts from here on.
fast_enter_queue() {
  [ "$fast_queued" = 0 ] || return 0
  fast_queued=1
  GOMAXPROCS="${GOMAXPROCS:-4}"
  export GOMAXPROCS
  renice 10 -p "$$" >/dev/null 2>&1 || true
  if [ "${CI_FAST_LOCK:-1}" = 0 ]; then
    echo "==> ci-fast lock: off (CI_FAST_LOCK=0), so no queue and no load wait; still nice 10, GOMAXPROCS=$GOMAXPROCS and go test -p 2"
    return 0
  fi
  feq_common="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
  if [ -z "$feq_common" ]; then
    echo "==> ci-fast lock: not a git checkout, so this run is not queued"
    return 0
  fi
  fast_wait_for_load "$(fast_ncpu)" "$CI_FAST_LOAD_WAIT"
  fast_lock_at="$feq_common/ci-fast.lock"
  fast_lock_acquire "$fast_lock_at"
}

# fast_docker_step <heading>
# gate_docker_step, which waits for the full gate's Docker lock for up to
# GATE_LOCK_TIMEOUT (2 hours), with that wait kept out of the fast queue
# (#1560). Held through it, the fast lock made every other worktree's commit
# wait out CI_FAST_LOCK_TIMEOUT behind one run that was doing nothing, and
# then go ahead unlocked, all together.
#
# The lock order is the Docker lock, then the fast lock. This run gives the
# fast lock back, waits for and takes the Docker lock holding nothing else,
# then takes the fast lock again before the step's command runs, so the
# heavy part still runs in one worktree at a time. It never waits for the
# Docker lock holding the fast lock, and the full gate only ever takes the
# Docker lock, so no two runs can each hold what the other waits for. A run
# that went ahead unlocked at the cap, or runs with CI_FAST_LOCK=0, has
# nothing to give back and just takes the Docker step.
fast_docker_step() {
  if [ -z "$fast_lock_mine" ]; then
    gate_docker_step "$1"
    return 0
  fi
  fast_lock_release
  echo "==> ci-fast lock: given back while this run waits for the full gate's Docker lock (#1560)"
  gate_docker_step "$1"
  fast_lock_acquire "$fast_lock_at"
}

# fast_tree <pid> -> it and every descendant, parents first.
fast_tree() {
  local child
  echo "$1"
  for child in $(pgrep -P "$1" 2>/dev/null); do
    fast_tree "$child"
  done
}

# fast_stop_step: TERM the watched step and everything under it, KILL what
# is still there CI_FAST_KILL_GRACE seconds later. The tree is read before
# the first signal, since children of a dead parent are reparented away.
fast_stop_step() {
  [ -n "$fast_run_pid" ] || return 0
  fss_pids="$(fast_tree "$fast_run_pid")"
  fast_run_pid=""
  # shellcheck disable=SC2086
  kill -TERM $fss_pids 2>/dev/null || true
  fss_i=0
  while [ "$fss_i" -lt "$CI_FAST_KILL_GRACE" ]; do
    fss_alive=0
    for fss_p in $fss_pids; do
      if kill -0 "$fss_p" 2>/dev/null; then fss_alive=1; fi
    done
    [ "$fss_alive" = 1 ] || break
    sleep 1
    fss_i=$((fss_i + 1))
  done
  for fss_p in $fss_pids; do
    if kill -0 "$fss_p" 2>/dev/null; then kill -KILL "$fss_p" 2>/dev/null || true; fi
  done
  return 0
}

# fast_leave_queue: everything the queue holds, given back. Called once the
# heavy sections are done and from every exit path.
fast_leave_queue() {
  fast_stop_step
  fast_lock_release
  if [ -n "$fast_tmp" ]; then
    rm -rf "$fast_tmp"
    fast_tmp=""
  fi
  return 0
}

# fast_show <log> <from byte>: print what the step wrote since <from byte>
# and set fast_shown to the new size.
fast_show() {
  fsh_size="$(wc -c <"$1" | tr -d ' ')"
  if [ "$fsh_size" -gt "$2" ]; then
    tail -c +"$(($2 + 1))" "$1" | head -c "$((fsh_size - $2))"
  fi
  fast_shown="$fsh_size"
}

# fast_run <cap seconds> <stall seconds> <dir> <command...>
# Run a heavy command in <dir> (GOWORK=off, as every module step here wants)
# under the watchdog: its output is passed through as it comes, and no new
# output for <stall seconds>, or a run past <cap seconds>, stops it and ends
# the run STALLED. 0 turns a limit off. Returns the command's own status.
fast_run() {
  frn_cap="$1"
  frn_stall="$2"
  frn_dir="$3"
  shift 3
  [ -n "$fast_tmp" ] || fast_tmp="$(mktemp -d "${TMPDIR:-/tmp}/ci-fast.XXXXXX")"
  frn_log="$fast_tmp/step.log"
  : >"$frn_log"
  (cd "$frn_dir" && GOWORK=off "$@") >"$frn_log" 2>&1 &
  fast_run_pid=$!
  frn_began="$(date +%s)"
  frn_last="$frn_began"
  frn_why=""
  frn_polls=0
  fast_shown=0
  while kill -0 "$fast_run_pid" 2>/dev/null; do
    # Short polls first, so a command that takes a second costs no more.
    case "$frn_polls" in
      0) sleep 0.1 ;;
      1) sleep 0.2 ;;
      2) sleep 0.5 ;;
      *) sleep "$CI_FAST_POLL" ;;
    esac
    frn_polls=$((frn_polls + 1))
    frn_now="$(date +%s)"
    frn_before="$fast_shown"
    fast_show "$frn_log" "$frn_before"
    if [ "$fast_shown" -gt "$frn_before" ]; then
      frn_last="$frn_now"
    fi
    if [ "$frn_cap" -gt 0 ] && [ $((frn_now - frn_began)) -ge "$frn_cap" ]; then
      frn_why="it was still running at the hard cap of ${frn_cap}s (CI_FAST_CAP_MIN=$CI_FAST_CAP_MIN)"
      break
    fi
    if [ "$frn_stall" -gt 0 ] && [ $((frn_now - frn_last)) -ge "$frn_stall" ]; then
      frn_why="no output for $((frn_now - frn_last))s (CI_FAST_STALL_MIN=$CI_FAST_STALL_MIN)"
      break
    fi
  done
  if [ -n "$frn_why" ]; then
    fast_stop_step
    fast_show "$frn_log" "$fast_shown"
    # shellcheck disable=SC2034  # read by gate_exit_marker in the sourced lib
    gate_marked=1
    echo "" >&2
    gate_clock_close >&2
    # shellcheck disable=SC2154  # set by gate_announce in the sourced lib
    echo "==> ci-local: FAILED (STALLED: $gate_last_step, $frn_why)" >&2
    echo "    ci-fast stopped the step and everything under it. If it was only slow, raise the limit for this" >&2
    echo "    run (CI_FAST_STALL_MIN or CI_FAST_CAP_MIN, in minutes, 0 turns one off); if it hangs again, it is a bug." >&2
    exit 1
  fi
  frn_status=0
  wait "$fast_run_pid" || frn_status=$?
  fast_run_pid=""
  fast_show "$frn_log" "$fast_shown"
  return "$frn_status"
}

# The library's traps, then the same four with the queue's cleanup ahead of
# the library's handlers (a trap is set, not appended, so these replace the
# ones gate_install_traps just set and call them).
gate_install_traps
fast_exit_status=0
trap 'fast_exit_status=$?; fast_leave_queue; gate_on_exit "$fast_exit_status"' EXIT
trap 'fast_leave_queue; gate_on_signal INT 130' INT
trap 'fast_leave_queue; gate_on_signal TERM 143' TERM
trap 'fast_leave_queue; gate_on_signal HUP 129' HUP

# The changed paths, newline separated. A path with a newline in it splits
# here, which can only make the sweep look at a file that is not there.
case "$mode" in
  staged)
    if [ -n "$commit_index" ]; then
      changed="$(GIT_INDEX_FILE="$commit_index" git diff --cached --name-only -z --no-renames --diff-filter=ACMRTD | tr '\0' '\n')"
    else
      changed="$(git diff --cached --name-only -z --no-renames --diff-filter=ACMRTD | tr '\0' '\n')"
    fi
    ;;
  base) changed="$(git diff --name-only -z --no-renames "$base...HEAD" | tr '\0' '\n')" ;;
  list) changed="$(cat "$paths_file")" ;;
esac

has() { printf '%s\n' "$changed" | grep -Eq "$1"; }

count="$(printf '%s\n' "$changed" | grep -c . || true)"
echo "==> ci-fast: the fast tier, scoped to $count changed path(s) ($mode). The full gate is scripts/ci-local.sh."

# ---------------------------------------------------------------- always

gate_step "mutation anchors in the compat, conformance and race selftests still match the tree (#458)"
bash scripts/selftest/check-anchors.sh

gate_step "/api/v1 bindings match the contract, and no implementation type leaks (#166)"
bash scripts/api/check-contract-drift.sh

# The brand guard (#1618). core/tests/brandguard only ran when a commit
# touched its own package, and the red-tests route (.husky/pre-commit) skips
# the tier, so a test file with the former name in it reached main through a
# red commit that no later commit re-checked. Any commit that touches a Go
# test file now runs the tree-wide scan (scripts/rename/check-no-former-name.sh,
# git grep over tracked files, no Go toolchain, about a second). It is not
# scoped to the staged files: the name in a file nobody touched is the same
# failure, and the scan is cheaper than working out which files it owns. It
# reads the index (git grep --cached, #1622), so it judges what is staged, not
# whatever the working tree happens to hold.
if has '_test\.go$'; then
  gate_step "no test file spells the former name (#1618)"
  # GIT_INDEX_FILE was unset above, and in a hook `commit -a` and
  # `commit -- <paths>` commit from a temporary index: without it
  # `git grep --cached` reads the real index, which still has the old blob.
  if [ -n "$commit_index" ]; then
    GIT_INDEX_FILE="$commit_index" bash scripts/rename/check-no-former-name.sh
  else
    bash scripts/rename/check-no-former-name.sh
  fi
fi

# --------------------------------------------------------------- formatting

gofiles=""
while IFS= read -r f; do
  if [ -n "$f" ] && [ -f "$f" ]; then
    case "$f" in *.go) gofiles="$gofiles $f" ;; esac
  fi
done <<EOF
$changed
EOF
if [ -n "$gofiles" ]; then
  gate_step "changed Go files are gofmt-clean"
  # shellcheck disable=SC2086
  unformatted="$(gofmt -l $gofiles)"
  if [ -n "$unformatted" ]; then
    echo "==> gofmt would change:" >&2
    printf '%s\n' "$unformatted" >&2
    exit 1
  fi
fi

# ------------------------------------------------------------ shell, python

shfiles=""
pyfiles=""
while IFS= read -r f; do
  if [ -n "$f" ] && [ -f "$f" ]; then
    case "$f" in
      .husky/*) sh -n "$f" ;;
      *.sh) shfiles="$shfiles $f" ;;
      scripts/*.py) pyfiles="$pyfiles $f" ;;
    esac
  fi
done <<EOF
$changed
EOF
if [ -n "$shfiles" ]; then
  gate_step "changed shell scripts parse (bash -n, and shellcheck where it is installed)"
  for f in $shfiles; do
    bash -n "$f"
  done
  if command -v shellcheck >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    shellcheck -S warning $shfiles
  fi
fi
if [ -n "$pyfiles" ]; then
  gate_step "changed Python under scripts/ passes ruff (#672)"
  if command -v ruff >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    ruff check --config scripts/bdtools/pyproject.toml $pyfiles
  else
    gate_note_skip "ruff over the changed Python (#672): ruff is not on PATH. Install it (pipx install ruff) and re-run."
  fi
fi

# ---------------------------------------------------------------- Go modules

# Packages whose tests need the Docker daemon. With it down they t.Skip and
# go test still says ok, so they go through gate_docker_step, which ledgers
# that instead of passing it (#160).
docker_re='(^|/)tests/(crashmatrix|sftpintegration|sshexecintegration|miniointegration|conformance|machinegate|containerhooks|adapterstacks|dockercli)$|(^|/)internal/transport/rclone$'

lint_ready=0
if command -v golangci-lint >/dev/null 2>&1; then
  lint_ready=1
  # Run under the Go the linter was built with, as the full gate does:
  # a linter cannot read export data a newer Go wrote.
  if [ -z "${GOTOOLCHAIN:-}" ]; then
    _built=$(golangci-lint --version 2>/dev/null | sed -n 's/.*built with go\([0-9][0-9.]*\).*/\1/p' | head -n 1)
    if [ -n "$_built" ]; then
      GOTOOLCHAIN="go$_built"
      export GOTOOLCHAIN
    fi
  fi
fi

for module in core apps/common distribution apps/generic apps/synology apps/ugos/backend; do
  [ -f "$module/go.mod" ] || continue

  # What in this module the change touches. apps/common/tests is a JS
  # workspace and Markdown inside a module is not code. The conformance
  # matrices and EPIC specs live under docs/ and are read by one core
  # package, so touching them touches that package.
  mod_files="$(printf '%s\n' "$changed" | grep -E "^$module/" | grep -vE '\.md$' || true)"
  if [ "$module" = "apps/common" ]; then
    mod_files="$(printf '%s\n' "$mod_files" | grep -vE '^apps/common/tests/' || true)"
  fi
  if [ "$module" = "core" ] && has '^(docs/conformance/|docs/EPIC-[A-Z]-)'; then
    mod_files="$mod_files
core/tests/compat/matrix_test.go"
  fi

  whole=0
  pkgs=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    case "$f" in
      "$module/go.mod" | "$module/go.sum")
        whole=1
        continue
        ;;
    esac
    d="$(dirname "$f")"
    while [ "$d" != "$module" ] && ! compgen -G "$d/*.go" >/dev/null; do
      d="$(dirname "$d")"
    done
    if [ "$d" = "$module" ]; then
      compgen -G "$module/*.go" >/dev/null || continue
      rel="."
    else
      rel="./${d#"$module"/}"
    fi
    case " $pkgs " in
      *" $rel "*) ;;
      *) pkgs="$pkgs $rel" ;;
    esac
  done <<EOF
$mod_files
EOF

  if [ "$whole" = 1 ] || [ -n "$pkgs" ]; then
    fast_enter_queue
  fi

  if [ "$whole" = 1 ]; then
    gate_step "$module go vet ./... (go.mod or go.sum changed)"
    fast_run "$fast_cap_s" "$(fast_at_least "$fast_stall_s" 1200)" "$module" go vet ./...
    gate_note_skip "$module golangci-lint and go test after a go.mod/go.sum change: a dependency bump can break any package, and only the full gate runs the whole module (docs/rclone-upgrade.md for core)"
    continue
  fi
  [ -n "$pkgs" ] || continue

  gate_step "$module go vet, touched packages:$pkgs"
  # shellcheck disable=SC2086
  fast_run "$fast_cap_s" "$(fast_at_least "$fast_stall_s" 1200)" "$module" go vet $pkgs

  if [ "$lint_ready" = 1 ]; then
    gate_step "$module golangci-lint, touched packages:$pkgs"
    # shellcheck disable=SC2086
    fast_run "$fast_cap_s" "$(fast_at_least "$fast_stall_s" 1200)" . gate_lint "$module" $pkgs
  else
    gate_note_skip "$module golangci-lint on the touched packages:$pkgs. golangci-lint is not on PATH; build the pinned one (scripts/ci/pins.env)."
  fi

  plain=""
  dock=""
  for rel in $pkgs; do
    case "$rel" in
      ./cmd/gotestwatch)
        gate_note_skip "$module cmd/gotestwatch: it is compiled and run under its own exit-status contract by the full gate (#533), not by go test"
        ;;
      *)
        if printf '%s\n' "$rel" | grep -Eq "$docker_re"; then
          dock="$dock $rel"
        else
          plain="$plain $rel"
        fi
        ;;
    esac
  done
  if [ -n "$plain" ]; then
    gate_step "$module go test, touched packages:$plain"
    # shellcheck disable=SC2086
    fast_run "$fast_cap_s" "$(fast_at_least "$fast_stall_s" 1320)" "$module" go test -p 2 -timeout 20m $plain
  fi
  if [ -n "$dock" ]; then
    # The full gate's Docker lock is waited for outside the fast queue
    # (fast_docker_step, #1560). These tests get -timeout 45m and pull
    # fixture images in silence, so the watchdog gives them at least 50
    # minutes, and 47 of quiet so Go's own timeout fires first.
    fast_docker_step "$module go test, Docker-backed touched packages:$dock"
    # shellcheck disable=SC2086
    fast_run "$(fast_at_least "$fast_cap_s" 3000)" "$(fast_at_least "$fast_stall_s" 2820)" "$module" go test -p 2 -timeout 45m $dock
  fi
done

# ----------------------------------------------------------------------- JS

# Only a workspace a staged path is in. gate_require_js_deps names the
# uninstalled ones and refuses, unless CI_LOCAL_SKIP_JS=1 chose the skip.
for ws in ui/shared apps/common/tests; do
  has "^$ws/" || continue
  if [ "$(gate_workspace_state "$ws")" = uninstalled ] && [ "${CI_LOCAL_FAST_JS_INSTALL:-0}" = "1" ]; then
    fast_enter_queue
    gate_step "$ws install (CI_LOCAL_FAST_JS_INSTALL=1)"
    eval "$(gate_install_hint "$ws")"
  fi
  gate_require_js_deps "$ws"
  case "$(gate_workspace_state "$ws")" in
    installed)
      fast_enter_queue
      gate_step "$ws typecheck"
      fast_run "$fast_cap_s" "$fast_stall_s" "$ws" npm run --silent lint
      gate_step "$ws eslint"
      fast_run "$fast_cap_s" "$fast_stall_s" "$ws" npm run --silent eslint
      if [ "$ws" = "ui/shared" ]; then
        gate_step "ui/shared typecheck every provider"
        fast_run "$fast_cap_s" "$fast_stall_s" ui/shared npm run --silent typecheck:providers
        gate_step "ui/shared tests"
        fast_run "$fast_cap_s" "$fast_stall_s" ui/shared npm test --silent
      fi
      ;;
    uninstalled)
      gate_note_skip "$ws typecheck and eslint ($(gate_install_hint "$ws")); skipped out loud with CI_LOCAL_SKIP_JS=1"
      ;;
  esac
done

# The heavy part is over: the next worktree's run can go.
fast_leave_queue

# Never a bare echo: "ok" has to be earned by an empty skip ledger.
gate_summary

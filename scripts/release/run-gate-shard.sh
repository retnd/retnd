#!/usr/bin/env bash
# Run one gate shard on one host (#1460, item C of the sharded gate, #1455).
#
#   bash scripts/release/run-gate-shard.sh <shard> [--host <mars|nas|mac>] [--out DIR]
#
# The shard's command is `bash scripts/ci-local.sh --shard <shard>`. On mars
# and nas it runs through scripts/ci-docker.sh --host ... --deps, the
# path release-e2e.sh already uses, so the tree shipping, the fence, the
# cleanup on every exit path and the /tmp handling are ci-docker.sh's and it
# is not edited here. `mac`, the default when --host is left out (the recorder
# passes none), runs the same command directly in this tree. mercury is refused:
# gate work stays off it (benchmarks own it, through bench-queue.sh).
#
# The ui-browser shard (#1532, #1537) is the exception to that default: it
# drives a Chromium through every spec file in ui/shared/e2e, and heavy retnd
# steps stay off the Mac, so with no --host it goes to mars. `--host mac` for
# it is refused unless RETND_UI_BROWSER_ALLOW_LOCAL=1 is set, and the gate then
# runs it through the Playwright lock.
#
# What comes back, in DIR (default $TMPDIR/retnd-shard-runs, outside the git
# dir ci-docker.sh ships, and refused inside the tree, which it would dirty):
#   <shard>.log      everything the run printed (also streamed to stdout)
#   <shard>.result   key=value lines: shard, host, platform, command,
#                    started_at (UTC), duration_s, exit, status, reason
# and one of three exit statuses, so the recorder never parses prose:
#   0  ok          the shard exited 0 AND the last "==> ci-local:" line of its
#                  log is exactly "==> ci-local: ok (shard <name>)"
#   1  failed      the last "==> ci-local:" line is FAILED and it exited non-zero
#   3  INCOMPLETE  anything else: refused before shipping, the gate's own
#                  INCOMPLETE, a dropped ssh session, a kill, exit 0 with no
#                  verdict. Prints its own "==> ci-local: INCOMPLETE" line so a
#                  reader of the log alone sees it. Never counted as a red
#                  test, never as a pass.
# 2 is a usage error (nothing ran, no result file).
#
# Environment parity (plan section 5), refused BEFORE anything is shipped:
#   - the clone must hold refs/remotes/origin/release and a v* tag. The git dir
#     travels with the tree, and a clone without them failed deep inside the
#     gate on Mercury;
#   - the tree must be clean: ci-docker.sh copies tracked plus untracked files,
#     so a dirty tree is not the committed tree the record names;
#   - mars and nas are native x86 and run linux/amd64, and a requested platform
#     that would run emulated is refused (an inherited DOCKER_DEFAULT_PLATFORM
#     is not: ci-docker.sh unsets it).
# Everything else happens through ci-docker.sh, which writes into one scratch
# directory under the host's workspace and removes it on exit. When rsync is
# refused (the NAS's receiver is) it copies the tree with tar over ssh, so the
# host runs `tar -x` into that scratch directory and nothing else of its own.
#
#   CI_DOCKER_MEMORY / CI_DOCKER_CPUS   the shard's container fence, passed on
#
# A shard can have a minimum memory, in shard_min_gib below (the manifest has
# no columns, the fence is this runner's business). selftest-encryption needs
# 9g: two copycrypt controls allocate several GiB and were OOM-killed at 4g. No
# CI_DOCKER_MEMORY asks ci-docker.sh for that minimum, and a smaller one is
# refused as INCOMPLETE before anything ships. The gate itself refuses the step
# in less than 9 GiB too, for a daemon that clamps the fence to its own memory.
#   CI_DOCKER_SH                        test seam: a stand-in for ci-docker.sh
set -uo pipefail

tools="$(cd "$(dirname "$0")/../.." && pwd)"
usage() { echo "usage: run-gate-shard.sh <shard> [--host <mars|nas|mac>] [--out DIR]" >&2; exit 2; }

shard="${1:-}"; [ -n "$shard" ] || usage
shift
host=""; out=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --host) [ "$#" -ge 2 ] || usage; host="$2"; shift 2 ;;
    --out) [ "$#" -ge 2 ] || usage; out="$2"; shift 2 ;;
    *) usage ;;
  esac
done
case "$shard" in [a-z0-9]*) ;; *) echo "run-gate-shard: bad shard name [$shard]" >&2; exit 2 ;; esac
case "$shard" in *[!a-z0-9-]*) echo "run-gate-shard: bad shard name [$shard]" >&2; exit 2 ;; esac
if [ -z "$host" ]; then
  case "$shard" in
    ui-browser) host=mars; echo "run-gate-shard: no --host given, so the ui-browser shard runs on mars (heavy steps stay off the Mac)" >&2 ;;
    *) host=mac ;;
  esac
fi
if [ "$shard" = ui-browser ] && [ "$host" = mac ] && [ "${RETND_UI_BROWSER_ALLOW_LOCAL:-0}" != 1 ]; then
  echo "run-gate-shard: the ui-browser shard does not run on the Mac, where heavy steps do not run. Use --host mars, or set RETND_UI_BROWSER_ALLOW_LOCAL=1 to run it here through the Playwright lock" >&2
  exit 2
fi
case "$host" in
  mars) target=devshell@192.168.0.100; platform=linux/amd64 ;;
  nas) target=rom@192.168.0.10; platform=linux/amd64 ;;
  mercury) echo "run-gate-shard: mercury is refused: gate shards stay off it (benchmarks own it, through bench-queue.sh)" >&2; exit 2 ;;
  mac) target=""; platform=host ;;
  *) echo "run-gate-shard: unknown host [$host] (mars, nas, mac)" >&2; usage ;;
esac

root="$(git rev-parse --show-toplevel)" || { echo "run-gate-shard: not in a git tree" >&2; exit 2; }
cd "$root" || exit 2
[ -n "$out" ] || out="${TMPDIR:-/tmp}/retnd-shard-runs"
mkdir -p "$out" || exit 2
out="$(cd "$out" && pwd -P)"
case "$out/" in
  "$(cd "$root" && pwd -P)"/*) echo "run-gate-shard: --out $out is inside the tree, which it would make dirty" >&2; exit 2 ;;
esac
log="$out/$shard.log"; resultf="$out/$shard.result"
cmd="bash scripts/ci-local.sh --shard $shard"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; t0="$(date +%s)"
: > "$log"
# A result left by an earlier run must not outlive this one if it dies before
# writing its own: a reader would take it for this run's.
rm -f "$resultf" "$resultf.tmp"

write_result() { # <exit> <status> <reason>
  {
    echo "shard=$shard"; echo "host=$host"; echo "platform=$platform"; echo "command=$cmd"
    echo "started_at=$started_at"; echo "duration_s=$(( $(date +%s) - t0 ))"
    echo "exit=$1"; echo "status=$2"; echo "reason=$3"
  } > "$resultf.tmp" && mv "$resultf.tmp" "$resultf"
}
child=""
incomplete() {
  echo "==> ci-local: INCOMPLETE (shard $shard). run-gate-shard: $1" | tee -a "$log" >&2
  write_result "${2:--}" incomplete "$1"; exit 3
}
on_signal() {
  if [ -n "$child" ]; then
    # TERM first, so ci-docker.sh runs its own cleanup; KILL if it is still
    # there after a grace period.
    kill -TERM "$child" 2>/dev/null
    for _ in 1 2 3 4 5 6 7 8 9 10; do kill -0 "$child" 2>/dev/null || break; sleep 0.5; done
    kill -KILL "$child" 2>/dev/null
  fi
  incomplete "interrupted"
}
trap on_signal INT TERM HUP


# --- parity checks, before anything is shipped ---------------------------------
git rev-parse -q --verify refs/remotes/origin/release >/dev/null \
  || incomplete "this clone has no origin/release. Run: git fetch origin release"
[ -n "$(git tag -l 'v*' | head -n 1)" ] \
  || incomplete "this clone has no v* tag. Run: git fetch --tags origin"
[ -z "$(git status --porcelain)" ] \
  || incomplete "the working tree is dirty. Commit or stash first: the shard has to run the committed tree"
case "$host" in
  mars|nas) want_default=linux/amd64 ;;
  *) want_default="" ;;
esac
if [ -n "$want_default" ]; then
  if [ -n "${RCM_CI_PLATFORM:-}" ] && [ "$RCM_CI_PLATFORM" != "$want_default" ]; then
    incomplete "RCM_CI_PLATFORM=$RCM_CI_PLATFORM would run emulated on $host, which is $want_default native"
  fi
fi

# --- per-shard minimum memory (#1531) --------------------------------------------
shard_min_gib() { case "$1" in selftest-encryption) echo 9 ;; *) echo 0 ;; esac; }
mem_mib() { # a docker --memory value as MiB, or nothing when it cannot be read
  case "$1" in
    *[gG]) n="${1%[gG]}"; unit=1024 ;;
    *[mM]) n="${1%[mM]}"; unit=1 ;;
    *[kK]) n="${1%[kK]}"; echo $(( n / 1024 )); return ;;
    *) n="$1"; unit=0 ;;
  esac
  case "$n" in ''|*[!0-9]*) return ;; esac
  if [ "$unit" = 0 ]; then echo $(( n / 1048576 )); else echo $(( n * unit )); fi
}
min_gib="$(shard_min_gib "$shard")"
if [ "$min_gib" -gt 0 ] && [ "$host" != mac ]; then
  if [ -z "${CI_DOCKER_MEMORY:-}" ]; then
    export CI_DOCKER_MEMORY="${min_gib}g"
  else
    have_mib="$(mem_mib "$CI_DOCKER_MEMORY")"
    if [ -z "$have_mib" ] || [ "$have_mib" -lt $(( min_gib * 1024 )) ]; then
      incomplete "shard $shard needs a fence of at least ${min_gib}g (two copycrypt controls allocate several GiB), and CI_DOCKER_MEMORY=$CI_DOCKER_MEMORY is less or unreadable"
    fi
  fi
fi

# --- run ------------------------------------------------------------------------
# In the background and waited for, so a signal reaches the trap now instead of
# when the pipeline ends, and the trap can take the shard's process with it.
# The tee is opened first and the shard writes to it, because $! after a command
# with a process substitution on it is the substitution's pid, not the shard's.
exec 3> >(tee "$log")
if [ "$host" = mac ]; then
  bash scripts/ci-local.sh --shard "$shard" >&3 2>&1 &
else
  ci_docker="${CI_DOCKER_SH:-$tools/scripts/ci-docker.sh}"
  export CI_DOCKER_REPO="$root"
  RCM_CI_PLATFORM="$want_default" bash "$ci_docker" --host "$target" --deps bash -c "$cmd" >&3 2>&1 &
fi
child=$!
wait "$child"
rc=$?
child=""
exec 3>&-
wait

# --- verdict: the gate's own words, never the exit status alone ------------------
# The LAST "==> ci-local:" line decides, the rule the recorder reads it by.
verdict="$(grep '^==> ci-local: ' "$log" | tail -n 1)"
if [ "$rc" = 0 ] && [ "$verdict" = "==> ci-local: ok (shard $shard)" ]; then
  write_result "$rc" ok "ci-local verdict ok"; exit 0
fi
case "$verdict" in
  "==> ci-local: FAILED"*)
    if [ "$rc" != 0 ] && [ "$rc" != 3 ] && [ "$rc" -lt 128 ]; then
      write_result "$rc" failed "ci-local verdict FAILED"; exit 1
    fi ;;
esac
incomplete "exit $rc without a usable ci-local verdict for shard $shard (refused, dropped, killed or INCOMPLETE)" "$rc"

#!/usr/bin/env bash
# Run one gate shard on one host (#1460, item C of the sharded gate, #1455).
#
#   bash scripts/release/run-gate-shard.sh <shard> --host <mars|nas|mercury|mac> [--out DIR]
#
# The shard's command is `bash scripts/ci-local.sh --shard <shard>`. On mars,
# nas and mercury it runs through scripts/ci-docker.sh --host ... --deps, the
# path release-e2e.sh already uses, so the tree shipping, the fence, the
# cleanup on every exit path and the /tmp handling are ci-docker.sh's and it
# is not edited here. `mac` runs the same command directly in this tree.
#
# What comes back, in DIR (default <git-common-dir>/local-gate/shard-runs):
#   <shard>.log      everything the run printed (also streamed to stdout)
#   <shard>.result   key=value lines: shard, host, platform, command,
#                    started_at (UTC), duration_s, exit, status, reason
# and one of three exit statuses, so the recorder never parses prose:
#   0  ok          the shard exited 0 AND its log carries "==> ci-local: ok"
#   1  failed      the gate printed "==> ci-local: FAILED" and exited non-zero
#   3  INCOMPLETE  anything else: refused before shipping, the gate's own
#                  INCOMPLETE, a dropped ssh session, a kill, exit 0 with no
#                  verdict. Never counted as a red test, never as a pass.
# 2 is a usage error (nothing ran, no result file).
#
# Environment parity (plan section 5), refused BEFORE anything is shipped:
#   - the clone must hold refs/remotes/origin/release and a v* tag. The git dir
#     travels with the tree, and a clone without them failed deep inside the
#     gate on Mercury;
#   - the tree must be clean: ci-docker.sh copies tracked plus untracked files,
#     so a dirty tree is not the committed tree the record names;
#   - mars and nas are native x86 and run linux/amd64, mercury runs its native
#     linux/arm64, and an inherited platform that would run emulated is refused.
# Only ssh and docker run on a host, through ci-docker.sh, which writes into
# one scratch directory under the host's workspace and removes it on exit.
#
#   CI_DOCKER_MEMORY / CI_DOCKER_CPUS   the shard's container fence, passed on
#   RUN_GATE_SHARD_MERCURY              ssh target of mercury (default rom@mercury)
#   CI_DOCKER_SH                        test seam: a stand-in for ci-docker.sh
set -uo pipefail

tools="$(cd "$(dirname "$0")/../.." && pwd)"
usage() { echo "usage: run-gate-shard.sh <shard> --host <mars|nas|mercury|mac> [--out DIR]" >&2; exit 2; }

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
case "$shard" in [a-z]*) ;; *) echo "run-gate-shard: bad shard name [$shard]" >&2; exit 2 ;; esac
case "$shard" in *[!a-z0-9-]*) echo "run-gate-shard: bad shard name [$shard]" >&2; exit 2 ;; esac
case "$host" in
  mars) target=devshell@192.168.0.100; platform=linux/amd64 ;;
  nas) target=rom@192.168.0.10; platform=linux/amd64 ;;
  mercury) target="${RUN_GATE_SHARD_MERCURY:-rom@mercury}"; platform=linux/arm64 ;;
  mac) target=""; platform=host ;;
  *) echo "run-gate-shard: unknown host [$host] (mars, nas, mercury, mac)" >&2; usage ;;
esac

root="$(git rev-parse --show-toplevel)" || { echo "run-gate-shard: not in a git tree" >&2; exit 2; }
cd "$root" || exit 2
[ -n "$out" ] || out="$(cd "$root" && cd "$(git rev-parse --git-common-dir)" && pwd)/local-gate/shard-runs"
mkdir -p "$out"
log="$out/$shard.log"; resultf="$out/$shard.result"
cmd="bash scripts/ci-local.sh --shard $shard"
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"; t0="$(date +%s)"
: > "$log"

write_result() { # <exit> <status> <reason>
  {
    echo "shard=$shard"; echo "host=$host"; echo "platform=$platform"; echo "command=$cmd"
    echo "started_at=$started_at"; echo "duration_s=$(( $(date +%s) - t0 ))"
    echo "exit=$1"; echo "status=$2"; echo "reason=$3"
  } > "$resultf.tmp" && mv "$resultf.tmp" "$resultf"
}
incomplete() { echo "run-gate-shard: INCOMPLETE ($1)" | tee -a "$log" >&2; write_result "${2:--}" incomplete "$1"; exit 3; }
trap 'incomplete "interrupted"' INT TERM

# --- parity checks, before anything is shipped ---------------------------------
git rev-parse -q --verify refs/remotes/origin/release >/dev/null \
  || incomplete "this clone has no origin/release. Run: git fetch origin release"
[ -n "$(git tag -l 'v*' | head -n 1)" ] \
  || incomplete "this clone has no v* tag. Run: git fetch --tags origin"
[ -z "$(git status --porcelain)" ] \
  || incomplete "the working tree is dirty. Commit or stash first: the shard has to run the committed tree"
case "$host" in
  mars|nas) want_default=linux/amd64 ;;
  mercury) want_default=linux/arm64 ;;
  *) want_default="" ;;
esac
if [ -n "$want_default" ]; then
  if [ -n "${RCM_CI_PLATFORM:-}" ] && [ "$RCM_CI_PLATFORM" != "$want_default" ]; then
    incomplete "RCM_CI_PLATFORM=$RCM_CI_PLATFORM would run emulated on $host, which is $want_default native"
  fi
  if [ -n "${DOCKER_DEFAULT_PLATFORM:-}" ] && [ "$DOCKER_DEFAULT_PLATFORM" != "$want_default" ]; then
    incomplete "DOCKER_DEFAULT_PLATFORM=$DOCKER_DEFAULT_PLATFORM would run emulated on $host, which is $want_default native"
  fi
fi

# --- run ------------------------------------------------------------------------
if [ "$host" = mac ]; then
  bash scripts/ci-local.sh --shard "$shard" 2>&1 | tee "$log"
else
  ci_docker="${CI_DOCKER_SH:-$tools/scripts/ci-docker.sh}"
  export CI_DOCKER_REPO="$root"
  RCM_CI_PLATFORM="$want_default" bash "$ci_docker" --host "$target" --deps bash -c "$cmd" 2>&1 | tee "$log"
fi
rc="${PIPESTATUS[0]}"

# --- verdict: the gate's own words, never the exit status alone ------------------
has() { grep -q "$1" "$log"; }
if [ "$rc" = 0 ] && has '^==> ci-local: ok$' && ! has '^==> ci-local: \(FAILED\|INCOMPLETE\)'; then
  write_result "$rc" ok "ci-local verdict ok"; exit 0
fi
if [ "$rc" != 0 ] && [ "$rc" != 3 ] && [ "$rc" -lt 128 ] && has '^==> ci-local: FAILED'; then
  write_result "$rc" failed "ci-local verdict FAILED"; exit 1
fi
write_result "$rc" incomplete "exit $rc without a usable ci-local verdict (refused, dropped, killed or INCOMPLETE)"
exit 3

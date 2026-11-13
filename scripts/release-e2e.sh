#!/usr/bin/env bash
# The full e2e for a release, in fenced docker on another host, and the stamp
# the pre-push hook on `release` asks for (#1375).
#
#   bash scripts/release-e2e.sh
#
# What it does, in order:
#   1. refuses a dirty tree (tracked or untracked), so the tree it stamps is
#      the tree that was tested;
#   2. picks an x86 docker host with /Users/rom/workspace/dh.sh (mars or the
#      NAS; never a Mac, so Playwright never meets this Mac's lock rules or
#      its memory) and works out the fence: 60% of the host's RAM capped at
#      16g, no swap (memory-swap equal to memory), 2/3 of its CPUs;
#   3. runs the e2e command (scripts/release-e2e/lib.sh, RELEASE_E2E_CMD_DEFAULT)
#      through scripts/ci-docker.sh --host, which ships the tree to a scratch
#      directory on that host, runs the fenced container, and removes the
#      scratch directory, image and cache volumes on every exit path
#      (its own EXIT/INT/TERM trap);
#   4. only on a green exit, writes $(git rev-parse --git-common-dir)/release-e2e/<tree>.stamp
#      (tree, command, host, time), atomically. Red or interrupted: no stamp.
#
# The fence limits the container the command runs in. The sibling containers
# the e2e itself starts on that host's daemon (the deployment under test)
# are not inside it; the host choice and the free-space check cover those.
#
# Test-only: RELEASE_E2E_TEST_CMD replaces the command and runs it locally
# (no docker). Its stamps go to release-e2e-TEST-ONLY/, a directory the
# hooks never read; only the self-test sets RELEASE_E2E_ALLOW_TEST_STAMPS=1
# to make them count. Success is the command's exit status and nothing else:
# no output is parsed, so a line that says "exit 0" proves nothing. DH_SH and CI_DOCKER_SH
# point at stand-ins for dh.sh and ci-docker.sh in the self-test.
set -euo pipefail
unset DH_UNFENCED GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR   # the fence is not optional for a release run

root="$(git rev-parse --show-toplevel)"
cd "$root"
# The gate's own scripts come from where this file lives, which the
# pre-merge-commit hook points at a copy extracted from HEAD. The tree under
# test is always the one in $root.
tools="$(cd "$(dirname "$0")/.." && pwd)"
export RE_TOOLS="$tools"
# shellcheck disable=SC1091
. "$tools/scripts/release-e2e/lib.sh"

die() { echo "release-e2e: $*" >&2; exit 2; }

# Index vs working tree, not HEAD vs working tree: during a merge the index
# legitimately differs from HEAD and that merged tree is what gets stamped.
if ! git diff --quiet || [ -n "$(git ls-files --others --exclude-standard)" ]; then
  die "the working tree is dirty. Commit or stash first: the stamp has to describe the tree that was tested."
fi

tree="$(re_tree_id "$(git write-tree)")"
dir="$(re_stamp_dir)"
[ -z "${RELEASE_E2E_TEST_CMD:-}" ] || dir="$(re_test_stamp_dir)"
mkdir -p "$dir"
cmd="${RELEASE_E2E_TEST_CMD:-$RELEASE_E2E_CMD_DEFAULT}"
log="$dir/last-run.log"
tmp=""
trap '[ -z "$tmp" ] || rm -f "$tmp"' EXIT

if [ -n "${RELEASE_E2E_TEST_CMD:-}" ]; then
  host=test-override
  echo "==> release-e2e: *** TEST-ONLY *** running [$cmd] locally. The stamp goes to $dir, which pre-push never reads. This is not release evidence." >&2
  set +e
  sh -c "$cmd" 2>&1 | tee "$log"
  status=${PIPESTATUS[0]}
  set -e
else
  dh="${DH_SH:-/Users/rom/workspace/dh.sh}"
  ci_docker="${CI_DOCKER_SH:-$tools/scripts/ci-docker.sh}"
  export CI_DOCKER_REPO="$root"
  host="${RELEASE_E2E_HOST:-$(bash "$dh" pick x86)}"
  case "$host" in
    mars) target=devshell@192.168.0.100 ;;
    nas) target=rom@192.168.0.10 ;;
    *) die "host [$host] is not an x86 docker host the runner knows (mars, nas). Is docker down on all of them? Try: bash $dh hosts" ;;
  esac
  info="$(bash "$dh" d "$host" info --format '{{.MemTotal}} {{.NCPU}}')" \
    || die "docker on $host did not answer. Host down? Pick another with RELEASE_E2E_HOST=mars|nas."
  mem_mb=$(( ${info% *} * 6 / 10 / 1048576 )); [ "$mem_mb" -le 16384 ] || mem_mb=16384
  ncpu="${info#* }"; cpus=$(( ncpu * 2 / 3 > 0 ? ncpu * 2 / 3 : 1 ))
  export CI_DOCKER_MEMORY="${RELEASE_E2E_MEMORY:-${mem_mb}m}"
  export CI_DOCKER_CPUS="${RELEASE_E2E_CPUS:-$cpus}"
  echo "==> release-e2e: $host ($target), memory $CI_DOCKER_MEMORY, no swap, cpus $CI_DOCKER_CPUS, tree $tree" >&2
  set +e
  bash "$ci_docker" --host "$target" --deps bash -c "$cmd" 2>&1 | tee "$log"
  status=${PIPESTATUS[0]}
  set -e
fi

if [ "$status" -ne 0 ]; then
  echo "release-e2e: RED (exit $status). No stamp written. Log: $log" >&2
  if grep -qiE 'oom|out of memory|killed' "$log"; then
    echo "release-e2e: the log mentions OOM or a kill. The fence did its job; report it, do not retry in a loop." >&2
  fi
  exit "$status"
fi

tmp="$(mktemp "$dir/.stamp.XXXXXX")"
{
  echo "schema=1"
  echo "status=green"
  echo "tree=$tree"
  echo "commit=$(git rev-parse HEAD)"
  echo "command=$cmd"
  echo "host=$host"
  echo "time=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "gate_sha256=$(re_gate_hash)"
} >"$tmp"
mv "$tmp" "$dir/$tree.stamp"; tmp=""
echo "release-e2e: green. Stamped tree $tree ($dir/$tree.stamp)." >&2

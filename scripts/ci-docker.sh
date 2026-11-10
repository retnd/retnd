#!/usr/bin/env bash
#
# Run anything in this repository in a container instead of on this machine.
#
#     bash scripts/ci-docker.sh                    # the whole gate
#     bash scripts/ci-docker.sh --fast             # flags pass to ci-local.sh
#     bash scripts/ci-docker.sh --shell            # a prompt in that environment
#     bash scripts/ci-docker.sh --exec go test ./core/...
#     bash scripts/ci-docker.sh --exec python3 -m unittest discover scripts/install
#     bash scripts/ci-docker.sh --host ssh://user@host [--fast]    # on another machine's daemon
#
# --exec is the general form and the gate is one use of it. Nothing here
# should need the host's go, node, npm, python or linker: a toolchain that
# comes from whatever the laptop updated overnight cannot be the thing a
# red result is measured against.
#
# WHY THIS EXISTS. scripts/ci-local.sh is the gate and it is not changed
# by any of this: the same script runs, with the same steps, and its
# verdict is still the verdict. What changes is what it runs ON. The
# toolchain moves from "whatever this laptop has today" to the pinned set
# in scripts/ci/Dockerfile, which is the difference between a red run
# meaning something and a red run meaning macOS shipped a new SDK
# overnight. See that file for the four separate environment failures in
# one evening that produced it.
#
# WHY THE HOST'S DAEMON, AND NOT ONE INSIDE. Docker-out-of-docker: the
# socket is mounted and every container the gate starts is a SIBLING on
# this machine's daemon, not a child of a daemon in here. Two reasons,
# and the first is decisive. The two-machine proof needs a privileged
# container; running it under a nested daemon means asking for privilege
# twice and getting a different topology than the one the proof is about.
# The second is that sibling containers are exactly what the gate creates
# today, so nothing about how the proof runs changes.
#
# WHY THE REPOSITORY IS MOUNTED AT ITS OWN PATH. That is what makes
# docker-out-of-docker work at all. The gate takes REPO_ROOT from `pwd`
# and hands paths under it to `docker run -v`, and those are interpreted
# by the HOST daemon. Mounted anywhere else, every one of them would
# name a directory that does not exist out there, and the failure would
# be an empty mount rather than an error. Same path inside and out, and
# they resolve to the same bytes.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
repo="$(cd "$here/.." && pwd)"

say() { printf '==> ci-docker: %s\n' "$*"; }
die() { printf 'ci-docker: %s\n' "$*" >&2; exit 2; }

# REMOTE MODE (#1267). `--host ssh://user@host` (or CI_DOCKER_HOST, the flag
# wins) runs the same gate on another machine's Docker daemon, so several
# gates do not all pile onto one laptop.
#
# WHY IT IS NOT JUST DOCKER_HOST. The gate bind-mounts paths under the repo
# and the daemon resolves every one of them on ITS disk. Point DOCKER_HOST at
# another machine and each mount names a directory that is not there, which
# docker turns into an EMPTY directory instead of an error, so the gate
# judges nothing and can say ok. Remote mode therefore ships the tree first,
# and a bare remote DOCKER_HOST with no --host is refused below.
#
# WHAT IT DOES: refuses when the remote disk is short, copies the files git
# knows about (tracked, plus untracked that are not ignored, minus the usual
# credential files) and the git dir (without its config or hooks) into ONE
# scratch directory, runs the unchanged gate there with the remote uid,
# streams the output, returns the gate's exit status, then removes the
# scratch directory, the toolchain image if this run built it, and the cache
# volumes this run created. It sends no environment variables to the
# container and writes no token anywhere on the remote.
#
#   CI_DOCKER_HOST          user@host or ssh://user@host[:port]; --host overrides
#   CI_DOCKER_REMOTE_DIR    parent of the scratch directory (default
#                           /work/nas-work for devshell@ hosts, else
#                           $HOME/workspace/nas-work on the remote)
#   CI_DOCKER_MIN_FREE_MB   refuse below this much free space (default 20480)
#   CI_DOCKER_KEEP_CACHE    1 keeps the image and cache volumes for a faster next run
#   CI_DOCKER_SYNC          rsync (default) or tar, for a host without rsync
#
# Only ssh, docker and writes into the scratch directory happen on the host.
remote=""
port=""
if [ "${1:-}" = "--host" ]; then
  [ "$#" -ge 2 ] || die "--host needs user@host or ssh://user@host"
  remote="$2"; shift 2
  [ -n "$remote" ] || die "--host is empty"
else
  remote="${CI_DOCKER_HOST:-}"
fi

target=""
if [ -n "$remote" ]; then
  target="${remote#ssh://}"
  case "$target" in
    *[!A-Za-z0-9._@:-]*|@*|*@|*@*@*) die "not a usable host [$remote]: want user@host or ssh://user@host[:port]" ;;
  esac
  case "$target" in
    [A-Za-z0-9._-]*@[A-Za-z0-9._-]*) : ;;
    *) die "not a usable host [$remote]: want user@host or ssh://user@host[:port]" ;;
  esac
  case "${remote%%://*}" in
    "$remote"|ssh) : ;;
    *) die "not a usable host [$remote]: only ssh:// is supported" ;;
  esac
  # ssh takes the port as -p, not as host:port.
  port=""
  case "$target" in
    *:*) port="${target##*:}"; target="${target%:*}" ;;
  esac
  case "$port" in *[!0-9]*) die "not a usable port in [$remote]" ;; esac
  export DOCKER_HOST="ssh://$target${port:+:$port}"
else
  case "${DOCKER_HOST:-}" in
    ""|unix://*) : ;;
    *) die "DOCKER_HOST=$DOCKER_HOST points at another machine, and the gate's bind mounts would be empty there. Use --host (or CI_DOCKER_HOST) so the tree is copied first." ;;
  esac
fi

# A git worktree keeps its objects somewhere else, and .git is a file
# pointing at them. Without this mount every git call in the gate fails
# in a checkout that looks perfectly normal from the outside.
git_common="$(cd "$repo" && git rev-parse --git-common-dir 2>/dev/null || true)"
case "$git_common" in
  /*) : ;;
  "") git_common="" ;;
  *)  git_common="$repo/$git_common" ;;
esac

# arm64 by default, and stated rather than derived. DOCKER_DEFAULT_PLATFORM
# is linux/amd64 in at least one shell around here, and inheriting that
# would put every run under qemu: correct, and slow enough that nobody
# would do it twice. Deriving it from `uname -m` instead would be quietly
# wrong the first time this runs somewhere else, so the default is the
# declared preference and RCM_CI_PLATFORM is how you say otherwise.
unset DOCKER_DEFAULT_PLATFORM
rsh=(ssh -o BatchMode=yes -o LogLevel=ERROR)
[ -z "$port" ] || rsh+=(-p "$port")
if [ -n "$target" ]; then
  # The remote hosts are x86_64, and the emulation warning below is about
  # THIS machine's architecture, which says nothing about theirs.
  platform="${RCM_CI_PLATFORM:-linux/amd64}"
  host_arch="remote"
else
  platform="${RCM_CI_PLATFORM:-linux/arm64}"
  host_arch="$(uname -m)"
fi
case "$host_arch:$platform" in
  remote:*) : ;;
  arm64:linux/arm64|aarch64:linux/arm64|x86_64:linux/amd64|amd64:linux/amd64) : ;;
  *) printf '==> ci-docker: %s on a %s host, so this runs emulated. RCM_CI_PLATFORM overrides it.\n' \
       "$platform" "$host_arch" >&2 ;;
esac

# Tagged by the Dockerfile's own hash AND the uid it is built for, so a
# changed Dockerfile rebuilds and two users on one machine never collide
# over an image whose passwd entry names only one of them.
# In remote mode that is the remote account: the copy is written by it, and
# a container running as another uid could not write into it.
if [ -n "$target" ]; then
  ci_uid="$("${rsh[@]}" "$target" 'id -u')"; ci_gid="$("${rsh[@]}" "$target" 'id -g')"
  case "$ci_uid:$ci_gid" in
    *[!0-9:]*|:*|*:) die "could not read the uid and gid of $target over ssh" ;;
  esac
else
  ci_uid="$(id -u)"; ci_gid="$(id -g)"
fi
image="retnd-ci:$(shasum -a 256 "$here/ci/Dockerfile" | cut -c1-12)-u${ci_uid}"

# Named volumes, and the reason is the same failure story as the image.
# The Go build cache on the host was wiped by three concurrent `go clean
# -cache` runs in the middle of a gate, and entries vanished mid-compile.
# In here the caches belong to the gate, so nothing else on the machine
# can take them away, and a second run is fast rather than cold.
# Keyed by uid. A named volume takes its ownership from whoever first
# wrote it, so one created by a root run is unwritable to a run as the
# host user, and Go reports it as "permission denied" on a cache file
# rather than as anything to do with ownership.
vol_prefix="rcm-ci-u${ci_uid}"
# Where the container sees the repo and its git dir. Locally that is where
# they already are; remotely it is the scratch copy made here.
run_repo="$repo"
run_git="$git_common"
built_here=0
have_image=0
scratch_base=""
scratch_name=""
vols_before=""

cleanup() {
  local st=$?
  trap - EXIT INT TERM
  set +e
  if [ -n "$target" ] && [ -n "$scratch_name" ]; then
    # From inside a container: the gate's containers write into the copy
    # (mount points, build output) and the host account may not own that.
    if [ "$have_image" = 1 ]; then
      docker run --rm --user 0:0 -v "$scratch_base:/ws" "$image" rm -rf -- "/ws/$scratch_name" >/dev/null 2>&1
    else
      # Nothing has run in a container yet, so the ssh account owns it all.
      "${rsh[@]}" "$target" "rm -rf -- '$scratch_base/$scratch_name'" >/dev/null 2>&1
    fi
    if [ "${CI_DOCKER_KEEP_CACHE:-0}" != 1 ]; then
      [ "$built_here" = 1 ] && docker rmi "$image" >/dev/null 2>&1
      for v in gocache gomodcache npm xdg nm-ui-shared nm-apps-common-tests; do
        case "$vols_before" in *"${vol_prefix}-$v"*) continue ;; esac
        docker volume rm "${vol_prefix}-$v" >/dev/null 2>&1
      done
    fi
    say "removed $target:$scratch_base/$scratch_name"
  fi
  exit "$st"
}

canon() { (cd "$1" && pwd -P); }

if [ -n "$target" ]; then
  [ -n "$git_common" ] && [ -d "$git_common" ] || die "remote mode needs a git checkout"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM

  scratch_base="${CI_DOCKER_REMOTE_DIR:-}"
  if [ -z "$scratch_base" ]; then
    case "$target" in
      devshell@*) scratch_base=/work/nas-work ;;
      *) scratch_base="$("${rsh[@]}" "$target" 'printf %s "$HOME"')/workspace/nas-work" ;;
    esac
  fi
  case "$scratch_base" in
    /*[!A-Za-z0-9._/-]*|[!/]*|/) die "unusable scratch parent [$scratch_base]" ;;
  esac

  min_free_mb="${CI_DOCKER_MIN_FREE_MB:-20480}"
  avail_kb="$("${rsh[@]}" "$target" "mkdir -p '$scratch_base' && df -Pk '$scratch_base'" | awk 'NR==2{print $4}')"
  case "$avail_kb" in ""|*[!0-9]*) die "could not read the free space under $scratch_base on $target" ;; esac
  if [ $((avail_kb / 1024)) -lt "$min_free_mb" ]; then
    die "only $((avail_kb / 1024)) MiB free under $scratch_base on $target and the gate needs $min_free_mb (CI_DOCKER_MIN_FREE_MB). Nothing was sent."
  fi

  scratch_name="ci-docker-$$-$(date +%s)"
  remote_root="$scratch_base/$scratch_name"
  run_repo="$remote_root/repo"
  run_git="$remote_root/git"
  "${rsh[@]}" "$target" "mkdir -p '$run_repo' '$run_git'"

  # The files git knows about, so what .gitignore keeps out (node_modules,
  # build output, most credential files) stays out, minus the credential
  # files that are tracked or simply not ignored. node_modules would be
  # macOS binaries anyway: the container installs its own.
  file_list() {
    ( cd "$repo" && git ls-files -z --cached --others --exclude-standard ) |
    while IFS= read -r -d '' f; do
      [ -e "$f" ] || [ -L "$f" ] || continue
      case "$f" in
        .env|.env.*|*/.env|*/.env.*|.npmrc|*/.npmrc|.netrc|*/.netrc|.git-credentials|*/.git-credentials) continue ;;
      esac
      printf '%s\0' "$f"
    done
  }
  # The git dir without its config (remote URLs can carry credentials), its
  # hooks (they are the local gate) and the local gate lock.
  git_excludes=(--exclude=config --exclude=config.worktree --exclude=hooks --exclude=ci-local.lock)

  say "copying the tree to $target:$remote_root"
  case "${CI_DOCKER_SYNC:-rsync}" in
    rsync)
      rsync_ssh="ssh -o BatchMode=yes -o LogLevel=ERROR${port:+ -p $port}"
      file_list | rsync -a --no-owner --no-group --from0 --files-from=- -e "$rsync_ssh" "$repo/" "$target:$run_repo/"
      rsync -a --no-owner --no-group "${git_excludes[@]}" -e "$rsync_ssh" "$git_common/" "$target:$run_git/"
      ;;
    tar)
      file_list | tar -cf - -C "$repo" --null -T - | "${rsh[@]}" "$target" "tar -xf - -C '$run_repo'"
      tar -cf - -C "$git_common" "${git_excludes[@]}" . | "${rsh[@]}" "$target" "tar -xf - -C '$run_git'"
      ;;
    *) die "CI_DOCKER_SYNC must be rsync or tar" ;;
  esac

  # A fresh .git pointer and config instead of the local ones, whose paths
  # and remotes mean nothing there.
  abs_gitdir="$(canon "$(cd "$repo" && git rev-parse --absolute-git-dir)")"
  common_canon="$(canon "$git_common")"
  gitdir_rel="${abs_gitdir#"$common_canon"}"
  printf 'gitdir: %s\n' "$run_git$gitdir_rel" | "${rsh[@]}" "$target" "cat > '$run_repo/.git'"
  printf '[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n' |
    "${rsh[@]}" "$target" "cat > '$run_git/config'"

  if [ "${CI_DOCKER_KEEP_CACHE:-0}" != 1 ]; then
    vols_before="$(docker volume ls -q 2>/dev/null || true)"
  fi
fi

# Replaces `exec` for the final docker run: in remote mode this shell has to
# stay alive to clean up, and must still hand back the gate's own status.
launch() {
  if [ -n "$target" ]; then
    set +e
    "$@"
    exit $?
  fi
  exec "$@"
}

declare -a mounts=(
  -v "/var/run/docker.sock:/var/run/docker.sock"
  -v "$run_repo:$run_repo"
  -v "${vol_prefix}-gocache:/ci/.cache/go-build"
  -v "${vol_prefix}-gomodcache:/ci/go/pkg/mod"
  -v "${vol_prefix}-npm:/ci/.npm"
  -v "${vol_prefix}-xdg:/ci/.cache/retnd-tests-gate"
)

# The host's own uid, not root, and group 0 for the socket.
#
# Root ignores file modes, and one installer test sets a directory
# unwritable to check the installer refuses rather than raising: as root
# it cannot set the case up and skips itself, which is coverage lost with
# nobody deciding to lose it. Group 0 because the daemon socket arrives
# inside as root:root 0660, so the group bit is the way in without being
# root. Docker Desktop maps writes on the bind mount back to the host
# user whatever uid makes them, so the repository stays owned by you.
as_user=(--user "$ci_uid:$ci_gid" --group-add 0)
[ -n "$git_common" ] && [ -d "$git_common" ] && mounts+=(-v "$run_git:$run_git")

# node_modules never comes from the host, and this is not an
# optimisation. The host is macOS: ui/shared/node_modules holds darwin
# binaries for esbuild and rollup, and a linux container cannot execute
# them. Masking each workspace with its own volume gives the container a
# linux install of the same lockfile, and leaves the host's alone for
# whatever the developer runs outside this.
declare -a js_workspaces=("ui/shared" "apps/common/tests")
declare -a nm_paths=()
for ws in "${js_workspaces[@]}"; do
  [ -d "$repo/$ws" ] || continue
  mounts+=(-v "${vol_prefix}-nm-$(printf '%s' "$ws" | tr '/' '-'):$run_repo/$ws/node_modules")
  nm_paths+=("$run_repo/$ws/node_modules")
done

if ! docker image inspect "$image" >/dev/null 2>&1; then
  say "building the toolchain image ($image, $platform)"
  docker build --platform "$platform" \
    --build-arg "CI_UID=$ci_uid" --build-arg "CI_GID=$ci_gid" \
    --build-arg "GOLANGCI_LINT_VERSION=v$(sed -n 's/^GOLANGCI_LINT_VERSION=//p' "$repo/scripts/ci/pins.env" | head -n 1)" \
    -t "$image" "$here/ci"
  built_here=1
else
  say "toolchain image $image is already built"
fi
have_image=1

# A named volume that shadows a path the IMAGE does not have is created
# empty and owned by root, and this runs as the host user, so npm's first
# mkdir under ui/shared/node_modules fails with EACCES. The go and npm
# caches are the same story. One root container fixes the ownership of
# each mount point before anything else touches them.
#
# Guarded by a stat rather than chowned unconditionally: on a warm run
# the Go cache is thousands of files already owned correctly, and walking
# it every time would add more than the runner saves.
prepare='
set -eu
for d in /ci/.cache/go-build /ci/go/pkg/mod /ci/.npm /ci/.cache/retnd-tests-gate '"$(printf '%s ' "${nm_paths[@]}")"'; do
  [ -d "$d" ] || continue
  if [ "$(stat -c %u "$d")" != "'"$ci_uid"'" ]; then
    chown "'"$ci_uid:$ci_gid"'" "$d"
  fi
done
'

# npm ci inside the container, and only when the lockfile it was last run
# for has changed. The stamp lives in the volume rather than beside the
# lockfile, because the lockfile is on the host and shared with every
# other checkout.
bootstrap='
set -eu
for ws in '"${js_workspaces[*]}"'; do
  [ -d "$ws" ] || continue
  lock="$ws/package-lock.json"
  [ -f "$lock" ] || continue
  want="$(sha256sum "$lock" | cut -d" " -f1)"
  stamp="$ws/node_modules/.ci-docker-lockfile"
  if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$want" ]; then
    echo "==> ci-docker: $ws dependencies are current"
    continue
  fi
  echo "==> ci-docker: installing $ws dependencies (lockfile changed or volume empty)"
  ( cd "$ws" && npm ci )
  printf "%s" "$want" > "$stamp"
done
'

case "${1:-}" in
  --shell)
    shift
    say "a shell in $image ($platform)"
    launch docker run --rm -it --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" bash
    ;;
  --exec|--deps|--shell)
    docker run --rm --platform "$platform" "${mounts[@]}" --user 0:0 -w "$run_repo" "$image" \
      bash -c "$prepare"
    ;;&
  --exec)
    # Deliberately NOT preceded by the dependency bootstrap. --exec is for
    # one command, `go test` and `python3 -m unittest` are most of them,
    # and neither needs node_modules; paying an npm ci to find out a Go
    # test passes is how a runner stops being used. Ask for it with
    # --deps when the command does need them.
    shift
    [ "$#" -gt 0 ] || { echo "ci-docker: --exec needs a command" >&2; exit 2; }
    launch docker run --rm --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" "$@"
    ;;
  --deps)
    shift
    say "installing dependencies if the lockfiles moved"
    docker run --rm --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" bash -c "$bootstrap"
    [ "$#" -gt 0 ] || exit 0
    launch docker run --rm --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" "$@"
    ;;
esac

docker run --rm --platform "$platform" "${mounts[@]}" --user 0:0 -w "$run_repo" "$image" bash -c "$prepare"

say "installing dependencies if the lockfiles moved"
docker run --rm --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" bash -c "$bootstrap"

say "running scripts/ci-local.sh in $image ($platform)"
launch docker run --rm --platform "$platform" "${mounts[@]}" "${as_user[@]}" -w "$run_repo" "$image" \
  bash scripts/ci-local.sh "$@"

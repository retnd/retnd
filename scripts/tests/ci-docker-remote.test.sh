#!/usr/bin/env bash
# Self-test for the remote mode of scripts/ci-docker.sh (#1267).
#
# The defect: the gate bind-mounts paths under the repo, and a daemon on
# another machine reads those paths from ITS disk. Pointing DOCKER_HOST at
# one used to give empty mounts and a gate that judged nothing, with no
# error. Remote mode has to ship the tree to the machine the daemon runs
# on, run the same gate there, hand back its exit status, and leave the
# machine as it found it.
#
# Driven the way the other gate shell tests are: stand-ins for docker,
# rsync, tar and ssh on PATH that log what they were asked to do and answer
# from environment variables. Nothing here talks to a daemon or a network.
# What this proves is the script's behaviour (what it asks for, in what
# order, and what it returns); it does not prove a real remote run works.
#
# Every refusal is paired with a control, because "nothing ran" is also
# true when the script fell over before it started.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
script="$repo_root/scripts/ci-docker.sh"

work="$(mktemp -d "${TMPDIR:-/tmp}/ci-docker-remote.XXXXXX")"
case "$work" in /*/ci-docker-remote.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || echo "         | $2"; }

# --- stand-ins --------------------------------------------------------------
bin="$work/bin"; log="$work/log"
mkdir -p "$bin" "$log"

cat > "$bin/docker" <<'SH'
#!/usr/bin/env bash
echo "DOCKER_HOST=${DOCKER_HOST:-} docker $*" >> "$FAKE_LOG/docker"
case "$1 ${2:-}" in
  "image inspect") [ "${FAKE_IMAGE:-0}" = 1 ]; exit $? ;;
  "volume ls") printf '%s' "${FAKE_VOLUMES:-}"; exit 0 ;;
  "info --format")
    case "$3" in
      *NCPU*) printf '%s %s %s\n' "${FAKE_NCPU-8}" "${FAKE_MEMTOTAL-34359738368}" "${FAKE_SWAPLIMIT-true}"; exit 0 ;;
    esac
    printf '%s\n' "${FAKE_DAEMON_OS-Docker Desktop}"; exit 0 ;;
esac
case "$*" in *"stat -c %g /var/run/docker.sock"*) printf '%s\n' "${FAKE_SOCK_GID-0}"; exit 0 ;; esac
case "$*" in *"test -f "*"/scripts/ci-local.sh"*) [ "${FAKE_MOUNT_EMPTY:-0}" != 1 ]; exit $? ;; esac
case "$*" in *"bash scripts/ci-local.sh"*) exit "${FAKE_GATE_RC:-0}" ;; esac
exit 0
SH
cat > "$bin/rsync" <<'SH'
#!/usr/bin/env bash
echo "rsync $*" >> "$FAKE_LOG/rsync"
case "$*" in *--files-from=-*) tr '\0' '\n' >> "$FAKE_LOG/rsync-list" ;; esac
exit 0
SH
cat > "$bin/tar" <<'SH'
#!/usr/bin/env bash
echo "tar $*" >> "$FAKE_LOG/tar"
case "$*" in *" -T -"*) tr '\0' '\n' >> "$FAKE_LOG/tar-list" ;; esac
exit 0
SH
cat > "$bin/ssh" <<'SH'
#!/usr/bin/env bash
cmd="${*: -1}"
echo "ssh $*" >> "$FAKE_LOG/ssh"
case "$cmd" in
  *"command -v docker"*) echo "${FAKE_REMOTE_DOCKER:-docker}" ;;
  *"id -u"*) echo "${FAKE_UID:-1000}" ;;
  *"id -g"*) echo "${FAKE_GID:-1000}" ;;
  *'$HOME'*) printf '%s' "/home/fake" ;;
  *"df -Pk"*) printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 999999999 1 %s 1%% /work\n' "${FAKE_AVAIL_KB:-99999999}" ;;
  *"cat >"*) { echo "--- stdin for: $cmd"; cat; } >> "$FAKE_LOG/ssh-stdin" ;;
esac
exit 0
SH
chmod +x "$bin"/*

# Runs the real script with the stand-ins first on PATH. $1.. are the
# script's arguments; the environment is whatever the caller exports.
run_script() {
  : > "$log/docker"; : > "$log/rsync"; : > "$log/tar"; : > "$log/ssh"; : > "$log/ssh-stdin"; : > "$log/rsync-list"; : > "$log/tar-list"
  env -u DOCKER_HOST -u CI_DOCKER_HOST FAKE_LOG="$log" PATH="$bin:$PATH" "$@" > "$work/out" 2>&1
  echo $? > "$work/rc"
}
rc() { cat "$work/rc"; }
gate_line() { grep 'bash scripts/ci-local.sh' "$log/docker" || true; }

host="ssh://runner@builder.example"

# --- R0 control: local mode is unchanged ------------------------------------
run_script bash "$script" --fast
if [ "$(rc)" = 0 ] && [ -z "$(cat "$log/ssh" "$log/rsync" "$log/tar")" ] \
   && gate_line | grep -q -- "-v $repo_root:$repo_root" && ! grep -q 'DOCKER_HOST=ssh' "$log/docker"; then
  pass "R0 without a host the gate runs locally, mounts the repo at its own path, and touches no ssh or rsync"
else
  fail "R0 local mode is unchanged" "rc=$(rc) $(head -c 400 "$work/out")"
fi

# --- R1 remote: daemon, mounts, platform, arguments --------------------------
run_script bash "$script" --host "$host" --fast
gl="$(gate_line)"
if [ "$(rc)" = 0 ] && [ -n "$gl" ]; then
  pass "R1a a remote run reaches the gate"
else
  fail "R1a a remote run reaches the gate" "rc=$(rc) $(head -c 400 "$work/out")"
fi
# One line per call starts with DOCKER_HOST=; the others are the rest of a
# multi-line `bash -c` script (prepare, bootstrap) and name no daemon.
if grep -q '^DOCKER_HOST=' "$log/docker" && ! grep '^DOCKER_HOST=' "$log/docker" | grep -v '^DOCKER_HOST=ssh://runner@builder.example ' | grep -q .; then
  pass "R1b every docker call is made against the remote daemon"
else
  fail "R1b every docker call is made against the remote daemon" "$(grep '^DOCKER_HOST=' "$log/docker" | grep -v '^DOCKER_HOST=ssh://runner@builder.example ' | head -3)"
fi
if ! printf '%s' "$gl" | grep -q -- "$repo_root"; then
  pass "R1c the gate mounts the copy on the remote disk, never the local repo path"
else
  fail "R1c the gate mounts the copy on the remote disk" "$gl"
fi
if printf '%s' "$gl" | grep -q -- '--user 1000:1000' && printf '%s' "$gl" | grep -q -- '--platform linux/amd64' \
   && printf '%s' "$gl" | grep -q -- 'scripts/ci-local.sh --fast'; then
  pass "R1d it runs as the remote user's uid and gid, defaults to amd64, and passes the gate's own flags through"
else
  fail "R1d remote uid, platform and flag pass-through" "$gl"
fi
if grep -q '^ssh .*runner@builder.example' "$log/ssh" && ! grep -q 'ssh://' "$log/ssh"; then
  pass "R1e ssh is given user@host, not the URL"
else
  fail "R1e ssh is given user@host" "$(head -3 "$log/ssh")"
fi

# --- R2 the gate's exit status comes back -----------------------------------
for code in 0 1 3 7; do
  run_script env FAKE_GATE_RC=$code bash "$script" --host "$host"
  if [ "$(rc)" = "$code" ]; then
    pass "R2 gate exit $code is the script's exit $code"
  else
    fail "R2 gate exit $code is the script's exit $code" "got $(rc)"
  fi
done

# --- R3 the host is left as it was found ------------------------------------
# A scratch copy is removed from inside a container (the tree is written by
# containers), on success and on failure alike, and an image this run built
# goes with it; an image that was already there stays.
for code in 0 5; do
  run_script env FAKE_GATE_RC=$code FAKE_IMAGE=0 bash "$script" --host "$host"
  if grep -qE 'docker run .*rm -rf -- /ws/ci-docker-' "$log/docker" && grep -q 'docker rmi retnd-ci:' "$log/docker"; then
    pass "R3 (gate exit $code) the scratch copy and the image this run built are removed"
  else
    fail "R3 (gate exit $code) cleanup of scratch copy and built image" "$(cat "$log/docker")"
  fi
done
run_script env FAKE_IMAGE=1 bash "$script" --host "$host"
if ! grep -q 'docker rmi' "$log/docker" && grep -qE 'rm -rf -- /ws/ci-docker-' "$log/docker"; then
  pass "R3 an image that was already on the host is kept, the scratch copy still goes"
else
  fail "R3 a pre-existing image is kept" "$(cat "$log/docker")"
fi
run_script env FAKE_VOLUMES=$'rcm-ci-u1000-gocache\n' bash "$script" --host "$host"
if grep -q 'docker volume rm' "$log/docker" && ! grep 'docker volume rm' "$log/docker" | grep -q 'u1000-gocache'; then
  pass "R3 cache volumes this run created are dropped, one that existed before is not touched"
else
  fail "R3 volume cleanup" "$(grep volume "$log/docker")"
fi
run_script env CI_DOCKER_KEEP_CACHE=1 bash "$script" --host "$host"
if ! grep -q 'docker volume rm' "$log/docker" && ! grep -q 'docker rmi' "$log/docker"; then
  pass "R3 CI_DOCKER_KEEP_CACHE=1 keeps the image and the volumes"
else
  fail "R3 CI_DOCKER_KEEP_CACHE=1" "$(cat "$log/docker")"
fi

# --- R4 a short disk is refused before anything is sent ---------------------
run_script env FAKE_AVAIL_KB=1048576 bash "$script" --host "$host"
if [ "$(rc)" != 0 ] && [ -z "$(cat "$log/rsync")" ] && [ -z "$(gate_line)" ] && grep -qi 'free' "$work/out"; then
  pass "R4a with 1 GiB free the run refuses, says why, sends nothing and runs no gate"
else
  fail "R4a short disk refusal" "rc=$(rc) $(head -c 300 "$work/out")"
fi
run_script env FAKE_AVAIL_KB=1048576 CI_DOCKER_MIN_FREE_MB=512 bash "$script" --host "$host"
if [ "$(rc)" = 0 ] && [ -n "$(gate_line)" ]; then
  pass "R4b control: the same disk passes when the floor is lowered, so R4a failed on space"
else
  fail "R4b control for the disk floor" "rc=$(rc) $(head -c 300 "$work/out")"
fi

# --- R5 nothing secret travels, nothing secret stays ------------------------
# A fixture checkout, so the file list is known: a tracked source file whose
# name contains "token", a tracked .npmrc, an untracked .env, and an ignored
# node_modules. The script copy sits in the fixture because it finds the
# Dockerfile and pins beside itself.
fx="$work/fx"
mkdir -p "$fx/scripts/ci" "$fx/src" "$fx/node_modules/dep"
cp "$script" "$fx/scripts/ci-docker.sh"
cp "$repo_root/scripts/ci/Dockerfile" "$repo_root/scripts/ci/pins.env" "$fx/scripts/ci/"
printf 'package x\n' > "$fx/src/token.go"
printf '//registry:_authToken=abc\n' > "$fx/.npmrc"
printf 'SECRET=1\n' > "$fx/.env"
printf 'x\n' > "$fx/node_modules/dep/index.js"
printf 'node_modules/\n' > "$fx/.gitignore"
git -C "$fx" init -q
git -C "$fx" add scripts src .npmrc .gitignore
git -C "$fx" -c user.name=t -c user.email=t@t commit -q -m fixture --no-verify
fx_script="$fx/scripts/ci-docker.sh"

run_script env GITHUB_TOKEN=tok-should-not-travel NPM_TOKEN=npm-should-not-travel bash "$fx_script" --host "$host"
list="$(cat "$log/rsync-list")"
if printf '%s\n' "$list" | grep -qx 'src/token.go' && printf '%s\n' "$list" | grep -qx 'scripts/ci-docker.sh'; then
  pass "R5a the file list comes from git, so a source file called token.go is sent"
else
  fail "R5a the file list comes from git" "rc=$(rc) list=[$list] $(head -c 300 "$work/out")"
fi
if ! printf '%s\n' "$list" | grep -qE '^(\.env|\.npmrc|node_modules/.*)$'; then
  pass "R5b .env (untracked), .npmrc (tracked) and the ignored node_modules are left out of the list"
else
  fail "R5b secrets and node_modules are left out" "$list"
fi
if ! cat "$log"/ssh "$log"/ssh-stdin "$log"/rsync "$log"/docker | grep -q should-not-travel; then
  pass "R5f a token in the caller's environment appears in no ssh, rsync or docker argument"
else
  fail "R5f token leaked into an argument" "$(grep -l should-not-travel "$log"/*)"
fi
if ! grep -qE -- ' (-e|--env|--env-file)[ =]' "$log/docker"; then
  pass "R5c the gate container is given no environment variables"
else
  fail "R5c the gate container is given no environment" "$(grep -- "--env" "$log/docker" | head -2)"
fi
if grep -q 'gitdir: ' "$log/ssh-stdin" && ! grep -q 'url = ' "$log/ssh-stdin"; then
  pass "R5d the remote git config is a minimal one written fresh, so remote URLs and credentials in .git/config do not travel"
else
  fail "R5d remote git config is written fresh" "$(cat "$log/ssh-stdin")"
fi
if grep -F -- '--exclude=config' "$log/rsync" | grep -qF -- '--exclude=hooks'; then
  pass "R5e the local .git/config and hooks are excluded from the sync"
else
  fail "R5e the local .git/config and hooks are excluded from the sync" "$(cat "$log/rsync")"
fi

# --- R6 the host string is validated ----------------------------------------
for bad in 'x; touch /tmp/pwned' 'ssh://nouser' 'tcp://u@h' '$(id)@h' 'u@h h2' ''; do
  run_script bash "$script" --host "$bad" --fast
  if [ "$(rc)" != 0 ] && [ -z "$(cat "$log/ssh" "$log/rsync" "$log/docker")" ]; then
    pass "R6 host [$bad] is refused before anything runs"
  else
    fail "R6 host [$bad] is refused" "rc=$(rc) $(cat "$log/ssh" | head -2)"
  fi
done
for good in 'u@h' 'ssh://u@h' 'ssh://u@h.example:2222' 'devshell@192.168.0.100'; do
  run_script bash "$script" --host "$good"
  if [ "$(rc)" = 0 ]; then pass "R6 control: host [$good] is accepted"; else fail "R6 control: host [$good] is accepted" "rc=$(rc) $(head -c 200 "$work/out")"; fi
done

# --- R6b the gate container is resource-limited (#1373) ----------------------
# A memory limit, no swap (memory-swap equal to memory) and a CPU quota, so one
# gate run cannot take the machine down with it. The env vars move the numbers.
run_script bash "$script" --fast
mem="$(gate_line | sed -nE 's/.*--memory[ =]([0-9]+[gGmM]).*/\1/p' | head -1)"
if [ -n "$mem" ] && gate_line | grep -qE -- "--memory-swap[ =]$mem( |\$)" \
   && gate_line | grep -qE -- '--cpus[ =][0-9.]+'; then
  pass "R6b the gate container gets a memory limit, memory-swap equal to it, and a cpu quota"
else
  fail "R6b the gate container gets a memory limit, no swap and a cpu quota" "$(gate_line | head -c 500)"
fi
run_script env CI_DOCKER_MEMORY=3g CI_DOCKER_CPUS=2 bash "$script" --fast
if gate_line | grep -qE -- '--memory[ =]3g' && gate_line | grep -qE -- '--memory-swap[ =]3g' && gate_line | grep -qE -- '--cpus[ =]2( |$)'; then
  pass "R6b CI_DOCKER_MEMORY and CI_DOCKER_CPUS set the limits"
else
  fail "R6b CI_DOCKER_MEMORY and CI_DOCKER_CPUS set the limits" "$(gate_line | head -c 500)"
fi

# --- R6c the limits follow what the daemon has (#1373 panel) ------------------
# Docker refuses --cpus above the daemon's CPU count, so a small Docker Desktop
# VM would fail every gate run. The numbers are clamped to `docker info`, the
# flags sit in their own fence, and a pids limit comes with them.
run_script env FAKE_NCPU=4 bash "$script" --fast
if gate_line | grep -qE -- '--cpus[ =]4( |$)' && gate_line | grep -qE -- '--pids-limit[ =]4096( |$)'; then
  pass "R6c a daemon with 4 CPUs gets --cpus 4 (the default 6 is clamped) and --pids-limit 4096"
else
  fail "R6c the cpu quota is clamped to the daemon's NCPU" "$(gate_line | head -c 500)"
fi
run_script env FAKE_NCPU=4 CI_DOCKER_CPUS=2 bash "$script" --fast
if gate_line | grep -qE -- '--cpus[ =]2( |$)'; then
  pass "R6c a quota below the daemon's CPU count is kept as asked"
else
  fail "R6c a lower CI_DOCKER_CPUS is kept" "$(gate_line | head -c 500)"
fi
run_script env FAKE_MEMTOTAL=2147483648 bash "$script" --fast
if gate_line | grep -qE -- '--memory[ =]2147483648( |$)' && gate_line | grep -qE -- '--memory-swap[ =]2147483648( |$)'; then
  pass "R6c a daemon with 2 GiB gets that as the memory limit, and memory-swap equal to it"
else
  fail "R6c the memory limit is clamped to the daemon's MemTotal" "$(gate_line | head -c 500)"
fi
fences="$(gate_line | grep -oE -- '--memory[ =]' | wc -l | tr -d ' ')"
if [ "$fences" = 1 ]; then
  pass "R6c the fence is on the gate line once, not doubled"
else
  fail "R6c the fence appears once on the gate line" "$(gate_line | head -c 500)"
fi
run_script env FAKE_SWAPLIMIT=false bash "$script" --fast
if [ "$(rc)" = 0 ] && grep -q 'ci-docker: WARNING' "$work/out" && grep -q 'swap' "$work/out"; then
  pass "R6d a daemon that cannot limit swap gets one WARNING line and the run carries on"
else
  fail "R6d SwapLimit=false warns and continues" "rc=$(rc) $(head -c 400 "$work/out")"
fi
run_script bash "$script" --fast
if ! grep -q 'ci-docker: WARNING' "$work/out"; then
  pass "R6d control: with swap limits available there is no warning"
else
  fail "R6d control: no warning when SwapLimit=true" "$(head -c 400 "$work/out")"
fi

# --- R6e the remote container is named and removed on every exit (#1375 panel) ----
# An interrupted or dropped ssh session can leave the fenced container running
# on the host while the scratch directory is deleted under it.
run_script bash "$script" --host "$host" --fast
name="$(gate_line | sed -nE 's/.*--name ([^ ]+).*/\1/p' | head -1)"
if case "$name" in ci-docker-*) true ;; *) false ;; esac && gate_line | grep -q -- '--label retnd.ci-docker=1' \
   && grep -q "docker rm -f $name" "$log/docker"; then
  pass "R6e a remote run names its container ci-docker-<pid>-<epoch>, labels it, and docker rm -f's it on cleanup"
else
  fail "R6e named remote container removed by cleanup" "name=[$name] $(gate_line | head -c 400) | $(grep 'rm -f' "$log/docker" | head -2)"
fi
FAKE_GATE_RC=1 run_script env FAKE_GATE_RC=1 bash "$script" --host "$host" --fast
if grep -q 'docker rm -f ci-docker-' "$log/docker"; then
  pass "R6e the container is removed when the gate is red, too"
else
  fail "R6e container removed on a red gate" "$(grep 'rm -f' "$log/docker" | head -2)"
fi
run_script bash "$script" --fast
if ! gate_line | grep -q -- '--name '; then
  pass "R6e a local run does not name its container"
else
  fail "R6e local run unnamed" "$(gate_line | head -c 300)"
fi

# --- R7 host selection ------------------------------------------------------
run_script env CI_DOCKER_HOST=env@envhost bash "$script"
if grep -q 'DOCKER_HOST=ssh://env@envhost ' "$log/docker"; then pass "R7a CI_DOCKER_HOST selects the host"; else fail "R7a CI_DOCKER_HOST selects the host" "$(head -2 "$log/docker")"; fi
run_script env CI_DOCKER_HOST=env@envhost bash "$script" --host flag@flaghost
if grep -q 'DOCKER_HOST=ssh://flag@flaghost ' "$log/docker" && ! grep -q 'envhost' "$log/docker"; then pass "R7b --host beats CI_DOCKER_HOST"; else fail "R7b --host beats CI_DOCKER_HOST" "$(head -2 "$log/docker")"; fi
# The original silent failure: a remote DOCKER_HOST and no remote mode.
: > "$log/docker"
env FAKE_LOG="$log" PATH="$bin:$PATH" DOCKER_HOST=ssh://u@h bash "$script" > "$work/out" 2>&1; echo $? > "$work/rc"
if [ "$(rc)" != 0 ] && grep -q -- '--host' "$work/out" && ! grep -q 'ci-local.sh' "$log/docker"; then
  pass "R7c a remote DOCKER_HOST without --host is refused and points at --host (the empty-mount trap)"
else
  fail "R7c remote DOCKER_HOST without --host is refused" "rc=$(rc) $(head -c 300 "$work/out")"
fi
: > "$log/docker"
env FAKE_LOG="$log" PATH="$bin:$PATH" DOCKER_HOST=unix:///var/run/docker.sock bash "$script" --fast > "$work/out" 2>&1; echo $? > "$work/rc"
if [ "$(rc)" = 0 ]; then pass "R7d control: a unix:// DOCKER_HOST is still fine locally"; else fail "R7d control: unix DOCKER_HOST" "rc=$(rc) $(head -c 300 "$work/out")"; fi

# --- R8 where the copy goes -------------------------------------------------
run_script bash "$script" --host devshell@192.168.0.100
if grep -qE 'docker run .* /work/nas-work/ci-docker-' "$log/docker" || grep -q '/work/nas-work/ci-docker-' "$log/ssh"; then
  pass "R8a devshell@ hosts use /work/nas-work for scratch"
else
  fail "R8a devshell@ scratch is /work/nas-work" "$(cat "$log/ssh" | head -3)"
fi
run_script bash "$script" --host rom@192.168.0.10
if grep -q '/home/fake/workspace/nas-work/ci-docker-' "$log/ssh"; then
  pass "R8b other hosts use \$HOME/workspace/nas-work for scratch"
else
  fail "R8b other hosts scratch is under ~/workspace" "$(cat "$log/ssh" | head -3)"
fi
run_script env CI_DOCKER_REMOTE_DIR=/srv/gates bash "$script" --host u@h
if grep -q '/srv/gates/ci-docker-' "$log/ssh"; then
  pass "R8c CI_DOCKER_REMOTE_DIR overrides the scratch parent"
else
  fail "R8c CI_DOCKER_REMOTE_DIR overrides the scratch parent" "$(cat "$log/ssh" | head -3)"
fi

# --- R9 transfer without rsync on the host ----------------------------------
run_script env CI_DOCKER_SYNC=tar bash "$script" --host u@h
if [ -s "$log/tar" ] && [ -s "$log/tar-list" ] && [ ! -s "$log/rsync" ] && [ "$(rc)" = 0 ]; then
  pass "R9 CI_DOCKER_SYNC=tar ships the tree with tar over ssh and never calls rsync"
else
  fail "R9 CI_DOCKER_SYNC=tar" "rc=$(rc) tar=$(wc -c < "$log/tar") rsync=$(wc -c < "$log/rsync")"
fi

# --- R11 Linux daemons: the socket's group, the host's network and /tmp (#1331)
# Docker Desktop: the socket arrives as root:root, so group 0, and nothing
# else changes. This is the Mac path and it has to stay exactly as it was.
run_script env FAKE_SOCK_GID=0 "FAKE_DAEMON_OS=Docker Desktop" bash "$script" --fast
gl="$(gate_line)"
if [ "$(rc)" = 0 ] && printf '%s' "$gl" | grep -q -- '--group-add 0 ' \
   && ! printf '%s' "$gl" | grep -q -- '--network' && ! printf '%s' "$gl" | grep -q -- '-v /tmp:/tmp'; then
  pass "R11a Docker Desktop: group 0 for the socket, no host network, no shared /tmp"
else
  fail "R11a Docker Desktop is unchanged" "rc=$(rc) $gl"
fi
# A Linux host: root:docker with that machine's own gid.
run_script env FAKE_SOCK_GID=116 "FAKE_DAEMON_OS=Debian GNU/Linux 13 (trixie)" bash "$script" --host "$host" --fast
gl="$(gate_line)"
if [ "$(rc)" = 0 ] && printf '%s' "$gl" | grep -q -- '--group-add 116 ' && ! printf '%s' "$gl" | grep -q -- '--group-add 0 ' \
   && printf '%s' "$gl" | grep -q -- '--network host' && printf '%s' "$gl" | grep -q -- '-v /tmp:/tmp'; then
  pass "R11b a Linux daemon: the socket's own gid, the host's network and the host's /tmp"
else
  fail "R11b a Linux daemon gets the socket gid, host network and /tmp" "rc=$(rc) $gl"
fi
run_script env FAKE_SOCK_GID=121 "FAKE_DAEMON_OS=Debian GNU/Linux 12 (bookworm)" bash "$script" --host "$host" --exec true
el="$(grep -E 'docker run .* true$' "$log/docker" | grep -v -- '--user 0:0' || true)"
if [ "$(rc)" = 0 ] && printf '%s' "$el" | grep -q -- '--group-add 121 ' && printf '%s' "$el" | grep -q -- '--network host'; then
  pass "R11c --exec gets the same group and network as the gate"
else
  fail "R11c --exec gets the same group and network" "rc=$(rc) $el"
fi
run_script env CI_DOCKER_SHARE_HOST=0 FAKE_SOCK_GID=116 "FAKE_DAEMON_OS=Debian GNU/Linux 13 (trixie)" bash "$script" --host "$host" --fast
gl="$(gate_line)"
if [ "$(rc)" = 0 ] && ! printf '%s' "$gl" | grep -q -- '--network' && ! printf '%s' "$gl" | grep -q -- '-v /tmp:/tmp' \
   && printf '%s' "$gl" | grep -q -- '--group-add 116 '; then
  pass "R11d CI_DOCKER_SHARE_HOST=0 turns the host network and /tmp off and keeps the socket group"
else
  fail "R11d CI_DOCKER_SHARE_HOST=0" "rc=$(rc) $gl"
fi
run_script env CI_DOCKER_SHARE_HOST=1 FAKE_SOCK_GID=0 "FAKE_DAEMON_OS=Docker Desktop" bash "$script" --fast
gl="$(gate_line)"
if [ "$(rc)" = 0 ] && printf '%s' "$gl" | grep -q -- '--network host'; then
  pass "R11e CI_DOCKER_SHARE_HOST=1 forces it on"
else
  fail "R11e CI_DOCKER_SHARE_HOST=1" "rc=$(rc) $gl"
fi
run_script env CI_DOCKER_SHARE_HOST=yes bash "$script" --fast
if [ "$(rc)" != 0 ] && [ -z "$(gate_line)" ]; then
  pass "R11f a CI_DOCKER_SHARE_HOST that is not auto, 1 or 0 is refused before the gate runs"
else
  fail "R11f bad CI_DOCKER_SHARE_HOST is refused" "rc=$(rc) $(head -c 300 "$work/out")"
fi
run_script env FAKE_SOCK_GID= bash "$script" --host "$host" --fast
if [ "$(rc)" != 0 ] && [ -z "$(gate_line)" ] && grep -q 'socket' "$work/out"; then
  pass "R11g a socket group that cannot be read is refused, not guessed"
else
  fail "R11g unreadable socket group is refused" "rc=$(rc) $(head -c 300 "$work/out")"
fi

# --- R12 the scratch copy is not group-writable on a umask-002 host (#1331)
run_script bash "$script" --host "$host" --fast
if grep -q "umask 022 && mkdir -p '.*/repo' '.*/git'" "$log/ssh" && grep -qE "chmod go-w '[^']*/ci-docker-[^']*' '[^']*/repo' '[^']*/git'" "$log/ssh"; then
  pass "R12 the scratch directories are made under umask 022 and chmod go-w after the copy, so the key-directory checks do not refuse them"
else
  fail "R12 scratch directories are not group-writable" "$(grep -E 'mkdir|chmod' "$log/ssh" | head -3)"
fi

# --- R13 a remote whose docker is not on a non-interactive PATH (#1331)
# Compared before and after rather than "none there": /tmp can be shared
# with other runs (it is, under ci-docker on a Linux daemon).
shims() { ls -d "${TMPDIR:-/tmp}"/ci-docker-ssh.* 2>/dev/null | sort; }
shims_before="$(shims)"
run_script env FAKE_REMOTE_DOCKER=/opt/homebrew/bin/docker bash "$script" --host "$host" --fast
if [ "$(rc)" = 0 ] && grep -q 'calling it by that path' "$work/out" && [ "$(shims)" = "$shims_before" ]; then
  pass "R13 a docker found only at /opt/homebrew/bin is called by that path, and the stand-in ssh is removed afterwards"
else
  fail "R13 remote docker by full path" "rc=$(rc) $(head -c 300 "$work/out")"
fi
# The stand-in rewrites the dial-stdio command docker's ssh helper sends,
# in both of the shapes CLIs have used, and leaves everything else alone.
shim_dir="$work/shimcheck"; mkdir -p "$shim_dir"
sed -n '/^      cat >"\$ssh_shim\/ssh" <<SHIM$/,/^SHIM$/p' "$script" | sed '1d;$d' |
  sed -e 's/\$remote_docker/\/opt\/homebrew\/bin\/docker/g' -e "s#\$real_ssh#$bin/echo-args#" -e 's/\\\$/$/g' >"$shim_dir/ssh"
printf '#!/usr/bin/env bash\nprintf "[%%s]" "$@"\n' >"$bin/echo-args"; chmod +x "$shim_dir/ssh" "$bin/echo-args"
got_a="$("$shim_dir/ssh" -o X -- u@h docker system dial-stdio)"
got_b="$("$shim_dir/ssh" -- u@h "docker system dial-stdio")"
got_c="$("$shim_dir/ssh" u@h "mkdir -p '/x/dockerish'")"
if [ "$got_a" = "[-o][X][--][u@h][/opt/homebrew/bin/docker][system][dial-stdio]" ] \
   && [ "$got_b" = "[--][u@h][/opt/homebrew/bin/docker system dial-stdio]" ] && [ "$got_c" = "[u@h][mkdir -p '/x/dockerish']" ]; then
  pass "R13b the stand-in ssh rewrites only the docker word, split or quoted"
else
  fail "R13b the stand-in ssh rewrite" "a=$got_a b=$got_b c=$got_c"
fi
rm -f "$bin/echo-args"

# The cleanup's own ssh goes through the stand-in too, so it has to outlive
# it: MERCURY (Homebrew docker, colima's empty mounts) is that case exactly.
run_script env FAKE_REMOTE_DOCKER=/opt/homebrew/bin/docker FAKE_MOUNT_EMPTY=1 bash "$script" --host "$host" --fast
if [ "$(rc)" != 0 ] && grep -q "rm -rf -- '.*/ci-docker-" "$log/ssh" && [ "$(shims)" = "$shims_before" ]; then
  pass "R13c with the stand-in ssh in place the scratch copy is still removed over ssh, and the stand-in goes last"
else
  fail "R13c cleanup through the stand-in ssh" "rc=$(rc) $(grep -c 'rm -rf' "$log/ssh") $(head -c 300 "$work/out")"
fi

# --- R15 a linked worktree's admin dir points at the copy, not the local path
run_script bash "$script" --host "$host" --fast
if grep -q -- "--- stdin for: cat > '.*/git/worktrees/[^']*/gitdir'" "$log/ssh-stdin" \
   && grep -A1 -- "/gitdir'" "$log/ssh-stdin" | grep -qE '/ci-docker-[^/]*/repo/\.git$'; then
  pass "R15 the copied worktree's gitdir names the copy, so a git worktree prune there keeps it"
elif [ "$(git -C "$repo_root" rev-parse --git-dir)" = "$(git -C "$repo_root" rev-parse --git-common-dir)" ]; then
  pass "R15 (not a linked worktree here, so there is no admin dir to point back)"
else
  fail "R15 the copied worktree's gitdir is rewritten" "$(grep -A1 gitdir "$log/ssh-stdin" | head -4)"
fi

# --- R14 a daemon that sees the copy as empty is refused before the gate runs
run_script env FAKE_MOUNT_EMPTY=1 bash "$script" --host "$host" --fast
if [ "$(rc)" != 0 ] && [ -z "$(gate_line)" ] && grep -q 'empty directory' "$work/out" \
   && ! grep -qE 'rm -rf -- /ws/ci-docker-' "$log/docker" && grep -q "rm -rf -- '.*/ci-docker-" "$log/ssh"; then
  pass "R14 an empty bind mount is a named refusal, and the real copy is removed over ssh rather than through the daemon"
else
  fail "R14 empty bind mount refused" "rc=$(rc) $(head -c 300 "$work/out")"
fi

# --- R16 the tests-checkout cache is a path the daemon can see (#1421) --------
# The browser lane bind-mounts the pinned tests checkout into the client
# machine as /suite, and that mount is made by the REMOTE daemon. A named
# volume's /ci/... path means nothing on the daemon's disk, so the mount came
# up as an empty root-owned directory and the client died on
# `mkdir /suite/test-results: EACCES`. In remote mode the cache has to be a
# bind mount of a directory inside the shipped scratch copy, at the same path
# inside and out, and XDG_CACHE_HOME has to say so.
run_script bash "$script" --host "$host" --fast
gl="$(gate_line)"
xdg_src="$(printf '%s' "$gl" | grep -oE -- '-v [^ ]*/ci-docker-[^ ]*/xdg:[^ ]*' | head -1)"
xdg_dir="${xdg_src#-v }"; xdg_dir="${xdg_dir%%:*}"
if [ -n "$xdg_dir" ] && [ "$(printf '%s' "$xdg_src" | cut -d: -f2)" = "$xdg_dir" ] \
   && printf '%s' "$gl" | grep -q -- "-e XDG_CACHE_HOME=$xdg_dir" \
   && ! printf '%s' "$gl" | grep -q -- '-xdg:/ci/.cache/retnd-tests-gate'; then
  pass "R16a remote mode mounts the tests cache from the scratch copy at the same path and points XDG_CACHE_HOME at it"
else
  fail "R16a remote tests cache is a same-path bind mount" "$gl"
fi
if grep -q "mkdir -p .*/xdg" "$log/ssh" || grep -q "/xdg" "$log/ssh"; then
  pass "R16b the cache directory is made by the remote account, so the remote uid owns it"
else
  fail "R16b the cache directory is made over ssh" "$(head -5 "$log/ssh")"
fi
run_script bash "$script" --fast
if gate_line | grep -q -- '-xdg:/ci/.cache/retnd-tests-gate' && ! gate_line | grep -q XDG_CACHE_HOME; then
  pass "R16c local mode keeps the named cache volume"
else
  fail "R16c local mode keeps the named cache volume" "$(gate_line)"
fi

# --- R10 the page documents the knobs ---------------------------------------
for v in CI_DOCKER_HOST CI_DOCKER_REMOTE_DIR CI_DOCKER_MIN_FREE_MB CI_DOCKER_KEEP_CACHE CI_DOCKER_SYNC CI_DOCKER_SHARE_HOST; do
  if grep -q "\`$v\`" "$repo_root/docs/reference/local-gate.md"; then
    pass "R10 docs/reference/local-gate.md documents $v"
  else
    fail "R10 docs/reference/local-gate.md documents $v"
  fi
done

echo "ci-docker-remote: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

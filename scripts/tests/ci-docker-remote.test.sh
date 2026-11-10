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
esac
case "$*" in *scripts/ci-local.sh*) exit "${FAKE_GATE_RC:-0}" ;; esac
exit 0
SH
cat > "$bin/rsync" <<'SH'
#!/usr/bin/env bash
echo "rsync $*" >> "$FAKE_LOG/rsync"
exit 0
SH
cat > "$bin/tar" <<'SH'
#!/usr/bin/env bash
echo "tar $*" >> "$FAKE_LOG/tar"
exit 0
SH
cat > "$bin/ssh" <<'SH'
#!/usr/bin/env bash
cmd="${*: -1}"
echo "ssh $*" >> "$FAKE_LOG/ssh"
case "$cmd" in
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
  : > "$log/docker"; : > "$log/rsync"; : > "$log/tar"; : > "$log/ssh"; : > "$log/ssh-stdin"
  env -u DOCKER_HOST -u CI_DOCKER_HOST FAKE_LOG="$log" PATH="$bin:$PATH" "$@" > "$work/out" 2>&1
  echo $? > "$work/rc"
}
rc() { cat "$work/rc"; }
gate_line() { grep 'scripts/ci-local.sh' "$log/docker" || true; }

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
if ! grep -v '^DOCKER_HOST=ssh://runner@builder.example ' "$log/docker" | grep -q .; then
  pass "R1b every docker call is made against the remote daemon"
else
  fail "R1b every docker call is made against the remote daemon" "$(grep -v '^DOCKER_HOST=ssh://runner@builder.example ' "$log/docker" | head -3)"
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
run_script env GITHUB_TOKEN=tok-should-not-travel NPM_TOKEN=npm-should-not-travel bash "$script" --host "$host"
sent="$(cat "$log/rsync")"
missing=""
for pat in node_modules '.env' '.npmrc' '.netrc' '.git-credentials' '*.pem' '*.key' '*token*'; do
  printf '%s' "$sent" | grep -qF -- "--exclude=$pat" || missing="$missing $pat"
done
if [ -z "$missing" ]; then
  pass "R5a rsync excludes node_modules and the usual credential files"
else
  fail "R5a rsync excludes" "not excluded:$missing"
fi
if ! cat "$log"/* | grep -q 'should-not-travel'; then
  pass "R5b a token in the caller's environment appears in no ssh, rsync or docker argument"
else
  fail "R5b token leaked into an argument" "$(grep -l should-not-travel "$log"/*)"
fi
if ! grep -qE -- '-(e|-env|-env-file) ' "$log/docker" && ! grep -q -- '--env' "$log/docker"; then
  pass "R5c the gate container is given no environment variables"
else
  fail "R5c the gate container is given no environment" "$(grep -- '-e\|--env' "$log/docker" | head -2)"
fi
if grep -q 'gitdir: ' "$log/ssh-stdin" && ! grep -q 'url = ' "$log/ssh-stdin"; then
  pass "R5d the remote git config is a minimal one written fresh, so remote URLs and credentials in .git/config do not travel"
else
  fail "R5d remote git config is written fresh" "$(cat "$log/ssh-stdin")"
fi
if printf '%s' "$sent" | grep -qF -- '--exclude=config' ; then
  pass "R5e the local .git/config is excluded from the sync"
else
  fail "R5e the local .git/config is excluded from the sync" "$sent"
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
if [ -s "$log/tar" ] && [ ! -s "$log/rsync" ] && [ "$(rc)" = 0 ]; then
  pass "R9 CI_DOCKER_SYNC=tar ships the tree with tar over ssh and never calls rsync"
else
  fail "R9 CI_DOCKER_SYNC=tar" "rc=$(rc) tar=$(wc -c < "$log/tar") rsync=$(wc -c < "$log/rsync")"
fi

# --- R10 the page documents the knobs ---------------------------------------
for v in CI_DOCKER_HOST CI_DOCKER_REMOTE_DIR CI_DOCKER_MIN_FREE_MB CI_DOCKER_KEEP_CACHE CI_DOCKER_SYNC; do
  if grep -q "\`$v\`" "$repo_root/docs/reference/local-gate.md"; then
    pass "R10 docs/reference/local-gate.md documents $v"
  else
    fail "R10 docs/reference/local-gate.md documents $v"
  fi
done

echo "ci-docker-remote: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

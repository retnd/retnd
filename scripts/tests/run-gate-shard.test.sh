#!/usr/bin/env bash
# Self-test for scripts/release/run-gate-shard.sh (#1460, plan item C of #1455).
#
# The runner takes a shard name and a host, runs `bash scripts/ci-local.sh
# --shard <name>` on that host through scripts/ci-docker.sh --host, and hands
# back a log, a result file and one of three exit statuses: 0 ok, 1 failed,
# 3 INCOMPLETE. Two layers of stand-ins, both from the same family as
# ci-docker-remote.test.sh:
#
#   layer 1  a stand-in ci-docker.sh (CI_DOCKER_SH) and a stand-in ci-local.sh
#            inside a throw-away git repo: what the runner asks for, how it
#            reads the answer, what it refuses before anything is shipped;
#   layer 2  the REAL ci-docker.sh with stand-ins for docker, ssh, rsync and
#            tar on PATH: what actually reaches a remote host (platform, scratch
#            directory, refs shipped, which ssh commands run).
#
# Nothing here talks to a daemon or a network. It proves the script's
# behaviour, not that a real remote run works. Every refusal has a control.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
runner="$repo_root/scripts/release/run-gate-shard.sh"
real_ci_docker="$repo_root/scripts/ci-docker.sh"

work="$(mktemp -d "${TMPDIR:-/tmp}/run-gate-shard.XXXXXX")"
case "$work" in /*/run-gate-shard.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || echo "         | $2"; }

# --- a tiny repo the runner can be pointed at --------------------------------
t="$work/tree"
mkdir -p "$t/scripts" "$work/bin" "$work/log" "$work/out"
git -C "$t" init -q -b main
git -C "$t" config user.email t@example.invalid
git -C "$t" config user.name t
cat > "$t/scripts/ci-local.sh" <<'SH'
#!/usr/bin/env bash
echo "stand-in ci-local $*" >> "$FAKE_LOG/ci-local"
shard=""; [ "${1:-}" = "--shard" ] && shard=" (shard $2)"
echo "==> ci-local: ok$shard"
exit "${FAKE_LOCAL_RC:-0}"
SH
echo x > "$t/file.txt"
git -C "$t" add -A
git -C "$t" commit -q -m one
git -C "$t" update-ref refs/remotes/origin/release HEAD
git -C "$t" tag v0.0.1

# Stand-in ci-docker.sh: records its arguments and the platform it was told,
# prints what FAKE_VERDICT says, exits with FAKE_RC.
cat > "$work/ci-docker-stub.sh" <<'SH'
#!/usr/bin/env bash
echo "ci-docker $*" >> "$FAKE_LOG/ci-docker"
echo "platform=${RCM_CI_PLATFORM:-unset} memory=${CI_DOCKER_MEMORY:-unset} cpus=${CI_DOCKER_CPUS:-unset} dockerplat=${DOCKER_DEFAULT_PLATFORM:-unset}" >> "$FAKE_LOG/ci-docker-env"
echo "some shard output"
[ -z "${FAKE_KILL_PARENT:-}" ] || kill -9 "$PPID"
if [ -n "${FAKE_SLEEP:-}" ]; then echo "$$" > "$FAKE_LOG/stub-pid"; sleep "$FAKE_SLEEP"; fi
[ -z "${FAKE_VERDICT:-}" ] || echo "$FAKE_VERDICT"
exit "${FAKE_RC:-0}"
SH
# Stand-ins that must stay untouched by the runner itself.
for b in ssh rsync tar docker; do
  printf '#!/usr/bin/env bash\necho "%s $*" >> "$FAKE_LOG/%s-direct"\nexit 0\n' "$b" "$b" > "$work/bin/$b"
done
chmod +x "$work/bin"/* "$work/ci-docker-stub.sh"

OK_LINE='==> ci-local: ok'
run_runner() {
  rm -rf "$work/out"; mkdir -p "$work/out"
  : > "$work/log/ci-docker"; : > "$work/log/ci-docker-env"; : > "$work/log/ci-local"
  for b in ssh rsync tar docker; do : > "$work/log/$b-direct"; done
  ( cd "$t" && env -u RCM_CI_PLATFORM -u DOCKER_DEFAULT_PLATFORM -u CI_DOCKER_HOST -u DOCKER_HOST \
      FAKE_LOG="$work/log" PATH="$work/bin:$PATH" CI_DOCKER_SH="$work/ci-docker-stub.sh" \
      "$@" ) > "$work/stdout" 2>&1
  echo $? > "$work/rc"
}
rc() { cat "$work/rc"; }
result_has() { grep -qx "$1" "$work/out/$2.result" 2>/dev/null; }

# --- U usage ------------------------------------------------------------------
run_runner bash "$runner"
[ "$(rc)" = 2 ] && pass "U1 no arguments is a usage error (exit 2)" || fail "U1 no arguments" "rc=$(rc)"
run_runner bash "$runner" static --out "$work/out"
[ "$(rc)" = 0 ] && [ ! -s "$work/log/ci-docker" ] && grep -q 'stand-in ci-local --shard static' "$work/log/ci-local" \
  && pass "U2 a shard without --host runs on this machine (the recorder passes none by default)" || fail "U2 no --host" "rc=$(rc)"
run_runner bash "$runner" static --host jupiter --out "$work/out"
[ "$(rc)" = 2 ] && [ ! -s "$work/log/ci-docker" ] && pass "U3 an unknown host is refused" || fail "U3 unknown host" "rc=$(rc)"
run_runner bash "$runner" '../x;y' --host nas --out "$work/out"
[ "$(rc)" = 2 ] && [ ! -s "$work/log/ci-docker" ] && pass "U4 a shard name that is not [a-z0-9][a-z0-9-]* is refused" || fail "U4 bad shard name" "rc=$(rc)"
run_runner env bash "$runner" 2fa --host mac --out "$work/out"
[ "$(rc)" = 0 ] && pass "U5 a shard name may start with a digit, as the recorder allows" || fail "U5 digit-leading name" "rc=$(rc)"

# --- G green, and what the runner asks of ci-docker ---------------------------
run_runner env FAKE_VERDICT="==> ci-local: ok (shard core-docker)" bash "$runner" core-docker --host mars --out "$work/out"
if [ "$(rc)" = 0 ] && result_has "status=ok" core-docker && result_has "shard=core-docker" core-docker \
   && result_has "host=mars" core-docker && result_has "exit=0" core-docker; then
  pass "G1 an ok verdict and exit 0 is exit 0, status=ok in the result file"
else
  fail "G1 green shard" "rc=$(rc) $(cat "$work/out/core-docker.result" 2>/dev/null | tr '\n' ' ')"
fi
if grep -q -- '--host devshell@192.168.0.100' "$work/log/ci-docker" && grep -q -- '--deps' "$work/log/ci-docker" \
   && grep -q -- 'scripts/ci-local.sh --shard core-docker' "$work/log/ci-docker"; then
  pass "G2 mars goes to devshell@192.168.0.100 through ci-docker --deps, running ci-local.sh --shard core-docker"
else
  fail "G2 ci-docker arguments" "$(cat "$work/log/ci-docker")"
fi
if grep -q 'some shard output' "$work/out/core-docker.log" && grep -q 'some shard output' "$work/stdout"; then
  pass "G3 the shard's output is in the log slice and is also streamed"
else
  fail "G3 log slice and stream" "$(head -c 300 "$work/out/core-docker.log" 2>/dev/null)"
fi
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
grep -q -- '--host rom@192.168.0.10' "$work/log/ci-docker" && [ "$(rc)" = 0 ] \
  && pass "G4 nas goes to rom@192.168.0.10" || fail "G4 nas target" "$(cat "$work/log/ci-docker")"
if result_has "command=bash scripts/ci-local.sh --shard static" static && grep -q '^duration_s=[0-9]' "$work/out/static.result" \
   && grep -q '^started_at=20[0-9][0-9]-' "$work/out/static.result"; then
  pass "G5 the result file records the command, a UTC start and the duration"
else
  fail "G5 result fields" "$(cat "$work/out/static.result" 2>/dev/null | tr '\n' ' ')"
fi

# --- F failed vs INCOMPLETE ---------------------------------------------------
run_runner env FAKE_RC=1 FAKE_VERDICT='==> ci-local: FAILED (go test)' bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 1 ] && result_has "status=failed" static && pass "F1 a FAILED verdict is exit 1, status=failed" || fail "F1 failed" "rc=$(rc)"
run_runner env FAKE_RC=3 FAKE_VERDICT='==> ci-local: INCOMPLETE. This run is not merge evidence.' bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && result_has "status=incomplete" static && pass "F2 the gate's own INCOMPLETE (3) is INCOMPLETE" || fail "F2 incomplete" "rc=$(rc)"
run_runner env FAKE_RC=255 bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && result_has "status=incomplete" static && pass "F3 a dropped ssh session (255, no verdict) is INCOMPLETE, never failed" || fail "F3 ssh drop" "rc=$(rc)"
run_runner env FAKE_RC=2 bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && pass "F4 a ci-docker refusal (2, no gate verdict) is INCOMPLETE" || fail "F4 refusal" "rc=$(rc)"
run_runner env FAKE_RC=137 bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && pass "F5 a killed run (137) is INCOMPLETE" || fail "F5 killed" "rc=$(rc)"
run_runner env FAKE_RC=0 bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && result_has "status=incomplete" static && pass "F6 exit 0 without the ok verdict line is INCOMPLETE" || fail "F6 no verdict" "rc=$(rc)"
run_runner env FAKE_RC=1 FAKE_VERDICT='==> ci-local: FAILED (x), then ==> ci-local: ok' bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 1 ] && pass "F7 a red exit never becomes ok because the log mentions ok" || fail "F7 red stays red" "rc=$(rc)"

# --- P preflight: refs, tags, a clean tree -------------------------------------
git -C "$t" update-ref -d refs/remotes/origin/release
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
if [ "$(rc)" = 3 ] && [ ! -s "$work/log/ci-docker" ] && grep -q 'git fetch origin' "$work/stdout" && result_has "status=incomplete" static; then
  pass "P1 no origin/release in the clone: INCOMPLETE before anything ships, with the fetch line"
else
  fail "P1 origin/release" "rc=$(rc) $(head -c 300 "$work/stdout")"
fi
git -C "$t" update-ref refs/remotes/origin/release HEAD
git -C "$t" tag -d v0.0.1 >/dev/null
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
if [ "$(rc)" = 3 ] && [ ! -s "$work/log/ci-docker" ] && grep -q 'git fetch --tags' "$work/stdout"; then
  pass "P2 no v* tag in the clone: INCOMPLETE before anything ships, with the fetch line"
else
  fail "P2 tags" "rc=$(rc) $(head -c 300 "$work/stdout")"
fi
git -C "$t" tag v0.0.1
echo more >> "$t/file.txt"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && [ ! -s "$work/log/ci-docker" ] && pass "P3 a modified tracked file: refused, the shipped tree must be the committed one" || fail "P3 dirty" "rc=$(rc)"
git -C "$t" checkout -q file.txt
echo u > "$t/untracked.txt"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && [ ! -s "$work/log/ci-docker" ] && pass "P4 an untracked file: refused, nothing outside the tree travels" || fail "P4 untracked" "rc=$(rc)"
rm -f "$t/untracked.txt"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 0 ] && pass "P5 control: refs, tag and a clean tree again, and the same call runs" || fail "P5 control" "rc=$(rc)"

# --- A platform: amd64 only on native x86, never emulated ---------------------
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
grep -q 'platform=linux/amd64' "$work/log/ci-docker-env" && pass "A1 nas is asked for linux/amd64" || fail "A1 nas platform" "$(cat "$work/log/ci-docker-env")"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host mars --out "$work/out"
grep -q 'platform=linux/amd64' "$work/log/ci-docker-env" && pass "A2 mars is asked for linux/amd64" || fail "A2 mars platform" "$(cat "$work/log/ci-docker-env")"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" RCM_CI_PLATFORM=linux/arm64 bash "$runner" static --host nas --out "$work/out"
[ "$(rc)" = 3 ] && [ ! -s "$work/log/ci-docker" ] && pass "A3 arm64 requested for an x86 host: refused" || fail "A3 arm64 on x86" "rc=$(rc)"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" DOCKER_DEFAULT_PLATFORM=linux/arm64 bash "$runner" static --host mars --out "$work/out"
[ "$(rc)" = 0 ] && pass "A4 an inherited DOCKER_DEFAULT_PLATFORM is not refused (ci-docker.sh unsets it)" || fail "A4 default platform" "rc=$(rc)"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" DOCKER_DEFAULT_PLATFORM=linux/amd64 bash "$runner" static --host mars --out "$work/out"
[ "$(rc)" = 0 ] && pass "A5 control: an inherited amd64 default is fine on mars" || fail "A5 amd64 default" "rc=$(rc)"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard js)" bash "$runner" js --host mercury --out "$work/out"
[ "$(rc)" = 2 ] && [ ! -s "$work/log/ci-docker" ] && pass "A6 mercury is refused: no bench-queue turn, and gate work stays off it" || fail "A6 mercury" "rc=$(rc)"

# --- M the mac, and the fence --------------------------------------------------
run_runner env FAKE_VERDICT="==> ci-local: ok (shard gh-rows)" bash "$runner" gh-rows --host mac --out "$work/out"
if [ "$(rc)" = 0 ] && [ ! -s "$work/log/ci-docker" ] && grep -q 'stand-in ci-local --shard gh-rows' "$work/log/ci-local"; then
  pass "M1 --host mac runs ci-local.sh --shard directly, no docker"
else
  fail "M1 mac" "rc=$(rc) $(cat "$work/log/ci-local")"
fi
run_runner env FAKE_VERDICT="==> ci-local: ok (shard core-docker)" CI_DOCKER_MEMORY=6g CI_DOCKER_CPUS=4 bash "$runner" core-docker --host mars --out "$work/out"
grep -q 'memory=6g cpus=4' "$work/log/ci-docker-env" && pass "M2 the per-shard fence (CI_DOCKER_MEMORY, CI_DOCKER_CPUS) reaches ci-docker" || fail "M2 fence" "$(cat "$work/log/ci-docker-env")"

# --- R what a reader of the result can trust ---------------------------------
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host mars --out "$work/out"
run_runner env FAKE_VERDICT="==> ci-local: ok" bash "$runner" static --host mars --out "$work/out"
[ "$(rc)" = 3 ] && pass "R2 a plain ok that names no shard is not a pass (a shard flag the gate ignored)" || fail "R2 plain ok" "rc=$(rc)"
run_runner env FAKE_VERDICT="==> ci-local: ok (shard core-docker)" bash "$runner" static --host mars --out "$work/out"
[ "$(rc)" = 3 ] && pass "R3 an ok for another shard is not a pass" || fail "R3 other shard" "rc=$(rc)"
git -C "$t" tag -d v0.0.1 >/dev/null
run_runner bash "$runner" static --host nas --out "$work/out"
grep -q '^==> ci-local: INCOMPLETE' "$work/stdout" && pass "R4 a refusal prints an INCOMPLETE verdict line, so the recorder reads exit 3 as incomplete" || fail "R4 verdict line" "$(head -c 300 "$work/stdout")"
git -C "$t" tag v0.0.1
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$t/shard-out"
[ "$(rc)" = 2 ] && pass "R5 --out inside the tree is refused (it would make the tree dirty)" || fail "R5 out in tree" "rc=$(rc)"
( cd "$t" && env FAKE_LOG="$work/log" PATH="$work/bin:$PATH" CI_DOCKER_SH="$work/ci-docker-stub.sh" TMPDIR="$work/tmp" FAKE_VERDICT="==> ci-local: ok (shard static)" \
    bash "$runner" static --host nas ) > "$work/stdout" 2>&1
[ ! -e "$t/.git/local-gate" ] && pass "R6 the default output stays out of the git dir that ci-docker ships" || fail "R6 default out" "$(ls "$t/.git")"
# a TERM reaches the shard's own process and returns promptly, and while a run
# is going its result is never a leftover status=ok
: > "$work/log/stub-pid"
rm -rf "$work/out"; mkdir -p "$work/out"; printf 'status=ok\nreason=stale\n' > "$work/out/static.result"
( cd "$t" && env -u RCM_CI_PLATFORM FAKE_LOG="$work/log" PATH="$work/bin:$PATH" CI_DOCKER_SH="$work/ci-docker-stub.sh" FAKE_SLEEP=30 \
    bash "$runner" static --host nas --out "$work/out" ) > "$work/stdout" 2>&1 &
runner_pid=$!
for _ in $(seq 1 50); do [ -s "$work/log/stub-pid" ] && break; sleep 0.1; done
if grep -qx 'status=ok' "$work/out/static.result" 2>/dev/null; then
  fail "R1 a run in flight leaves no stale status=ok to be read" "$(cat "$work/out/static.result")"
else
  pass "R1 a run in flight leaves no stale status=ok to be read"
fi
t_start="$(date +%s)"
kill -TERM "$runner_pid" 2>/dev/null
wait "$runner_pid"; term_rc=$?
t_took=$(( $(date +%s) - t_start ))
stub_pid="$(cat "$work/log/stub-pid")"
if [ "$term_rc" = 3 ] && [ "$t_took" -lt 10 ] && ! kill -0 "$stub_pid" 2>/dev/null; then
  pass "R7 TERM returns INCOMPLETE at once and takes the shard's process with it"
else
  fail "R7 TERM" "rc=$term_rc took=${t_took}s stub=$stub_pid"
  kill "$stub_pid" 2>/dev/null
fi

# --- N the runner itself touches nothing but ci-docker -------------------------
run_runner env FAKE_VERDICT="==> ci-local: ok (shard static)" bash "$runner" static --host nas --out "$work/out"
if [ "$(rc)" = 0 ] && [ -z "$(cat "$work/log/ssh-direct" "$work/log/rsync-direct" "$work/log/tar-direct" "$work/log/docker-direct")" ]; then
  pass "N1 the runner calls no ssh, rsync, tar or docker of its own; everything remote goes through ci-docker.sh"
else
  fail "N1 runner stays out of the host" "$(cat "$work/log/ssh-direct" "$work/log/docker-direct" | head -3)"
fi

# --- L layer 2: the real ci-docker.sh over stand-in docker and ssh -------------
l2="$work/l2"; mkdir -p "$l2/bin" "$l2/log"
cat > "$l2/bin/docker" <<'SH'
#!/usr/bin/env bash
echo "DOCKER_HOST=${DOCKER_HOST:-} docker $*" >> "$FAKE_LOG/docker"
case "$1 ${2:-}" in
  "image inspect") exit 1 ;;
  "volume ls") exit 0 ;;
  "info --format")
    case "$3" in *NCPU*) echo "8 34359738368 true"; exit 0 ;; esac
    echo "Ubuntu"; exit 0 ;;
esac
case "$*" in *"stat -c %g /var/run/docker.sock"*) echo 0; exit 0 ;; esac
case "$*" in *"test -f "*"/scripts/ci-local.sh"*) exit 0 ;; esac
case "$*" in *"bash scripts/ci-local.sh --shard core-docker"*) echo "==> ci-local: ok (shard core-docker)"; exit 0 ;; esac
exit 0
SH
cat > "$l2/bin/rsync" <<'SH'
#!/usr/bin/env bash
echo "rsync $*" >> "$FAKE_LOG/rsync"
case "$*" in *--files-from=-*) tr '\0' '\n' >> "$FAKE_LOG/rsync-list" ;; esac
exit 0
SH
cat > "$l2/bin/ssh" <<'SH'
#!/usr/bin/env bash
cmd="${*: -1}"
echo "ssh $*" >> "$FAKE_LOG/ssh"
case "$cmd" in
  *"command -v docker"*) echo docker ;;
  *"id -u"*) echo 1000 ;;
  *"id -g"*) echo 1000 ;;
  *'$HOME'*) printf '%s' "/home/fake" ;;
  *"df -Pk"*) printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/x 999999999 1 99999999 1%% /work\n' ;;
esac
exit 0
SH
chmod +x "$l2/bin"/*
run_real() {
  rm -rf "$work/out"; mkdir -p "$work/out"
  for f in docker rsync ssh rsync-list; do : > "$l2/log/$f"; done
  ( cd "$t" && env -u RCM_CI_PLATFORM -u DOCKER_DEFAULT_PLATFORM -u CI_DOCKER_HOST -u DOCKER_HOST \
      FAKE_LOG="$l2/log" PATH="$l2/bin:$PATH" CI_DOCKER_SH="$real_ci_docker" CI_DOCKER_REPO="$t" "$@" ) > "$work/stdout" 2>&1
  echo $? > "$work/rc"
}
run_real bash "$runner" core-docker --host nas --out "$work/out"
gl="$(grep 'bash scripts/ci-local.sh' "$l2/log/docker" | grep -v 'test -f' | head -1)"
if [ "$(rc)" = 0 ] && printf '%s' "$gl" | grep -q -- '--platform linux/amd64' && printf '%s' "$gl" | grep -q -- 'scripts/ci-local.sh --shard core-docker' \
   && result_has "status=ok" core-docker; then
  pass "L1 through the real ci-docker.sh the nas shard runs linux/amd64 with --shard core-docker, and reads ok"
else
  fail "L1 real ci-docker.sh" "rc=$(rc) gl=$gl $(head -c 300 "$work/stdout")"
fi
if grep '^DOCKER_HOST=' "$l2/log/docker" | grep -qv '^DOCKER_HOST=ssh://rom@192.168.0.10 '; then
  fail "L2 every docker call is against the nas daemon" "$(grep '^DOCKER_HOST=' "$l2/log/docker" | grep -v 'rom@192.168.0.10' | head -2)"
else
  pass "L2 every docker call is made against the nas daemon, none locally"
fi
if grep -q '/home/fake/workspace/nas-work/ci-docker-' "$l2/log/rsync" && ! grep -E 'rsync .* (/home/fake|rom@192.168.0.10:/home/fake)/[a-z]' "$l2/log/rsync" | grep -v 'workspace/nas-work/ci-docker-' | grep -q .; then
  pass "L3 the tree and the git dir land only under ~/workspace/nas-work/ci-docker-*"
else
  fail "L3 scratch only" "$(cat "$l2/log/rsync" | head -4)"
fi
if grep -q -- '--exclude=config' "$l2/log/rsync" && grep -q -- '--exclude=hooks' "$l2/log/rsync" && ! grep -q -- '--exclude=refs' "$l2/log/rsync" && ! grep -q -- '--exclude=packed-refs' "$l2/log/rsync"; then
  pass "L4 refs and tags travel with the git dir; config and hooks (tokens, local hooks) do not"
else
  fail "L4 git dir shipping" "$(grep -- '--exclude' "$l2/log/rsync" | head -2)"
fi
# Only ssh and docker are ever run on the remote; no ssh command installs, builds or edits anything outside the scratch copy.
if grep -E '^ssh ' "$l2/log/ssh" | grep -Eqiw 'apt|apt-get|yum|pip|npm|brew|curl|wget|sudo'; then
  fail "N2 nothing beyond ssh and docker on the nas" "$(grep -Eiw 'apt|pip|npm|brew|curl|wget|sudo' "$l2/log/ssh" | head -2)"
else
  pass "N2 nothing beyond ssh, docker and the scratch copy runs on the nas: no package manager, no download, no sudo"
fi
run_real bash "$runner" core-docker --host mars --out "$work/out"
if [ "$(rc)" = 0 ] && grep -q 'devshell@192.168.0.100' "$l2/log/ssh" && grep -q '/work/nas-work/ci-docker-' "$l2/log/ssh"; then
  pass "L5 mars uses the devshell account and its /work/nas-work scratch parent"
else
  fail "L5 mars scratch" "rc=$(rc) $(head -3 "$l2/log/ssh")"
fi

echo
echo "run-gate-shard: $checks checks, $failures failed"
[ "$failures" = 0 ]

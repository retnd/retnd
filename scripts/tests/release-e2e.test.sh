#!/usr/bin/env bash
# Self-test for the release e2e gate (#1375): the pre-merge-commit and pre-push
# hooks, the runner, the attestation and scripts/release.sh.
#
# Everything runs in throwaway repositories with a fake e2e command
# (RELEASE_E2E_TEST_CMD), a throwaway signing key, and stand-ins for gh,
# dh.sh and ci-docker.sh. No docker, no network, no real suite.
set -uo pipefail
unset GIT_DIR GIT_INDEX_FILE GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/release-e2e-test.XXXXXX")"
case "$work" in /*/release-e2e-test.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

checks=0; failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || echo "         | $2"; }
expect() { # expect <desc> <cmd...>: passes when the command succeeds
  local d="$1"; shift
  if "$@" >/dev/null 2>&1; then pass "$d"; else fail "$d"; fi
}
expect_not() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then fail "$d"; else pass "$d"; fi; }

export GIT_AUTHOR_NAME=t GIT_AUTHOR_EMAIL=t@example.com GIT_COMMITTER_NAME=t GIT_COMMITTER_EMAIL=t@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

bin="$work/bin"; mkdir -p "$bin"
ghlog="$work/gh.log"
cat > "$bin/gh" <<'SH'
#!/usr/bin/env bash
{ echo "gh $*"; git ls-remote origin 'refs/heads/release-prep/*' | cut -f2;
  while [ $# -gt 0 ]; do [ "$1" = --body-file ] && { echo "--- body"; cat "$2"; }; shift; done; } >> "$FAKE_GH_LOG"
SH
chmod +x "$bin/gh"
export FAKE_GH_LOG="$ghlog" RELEASE_BUMP_PROVENANCE=0
# Only this script lets stamps from the test-only command count. Never set it anywhere else.
export PRE_COMMIT_LOG="$work/precommit.log"

mkkey() { ssh-keygen -q -t ed25519 -N '' -C test -f "$1" >/dev/null; }
mkkey "$work/keyA"; mkkey "$work/keyB"

n=0
# make_repo: a sandbox clone on main with the gate files, a `release` branch, hooks installed.
make_repo() {
  n=$((n + 1)); local o="$work/o$n.git" r="$work/r$n"
  git init -q --bare -b main "$o"; git clone -q "$o" "$r" 2>/dev/null; cd "$r" || exit 1
  git checkout -q -b main
  mkdir -p .husky scripts/release scripts/release-e2e distribution/packaging apps/x
  cp "$repo_root/.husky/pre-merge-commit" "$repo_root/.husky/pre-push" .husky/
  cp "$repo_root/scripts/release.sh" "$repo_root/scripts/release-e2e.sh" scripts/
  cp "$repo_root/scripts/release-e2e/lib.sh" "$repo_root/scripts/release-e2e/attest.sh" scripts/release-e2e/
  cp "$repo_root/scripts/release/gate-tree.sh" "$repo_root/scripts/release/bump-version.sh" scripts/release/
  printf '#!/bin/sh\necho "fast=${CI_LOCAL_FAST:-} skipjs=${CI_LOCAL_SKIP_JS:-}" >> "${PRE_COMMIT_LOG:-/dev/null}"\n' > .husky/pre-commit; chmod +x .husky/pre-commit
  cp "$repo_root/scripts/ci-docker.sh" scripts/; mkdir -p scripts/ci; echo "FROM scratch" > scripts/ci/Dockerfile
  mkdir -p provenance
  printf '# test signers\nretnd-release namespaces="retnd-local-gate,retnd-e2e-attest" %s\n' "$(cut -d' ' -f1,2 "$work/keyA.pub")" > provenance/allowed_signers
  printf '{\n  "image": {\n    "tag": "1.0.0",\n    "reference": "ghcr.io/x/x:1.0.0",\n    "published": true\n  }\n}\n' > distribution/packaging/canonical.json
  printf 'image: ghcr.io/x/x:1.0.0\n' > apps/x/compose.yml
  printf '## [1.1.0] - soon\n\n## [1.0.0] - old\n- first 1.0.0\n' > CHANGELOG.md
  git add -A; git commit -q -m "Prepare 1.0.0"
  git push -q origin main; git branch release; git push -q origin release
  git config core.hooksPath .husky
  RELEASE_E2E_ALLOW_TEST_STAMPS="$(cd "$(git rev-parse --git-common-dir)" && pwd -P)"; export RELEASE_E2E_ALLOW_TEST_STAMPS
  git remote set-head origin main >/dev/null 2>&1 || true
}
stamps() { ls "$(git rev-parse --git-common-dir)"/release-e2e*/*.stamp 2>/dev/null | wc -l | tr -d ' '; }
tdir() { printf '%s/release-e2e-TEST-ONLY' "$(git rev-parse --git-common-dir)"; }

echo "== runner =="
make_repo
touch stray; out="$(RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh 2>&1)"; st=$?
[ "$st" -ne 0 ] && echo "$out" | grep -q dirty && [ "$(stamps)" = 0 ] && pass "a dirty tree is refused, no stamp" || fail "dirty tree" "$out"
rm stray
RELEASE_E2E_TEST_CMD=false bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
[ "$st" -ne 0 ] && [ "$(stamps)" = 0 ] && pass "a red e2e writes no stamp and fails" || fail "red e2e"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
f="$(ls "$(tdir)"/*.stamp 2>/dev/null | head -1)"
tree="$(bash scripts/release/gate-tree.sh HEAD)"
if [ "$st" = 0 ] && [ "$(basename "$f" .stamp)" = "$tree" ] && grep -q '^status=green' "$f" && grep -q '^command=true' "$f" && grep -q '^host=' "$f" && grep -q '^time=' "$f"; then
  pass "a green e2e stamps tree, command, host and time"; else fail "green stamp" "$(cat "$f" 2>/dev/null)"; fi
expect_not "the stamp is not in the working tree" test -n "$(git status --porcelain)"

echo "== runner on a real host path (stand-in dh.sh and ci-docker.sh) =="
make_repo
cat > "$work/dh.sh" <<'SH'
#!/usr/bin/env bash
case "$1" in pick) echo "${FAKE_PICK:-mars}" ;; d) [ -z "${FAKE_DOWN:-}" ] || exit 1; echo "17179869184 8 ${FAKE_MEMLIMIT:-true} ${FAKE_SWAPLIMIT:-true} ${FAKE_CFSQUOTA:-true}" ;; esac
SH
cat > "$work/ci-docker.sh" <<'SH'
#!/usr/bin/env bash
echo "args=$* mem=$CI_DOCKER_MEMORY cpus=$CI_DOCKER_CPUS" >> "$FAKE_CI_LOG"
exit "${FAKE_CI_EXIT:-0}"
SH
export FAKE_CI_LOG="$work/ci.log"
DH_SH="$work/dh.sh" CI_DOCKER_SH="$work/ci-docker.sh" bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
if [ "$st" = 0 ] && grep -q 'args=--host devshell@192.168.0.100 --deps bash -c bash scripts/e2e/run-tests-repo-gate.sh mem=9830m cpus=5' "$FAKE_CI_LOG" && grep -q '^host=mars' "$(git rev-parse --git-common-dir)"/release-e2e/*.stamp; then
  pass "fence: 60% of RAM, no swap setting passed through, 2/3 cpus, the repo's own e2e command, host recorded"
else fail "host path" "$(cat "$FAKE_CI_LOG" 2>/dev/null)"; fi
rm -f "$(git rev-parse --git-common-dir)"/release-e2e/*.stamp
FAKE_DOWN=1 DH_SH="$work/dh.sh" CI_DOCKER_SH="$work/ci-docker.sh" bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
[ "$st" = 2 ] && [ "$(stamps)" = 0 ] && pass "docker host down: refused, no stamp" || fail "host down"
FAKE_CI_EXIT=1 DH_SH="$work/dh.sh" CI_DOCKER_SH="$work/ci-docker.sh" bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
[ "$st" = 1 ] && [ "$(stamps)" = 0 ] && pass "red remote run: no stamp" || fail "red remote"

for cap in FAKE_MEMLIMIT:MemoryLimit FAKE_SWAPLIMIT:SwapLimit FAKE_CFSQUOTA:CPUCfsQuota; do
  make_repo; : > "$FAKE_CI_LOG"
  out="$(env "${cap%%:*}=false" DH_SH="$work/dh.sh" CI_DOCKER_SH="$work/ci-docker.sh" bash scripts/release-e2e.sh 2>&1)"; st=$?
  [ "$st" != 0 ] && [ "$(stamps)" = 0 ] && [ ! -s "$FAKE_CI_LOG" ] && echo "$out" | grep -q "${cap##*:}" \
    && pass "a host without ${cap##*:} is refused by name before anything runs, no stamp" || fail "missing ${cap##*:}" "$out"
done

echo "== pre-merge-commit =="
make_repo
git checkout -q -b feat; echo f > f.txt; git add f.txt; git commit -q -m feat; git checkout -q release
before="$(git rev-parse HEAD)"
RELEASE_E2E_TEST_CMD=false git merge --no-ff feat -m "merge feat" >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(git rev-parse HEAD)" = "$before" ] && [ "$(stamps)" = 0 ] && pass "merge into release with a red e2e is blocked" || fail "red merge blocked"
git merge --abort >/dev/null 2>&1 || true
RELEASE_E2E_TEST_CMD=true git merge --no-ff feat -m "merge feat" >/dev/null 2>&1; st=$?
tree="$(bash scripts/release/gate-tree.sh HEAD)"
[ "$st" = 0 ] && [ "$(git rev-parse HEAD)" != "$before" ] && [ -f "$(tdir)/$tree.stamp" ] && pass "green merge is allowed and stamps the merged tree" || fail "green merge"
git checkout -q main; git checkout -q -b feat2; echo g > g.txt; git add g.txt; git commit -q -m feat2; git checkout -q main
RELEASE_E2E_TEST_CMD=false git merge --no-ff feat2 -m "merge feat2" >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "a merge on any other branch never runs the e2e" || fail "other branch merge"

# decoy: a failing command that prints an "exit 0" line is still red
make_repo
RELEASE_E2E_TEST_CMD='echo "suite e2e EXIT 0"; echo "EXIT 0"; false' bash scripts/release-e2e.sh >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(stamps)" = 0 ] && pass "green means the command's exit status, not 'exit 0' text in its output (decoy line)" || fail "decoy"
# the gate runs from HEAD's copy: a merged branch that swaps the runner for a green one is still blocked
make_repo
git checkout -q -b evil; printf '#!/bin/sh\nexit 0\n' > scripts/release-e2e.sh; git add -A; git commit -q -m "swap the gate"; git checkout -q release
before="$(git rev-parse HEAD)"; RELEASE_E2E_TEST_CMD=false git merge --no-ff evil -m "merge evil" >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(git rev-parse HEAD)" = "$before" ] && pass "a branch that replaces scripts/release-e2e.sh cannot bless its own merge (gate runs from HEAD's copy)" || fail "swapped gate"
git merge --abort >/dev/null 2>&1 || true
make_repo
git checkout -q -b nogate main; git rm -q -r scripts/release-e2e.sh; git commit -q -m "no runner"; git checkout -q -B release nogate
git checkout -q -b other main; echo o > o.txt; git add o.txt; git commit -q -m other; git checkout -q release
RELEASE_E2E_TEST_CMD=true git merge --no-ff other -m "m" >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "fail closed: HEAD of release without the gate scripts refuses the merge" || fail "no gate at HEAD"
git merge --abort >/dev/null 2>&1 || true

# the hooks refuse an absolute or foreign core.hooksPath, like pre-commit (#1361)
make_repo
git checkout -q -b feat-hp; echo h > h.txt; git add h.txt; git commit -q -m hp; git checkout -q main
git config core.hooksPath "$PWD/.husky"
out="$(git merge --no-ff feat-hp -m "merge hp" 2>&1)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'pre-merge-commit: refusing to run' && echo "$out" | grep -q 'core.hooksPath' && pass "pre-merge-commit refuses an absolute core.hooksPath, even off the release branch" || fail "merge hooksPath guard" "$out"
git merge --abort >/dev/null 2>&1 || true
out="$(git push origin feat-hp 2>&1)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'pre-push: refusing to run' && pass "pre-push refuses an absolute core.hooksPath" || fail "push hooksPath guard" "$out"
git config core.hooksPath .husky
git push origin feat-hp >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "(control) the relative .husky path passes the guard" || fail "hooksPath control"

# a quoted override line in a body is not an override: only real trailers count
make_repo
git checkout -q -b feat-q; echo q > q.txt; git add q.txt; git commit -q -m feat-q; git checkout -q release
before="$(git rev-parse HEAD)"
RELEASE_E2E_TEST_CMD=false git merge --no-ff feat-q -m "merge q

Quoting the mechanism:
Release-Gate-Override: this quoted line is body text, not a trailer

and a closing paragraph." >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(git rev-parse HEAD)" = "$before" ] && pass "pre-merge-commit: a quoted Release-Gate-Override line in the body does not skip the e2e" || fail "merge quoted override"
git merge --abort >/dev/null 2>&1 || true
git checkout -q release; echo q2 > q2.txt; git add q2.txt
git commit -q -m "quote" -m "Quoting the mechanism:
Release-Gate-Override: this quoted line is body text, not a trailer

and a closing paragraph."
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "pre-push: a quoted Release-Gate-Override line in the body does not skip" || fail "push quoted override"

echo "== pre-push =="
make_repo
git checkout -q release; echo a > a.txt; git add a.txt; git commit -q -m "change a"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(git ls-remote origin refs/heads/release | cut -f1)" != "$(git rev-parse HEAD)" ] && pass "push to release without a stamp is refused" || fail "no stamp push"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
echo b > b.txt; git add b.txt; git commit -q -m "change b"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "push with a stamp for a different tree is refused" || fail "other tree push"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "(control) the test script's own flag lets a test-only stamp count" || fail "test flag"
echo e > e.txt; git add e.txt; git commit -q -m "change e"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
env -u RELEASE_E2E_ALLOW_TEST_STAMPS RELEASE_E2E_TEST_CMD=true git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(stamps)" -ge 1 ] && pass "RELEASE_E2E_TEST_CMD set, run, then a push to release is refused (test stamps live in a dir the hook never reads)" || fail "test stamp honoured"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && [ "$(git ls-remote origin refs/heads/release | cut -f1)" = "$(git rev-parse HEAD)" ] && pass "push with a stamp for the exact tree passes" || fail "matching stamp push"
git checkout -q release; echo sp > sp.txt; git add sp.txt; git commit -q -m "evil commit"; before_sp="$(git ls-remote origin refs/heads/release | cut -f1)"
git push origin 'HEAD^{/evil commit}:refs/heads/release' >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && [ "$(git ls-remote origin refs/heads/release | cut -f1)" = "$before_sp" ] && pass "a local ref with spaces cannot hide the push to release (fields parsed from the end)" || fail "spaces in the local ref" "pushed with no stamp"
git reset -q --hard HEAD~1
git checkout -q -b feat3; echo c > c.txt; git add c.txt; git commit -q -m c
git push origin feat3 >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "push to a feature branch passes straight through" || fail "feature push"
git checkout -q release; echo d > d.txt; git add d.txt; git commit -q -m "d" -m "Release-Gate-Override: short"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "an override reason under 12 characters is refused" || fail "short override"
git commit -q --amend -m "d" -m "Release-Gate-Override: hotfix, owner approved"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && grep -q 'hotfix' "$(git rev-parse --git-common-dir)/release-e2e/overrides.log" && pass "the existing override trailer passes and is logged" || fail "long override"

echo "== pre-push: stamp validation, the remote's gate, bootstrap =="
# mkstamp <file> <tree> [key=value overrides...]: a stamp as the runner writes it on a real host
mkstamp() {
  local f="$1" t="$2"; shift 2
  local g; g="$(export RE_TOOLS="$PWD"; . scripts/release-e2e/lib.sh; re_gate_hash)"
  { echo schema=1; echo status=green; echo "tree=$t"; echo "commit=$(git rev-parse HEAD)"; echo "command=bash scripts/e2e/run-tests-repo-gate.sh"
    echo host=mars; echo time=2026-11-12T00:00:00Z; echo "gate_sha256=$g"; } > "$f"
  for kv in "$@"; do
    case "$kv" in
      drop:*) grep -v "^${kv#drop:}=" "$f" > "$f.n"; mv "$f.n" "$f" ;;
      add:*) echo "${kv#add:}" >> "$f" ;;
      *) k="${kv%%=*}"; grep -v "^$k=" "$f" > "$f.n"; echo "$kv" >> "$f.n"; mv "$f.n" "$f" ;;
    esac
  done
}
make_repo; git checkout -q release; echo v > v.txt; git add v.txt; git commit -q -m "valid stamp case"
tree="$(bash scripts/release/gate-tree.sh HEAD)"; sd="$(git rev-parse --git-common-dir)/release-e2e"; mkdir -p "$sd"
unset RELEASE_E2E_ALLOW_TEST_STAMPS
try() { # <desc> <expect pass|refuse> <mkstamp args...>
  local d="$1" want="$2"; shift 2
  rm -f "$sd"/*.stamp; mkstamp "$sd/$tree.stamp" "$tree" "$@" 2>/dev/null; [ "$want" != empty ] || : > "$sd/$tree.stamp"
  git push origin release >/dev/null 2>&1; st=$?
  if { [ "$want" = pass ] && [ "$st" = 0 ]; } || { [ "$want" != pass ] && [ "$st" != 0 ]; }; then pass "$d"; else fail "$d" "push status $st"; fi
  if [ "$st" = 0 ]; then git reset -q --hard HEAD; RELEASE_E2E_SKIP='test reset of the remote tip' git push -q -f origin "$(git rev-parse HEAD~1)":refs/heads/release 2>/dev/null; fi
}
try "a stamp as the runner writes it (trusted command, real host, gate hash) is accepted" pass
try "an empty stamp file is refused" empty
try "a stamp with an extra key is refused" refuse add:evil=1
try "a stamp missing a key is refused" refuse drop:time
try "a stamp whose tree= differs from its filename is refused" refuse tree=0000000000000000000000000000000000000001
try "a stamp with another command is refused" refuse "command=true"
try "a stamp from a host outside the allowlist is refused" refuse host=laptop
try "a multi-host stamp of real hosts joined with + is accepted" pass host=mars+nas
try "a stamp bound to a different gate (gate_sha256) is refused" refuse gate_sha256=deadbeef
try "a stamp that says red is refused" refuse status=red
rm -f "$sd"/*.stamp; mkstamp "$(re_dir=$(git rev-parse --git-common-dir); mkdir -p "$re_dir/release-e2e-TEST-ONLY"; echo "$re_dir/release-e2e-TEST-ONLY")/$tree.stamp" "$tree" host=test-override
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "a TEST-ONLY stamp is never counted without the test script's flag" || fail "test-only counted"

# the remote's gate judges, not the pushed tree's: a push that edits the gate is refused unless logged
make_repo; git checkout -q release
printf '\n# weaker\n' >> scripts/release-e2e/lib.sh; git add -A; git commit -q -m "edit the gate"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "a push that changes a gate file is refused even with a (test) stamp" || fail "gate edit pushed"
git commit -q --amend -m "edit the gate" -m "Release-Gate-Override: owner approved gate change"
git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && grep -q 'owner approved gate change' "$(git rev-parse --git-common-dir)/release-e2e/overrides.log" && pass "a gate change passes only with the logged Release-Gate-Override trailer" || fail "gate change override"

make_repo; git checkout -q release; mkdir -p release-attest; echo x > release-attest/e2e.json; echo y > release-attest/notes.txt; git add -A; git commit -q -m "attest extras"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "release-attest/ may hold only e2e.json and its signature" || fail "attest extras"

# bootstrap: no release branch on the remote
make_repo; RELEASE_E2E_SKIP='test setup: delete release' git push -q origin :refs/heads/release 2>/dev/null
git checkout -q release; echo b > b.txt; git add b.txt; git commit -q -m "b"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
git push origin release >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "the bootstrap (remote has no release) is refused even with a stamp" || fail "bootstrap pushed"
RELEASE_E2E_SKIP='one-time bootstrap of the release branch' git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && grep -q 'one-time bootstrap' "$(git rev-parse --git-common-dir)/release-e2e/overrides.log" && pass "the bootstrap passes with a logged skip" || fail "bootstrap skip"

# remote tip not in this clone: fetch first
make_repo; git clone -q "$work/o$n.git" "$work/other$n" 2>/dev/null
( cd "$work/other$n" && git config user.name t && git config user.email t@e.com && git checkout -q release && echo z > z.txt && git add z.txt && git commit -q -m z && RELEASE_E2E_SKIP='test: advance the remote tip' git push -q origin release ) >/dev/null 2>&1
git checkout -q release; echo m > m.txt; git add m.txt; git commit -q -m m
out="$(git push origin release 2>&1)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -qi 'fetch first' && pass "a remote tip missing locally is refused with 'fetch first'" || fail "fetch first" "$out"

echo "== hooks: attribute-proof copies, inline skip record, one release line =="
# export-ignore on a gate file in the tip must not make the gate vanish (git archive would drop it)
make_repo; git checkout -q main
printf 'scripts/release-e2e/attest.sh export-ignore\nscripts/ci-docker.sh export-ignore\n' > .gitattributes; git add .gitattributes; git commit -q -m "attrs"
git push -q origin main; RELEASE_E2E_SKIP='test setup: attributes on release' git push -q origin main:refs/heads/release 2>/dev/null; git checkout -q release; git reset -q --hard origin/release
echo ea > ea.txt; git add ea.txt; git commit -q -m "ea"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
git push origin release >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "pre-push: export-ignore in the tip's .gitattributes does not drop gate files (blobs are read raw)" || fail "export-ignore pre-push"
git checkout -q -b feat-ea main; echo m > m.txt; git add m.txt; git commit -q -m m; git checkout -q release
RELEASE_E2E_TEST_CMD=true git merge --no-ff feat-ea -m "merge" >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "pre-merge-commit: export-ignore in HEAD's .gitattributes does not drop gate files" || fail "export-ignore pre-merge"
git merge --abort >/dev/null 2>&1 || true

# a topic that neuters the log function in lib.sh must not get a skip through unlogged
make_repo; git checkout -q release
printf 're_stamp_ok() { return 0; }\nre_log_override() { return 0; }\n' >> scripts/release-e2e/lib.sh
git add -A; git commit -q -m "neuter the log" -m "Release-Gate-Override: gate change, owner approved"
git push origin release >/dev/null 2>&1; st=$?
log="$(git rev-parse --git-common-dir)/release-e2e/overrides.log"
if [ "$st" = 0 ] && grep -q 'reason=gate change, owner approved' "$log" && grep -q 'sha=' "$log" && grep -q 'old_tip=' "$log" && grep -q 'ref=refs/heads/release' "$log" && grep -q 'waived=' "$log" && grep -q 'user=' "$log" && grep -q 'branch=' "$log" && grep -q 'time=' "$log"; then
  pass "pre-push: the skip record is written by the hook itself, with every field, whatever the tree's lib.sh does"
else fail "inline skip record (pre-push)" "$(cat "$log" 2>/dev/null)"; fi
# the same for the merge hook, with a HEAD that has no gate at all
make_repo; git checkout -q -b nogate2 main; git rm -q -r scripts/release-e2e.sh; git commit -q -m "no runner"; git checkout -q -B release nogate2
git checkout -q -b other2 main; echo o > o.txt; git add o.txt; git commit -q -m other; git checkout -q release
out="$(git merge --no-ff other2 -m "m" 2>&1)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'RELEASE_E2E_SKIP' && pass "pre-merge-commit: HEAD without the gate is refused and the message names the skip" || fail "no gate message" "$out"
git merge --abort >/dev/null 2>&1 || true
RELEASE_E2E_SKIP='bootstrap merge, existing release has no gate' git merge --no-ff other2 -m "m" >/dev/null 2>&1; st=$?
log="$(git rev-parse --git-common-dir)/release-e2e/overrides.log"
[ "$st" = 0 ] && grep -q 'hook=pre-merge-commit' "$log" && grep -q 'waived=' "$log" && pass "pre-merge-commit: the skip lets it through and the hook records it inline" || fail "merge skip record" "$(cat "$log" 2>/dev/null)"

# more than one stdin line naming refs/heads/release: refuse (a planted line ahead of the real one)
make_repo; git checkout -q release
z=0000000000000000000000000000000000000000; h="$(git rev-parse HEAD)"
printf 'a %s refs/heads/release %s\nb %s refs/heads/release %s\n' "$z" "$z" "$h" "$h" | sh .husky/pre-push origin url >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "two stdin lines naming refs/heads/release are refused" || fail "planted line"
RELEASE_E2E_SKIP='a skip must not hide a planted line' sh -c "printf 'a %s refs/heads/release %s\nb %s refs/heads/release %s\n' $z $z $h $h | sh .husky/pre-push origin url" >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "...and a skip does not lift that" || fail "skip lifted planted line"
grep -v '^ *#' .husky/pre-push .husky/pre-merge-commit | grep -q 'GIT_COMMON_DIR' && pass "the hooks unset GIT_COMMON_DIR" || fail "GIT_COMMON_DIR"
! grep -v '^ *#' .husky/pre-push .husky/pre-merge-commit scripts/release-e2e.sh scripts/release.sh | grep -qE '(^|[;&| ])exec ' && pass "no hook execs a script that needs a cleanup trap afterwards" || fail "exec used"

# the injected-line attack: a fake release line naming a local-only commit F whose gate is `exit 0`
make_repo; git checkout -q release
h="$(git rev-parse HEAD)"; echo real > real.txt; git add real.txt; git commit -q -m "real E"; E="$(git rev-parse HEAD)"
git checkout -q -b fake "$h"; printf '#!/bin/sh\nexit 0\n' > scripts/release-e2e.sh; git commit -q -am "fake F"; F="$(git rev-parse HEAD)"; git checkout -q release
before="$(git ls-remote origin refs/heads/release | cut -f1)"
printf 'HEAD^{/evil\nlocal %s refs/heads/release %s}  %s refs/heads/release %s\n' "$F" "$F" "$E" "$before" | sh .husky/pre-push origin url >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "a line with fewer than four fields (fake line before the real one) is refused" || fail "fake before"
printf '%s %s refs/heads/release %s\nHEAD^{/evil\nlocal %s refs/heads/release %s}\n' "refs/heads/release" "$E" "$before" "$F" "$F" | sh .husky/pre-push origin url >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "...and after it" || fail "fake after"
printf 'a %s refs/heads/release %s\n' "$E" "$before" | RELEASE_E2E_SKIP='a skip must not lift malformed lines' sh -c "printf 'x\\ny %s refs/heads/release %s\\n' $F $F | sh .husky/pre-push origin url" >/dev/null 2>&1; st=$?
[ "$st" != 0 ] && pass "a skip does not lift a malformed line" || fail "skip lifted malformed"
git push origin "$(printf 'HEAD^{/evil\nlocal %s refs/heads/release %s}' "$F" "$F"):refs/heads/release" >/dev/null 2>&1
[ "$(git ls-remote origin refs/heads/release | cut -f1)" = "$before" ] && pass "the real git push with that refspec leaves the remote release untouched" || fail "real push moved release"

# a reason with newlines and escapes cannot forge record lines
make_repo; git checkout -q release; echo r > r.txt; git add r.txt; git commit -q -m r
log="$(git rev-parse --git-common-dir)/release-e2e/overrides.log"
RELEASE_E2E_SKIP="$(printf 'long enough reason\ntime=x hook=forged waived=none user=root\033[2J')" git push origin release >/dev/null 2>&1; st=$?
if [ "$st" = 0 ] && [ "$(wc -l < "$log" | tr -d ' ')" = 1 ] && ! grep -q "$(printf '\033')" "$log" && [ "$(grep -o ' user=' "$log" | wc -l | tr -d ' ')" = 2 ]; then
  pass "a skip reason with newlines and escape characters stays one flat record line"
else fail "forged record" "$(cat -v "$log")"; fi
git commit -q --amend -m r -m "Release-Gate-Override: trailer reason with	tab and $(printf '\r')carriage"
RELEASE_E2E_SKIP="" git push -f origin release >/dev/null 2>&1
[ "$(wc -l < "$log" | tr -d ' ')" = 2 ] && ! grep -q "$(printf '\r')" "$log" && pass "the same for the override trailer text" || fail "trailer flatten" "$(cat -v "$log")"

echo "== attestation =="
make_repo
sed -i.bak 's/"1.0.0"/"1.1.0"/' distribution/packaging/canonical.json; rm distribution/packaging/canonical.json.bak
git add -A; git commit -q -m "Release v1.1.0"
export RELEASE_ATTEST_KEY="$work/keyA"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e/attest.sh sign --version 1.1.0 >/dev/null 2>&1 && bash scripts/release-e2e/attest.sh verify --expect-version 1.1.0 >/dev/null 2>&1 && pass "sign then verify (working tree) passes" || fail "sign/verify"
git add release-attest; git commit -q -m attest
expect "verify of the committed attestation passes (tree ignores release-attest/)" bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.1.0
expect_not "verify fails on an expected-version mismatch" bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.2.0
sed -i.bak 's/"version": "1.1.0"/"version": "1.2.0"/' release-attest/e2e.json; rm release-attest/e2e.json.bak
expect_not "a tampered attestation fails the signature" bash scripts/release-e2e/attest.sh verify
git checkout -q -- release-attest
echo x > code.txt; git add code.txt; git commit -q -m "code after the run"
expect_not "code changed after the run: the tree no longer matches" bash scripts/release-e2e/attest.sh verify --commit HEAD
git reset -q --hard HEAD~1
sed -i.bak 's/"tag": "1.1.0"/"tag": "1.3.0"/' distribution/packaging/canonical.json; rm distribution/packaging/canonical.json.bak; git commit -q -am "manifest drift"
expect_not "an attestation that disagrees with canonical.json fails" bash scripts/release-e2e/attest.sh verify --commit HEAD
git reset -q --hard HEAD~1
printf '# no key\n' > provenance/allowed_signers; git commit -q -am "no key"
expect_not "an allowed_signers with no key fails closed" env RETND_TRUST_REF=HEAD bash scripts/release-e2e/attest.sh verify --commit HEAD
git reset -q --hard HEAD~1
echo y > late.txt; git add late.txt; git commit -q -m "late change"
expect_not "sign refuses when the code changed after the e2e (stamp and attested tree must be the same tree)" env RELEASE_ATTEST_KEY="$work/keyA" bash scripts/release-e2e/attest.sh sign --version 1.1.0
git reset -q --hard HEAD~1
expect_not "signing without the key file stops" env RELEASE_ATTEST_KEY="$work/nokey" bash scripts/release-e2e/attest.sh sign --version 1.1.0
unset RELEASE_ATTEST_KEY

# one trust file: the key is the one in provenance/allowed_signers for the e2e namespace
printf 'retnd-release namespaces="retnd-local-gate" %s\n' "$(cut -d' ' -f1,2 "$work/keyA.pub")" > provenance/allowed_signers; git commit -q -am "key not trusted for the e2e namespace"
expect_not "a key line that does not list retnd-e2e-attest does not verify an attestation" env RETND_TRUST_REF=HEAD bash scripts/release-e2e/attest.sh verify --commit HEAD
git reset -q --hard HEAD~1
[ ! -e scripts/release-e2e/release-attest.pub ] && pass "scripts/release-e2e/release-attest.pub is gone, provenance/allowed_signers is the one trust file" || fail "the .pub is still around"

# the trust anchor: a key swapped in the same commit as an attestation it signed
git checkout -q -b swap
printf '# swapped\nretnd-release namespaces="retnd-local-gate,retnd-e2e-attest" %s\n' "$(cut -d' ' -f1,2 "$work/keyB.pub")" > provenance/allowed_signers
git commit -q -am "swap the signers"
RELEASE_E2E_TEST_CMD=true bash scripts/release-e2e.sh >/dev/null 2>&1
RELEASE_E2E_TEST_CMD=true RELEASE_ATTEST_KEY="$work/keyB" bash scripts/release-e2e/attest.sh sign --version 1.1.0 >/dev/null 2>&1
git add release-attest; git commit -q -m "attest with the swapped key"
out="$(bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.1.0 2>&1)"; st=$?
[ "$st" = 1 ] && echo "$out" | grep -q 'differs from' && echo "$out" | grep -q 'origin/main' && pass "a forged attestation plus a swapped allowed_signers in the same commit is refused" || fail "swapped signers" "$out"
git commit -q --amend -m "attest with the swapped key" -m "Release-Gate-Override: rotating the release key, reviewed on the PR"
bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.1.0 >/dev/null 2>&1; st=$?
[ "$st" = 0 ] && pass "the Release-Gate-Override trailer is the way past a changed allowed_signers" || fail "swap override"
git commit -q --amend -m "attest with the swapped key"
out="$(env RETND_TRUST_REF=refs/nope bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.1.0 2>&1)"; st=$?
[ "$st" = 1 ] && echo "$out" | grep -q 'trust reference' && pass "no trust reference fails closed" || fail "no trust ref" "$out"
git checkout -q main; git branch -D swap >/dev/null 2>&1

# gate-tree leaves out the two exact attestation paths, not the directory
git checkout -q -b exact
t0="$(bash scripts/release/gate-tree.sh HEAD)"
mkdir -p release-attest; echo '{}' > release-attest/e2e.json; echo sig > release-attest/e2e.json.sig; git add release-attest; git commit -q -m "attestation files"
t1="$(bash scripts/release/gate-tree.sh HEAD)"
echo planted > release-attest/notes.txt; git add release-attest; git commit -q -m "a third file"
t2="$(bash scripts/release/gate-tree.sh HEAD)"
[ "$t0" = "$t1" ] && [ "$t1" != "$t2" ] && pass "gate-tree leaves out release-attest/e2e.json and its .sig, and a third file under release-attest/ moves the hash" || fail "exact paths" "$t0 $t1 $t2"
git checkout -q main; git branch -D exact >/dev/null 2>&1

echo "== release.sh =="
make_repo
r() { RELEASE_E2E_TEST_CMD=true RELEASE_ATTEST_KEY="$work/keyA" PATH="$bin:$PATH" bash scripts/release.sh "$@" 2>&1; }
for bad in 1.2 v1.2.3 1.2.3.4 1.2.3- abc ""; do
  out="$(r "$bad" --issue 7 --dry-run)"; st=$?
  [ "$st" != 0 ] && pass "refuses version [$bad]" || fail "bad version [$bad]" "$out"
done
for old in 1.0.0 0.9.0; do   # 1.0.0 is published in this repo, so "already at" does not apply
  out="$(r "$old" --issue 7 --dry-run)"; st=$?
  [ "$st" != 0 ] && echo "$out" | grep -q 'not greater' && pass "refuses non-greater $old" || fail "non-greater $old" "$out"
done
out="$(r 0.9.0 --issue 7 --dry-run --allow-downgrade)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'CHANGELOG' && pass "--allow-downgrade gets past the version check" || fail "allow-downgrade" "$out"
git tag v1.1.0; out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'already exists locally' && pass "refuses a tag that exists locally" || fail "local tag" "$out"
git tag -d v1.1.0 >/dev/null; git tag v1.1.0; git push -q origin v1.1.0; git tag -d v1.1.0 >/dev/null
out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'on origin' && pass "refuses a tag that exists on the remote" || fail "remote tag" "$out"
git push -q origin :refs/tags/v1.1.0
out="$(RELEASE_E2E_TEST_CMD=true RELEASE_ATTEST_KEY="$work/nokey" PATH="$bin:$PATH" bash scripts/release.sh 1.1.0 --issue 7 2>&1)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'key' && [ ! -f "$ghlog" ] && [ "$(git branch --list 'release-prep/*' | wc -l | tr -d ' ')" = 0 ] && pass "stops on a missing key before any branch, e2e or PR" || fail "missing key" "$out"

out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?
[ "$st" = 0 ] && echo "$out" | grep -q 'branch release-prep/v1.1.0' && pass "the prep branch is exactly release-prep/v<version>" || fail "prep branch name" "$out"
make_repo; git branch -D release >/dev/null; RELEASE_E2E_SKIP='test setup: delete release' git push -q origin :refs/heads/release 2>/dev/null
git branch release/old; out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'release-prep/old' && pass "a local release/... branch is refused, message names the rename" || fail "local release/ clash" "$out"
git branch -D release/old >/dev/null
git push -q origin HEAD:refs/heads/release/x; out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'release-prep/x' && pass "a remote release/... branch is refused too" || fail "remote release/ clash" "$out"
git push -q origin :refs/heads/release/x
make_repo

snap() { { git rev-parse HEAD; git branch -a; git status --porcelain; git ls-remote origin; ls "$(git rev-parse --git-common-dir)/release-e2e" 2>/dev/null; ls release-attest 2>&1; } | cat; }
s1="$(snap)"; out="$(r 1.1.0 --issue 7 --dry-run)"; st=$?; s2="$(snap)"
[ "$st" = 0 ] && [ "$s1" = "$s2" ] && [ ! -f "$ghlog" ] && echo "$out" | grep -q 'v1.1.0' && pass "--dry-run prints the steps and changes nothing" || fail "dry run" "$out"

rm -f "$ghlog"; make_repo
out="$(RELEASE_E2E_TEST_CMD=false RELEASE_ATTEST_KEY="$work/keyA" PATH="$bin:$PATH" bash scripts/release.sh 1.1.0 --issue 7 --yes 2>&1)"; st=$?
[ "$st" != 0 ] && [ ! -f "$ghlog" ] && [ -z "$(git ls-remote origin 'refs/heads/release-prep/*')" ] && git log -1 --format=%s | grep -qx 'Release v1.1.0' && pass "stops on a red e2e: bump commit is local, no push, no PR" || fail "red e2e" "$out"

rm -f "$ghlog"; make_repo
out="$(RELEASE_E2E_TEST_CMD=true RELEASE_ATTEST_KEY="$work/keyB" PATH="$bin:$PATH" bash scripts/release.sh 1.1.0 --issue 7 --yes 2>&1)"; st=$?
[ "$st" != 0 ] && [ ! -f "$ghlog" ] && [ -z "$(git ls-remote origin 'refs/heads/release-prep/*')" ] && echo "$out" | grep -q INVALID && pass "stops when verify fails (wrong key): no push, no PR" || fail "verify fails" "$out"
git checkout -q -- release-attest 2>/dev/null; rm -rf release-attest

rm -f "$ghlog"; make_repo
out="$(printf 'n\n' | RELEASE_E2E_TEST_CMD=true RELEASE_ATTEST_KEY="$work/keyA" PATH="$bin:$PATH" bash scripts/release.sh 1.1.0 --issue 7 2>&1)"; st=$?
[ "$st" != 0 ] && [ ! -f "$ghlog" ] && [ -z "$(git ls-remote origin 'refs/heads/release-prep/*')" ] && pass "answering no at the confirmation stops before the push" || fail "confirm no" "$out"

rm -f "$ghlog"; make_repo
rm -f "$ghlog"; make_repo; : > "$PRE_COMMIT_LOG"
out="$(r 1.1.0 --issue 7 --yes)"; st=$?
if [ "$st" = 0 ] && [ "$(grep -c '^gh pr create' "$ghlog")" = 1 ] \
   && grep -q '^gh pr create --base release --head release-prep/v1.1.0 --title Release v1.1.0' "$ghlog" \
   && grep -q '^refs/heads/release-prep/v1.1.0' "$ghlog" && tail -1 "$ghlog" | grep -qx 'Closes #7'; then
  pass "all green: one PR 'Release v1.1.0' into release, branch pushed before the PR, body closes the issue"
else fail "green release" "$out"; fi
grep -c 'fast= skipjs=' "$PRE_COMMIT_LOG" | grep -qx 2 && ! grep -v '^ *#' "$repo_root/scripts/release.sh" | grep -q -e '--no-verify' -e 'CI_LOCAL_' && pass "both release commits went through the pre-commit hook plainly: no CI_LOCAL_* variable, no --no-verify" || fail "commit env" "$(cat "$PRE_COMMIT_LOG")"
[ -z "$(git tag)" ] && [ -z "$(git ls-remote --tags origin)" ] && pass "no tag was created locally or on the remote" || fail "tag created"
if git show HEAD~1:distribution/packaging/canonical.json | grep -q '"tag": "1.1.0"' && git show HEAD~1:apps/x/compose.yml | grep -q ':1.1.0' \
   && git show HEAD~1:distribution/packaging/canonical.json | grep -q '"published": false' \
   && [ "$(git log --format=%s -3 | head -2 | tr '\n' '|')" = "Add the signed e2e attestation for v1.1.0|Release v1.1.0|" ]; then
  pass "one bump commit 'Release v1.1.0' sits before the attestation commit; canonical.json and the compose file moved"
else fail "bump commit" "$(git log --format=%s -3)"; fi
left="$(git grep -n '1\.0\.0' -- . ':!CHANGELOG.md' | grep -v 'Prepare' || true)"
[ -z "$left" ] && pass "after the bump no file but CHANGELOG.md still names 1.0.0" || fail "leftover old version" "$left"
expect "the pushed attestation verifies against the version" bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.1.0

# "prepared on main": the version is already set and has a changelog section, so
# there is no bump commit and the release tree is main's tree.
prepared_repo() {
  make_repo
  sed -i.bak 's/"published": true/"published": false/' distribution/packaging/canonical.json; rm distribution/packaging/canonical.json.bak
  git commit -q -am "Prepare 1.0.0 for release"; git push -q origin main
}
prepared_repo
out="$(r 1.0.0 --issue 7 --dry-run)"; st=$?
[ "$st" = 0 ] && echo "$out" | grep -q 'prepared on main' && pass "a version that is already set (unpublished, with a changelog section) passes the version check as 'prepared on main'" || fail "prepared dry run" "$out"
out="$(r 0.9.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'not greater' && pass "an older version with no bump is still refused" || fail "older refused" "$out"
rm -f "$ghlog"; prepared_repo; main_tree="$(bash scripts/release/gate-tree.sh HEAD)"; main_sha="$(git rev-parse HEAD)"
out="$(r 1.0.0 --issue 7 --yes)"; st=$?
if [ "$st" = 0 ] && [ "$(git rev-parse HEAD~1)" = "$main_sha" ] && [ "$(git log --format=%s -1)" = "Add the signed e2e attestation for v1.0.0" ] \
   && [ "$(bash scripts/release/gate-tree.sh HEAD)" = "$main_tree" ] && ! git log --format=%s "$main_sha"..HEAD | grep -q '^Release v'; then
  pass "prepared on main: the prep branch is main's tip plus the attestation only, and its gate tree is main's"
else fail "prepared full run" "$out"; fi
expect "the prepared attestation verifies against the version" bash scripts/release-e2e/attest.sh verify --commit HEAD --expect-version 1.0.0
# a published version cannot be cut again
make_repo; out="$(r 1.0.0 --issue 7 --dry-run)"; st=$?
[ "$st" != 0 ] && echo "$out" | grep -q 'not greater' && pass "a published version is not prepared on main, it is refused" || fail "published refused" "$out"

echo
echo "release-e2e.test.sh: $((checks - failures))/$checks passed"
[ "$failures" = 0 ]

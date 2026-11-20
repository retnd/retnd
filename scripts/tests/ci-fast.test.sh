#!/usr/bin/env bash
# Self-test for the fast tier, the hook that runs it and the pre-merge
# check (#1361).
#
# Every case builds a throwaway repository that carries the REAL
# scripts/ci-fast.sh, scripts/merge-gate.sh, scripts/lib/ci-local-gate.sh
# and the two hooks, with stubs for everything they call (go, gofmt,
# golangci-lint, docker, npm, ruff, gh and the two check scripts). The stubs
# write what they were asked to a call log, so what a case measures is
# WHICH checks the tier chose to run for a set of changed paths, never the
# checks themselves. Those are measured where they live.
#
#   Group A  ci-fast.sh: which paths pick which checks
#   Group B  .husky/pre-commit and prepare-commit-msg, through real commits
#   Group C  scripts/merge-gate.sh against a bare "remote" and a stub gh
#   Group D  the live-issue test stands down under CI_LOCAL_TIER=fast
#   Group E  the queue (#1544): one fast tier at a time across worktrees,
#            the load wait, nice/GOMAXPROCS/-p 2, the stall and cap kills,
#            and waiting for the full gate's Docker lock outside it (#1560)
#
# Every negative assertion has a positive control next to it: "go test was
# not run for ./internal/b" is only worth anything beside "go test was run
# for ./internal/a" in the same run.
set -uo pipefail

SCRIPTS_DIR="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$SCRIPTS_DIR/.." && pwd)"

SANDBOX="$REPO_ROOT/.ci-fast-test"
case "$SANDBOX" in
  /*/.ci-fast-test) ;;
  *) echo "refusing to use sandbox path [$SANDBOX]" >&2; exit 1 ;;
esac
rm -rf "$SANDBOX"
mkdir -p "$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

checks=0
failures=0
pass() { checks=$((checks + 1)); printf '    ok   %s\n' "$1"; }
fail() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  printf '    FAIL %s\n' "$1" >&2
  if [ $# -gt 1 ]; then printf '%s\n' "$2" | sed 's/^/         | /' >&2; fi
}
assert_contains() { # <label> <needle> <haystack>
  case "$3" in *"$2"*) pass "$1" ;; *) fail "$1 -- expected to contain: $2" "$3" ;; esac
}
assert_not_contains() { # <label> <needle> <haystack>
  case "$3" in *"$2"*) fail "$1 -- expected NOT to contain: $2" "$3" ;; *) pass "$1" ;; esac
}
assert_eq() { # <label> <want> <got>
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 -- want [$2], got [$3]"; fi
}
assert_nonzero() { # <label> <status>
  if [ "$2" -ne 0 ]; then pass "$1"; else fail "$1 -- expected a non-zero exit, got 0"; fi
}

# ---------------------------------------------------------------- fixture

make_repo() { # -> a repository with one commit on main, a remote, and the stubs
  # mktemp, not a counter: every caller runs this in $(...), a subshell, so a
  # counter bumped here never reaches the next call, every fixture was r1, and
  # each test inherited the last one's tree (its calls.log got committed).
  local fix
  fix="$(mktemp -d "$SANDBOX/r.XXXXXX")"
  (
    cd "$fix" || exit 1
    git init -q -b main
    git config user.email t@example.invalid
    git config user.name t

    mkdir -p scripts/lib scripts/selftest scripts/api scripts/format scripts/rename .husky bin \
      core/internal/a core/internal/b core/tests/compat core/tests/crashmatrix \
      ui/shared/src apps/common/tests docs/conformance

    cp "$REPO_ROOT/scripts/ci-fast.sh" "$REPO_ROOT/scripts/merge-gate.sh" scripts/
    cp "$REPO_ROOT/scripts/lib/ci-local-gate.sh" scripts/lib/
    cp "$REPO_ROOT/.husky/pre-commit" "$REPO_ROOT/.husky/prepare-commit-msg" .husky/
    chmod +x scripts/*.sh .husky/*

    # The two checks that always run, as stubs that log themselves.
    for s in scripts/selftest/check-anchors.sh scripts/api/check-contract-drift.sh; do
      printf '#!/usr/bin/env bash\necho "check %s" >>"$FIX_LOG"\nexit 0\n' "$(basename "$s")" >"$s"
      chmod +x "$s"
    done
    # The former-name scan (#1618): a stub that logs itself and fails when
    # STUB_BRAND_BAD=1, standing in for the git grep it is.
    cat >scripts/rename/check-no-former-name.sh <<'STUB'
#!/usr/bin/env bash
echo "check check-no-former-name.sh" >>"$FIX_LOG"
if [ "${STUB_BRAND_BAD:-0}" = 1 ]; then
  echo "check-no-former-name: FAILED: core/internal/a/a_test.go:3"
  exit 1
fi
exit 0
STUB
    chmod +x scripts/rename/check-no-former-name.sh
    # What a full run would be, for the CI_LOCAL_FULL route.
    printf '#!/usr/bin/env sh\necho "==> ci-local: ok"\nexit 0\n' >scripts/ci-local.sh
    chmod +x scripts/ci-local.sh

    printf 'module example.invalid/core\n\ngo 1.27.0\n' >core/go.mod
    printf 'package a\n' >core/internal/a/a.go
    printf 'package b\n' >core/internal/b/b.go
    printf 'package compat\n' >core/tests/compat/matrix_test.go
    printf 'package crashmatrix\n' >core/tests/crashmatrix/c.go
    printf '{"name":"shared"}\n' >ui/shared/package.json
    printf '{"name":"cpt"}\n' >apps/common/tests/package.json
    printf 'x\n' >docs/x.md

    # Stubs. They log "<dir>: <tool> <args> [tier=...]" and succeed.
    for t in go gofmt golangci-lint npm ruff shellcheck; do
      cat >"bin/$t" <<'STUB'
#!/usr/bin/env bash
tool="$(basename "$0")"
case "$tool $*" in
  "golangci-lint --version") echo "golangci-lint has version v1.0.0 built with go1.27.0"; exit 0 ;;
esac
echo "${PWD#"$FIX_ROOT"/}: $tool $* tier=${CI_LOCAL_TIER:-}" >>"$FIX_LOG"
if [ "$tool" = gofmt ] && [ "${STUB_GOFMT_BAD:-0}" = 1 ]; then echo "core/internal/a/a.go"; fi
exit 0
STUB
      chmod +x "bin/$t"
    done
    # No daemon.
    printf '#!/usr/bin/env bash\nexit 1\n' >bin/docker
    chmod +x bin/docker

    git add -A
    git -c core.hooksPath=/dev/null commit -qm base
    git config core.hooksPath .husky
    git init -q --bare "$fix.remote.git"
    git remote add origin "$fix.remote.git"
    git push -q origin main
  ) >/dev/null 2>&1
  printf '%s\n' "$fix"
}

out=""
status=0

# fx <repo> [VAR=VAL ...] -- <command...>: run a command in the fixture with
# the stubs first on PATH and every knob of the caller's scrubbed.
fx() {
  local fix="$1"
  shift
  local vars=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  : >"$fix/calls.log"
  out="$(cd "$fix" && env -u CI_LOCAL_FAST -u CI_LOCAL_SKIP_JS -u CI_LOCAL_SKIP_DOCKER \
    -u CI_LOCAL_RED -u CI_LOCAL_FULL -u CI_LOCAL_TIER -u CI_LOCAL_FAST_JS_INSTALL \
    -u GOTOOLCHAIN -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR \
    -u GOMAXPROCS -u CI_FAST_LOCK -u CI_FAST_LOCK_TIMEOUT -u CI_FAST_STALL_MIN -u CI_FAST_CAP_MIN \
    -u CI_FAST_KILL_GRACE \
    FIX_ROOT="$fix" FIX_LOG="$fix/calls.log" CI_LOCAL_LOCK_DIR="$fix/.gate-lock" \
    CI_FAST_LOAD_WAIT=0 CI_FAST_LOAD_POLL=0.2 CI_FAST_POLL=0.2 \
    PATH="$fix/bin:$PATH" ${vars[@]+"${vars[@]}"} "$@" 2>&1)"
  status=$?
}
calls() { cat "$1/calls.log" 2>/dev/null; }

# fast <repo> [VAR=VAL ...] -- <paths...>: ci-fast.sh over a list of paths.
fast() {
  local fix="$1"
  shift
  local vars=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  printf '%s\n' "$@" >"$fix/paths.txt"
  fx "$fix" ${vars[@]+"${vars[@]}"} -- bash scripts/ci-fast.sh --paths-from "$fix/paths.txt"
}

# ---------------------------------------------------- Group A: the tier

echo "==> A. ci-fast.sh: the changed paths pick the checks"

# A1: docs only, in a worktree whose JS workspaces have no node_modules.
# The control is that the always-checks ran; the point is that nothing
# about JS or Go did and the run still ended ok (the EPIC U lanes were
# refused here by the full gate's dependency preflight).
fix="$(make_repo)"
fast "$fix" -- docs/x.md
assert_eq "A1 a docs-only change passes in a worktree with no JS deps" 0 "$status"
assert_contains "A1 and says ok" 'ci-local: ok' "$out"
assert_contains "A1 the anchors check ran" 'check check-anchors.sh' "$(calls "$fix")"
assert_contains "A1 the contract drift check ran" 'check check-contract-drift.sh' "$(calls "$fix")"
assert_not_contains "A1 no Go tool ran" ': go ' "$(calls "$fix")"
assert_not_contains "A1 no JS tool ran" 'npm' "$(calls "$fix")"
assert_not_contains "A1 the JS preflight did not refuse" 'NOT installed' "$out"

# A2: one Go package. Vet, lint and test see that package and no other, and
# gofmt sees the file. B is the negative; A is the control.
fix="$(make_repo)"
fast "$fix" -- core/internal/a/a.go
assert_eq "A2 a Go-only change passes in a worktree with no JS deps" 0 "$status"
c="$(calls "$fix")"
assert_contains "A2 gofmt saw the changed file" 'gofmt -l core/internal/a/a.go' "$c"
assert_contains "A2 vet saw the touched package" 'core: go vet ./internal/a' "$c"
assert_contains "A2 lint saw the touched package" 'core: golangci-lint run' "$c"
assert_contains "A2 lint was handed the package, not the module" './internal/a' "$(printf '%s\n' "$c" | grep golangci-lint)"
assert_contains "A2 test saw the touched package" 'core: go test -p 2 -timeout 20m ./internal/a' "$c"
assert_not_contains "A2 the neighbouring package was not touched" './internal/b' "$c"
assert_not_contains "A2 nothing ran over the whole module" './...' "$c"
assert_not_contains "A2 no -race: that is the full gate's" '-race' "$c"

# A3: a testdata file belongs to its package, and two packages are two.
fix="$(make_repo)"
mkdir -p "$fix/core/internal/a/testdata"
fast "$fix" -- core/internal/a/testdata/f.json core/internal/b/b.go
c="$(calls "$fix")"
assert_contains "A3 a testdata file selects the package it sits in" './internal/a' "$c"
assert_contains "A3 the second package is selected too" './internal/b' "$c"

# A4: go.mod. The module is vetted whole, and the rest is ledgered, which
# makes the run INCOMPLETE rather than ok.
fix="$(make_repo)"
fast "$fix" -- core/go.mod
c="$(calls "$fix")"
assert_contains "A4 a go.mod change vets the whole module" 'core: go vet ./...' "$c"
assert_not_contains "A4 and runs no test" 'go test' "$c"
assert_eq "A4 and ends INCOMPLETE" 3 "$status"
assert_contains "A4 the ledger names go.mod" 'go.mod/go.sum' "$out"

# A5: the conformance matrix. The package that reads it runs, under the
# fast tier's name, which is what stands the live check down (Group D).
fix="$(make_repo)"
fast "$fix" -- docs/conformance/epic-t-matrix.md
c="$(calls "$fix")"
assert_contains "A5 a matrix change runs core/tests/compat" 'core: go test -p 2 -timeout 20m ./tests/compat tier=fast' "$c"
assert_eq "A5 and passes" 0 "$status"

# A6: a JS path in a worktree with no deps is refused, by name, with the
# fix; skipping it out loud ledgers; installed deps run that workspace only.
fix="$(make_repo)"
fast "$fix" -- ui/shared/src/x.ts
assert_nonzero "A6 a JS change with no deps is refused" "$status"
assert_contains "A6 the refusal names the workspace" 'ui/shared' "$out"
assert_contains "A6 and the command that fixes it" 'cd ui/shared && npm' "$out"
assert_not_contains "A6 and does not name the workspace nothing touched" 'apps/common/tests present but NOT' "$out"
fast "$fix" CI_LOCAL_SKIP_JS=1 -- ui/shared/src/x.ts
assert_eq "A6 CI_LOCAL_SKIP_JS=1 ends INCOMPLETE" 3 "$status"
assert_not_contains "A6 and ran no npm" 'npm' "$(calls "$fix")"
mkdir -p "$fix/ui/shared/node_modules"
fast "$fix" -- ui/shared/src/x.ts
c="$(calls "$fix")"
assert_eq "A6 installed deps run and pass" 0 "$status"
assert_contains "A6 typecheck ran in ui/shared" 'ui/shared: npm run --silent lint' "$c"
assert_contains "A6 so did the vitest suite" 'ui/shared: npm test --silent' "$c"
assert_not_contains "A6 apps/common/tests was not touched" 'apps/common/tests: npm' "$c"

# A7: gofmt finding something is a failure, and the anchors check failing
# is one too. A1 is the control for both.
fix="$(make_repo)"
fast "$fix" STUB_GOFMT_BAD=1 -- core/internal/a/a.go
assert_nonzero "A7 an unformatted file fails the run" "$status"
assert_contains "A7 and is named" 'core/internal/a/a.go' "$out"
assert_not_contains "A7 vet did not run after it" 'go vet' "$(calls "$fix")"
printf '#!/usr/bin/env bash\nexit 1\n' >"$fix/scripts/selftest/check-anchors.sh"
fast "$fix" -- docs/x.md
assert_nonzero "A7 a stale anchor fails the run" "$status"
assert_contains "A7 and the verdict says FAILED" 'ci-local: FAILED' "$out"

# A8: a Docker-backed package with no daemon is ledgered, not passed.
fix="$(make_repo)"
fast "$fix" -- core/tests/crashmatrix/c.go
assert_eq "A8 a Docker-backed package with no daemon ends INCOMPLETE" 3 "$status"
assert_contains "A8 the ledger says the daemon was never reachable" 'never reachable' "$out"

# A9: the staged route reads the index, which is what the hook uses.
fix="$(make_repo)"
(cd "$fix" && printf 'package a\n\nvar X = 1\n' >core/internal/a/a.go && git add core/internal/a/a.go)
fx "$fix" -- bash scripts/ci-fast.sh --staged
assert_contains "A9 --staged selects the staged package" 'core: go vet ./internal/a' "$(calls "$fix")"
assert_not_contains "A9 and not the unstaged one" './internal/b' "$(calls "$fix")"

# A10 (#1618): the former-name scan runs on any commit that touches a Go
# test file, and on no other. The docs-only run in A1 is the negative, the
# test-file run is the control.
fix="$(make_repo)"
fast "$fix" -- core/internal/a/a_test.go
assert_eq "A10 a test-file change passes" 0 "$status"
assert_contains "A10 the former-name scan ran" 'check check-no-former-name.sh' "$(calls "$fix")"
fast "$fix" -- core/internal/a/a.go
assert_not_contains "A10 a product-file change does not run the scan" 'check-no-former-name' "$(calls "$fix")"
fast "$fix" -- docs/x.md
assert_not_contains "A10 a docs change does not run the scan" 'check-no-former-name' "$(calls "$fix")"
fast "$fix" -- core/internal/a/a.go core/internal/b/b_test.go
assert_contains "A10 a mixed change runs it, one test file is enough" 'check check-no-former-name.sh' "$(calls "$fix")"

# A11: a hit fails the run before any Go tool starts, and is named.
fix="$(make_repo)"
fast "$fix" STUB_BRAND_BAD=1 -- core/internal/a/a_test.go
assert_nonzero "A11 a test file spelling the former name fails the run" "$status"
assert_contains "A11 and the hit is named" 'core/internal/a/a_test.go:3' "$out"
assert_contains "A11 and the verdict says FAILED" 'ci-local: FAILED' "$out"
assert_not_contains "A11 no vet ran after it" 'go vet' "$(calls "$fix")"

# ------------------------------------------- Group B: the hook, for real

echo "==> B. the hook and the stamp"

last_message() { (cd "$1" && git log -1 --format=%B); }
commit_count() { (cd "$1" && git rev-list --count HEAD); }

# B1: a commit goes through the fast tier and is stamped fast.
fix="$(make_repo)"
(cd "$fix" && printf 'more\n' >>docs/x.md && git add docs/x.md)
fx "$fix" -- git commit -qm "docs"
assert_eq "B1 a docs commit commits through the hook" 0 "$status"
assert_contains "B1 the fast tier ran" 'ci-fast: the fast tier' "$out"
assert_contains "B1 the commit is stamped fast" 'Local-Gate: fast' "$(last_message "$fix")"
assert_not_contains "B1 and not unverified" 'Local-Gate: unverified' "$(last_message "$fix")"

# B2: --no-verify is still stamped unverified (pre-commit never ran).
fix="$(make_repo)"
(cd "$fix" && printf 'more\n' >>docs/x.md && git add docs/x.md)
fx "$fix" -- git commit -qm "bypass" --no-verify
assert_contains "B2 --no-verify is stamped unverified" 'Local-Gate: unverified' "$(last_message "$fix")"
assert_not_contains "B2 and the fast tier did not run" 'ci-fast:' "$out"

# B3: red tests first still works, and is stamped red with its reason.
fix="$(make_repo)"
# On a branch: the hook refuses a red commit on main or release, which is a
# rule of its own and not this row's.
(cd "$fix" && git checkout -q -b red-tests && printf 'package a\n' >core/internal/a/a_test.go && git add core/internal/a/a_test.go)
fx "$fix" CI_LOCAL_RED="#1361: red tests for the tier" -- git commit -qm "red"
assert_eq "B3 a red commit commits" 0 "$status"
assert_contains "B3 stamped red with the reason" 'Local-Gate: red #1361: red tests for the tier' "$(last_message "$fix")"
assert_not_contains "B3 the fast tier did not run" 'ci-fast:' "$out"

# B3b (#1618): the red route skips the tier but not the former-name scan.
# This is how the name reached main: a red commit of test files, then a fix
# commit that touched other files, and nothing ran the guard in between.
fix="$(make_repo)"
(cd "$fix" && git checkout -q -b red-tests && printf 'package a\n' >core/internal/a/a_test.go && git add core/internal/a/a_test.go)
fx "$fix" CI_LOCAL_RED="#1618: red tests for the scan" STUB_BRAND_BAD=1 -- git commit -qm "red"
assert_nonzero "B3b a red commit with the former name in a test file is refused" "$status"
assert_contains "B3b and says why" 'spells the former name' "$out"
assert_eq "B3b and no commit was made" 1 "$(commit_count "$fix")"
fx "$fix" CI_LOCAL_RED="#1618: red tests for the scan" -- git commit -qm "red"
assert_eq "B3b the same red commit without the name goes through" 0 "$status"
assert_contains "B3b and the scan ran" 'check check-no-former-name.sh' "$(calls "$fix")"
assert_contains "B3b and is stamped red" 'Local-Gate: red' "$(last_message "$fix")"

# B4: CI_LOCAL_FULL=1 runs the full gate, and a green full run leaves no
# trailer (the only thing that does).
fix="$(make_repo)"
(cd "$fix" && printf 'more\n' >>docs/x.md && git add docs/x.md)
fx "$fix" CI_LOCAL_FULL=1 -- git commit -qm "full"
assert_eq "B4 a full-gate commit commits" 0 "$status"
assert_contains "B4 the full gate ran" 'ci-local: ok' "$out"
assert_not_contains "B4 and the fast tier did not" 'ci-fast:' "$out"
assert_not_contains "B4 a green full run carries no Local-Gate trailer" 'Local-Gate:' "$(last_message "$fix")"

# B5: a failing fast tier blocks the commit.
fix="$(make_repo)"
(cd "$fix" && printf 'package a\n\nvar X = 1\n' >core/internal/a/a.go && git add core/internal/a/a.go)
fx "$fix" STUB_GOFMT_BAD=1 -- git commit -qm "unformatted"
assert_nonzero "B5 a red fast tier refuses the commit" "$status"
assert_eq "B5 and no commit was made" 1 "$(commit_count "$fix")"

# B6: an absolute core.hooksPath is refused, by every route, with the fix.
# B1 is the control: the same commit goes through with the relative path.
fix="$(make_repo)"
(cd "$fix" && printf 'more\n' >>docs/x.md && git add docs/x.md && git config core.hooksPath "$fix/.husky")
fx "$fix" -- git commit -qm "absolute"
assert_nonzero "B6 an absolute hooksPath refuses the commit" "$status"
assert_contains "B6 and says why" 'absolute path' "$out"
assert_contains "B6 and names the fix" 'bash scripts/install-hooks.sh' "$out"
assert_eq "B6 and no commit was made" 1 "$(commit_count "$fix")"
(cd "$fix" && git config core.hooksPath .husky)
fx "$fix" CI_LOCAL_RED="#1361: red tests for the tier" -- sh "$fix/.husky/pre-commit"
assert_not_contains "B6 the relative path is accepted" 'refusing to run' "$out"

# B7: a relative path that resolves to some other directory, and a hook
# file that is not this checkout's own, are both refused, red route included.
fix="$(make_repo)"
(cd "$fix" && mkdir other && cp .husky/pre-commit .husky/prepare-commit-msg other/ \
  && printf 'package a\n' >core/internal/a/a_test.go && git add core/internal/a/a_test.go \
  && git config core.hooksPath other)
fx "$fix" CI_LOCAL_RED="#1361: red tests for the tier" -- git commit -qm "elsewhere"
assert_nonzero "B7 a hooksPath that is not .husky is refused" "$status"
assert_contains "B7 and says it does not resolve to .husky" "does not resolve to this checkout's .husky" "$out"
(cd "$fix" && git config core.hooksPath .husky)
mkdir -p "$SANDBOX/foreign"
cp "$fix/.husky/pre-commit" "$SANDBOX/foreign/pre-commit"
fx "$fix" CI_LOCAL_RED="#1361: red tests for the tier" -- sh "$SANDBOX/foreign/pre-commit"
assert_nonzero "B7 a hook file from outside this checkout is refused" "$status"
assert_contains "B7 and the refusal names the fix" 'bash scripts/install-hooks.sh' "$out"

# B8: a pathspec commit (git commit -- <paths>, what the shared commit helper
# does) of a file that was never `git add`ed. Git builds a temporary index
# for it and points GIT_INDEX_FILE there; the real index has none of it, so
# a tier that reads the real index scoped to 0 paths and checked nothing
# (#1554). It has to scope to the one path and run its checks, and the
# commit's index must not leak into what the checks run.
fix="$(make_repo)"
cat >"$fix/bin/go" <<'STUB'
#!/usr/bin/env bash
echo "${PWD#"$FIX_ROOT"/}: go $* tier=${CI_LOCAL_TIER:-}" >>"$FIX_LOG"
echo "go $1 index=${GIT_INDEX_FILE:-unset}" >>"$FIX_LOG"
exit 0
STUB
chmod +x "$fix/bin/go"
# The stub is committed, so the working tree differs from the commit only by
# the one path and the stamp can be fast rather than unverified.
(cd "$fix" && git add bin/go && git -c core.hooksPath=/dev/null commit -qm "index-reporting go" && printf 'package a\n\nvar X = 1\n' >core/internal/a/a.go)
fx "$fix" -- git commit -qm "pathspec" -- core/internal/a/a.go
c="$(calls "$fix")"
assert_eq "B8 a pathspec commit of an unstaged file commits" 0 "$status"
assert_contains "B8 the tier scoped to the one path" 'scoped to 1 changed path(s)' "$out"
assert_contains "B8 vet ran on its package" 'core: go vet ./internal/a' "$c"
assert_contains "B8 test ran on its package" 'core: go test' "$c"
assert_not_contains "B8 the neighbouring package was not touched" './internal/b' "$c"
assert_contains "B8 the checks never see the commit's index" 'go vet index=unset' "$c"
assert_eq "B8 the commit was made" 3 "$(commit_count "$fix")"
assert_contains "B8 and stamped fast" 'Local-Gate: fast' "$(last_message "$fix")"

# B9: whatever the tier did, a commit that changes files may not go through
# on a run that scoped to 0 paths: that run checked nothing (#1554). A fast
# tier that loses the commit's index is stood in for by a stub that always
# says 0. An empty commit scoping to 0 is fine, and is the control.
fix="$(make_repo)"
printf '#!/usr/bin/env bash\necho "==> ci-fast: the fast tier, scoped to 0 changed path(s) (staged). The full gate is scripts/ci-local.sh."\necho "==> ci-local: ok"\nexit 0\n' >"$fix/scripts/ci-fast.sh"
(cd "$fix" && git add scripts/ci-fast.sh && git -c core.hooksPath=/dev/null commit -qm "blind tier" && printf 'more\n' >>docs/x.md && git add docs/x.md)
fx "$fix" -- git commit -qm "nothing checked"
assert_nonzero "B9 a non-empty commit on a 0-path run is refused" "$status"
assert_contains "B9 and says why" 'scoped to 0 changed paths' "$out"
assert_contains "B9 and names the issue" '#1554' "$out"
assert_eq "B9 no commit was made" 2 "$(commit_count "$fix")"
(cd "$fix" && git reset -q docs/x.md && git checkout -q docs/x.md)
fx "$fix" -- git commit -q --allow-empty -m "empty"
assert_eq "B9 an empty commit on a 0-path run still commits" 0 "$status"
assert_eq "B9 and was made" 3 "$(commit_count "$fix")"

# B10: a commit whose only change is a file turned into a symlink (a type
# change, T to git) is a change like any other: the tier scopes it, so the
# B9 refusal does not fire on it.
fix="$(make_repo)"
(cd "$fix" && rm docs/x.md && ln -s other.md docs/x.md && git add docs/x.md)
fx "$fix" -- git commit -qm "type change"
assert_eq "B10 a type-change-only commit commits" 0 "$status"
assert_contains "B10 the tier scoped the changed path" 'scoped to 1 changed path(s)' "$out"
assert_not_contains "B10 and the 0-path refusal did not fire" 'scoped to 0 changed paths' "$out"

# --------------------------------------- Group C: the pre-merge check

echo "==> C. scripts/merge-gate.sh"

# A stub gh that answers the four questions merge-gate asks and logs the
# one write. STUB_HEAD is what the API claims the PR head is.
write_gh() { # <repo>
  cat >"$1/bin/gh" <<'STUB'
#!/usr/bin/env bash
case "$1 $2" in
  "auth status") exit 0 ;;
  "repo view") echo "o/r"; exit 0 ;;
  "pr view")
    q=""
    while [ $# -gt 0 ]; do if [ "$1" = "-q" ]; then q="$2"; fi; shift; done
    case "$q" in
      .state) echo OPEN ;;
      .headRefOid) echo "$STUB_HEAD" ;;
      .baseRefName) echo main ;;
    esac
    exit 0
    ;;
  "api -X") echo "gh $*" >>"$FIX_LOG"; exit 0 ;;
esac
exit 1
STUB
  chmod +x "$1/bin/gh"
}

# pr_repo <breaking> -> a fixture with a PR at refs/pull/7/head that changes
# a doc, and, when <breaking> is 1, also breaks the anchors check.
pr_repo() {
  local fix
  fix="$(make_repo)"
  (
    cd "$fix" || exit 1
    git checkout -q -b pr-branch
    printf 'pr\n' >>docs/x.md
    if [ "$1" = 1 ]; then printf '#!/usr/bin/env bash\nexit 1\n' >scripts/selftest/check-anchors.sh; fi
    git add -A
    git commit -q --no-verify -m "the pr"
    git push -q origin pr-branch:refs/pull/7/head
    git checkout -q main
  ) >/dev/null 2>&1
  # After the checkout back to main: written before, it was committed on the
  # PR branch by the add -A and gone from main's tree, so the real gh ran.
  write_gh "$fix"
  printf '%s\n' "$fix"
}
pr_head() { (cd "$1" && git rev-parse pr-branch); }

fix="$(pr_repo 0)"
head="$(pr_head "$fix")"
fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7
assert_eq "C1 a green PR exits 0" 0 "$status"
c="$(calls "$fix")"
assert_contains "C1 it posts to the PR head's commit" "repos/o/r/statuses/$head" "$c"
assert_contains "C1 as success" 'state=success' "$c"
assert_contains "C1 under the context protection requires" 'context=retnd/local-gate' "$c"
assert_contains "C1 the anchors check ran in the PR's tree" 'check check-anchors.sh' "$c"

fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7 --dry-run
assert_eq "C2 --dry-run on a green PR exits 0" 0 "$status"
assert_not_contains "C2 and posts nothing" 'statuses' "$(calls "$fix")"
assert_contains "C2 and says what it would have posted" 'dry-run' "$out"

fix="$(pr_repo 1)"
head="$(pr_head "$fix")"
fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7
assert_eq "C3 a PR that breaks a check exits 1" 1 "$status"
assert_contains "C3 and posts failure" 'state=failure' "$(calls "$fix")"
assert_not_contains "C3 and never success" 'state=success' "$(calls "$fix")"

fix="$(pr_repo 0)"
fx "$fix" STUB_HEAD="0000000000000000000000000000000000000000" -- bash scripts/merge-gate.sh 7
assert_eq "C4 a PR that moved while it was fetched is refused" 2 "$status"
assert_not_contains "C4 and nothing is posted" 'statuses' "$(calls "$fix")"

# C6/C7: the check judges the MERGE RESULT, not the head on its own. A PR
# that is green by itself must fail once the base has moved under it, whether
# the move breaks a check without touching the PR's files (C6) or conflicts
# with the PR's own edit (C7).
move_main() { # <fix> <file> <content> <break:0|1>
  (
    cd "$1" || exit 1
    printf '%s\n' "$3" >>"$2"
    if [ "$4" = 1 ]; then printf '#!/usr/bin/env bash\nexit 1\n' >scripts/selftest/check-anchors.sh; fi
    git add -A
    git commit -q --no-verify -m "main moves"
    git push -q origin main
  ) >/dev/null 2>&1
}

fix="$(pr_repo 0)"
head="$(pr_head "$fix")"
move_main "$fix" docs/other.md "main only" 1
fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7
assert_eq "C6 green on its own head, red on the merge with a moved base: exits 1" 1 "$status"
assert_contains "C6 and says it checked the merge result" 'base merged into the head' "$out"
assert_contains "C6 and posts failure on the PR head" "repos/o/r/statuses/$head" "$(calls "$fix")"
assert_not_contains "C6 and never success" 'state=success' "$(calls "$fix")"

fix="$(pr_repo 0)"
head="$(pr_head "$fix")"
move_main "$fix" docs/x.md "main edits the same spot" 0
fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7
assert_eq "C7 a base that conflicts with the PR exits 1" 1 "$status"
assert_contains "C7 and says so" 'does not merge cleanly' "$out"
assert_contains "C7 and posts failure" 'state=failure' "$(calls "$fix")"
assert_not_contains "C7 and runs no check" 'check check-anchors.sh' "$(calls "$fix")"

fix="$(pr_repo 0)"
head="$(pr_head "$fix")"
move_main "$fix" docs/other.md "main only" 0
fx "$fix" STUB_HEAD="$head" -- bash scripts/merge-gate.sh 7
assert_eq "C8 a moved base that stays green still passes" 0 "$status"
assert_contains "C8 and the anchors check ran on the merge result" 'check check-anchors.sh' "$(calls "$fix")"

# C5: the worktree is gone afterwards, whatever the outcome.
left="$(cd "$fix" && git worktree list | grep -c merge-gate || true)"
assert_eq "C5 no merge-gate worktree is left registered" 0 "$left"

# ------------------------------------------- Group E: the queue (#1544)

echo "==> E. the queue: one fast tier at a time, under nice, with caps"

# A go that says, for each call, what it ran under: its nice value, the
# GOMAXPROCS it got, and whether the fast-tier lock was held. With
# STUB_SERIAL_LOG it also writes begin/end lines around a sleep, so two runs
# can be checked for overlap. STUB_GO_HANG makes `go test` hang, silently
# or printing forever, and both hangs end on their own after about 30s so a
# tier that never kills them still finishes (slowly, and red).
write_queue_go() { # <dir>
  cat >"$1/bin/go" <<'STUB'
#!/usr/bin/env bash
echo "${PWD#"$FIX_ROOT"/}: go $* tier=${CI_LOCAL_TIER:-}" >>"$FIX_LOG"
common="$(git rev-parse --path-format=absolute --git-common-dir)"
# Held means a link that resolves to a holder's directory, not a dangling one.
if [ -L "$common/ci-fast.lock" ] && [ -d "$common/ci-fast.lock" ]; then held=held; else held=free; fi
echo "go $1 nice=$(ps -o nice= -p $$ | tr -d ' ') gomaxprocs=${GOMAXPROCS:-unset} lock=$held" >>"$FIX_LOG"
if [ -n "${STUB_SERIAL_LOG:-}" ]; then
  echo "begin $1 ${STUB_WT:-?}" >>"$STUB_SERIAL_LOG"
  sleep "${STUB_GO_SLEEP:-0}"
  echo "end $1 ${STUB_WT:-?}" >>"$STUB_SERIAL_LOG"
fi
case "${STUB_GO_HANG:-}:$1" in
  silent:test)
    echo $$ >"$FIX_ROOT/hung.pid"
    sleep 30 &
    echo $! >"$FIX_ROOT/hung.child"
    wait
    ;;
  chatty:test)
    echo $$ >"$FIX_ROOT/hung.pid"
    for _ in $(seq 1 120); do echo "still going"; sleep 0.25; done
    ;;
  hold:vet)
    echo $$ >"$FIX_ROOT/hung.pid"
    sleep 30 &
    echo $! >"$FIX_ROOT/hung.child"
    wait
    ;;
esac
# Healthy but quiet: go vet and go test say nothing until they finish, and
# a slow package can take minutes doing it.
case "$1" in vet | test) [ -z "${STUB_GO_QUIET:-}" ] || sleep "$STUB_GO_QUIET" ;; esac
exit 0
STUB
  chmod +x "$1/bin/go"
}

# A golangci-lint that is silent for STUB_LINT_QUIET seconds and then passes,
# as a cold-cache lint is.
write_quiet_lint() { # <dir>
  cat >"$1/bin/golangci-lint" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  --version) echo "golangci-lint has version v1.0.0 built with go1.27.0"; exit 0 ;;
esac
echo "${PWD#"$FIX_ROOT"/}: golangci-lint $* tier=${CI_LOCAL_TIER:-}" >>"$FIX_LOG"
sleep "${STUB_LINT_QUIET:-0}"
exit 0
STUB
  chmod +x "$1/bin/golangci-lint"
}

# An npm whose `npm test` hangs in silence, recording its pid and its child's.
write_hanging_npm() { # <dir>
  cat >"$1/bin/npm" <<'STUB'
#!/usr/bin/env bash
echo "${PWD#"$FIX_ROOT"/}: npm $* tier=${CI_LOCAL_TIER:-}" >>"$FIX_LOG"
if [ "$1" = test ]; then
  echo $$ >"$FIX_ROOT/hung.pid"
  sleep 30 &
  echo $! >"$FIX_ROOT/hung.child"
  wait
fi
exit 0
STUB
  chmod +x "$1/bin/npm"
}

# The anchors check, saying whether the lock was held when it ran.
write_queue_anchors() { # <dir>
  cat >"$1/scripts/selftest/check-anchors.sh" <<'STUB'
#!/usr/bin/env bash
common="$(git rev-parse --path-format=absolute --git-common-dir)"
if [ -L "$common/ci-fast.lock" ]; then held=held; else held=free; fi
echo "check check-anchors.sh lock=$held" >>"$FIX_LOG"
exit 0
STUB
  chmod +x "$1/scripts/selftest/check-anchors.sh"
}

# A sysctl that reports STUB_LOAD as the 1-minute load on a 10-core machine
# and hands every other question to the real one.
write_load_sysctl() { # <dir>
  cat >"$1/bin/sysctl" <<'STUB'
#!/usr/bin/env bash
case "$*" in
  "-n vm.loadavg") echo "{ ${STUB_LOAD:-0.50} 1.00 1.00 }"; exit 0 ;;
  "-n hw.ncpu") echo 10; exit 0 ;;
esac
for real in /usr/sbin/sysctl /sbin/sysctl; do
  [ -x "$real" ] && exec "$real" "$@"
done
exit 1
STUB
  chmod +x "$1/bin/sysctl"
}

# plant_holder <fix> <pid>: a lock in the fast tier's shape, held by <pid>
# on this host and boot, from some other worktree.
plant_holder() {
  local common="$1/.git" dir
  dir="$common/ci-fast.lock.$2.planted"
  mkdir -p "$dir"
  echo "$2" >"$dir/pid"
  hostname >"$dir/host"
  (. "$REPO_ROOT/scripts/lib/ci-local-gate.sh" && gate_boot_id) >"$dir/boot"
  echo "/elsewhere/other-worktree" >"$dir/where"
  ln -s "ci-fast.lock.$2.planted" "$common/ci-fast.lock"
}
lock_left() { if [ -L "$1/.git/ci-fast.lock" ] || [ -e "$1/.git/ci-fast.lock" ]; then echo held; else echo free; fi; }
alive() { if kill -0 "$1" 2>/dev/null; then echo alive; else echo gone; fi; }

# E1: the caps, the nice value and where the lock starts. The always-steps
# run before the lock is taken (they are seconds); vet and test run inside
# it, at nice 10 or more, with GOMAXPROCS=4 and go test -p 2. Afterwards the
# lock is gone.
fix="$(make_repo)"
write_queue_go "$fix"
write_queue_anchors "$fix"
fast "$fix" -- core/internal/a/a.go
c="$(calls "$fix")"
assert_eq "E1 a Go change passes" 0 "$status"
assert_contains "E1 the anchors check ran outside the lock" 'check check-anchors.sh lock=free' "$c"
assert_contains "E1 go vet ran inside the lock with GOMAXPROCS=4" 'go vet nice=' "$c"
assert_contains "E1 go vet saw GOMAXPROCS=4 and the lock held" 'gomaxprocs=4 lock=held' "$(printf '%s\n' "$c" | grep '^go vet ')"
assert_contains "E1 go test saw GOMAXPROCS=4 and the lock held" 'gomaxprocs=4 lock=held' "$(printf '%s\n' "$c" | grep '^go test ')"
assert_contains "E1 go test runs at most two test binaries at once" 'core: go test -p 2 -timeout 20m ./internal/a' "$c"
nice_seen="$(printf '%s\n' "$c" | sed -n 's/^go test nice=\([0-9-]*\) .*/\1/p' | head -1)"
if [ -n "$nice_seen" ] && [ "$nice_seen" -ge 10 ]; then
  pass "E1 go test ran at nice 10 or more (saw $nice_seen)"
else
  fail "E1 go test ran at nice 10 or more -- saw [$nice_seen]" "$c"
fi
assert_eq "E1 the lock is released when the run ends" free "$(lock_left "$fix")"

# E1b: a GOMAXPROCS the caller chose is kept.
fast "$fix" GOMAXPROCS=7 -- core/internal/a/a.go
assert_contains "E1b a caller's GOMAXPROCS wins" 'gomaxprocs=7' "$(printf '%s\n' "$(calls "$fix")" | grep '^go test ')"

# E1c: a docs-only change never queues: nothing heavy runs, so there is
# nothing to wait for.
fast "$fix" -- docs/x.md
assert_eq "E1c a docs-only change passes" 0 "$status"
assert_not_contains "E1c and never mentions the queue" 'ci-fast lock' "$out"

# E2: two runs started together from two worktrees of one clone take turns.
# The stub go sleeps a second inside each call, so overlapping runs would
# interleave their begin/end lines; serial ones write one worktree's four
# lines, then the other's.
fix="$(make_repo)"
serial="$SANDBOX/serial.$$.log"
: >"$serial"
for w in w1 w2; do
  (cd "$fix" && git worktree add -q --detach "$fix.$w" main) >/dev/null 2>&1
  write_queue_go "$fix.$w"
  (cd "$fix.$w" && printf 'package a\n\nvar X = 1\n' >core/internal/a/a.go && git add core/internal/a/a.go)
done
e2_began="$(date +%s)"
for w in w1 w2; do
  (
    fx "$fix.$w" STUB_SERIAL_LOG="$serial" STUB_GO_SLEEP=1 STUB_WT="$w" -- bash scripts/ci-fast.sh --staged
    printf '%s\n' "$out" >"$SANDBOX/e2.$w.out"
    echo "$status" >"$SANDBOX/e2.$w.status"
  ) &
done
wait
e2_took=$(( $(date +%s) - e2_began ))
assert_eq "E2 the first worktree's run passes" 0 "$(cat "$SANDBOX/e2.w1.status")"
assert_eq "E2 the second worktree's run passes" 0 "$(cat "$SANDBOX/e2.w2.status")"
assert_eq "E2 both runs called go twice each (vet, test)" 8 "$(grep -c . "$serial")"
interleaved=0
prev=""
while read -r what step wt; do
  if [ "$what" = begin ]; then
    [ -z "$prev" ] || interleaved=1
    prev="$step $wt"
  else
    [ "$prev" = "$step $wt" ] || interleaved=1
    prev=""
  fi
done <"$serial"
assert_eq "E2 no go call overlapped another" 0 "$interleaved"
assert_eq "E2 one worktree's Go section ran whole, then the other's" 2 "$(awk '{print $3}' "$serial" | uniq | wc -l | tr -d ' ')"
assert_contains "E2 the run that queued said who it was waiting for" 'ci-fast lock: waiting' "$(cat "$SANDBOX/e2.w1.out" "$SANDBOX/e2.w2.out")"
if [ "$e2_took" -ge 4 ]; then pass "E2 the two runs took at least the sum of their locked sections (${e2_took}s)"; else fail "E2 the two runs took at least 4s -- took ${e2_took}s" "$(cat "$serial")"; fi
assert_eq "E2 no lock is left behind" free "$(lock_left "$fix")"

# E3: a lock whose holder is dead is reclaimed on the way in, by name.
fix="$(make_repo)"
write_queue_go "$fix"
sleep 0 &
dead_pid=$!
wait "$dead_pid"
plant_holder "$fix" "$dead_pid"
fast "$fix" CI_FAST_LOCK_TIMEOUT=5 -- core/internal/a/a.go
assert_eq "E3 the run passes" 0 "$status"
assert_contains "E3 it says it reclaimed the dead holder's lock" "reclaiming" "$out"
assert_contains "E3 naming the worktree that left it" "/elsewhere/other-worktree" "$out"
assert_contains "E3 then ran go test under its own lock" 'lock=held' "$(printf '%s\n' "$(calls "$fix")" | grep '^go test ')"
assert_eq "E3 and left nothing behind" free "$(lock_left "$fix")"

# E4: a live holder makes a run wait, and the wait has a cap: past
# CI_FAST_LOCK_TIMEOUT it goes ahead without the lock, out loud, rather than
# blocking a commit forever. CI_FAST_LOCK=0 takes no lock and does not wait.
fix="$(make_repo)"
write_queue_go "$fix"
sleep 120 &
live_pid=$!
plant_holder "$fix" "$live_pid"
e4_began="$(date +%s)"
fast "$fix" CI_FAST_LOCK_TIMEOUT=2 -- core/internal/a/a.go
e4_took=$(( $(date +%s) - e4_began ))
assert_eq "E4 a run behind a live holder still passes" 0 "$status"
assert_contains "E4 it waited, naming the holder" "waiting" "$out"
assert_contains "E4 the holder's pid is named" "$live_pid" "$out"
assert_contains "E4 past the cap it goes ahead, and says so" "without the lock" "$out"
if [ "$e4_took" -ge 2 ]; then pass "E4 it waited out the cap (${e4_took}s)"; else fail "E4 it waited at least 2s -- took ${e4_took}s" "$out"; fi
assert_eq "E4 the live holder's lock is left alone" held "$(lock_left "$fix")"
e4b_began="$(date +%s)"
fast "$fix" CI_FAST_LOCK=0 CI_FAST_LOCK_TIMEOUT=30 -- core/internal/a/a.go
e4b_took=$(( $(date +%s) - e4b_began ))
assert_eq "E4 CI_FAST_LOCK=0 passes" 0 "$status"
assert_contains "E4 CI_FAST_LOCK=0 says the queue is off" "CI_FAST_LOCK=0" "$out"
assert_not_contains "E4 CI_FAST_LOCK=0 does not wait for the holder" "waiting" "$out"
if [ "$e4b_took" -lt 20 ]; then pass "E4 CI_FAST_LOCK=0 did not sit out the 30s cap (${e4b_took}s)"; else fail "E4 CI_FAST_LOCK=0 finished well inside 30s -- took ${e4b_took}s" "$out"; fi
assert_contains "E4 CI_FAST_LOCK=0 still runs at GOMAXPROCS=4" 'gomaxprocs=4' "$(printf '%s\n' "$(calls "$fix")" | grep '^go test ')"
kill "$live_pid" 2>/dev/null
wait "$live_pid" 2>/dev/null

# E5: the load wait. Above the core count it waits, one line to say so,
# and gives up after CI_FAST_LOAD_WAIT seconds and runs anyway. At a low
# load it says nothing and does not wait.
fix="$(make_repo)"
write_queue_go "$fix"
write_load_sysctl "$fix"
e5_began="$(date +%s)"
fast "$fix" STUB_LOAD=99.00 CI_FAST_LOAD_WAIT=2 -- core/internal/a/a.go
e5_took=$(( $(date +%s) - e5_began ))
assert_eq "E5 a run on a loaded machine still passes" 0 "$status"
assert_contains "E5 it waited for the load, and said so" "waiting for the load" "$out"
assert_contains "E5 naming the load and the core count" "99.00" "$out"
assert_contains "E5 then went ahead anyway" "going ahead" "$out"
if [ "$e5_took" -ge 2 ]; then pass "E5 it waited out CI_FAST_LOAD_WAIT (${e5_took}s)"; else fail "E5 it waited at least 2s -- took ${e5_took}s" "$out"; fi
assert_contains "E5 and go test still ran" 'go test -p 2' "$(calls "$fix")"
fast "$fix" STUB_LOAD=0.50 CI_FAST_LOAD_WAIT=2 -- core/internal/a/a.go
assert_eq "E5 a quiet machine passes" 0 "$status"
assert_not_contains "E5 and does not wait" "waiting for the load" "$out"
fast "$fix" STUB_LOAD=99.00 CI_FAST_LOAD_WAIT=30 CI_FAST_LOCK=0 -- core/internal/a/a.go
assert_not_contains "E5 CI_FAST_LOCK=0 skips the load wait too" "waiting for the load" "$out"

# E6: a step that prints nothing for CI_FAST_STALL_MIN is killed, with
# everything under it, and the run fails saying STALLED and why, on one
# exact verdict line. The lock is given back. The step is the ui/shared test
# suite: the Go steps have quiet floors of their own (E8).
fix="$(make_repo)"
write_hanging_npm "$fix"
mkdir -p "$fix/ui/shared/node_modules"
e6_began="$(date +%s)"
fast "$fix" CI_FAST_STALL_MIN=0.05 CI_FAST_KILL_GRACE=1 -- ui/shared/src/x.ts
e6_took=$(( $(date +%s) - e6_began ))
assert_nonzero "E6 a silent hang fails the run" "$status"
assert_contains "E6 the exact verdict line names the step and why" "==> ci-local: FAILED (STALLED: ui/shared tests, no output for " "$out"
assert_contains "E6 and names the knob" "(CI_FAST_STALL_MIN=0.05))" "$out"
if [ "$e6_took" -lt 20 ]; then pass "E6 it was killed well before the hang ended (${e6_took}s)"; else fail "E6 killed inside 20s -- took ${e6_took}s" "$out"; fi
assert_eq "E6 the hung npm is gone" gone "$(alive "$(cat "$fix/hung.pid" 2>/dev/null || echo 999999)")"
assert_eq "E6 and so is its child" gone "$(alive "$(cat "$fix/hung.child" 2>/dev/null || echo 999999)")"
assert_eq "E6 the lock is given back" free "$(lock_left "$fix")"

# E7: a step that keeps printing but never ends is stopped at the hard cap.
fix="$(make_repo)"
write_queue_go "$fix"
e7_began="$(date +%s)"
fast "$fix" STUB_GO_HANG=chatty CI_FAST_CAP_MIN=0.05 CI_FAST_KILL_GRACE=1 -- core/internal/a/a.go
e7_took=$(( $(date +%s) - e7_began ))
assert_nonzero "E7 a step past the hard cap fails the run" "$status"
assert_contains "E7 the verdict says STALLED" "STALLED" "$out"
assert_contains "E7 and that it hit the hard cap" "hard cap" "$out"
assert_contains "E7 and names the knob" "CI_FAST_CAP_MIN" "$out"
assert_contains "E7 the step's own output still came through" "still going" "$out"
if [ "$e7_took" -lt 20 ]; then pass "E7 it was stopped well before the step ended (${e7_took}s)"; else fail "E7 stopped inside 20s -- took ${e7_took}s" "$out"; fi
assert_eq "E7 the go that would not stop is gone" gone "$(alive "$(cat "$fix/hung.pid" 2>/dev/null || echo 999999)")"
assert_eq "E7 the lock is given back" free "$(lock_left "$fix")"

# E8: healthy steps that are quiet for longer than CI_FAST_STALL_MIN are not
# stalls. go vet and go test print nothing until a package is done, and
# golangci-lint nothing until it finishes, so they get quiet floors of their
# own; go test's sits above its -timeout, so a real hang still ends in Go's
# own timeout panic and goroutine dump rather than a kill.
fix="$(make_repo)"
write_queue_go "$fix"
write_quiet_lint "$fix"
fast "$fix" STUB_GO_QUIET=5 STUB_LINT_QUIET=5 CI_FAST_STALL_MIN=0.05 CI_FAST_KILL_GRACE=1 -- core/internal/a/a.go
assert_eq "E8 quiet-but-healthy vet, lint and test pass" 0 "$status"
assert_not_contains "E8 and nothing was called stalled" "STALLED" "$out"
assert_contains "E8 go test ran to the end" 'core: go test -p 2 -timeout 20m ./internal/a' "$(calls "$fix")"

# E9: a signal mid-step gives the lock back and stops the step's tree, and
# the verdict says which signal.
fix="$(make_repo)"
write_queue_go "$fix"
printf 'core/internal/a/a.go\n' >"$fix/paths.txt"
: >"$fix/calls.log"
(cd "$fix" && exec env -u GOMAXPROCS -u CI_FAST_LOCK FIX_ROOT="$fix" FIX_LOG="$fix/calls.log" CI_LOCAL_LOCK_DIR="$fix/.gate-lock" \
  CI_FAST_LOAD_WAIT=0 CI_FAST_POLL=0.2 CI_FAST_KILL_GRACE=1 STUB_GO_HANG=hold PATH="$fix/bin:$PATH" \
  bash scripts/ci-fast.sh --paths-from "$fix/paths.txt") >"$SANDBOX/e9.out" 2>&1 &
e9_pid=$!
for _ in $(seq 1 100); do [ -s "$fix/hung.child" ] && break; sleep 0.1; done
assert_eq "E9 the run is holding the lock mid-step" held "$(lock_left "$fix")"
kill -TERM "$e9_pid"
wait "$e9_pid"
e9_status=$?
assert_eq "E9 SIGTERM ends the run with 143" 143 "$e9_status"
assert_contains "E9 the verdict names the signal" "interrupted by SIGTERM" "$(cat "$SANDBOX/e9.out")"
assert_eq "E9 the lock is given back" free "$(lock_left "$fix")"
assert_eq "E9 the step is gone" gone "$(alive "$(cat "$fix/hung.pid" 2>/dev/null || echo 999999)")"
assert_eq "E9 and so is its child" gone "$(alive "$(cat "$fix/hung.child" 2>/dev/null || echo 999999)")"

# E10: a signal in the instant after the link is made and before the run
# notes that it holds it. The fixture's copy of the library signals the run
# from inside gate_lock_try, right after its `ln -s` succeeds. Whatever the
# timing, no link may be left pointing at nothing.
fix="$(make_repo)"
write_queue_go "$fix"
cat >>"$fix/scripts/lib/ci-local-gate.sh" <<'LIB'
eval "$(declare -f gate_lock_try | sed '1s/gate_lock_try/e10_real_gate_lock_try/')"
gate_lock_try() {
  e10_real_gate_lock_try "$@" || return 1
  kill -TERM $$
  return 0
}
LIB
fast "$fix" -- core/internal/a/a.go
assert_eq "E10 the signal ends the run with 143" 143 "$status"
assert_eq "E10 and no lock is left behind, dangling or not" free "$(lock_left "$fix")"

# E11: a link whose holder directory is gone (left by a signal in that window
# on an older build, or by a reclaimer killed between its two renames) is
# dead. It is reclaimed after a few polls, not waited out to the cap. The
# second fixture also has the killed reclaimer's renamed directory beside it.
for case in dangling reclaimer; do
  fix="$(make_repo)"
  write_queue_go "$fix"
  ln -s ci-fast.lock.4242.gone "$fix/.git/ci-fast.lock"
  if [ "$case" = reclaimer ]; then
    mkdir -p "$fix/.git/ci-fast.lock.4242.gone.reclaimed.4343"
    echo 4242 >"$fix/.git/ci-fast.lock.4242.gone.reclaimed.4343/pid"
  fi
  e11_began="$(date +%s)"
  fast "$fix" CI_FAST_LOCK_TIMEOUT=20 -- core/internal/a/a.go
  e11_took=$(( $(date +%s) - e11_began ))
  assert_eq "E11 ($case) the run passes" 0 "$status"
  assert_contains "E11 ($case) it reclaims the dead link, and says so" "reclaiming" "$out"
  assert_not_contains "E11 ($case) and does not go ahead unlocked" "without the lock" "$out"
  if [ "$e11_took" -lt 15 ]; then pass "E11 ($case) well inside the 20s cap (${e11_took}s)"; else fail "E11 ($case) inside 15s -- took ${e11_took}s" "$out"; fi
  assert_contains "E11 ($case) go test ran under its own lock" 'lock=held' "$(printf '%s\n' "$(calls "$fix")" | grep '^go test ')"
  assert_eq "E11 ($case) nothing is left behind" free "$(lock_left "$fix")"
  assert_eq "E11 ($case) no stray holder directories either" 0 "$(find "$fix/.git" -maxdepth 1 -name 'ci-fast.lock.*' | grep -c .)"
done

# The Docker lock (#1560). A Docker-backed package goes through
# gate_docker_step, which waits for the full gate's ci-local.lock, for up to
# GATE_LOCK_TIMEOUT. That wait must happen outside the fast queue, or one
# commit sits on ci-fast.lock for as long as a release full gate runs and
# every other worktree's commit waits out CI_FAST_LOCK_TIMEOUT and then goes
# ahead unlocked, all together. The cases below share one Docker lock
# between two worktrees and plant a live "full gate" holding it.

# A daemon that answers, so the Docker step runs instead of being ledgered.
write_docker_up() { # <dir>
  printf '#!/usr/bin/env bash\nexit 0\n' >"$1/bin/docker"
  chmod +x "$1/bin/docker"
}

# plant_gate_holder <lock path> <pid>: the full gate's Docker lock, in its
# symlink shape, held by <pid> on this host and boot.
plant_gate_holder() {
  local dir="$1.$2.planted"
  mkdir -p "$dir"
  echo "$2" >"$dir/pid"
  hostname >"$dir/host"
  (. "$REPO_ROOT/scripts/lib/ci-local-gate.sh" && gate_boot_id) >"$dir/boot"
  echo "/elsewhere/full-gate" >"$dir/where"
  ln -s "$(basename "$dir")" "$1"
}

# bg_fast <worktree> <out> [VAR=VAL ...] -- <paths...>: ci-fast.sh over a
# list of paths in the background, as fast does, writing to <out>. Its pid is
# left in bg_pid.
bg_fast() {
  local wt="$1" o="$2"
  shift 2
  local vars=()
  while [ $# -gt 0 ] && [ "$1" != "--" ]; do vars+=("$1"); shift; done
  [ "${1:-}" = "--" ] && shift
  printf '%s\n' "$@" >"$wt/paths.txt"
  : >"$wt/calls.log"
  (cd "$wt" && exec env -u CI_LOCAL_FAST -u CI_LOCAL_SKIP_JS -u CI_LOCAL_SKIP_DOCKER -u CI_LOCAL_TIER \
    -u GOTOOLCHAIN -u GIT_INDEX_FILE -u GIT_DIR -u GIT_WORK_TREE -u GIT_COMMON_DIR \
    -u GOMAXPROCS -u CI_FAST_LOCK -u CI_FAST_LOCK_TIMEOUT -u CI_FAST_STALL_MIN -u CI_FAST_CAP_MIN \
    -u CI_FAST_KILL_GRACE \
    FIX_ROOT="$wt" FIX_LOG="$wt/calls.log" CI_FAST_LOAD_WAIT=0 CI_FAST_POLL=0.2 GATE_LOCK_POLL=0.2 \
    CI_FAST_KILL_GRACE=1 PATH="$wt/bin:$PATH" ${vars[@]+"${vars[@]}"} \
    bash scripts/ci-fast.sh --paths-from "$wt/paths.txt") >"$o" 2>&1 &
  bg_pid=$!
}

# wait_for <file> <text> <seconds>: 0 once <file> contains <text>.
wait_for() {
  local i=0
  while [ "$i" -lt $(($3 * 10)) ]; do
    grep -qF -- "$2" "$1" 2>/dev/null && return 0
    sleep 0.1
    i=$((i + 1))
  done
  return 1
}

# line_of <file> <text>: the number of the last line holding <text>, or 0.
line_of() { grep -nF -- "$2" "$1" 2>/dev/null | tail -1 | cut -d: -f1 | grep . || echo 0; }

# two_worktrees <fix>: w1 and w2 beside it, with the queue's go and a daemon.
two_worktrees() {
  local w
  for w in w1 w2; do
    (cd "$1" && git worktree add -q --detach "$1.$w" main) >/dev/null 2>&1
    write_queue_go "$1.$w"
    write_docker_up "$1.$w"
  done
}

# E12: while a full gate holds the Docker lock, a commit with a Docker-backed
# package waits for it without holding the fast lock, so a plain commit in
# another worktree goes straight through, under the lock, with no wait. Once
# the full gate lets go, the first run takes the Docker lock, then the fast
# lock again, and runs its Docker-backed go test holding both.
fix="$(make_repo)"
two_worktrees "$fix"
gate_lock="$fix/.gate-lock-shared"
sleep 300 &
full_gate=$!
plant_gate_holder "$gate_lock" "$full_gate"
bg_fast "$fix.w1" "$SANDBOX/e12.w1.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 GATE_LOCK_TIMEOUT=60 \
  -- core/tests/crashmatrix/c.go
e12_w1=$bg_pid
if wait_for "$SANDBOX/e12.w1.out" "gate lock: waiting" 20; then
  pass "E12 the Docker-backed run waits for the full gate's Docker lock"
else
  fail "E12 the Docker-backed run waits for the full gate's Docker lock" "$(cat "$SANDBOX/e12.w1.out")"
fi
sleep 1
assert_eq "E12 and holds no fast lock while it waits" free "$(lock_left "$fix")"
assert_contains "E12 it says it gave the fast lock back" "given back" "$(cat "$SANDBOX/e12.w1.out")"
e12_began="$(date +%s)"
fast "$fix.w2" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=8 -- core/internal/a/a.go
e12_took=$(( $(date +%s) - e12_began ))
assert_eq "E12 a plain commit in another worktree passes meanwhile" 0 "$status"
assert_contains "E12 it took the fast lock" "ci-fast lock: held" "$out"
assert_not_contains "E12 without waiting for it" "ci-fast lock: waiting" "$out"
assert_not_contains "E12 and did not go ahead unlocked" "without the lock" "$out"
if [ "$e12_took" -lt 6 ]; then pass "E12 well inside its 8s lock cap (${e12_took}s)"; else fail "E12 inside 6s -- took ${e12_took}s" "$out"; fi
assert_eq "E12 the first run is still waiting for Docker" alive "$(alive "$e12_w1")"
kill "$full_gate" 2>/dev/null
wait "$full_gate" 2>/dev/null
wait "$e12_w1"
e12_status=$?
e12_out="$(cat "$SANDBOX/e12.w1.out")"
assert_eq "E12 once the full gate is done the Docker-backed run passes" 0 "$e12_status"
assert_contains "E12 its Docker-backed go test ran" 'core: go test -p 2 -timeout 45m ./tests/crashmatrix' "$(calls "$fix.w1")"
assert_contains "E12 holding the fast lock" 'lock=held' "$(grep '^go test ' "$fix.w1/calls.log")"
e12_gate_at="$(line_of "$SANDBOX/e12.w1.out" "gate lock: acquired")"
e12_fast_at="$(grep -nE 'ci-fast lock: (held|acquired)' "$SANDBOX/e12.w1.out" | tail -1 | cut -d: -f1)"
if [ "$e12_gate_at" -gt 0 ] && [ "${e12_fast_at:-0}" -gt "$e12_gate_at" ]; then
  pass "E12 it took the Docker lock first, then the fast lock again (lines $e12_gate_at, $e12_fast_at)"
else
  fail "E12 the Docker lock first, then the fast lock again -- lines [$e12_gate_at] and [${e12_fast_at:-}]" "$e12_out"
fi
assert_eq "E12 no fast lock is left behind" free "$(lock_left "$fix")"
if [ -L "$gate_lock" ] || [ -e "$gate_lock" ]; then fail "E12 no Docker lock is left behind" "$(ls -la "$fix")"; else pass "E12 no Docker lock is left behind"; fi

# E13: no lock-order deadlock, and still one worktree's heavy steps at a time.
# w1 holds the Docker lock and wants the fast lock back; w2 holds the fast
# lock with a plain package in hand and a Docker-backed one to come. If w2
# waited for Docker holding the fast lock, they would sit there until a cap.
# They both have to finish well inside the caps, locked, never overlapping.
fix="$(make_repo)"
two_worktrees "$fix"
gate_lock="$fix/.gate-lock-shared"
serial="$SANDBOX/e13.serial.log"
: >"$serial"
sleep 300 &
full_gate=$!
plant_gate_holder "$gate_lock" "$full_gate"
e13_began="$(date +%s)"
bg_fast "$fix.w1" "$SANDBOX/e13.w1.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 GATE_LOCK_TIMEOUT=60 \
  STUB_SERIAL_LOG="$serial" STUB_GO_SLEEP=1 STUB_WT=w1 -- core/tests/crashmatrix/c.go
e13_w1=$bg_pid
wait_for "$SANDBOX/e13.w1.out" "gate lock: waiting" 20 || true
bg_fast "$fix.w2" "$SANDBOX/e13.w2.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 GATE_LOCK_TIMEOUT=60 \
  STUB_SERIAL_LOG="$serial" STUB_GO_SLEEP=2 STUB_WT=w2 -- core/internal/a/a.go core/tests/crashmatrix/c.go
e13_w2=$bg_pid
if wait_for "$SANDBOX/e13.w2.out" "ci-fast lock: held" 20; then
  pass "E13 w2 takes the fast lock while w1 waits for Docker"
else
  fail "E13 w2 takes the fast lock while w1 waits for Docker" "$(cat "$SANDBOX/e13.w2.out")"
fi
kill "$full_gate" 2>/dev/null
wait "$full_gate" 2>/dev/null
wait "$e13_w1"
e13_s1=$?
wait "$e13_w2"
e13_s2=$?
e13_took=$(( $(date +%s) - e13_began ))
assert_eq "E13 w1 passes" 0 "$e13_s1"
assert_eq "E13 w2 passes" 0 "$e13_s2"
assert_not_contains "E13 neither went ahead without a lock" "without the lock" "$(cat "$SANDBOX/e13.w1.out" "$SANDBOX/e13.w2.out")"
assert_not_contains "E13 neither hit the Docker lock's cap" "FAILED (gate lock)" "$(cat "$SANDBOX/e13.w1.out" "$SANDBOX/e13.w2.out")"
if [ "$e13_took" -lt 40 ]; then pass "E13 both finished well inside the 60s caps (${e13_took}s)"; else fail "E13 inside 40s -- took ${e13_took}s" "$(cat "$serial")"; fi
assert_eq "E13 every go call ran (w1 vet, test; w2 vet, test, test)" 10 "$(grep -c . "$serial")"
interleaved=0
prev=""
while read -r what step wt; do
  if [ "$what" = begin ]; then
    [ -z "$prev" ] || interleaved=1
    prev="$step $wt"
  else
    [ "$prev" = "$step $wt" ] || interleaved=1
    prev=""
  fi
done <"$serial"
assert_eq "E13 no go call overlapped another" 0 "$interleaved"
assert_eq "E13 no fast lock is left behind" free "$(lock_left "$fix")"
if [ -L "$gate_lock" ] || [ -e "$gate_lock" ]; then fail "E13 no Docker lock is left behind" "$(ls -la "$fix")"; else pass "E13 no Docker lock is left behind"; fi

# E14: a signal in either wait gives back what the run holds, and only that.
# First while it waits for the Docker lock (it holds nothing), then while it
# holds the Docker lock and waits for the fast lock behind another worktree.
fix="$(make_repo)"
two_worktrees "$fix"
gate_lock="$fix/.gate-lock-shared"
sleep 300 &
full_gate=$!
plant_gate_holder "$gate_lock" "$full_gate"
bg_fast "$fix.w1" "$SANDBOX/e14a.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 GATE_LOCK_TIMEOUT=60 \
  -- core/tests/crashmatrix/c.go
e14_w1=$bg_pid
wait_for "$SANDBOX/e14a.out" "gate lock: waiting" 20 || true
kill -TERM "$e14_w1"
wait "$e14_w1"
assert_eq "E14 SIGTERM while waiting for Docker ends the run with 143" 143 "$?"
assert_eq "E14 and leaves no fast lock" free "$(lock_left "$fix")"
assert_eq "E14 and leaves the full gate's Docker lock alone" "$(basename "$gate_lock").$full_gate.planted" "$(readlink "$gate_lock")"

bg_fast "$fix.w1" "$SANDBOX/e14b.w1.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 GATE_LOCK_TIMEOUT=60 \
  -- core/tests/crashmatrix/c.go
e14_w1=$bg_pid
wait_for "$SANDBOX/e14b.w1.out" "gate lock: waiting" 20 || true
bg_fast "$fix.w2" "$SANDBOX/e14b.w2.out" CI_LOCAL_LOCK_DIR="$gate_lock" CI_FAST_LOCK_TIMEOUT=60 STUB_GO_HANG=hold \
  -- core/internal/a/a.go
e14_w2=$bg_pid
for _ in $(seq 1 200); do [ -s "$fix.w2/hung.child" ] && break; sleep 0.1; done
kill "$full_gate" 2>/dev/null
wait "$full_gate" 2>/dev/null
wait_for "$SANDBOX/e14b.w1.out" "gate lock: acquired" 20 || true
e14_gate_at="$(line_of "$SANDBOX/e14b.w1.out" "gate lock: acquired")"
for _ in $(seq 1 100); do
  [ "$(line_of "$SANDBOX/e14b.w1.out" "ci-fast lock: waiting")" -gt "$e14_gate_at" ] && break
  sleep 0.1
done
if [ "$e14_gate_at" -gt 0 ] && [ "$(line_of "$SANDBOX/e14b.w1.out" "ci-fast lock: waiting")" -gt "$e14_gate_at" ]; then
  pass "E14 w1 holds the Docker lock and waits for the fast lock behind w2"
else
  fail "E14 w1 holds the Docker lock and waits for the fast lock behind w2" "$(cat "$SANDBOX/e14b.w1.out")"
fi
kill -TERM "$e14_w1"
wait "$e14_w1"
assert_eq "E14 SIGTERM in that wait ends the run with 143" 143 "$?"
if [ -L "$gate_lock" ] || [ -e "$gate_lock" ]; then fail "E14 it gives the Docker lock back" "$(ls -la "$fix")"; else pass "E14 it gives the Docker lock back"; fi
assert_eq "E14 and leaves w2's fast lock alone" held "$(lock_left "$fix")"
assert_eq "E14 w2 is still running" alive "$(alive "$e14_w2")"
kill -TERM "$e14_w2"
wait "$e14_w2" 2>/dev/null
assert_eq "E14 once w2 is stopped no fast lock is left" free "$(lock_left "$fix")"

# ---------------------------------- Group D: the live check stands down

echo "==> D. the live BLOCKED-row citation test stands down in the fast tier"

if command -v go >/dev/null 2>&1; then
  d_out="$(cd "$REPO_ROOT/core" && env -u CI_LOCAL_GITHUB_LEDGER CI_LOCAL_TIER=fast GOWORK=off \
    go test ./tests/compat -run 'TestEveryBlockedRowCitesAnIssueThatIsStillOpen|TestTheBlockedCitationGuardCanFail' -v 2>&1)"
  assert_contains "D1 the live check is skipped under CI_LOCAL_TIER=fast" '--- SKIP: TestEveryBlockedRowCitesAnIssueThatIsStillOpen' "$d_out"
  assert_contains "D1 the skip names where it runs instead" 'full gate' "$d_out"
  assert_contains "D1 the offline half still runs and passes" '--- PASS: TestTheBlockedCitationGuardCanFail' "$d_out"
else
  echo "    skip D1: go is not on PATH"
fi

echo
if [ "$failures" -eq 0 ]; then
  echo "==> ci-fast self-test: ok ($checks checks)"
  exit 0
fi
echo "==> ci-fast self-test: $failures of $checks checks FAILED" >&2
exit 1

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
#            the load wait, nice/GOMAXPROCS/-p 2, the stall and cap kills
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

    mkdir -p scripts/lib scripts/selftest scripts/api scripts/format .husky bin \
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
(cd "$fix" && printf 'package a\n\nvar X = 1\n' >core/internal/a/a.go)
fx "$fix" -- git commit -qm "pathspec" -- core/internal/a/a.go
c="$(calls "$fix")"
assert_eq "B8 a pathspec commit of an unstaged file commits" 0 "$status"
assert_contains "B8 the tier scoped to the one path" 'scoped to 1 changed path(s)' "$out"
assert_contains "B8 vet ran on its package" 'core: go vet ./internal/a' "$c"
assert_contains "B8 test ran on its package" 'core: go test' "$c"
assert_not_contains "B8 the neighbouring package was not touched" './internal/b' "$c"
assert_contains "B8 the checks never see the commit's index" 'go vet index=unset' "$c"
assert_eq "B8 the commit was made" 2 "$(commit_count "$fix")"
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
if [ -L "$common/ci-fast.lock" ]; then held=held; else held=free; fi
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
esac
exit 0
STUB
  chmod +x "$1/bin/go"
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
# everything under it, and the run fails saying STALLED and why. The lock is
# given back.
fix="$(make_repo)"
write_queue_go "$fix"
e6_began="$(date +%s)"
fast "$fix" STUB_GO_HANG=silent CI_FAST_STALL_MIN=0.05 CI_FAST_KILL_GRACE=1 -- core/internal/a/a.go
e6_took=$(( $(date +%s) - e6_began ))
assert_nonzero "E6 a silent hang fails the run" "$status"
assert_contains "E6 the verdict says STALLED" "STALLED" "$out"
assert_contains "E6 and why" "no output for" "$out"
assert_contains "E6 and names the knob" "CI_FAST_STALL_MIN" "$out"
assert_contains "E6 the verdict is FAILED" "ci-local: FAILED" "$out"
if [ "$e6_took" -lt 20 ]; then pass "E6 it was killed well before the hang ended (${e6_took}s)"; else fail "E6 killed inside 20s -- took ${e6_took}s" "$out"; fi
assert_eq "E6 the hung go is gone" gone "$(alive "$(cat "$fix/hung.pid" 2>/dev/null || echo 999999)")"
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

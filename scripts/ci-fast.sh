#!/usr/bin/env bash
# The fast tier of the gate (#1361).
#
#   scripts/ci-fast.sh                    # the staged paths (what pre-commit runs)
#   scripts/ci-fast.sh --base <rev>       # everything changed since <rev>...HEAD
#   scripts/ci-fast.sh --paths-from FILE  # a list of paths, one per line
#   scripts/ci-fast.sh --root DIR         # run in another checkout of the repository
#
# scripts/ci-local.sh is the full gate: the Docker crash matrix, the
# cross-compiles, the -race suites, the browser lane. It takes the better
# part of an hour, so on every commit it got bypassed, and all 28 PRs of
# EPIC N and EPIC T reached main carrying `Local-Gate: unverified` (#1361).
# This is the tier that is small enough to leave switched on. It runs what
# the change can have broken and nothing else:
#
#   always        the mutation-anchor check and the /api/v1 contract drift
#                 check (seconds each, and the two that would have caught
#                 both headline breaks on main)
#   Go files      gofmt on the changed files, then go vet, golangci-lint
#                 and go test on the packages those files live in, per
#                 module. Nothing downstream of them is rebuilt. A
#                 go.mod/go.sum change vets the whole module and ledgers the
#                 rest, because a dependency bump can break any package
#   conformance   docs/conformance/ or an EPIC spec staged runs the compat
#                 package that reads them (core/tests/compat)
#   shell, python bash -n (and shellcheck, when it is installed) on changed
#                 shell, ruff on changed Python under scripts/
#   JS            only when a path under ui/shared or apps/common/tests is
#                 staged, and then only that workspace (typecheck, eslint,
#                 and for ui/shared its vitest suite). A fresh worktree with
#                 no node_modules therefore does not block a Go-only or
#                 docs-only commit; a JS commit in one is refused with the
#                 command that fixes it, as the full gate does (#160)
#
# It does NOT run, and the full gate on the release PR still does: the
# whole-module suites, -race, the cross-compiles, the production builds, the
# browser and two-machine lanes, the repository-structure proofs and the
# gate's own self-test. A fast run passing is therefore not merge evidence
# for a release, and the commit it gates is stamped `Local-Gate: fast`, never
# left unstamped (.husky/prepare-commit-msg).
#
# It exports CI_LOCAL_TIER=fast, which core/tests/compat reads to stand down
# the one test that asks GitHub for live issue state. That one belongs to the
# full run (docs/reference/local-gate.md, "Tiers").
#
# The verdicts are the full gate's, from the same library: exit 0 for
# "ci-local: ok", 3 for "ci-local: INCOMPLETE" (something that applied could
# not run: no golangci-lint, a Docker suite with no daemon, a skipped JS
# workspace), 1 for "ci-local: FAILED".
#
# CI_LOCAL_FAST_JS_INSTALL=1 installs an uninstalled JS workspace that a
# staged path needs, instead of refusing. scripts/merge-gate.sh sets it,
# because its worktree is always fresh.
set -e

mode=staged
base=""
paths_file=""
root_arg=""
while [ $# -gt 0 ]; do
  case "$1" in
    --staged) mode=staged ;;
    --base)
      [ $# -ge 2 ] || { echo "ci-fast: --base needs a revision" >&2; exit 2; }
      mode=base
      base="$2"
      shift
      ;;
    --paths-from)
      [ $# -ge 2 ] || { echo "ci-fast: --paths-from needs a file" >&2; exit 2; }
      mode=list
      paths_file="$2"
      shift
      ;;
    --root)
      [ $# -ge 2 ] || { echo "ci-fast: --root needs a directory" >&2; exit 2; }
      root_arg="$2"
      shift
      ;;
    -h | --help)
      sed -n '2,/^set -e$/p' "$0" | sed '$d' | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "ci-fast: unknown argument [$1]. See --help." >&2
      exit 2
      ;;
  esac
  shift
done

here="$(cd "$(dirname "$0")" && pwd)"
if [ -n "$root_arg" ]; then
  cd "$root_arg"
fi

# A hook's git environment points at the commit in progress, and every git
# call below has to resolve the repository fresh (see scripts/ci-local.sh).
unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

# The same PATH rules as the full gate: Homebrew's tools, but not ahead of a
# golangci-lint the caller put on PATH on purpose.
_caller_golangci=$(command -v golangci-lint 2>/dev/null || true)
export PATH="/opt/homebrew/bin:$PATH"
if [ -n "$_caller_golangci" ]; then
  PATH="$(dirname "$_caller_golangci"):$PATH"
  export PATH
fi
if [ -n "${GOLANGCI_LINT_DIR:-}" ]; then
  export PATH="$GOLANGCI_LINT_DIR:$PATH"
fi

export CI_LOCAL_TIER=fast
REPO_ROOT="$(pwd)"

# shellcheck source=scripts/lib/ci-local-gate.sh
. "$here/lib/ci-local-gate.sh"
gate_install_traps

# The changed paths, newline separated. A path with a newline in it splits
# here, which can only make the sweep look at a file that is not there.
case "$mode" in
  staged) changed="$(git diff --cached --name-only -z --no-renames --diff-filter=ACMRD | tr '\0' '\n')" ;;
  base) changed="$(git diff --name-only -z --no-renames "$base...HEAD" | tr '\0' '\n')" ;;
  list) changed="$(cat "$paths_file")" ;;
esac

has() { printf '%s\n' "$changed" | grep -Eq "$1"; }

count="$(printf '%s\n' "$changed" | grep -c . || true)"
echo "==> ci-fast: the fast tier, scoped to $count changed path(s) ($mode). The full gate is scripts/ci-local.sh."

# ---------------------------------------------------------------- always

gate_step "mutation anchors in the compat, conformance and race selftests still match the tree (#458)"
bash scripts/selftest/check-anchors.sh

gate_step "/api/v1 bindings match the contract, and no implementation type leaks (#166)"
bash scripts/api/check-contract-drift.sh

# --------------------------------------------------------------- formatting

gofiles=""
while IFS= read -r f; do
  if [ -n "$f" ] && [ -f "$f" ]; then
    case "$f" in *.go) gofiles="$gofiles $f" ;; esac
  fi
done <<EOF
$changed
EOF
if [ -n "$gofiles" ]; then
  gate_step "changed Go files are gofmt-clean"
  # shellcheck disable=SC2086
  unformatted="$(gofmt -l $gofiles)"
  if [ -n "$unformatted" ]; then
    echo "==> gofmt would change:" >&2
    printf '%s\n' "$unformatted" >&2
    exit 1
  fi
fi

# ------------------------------------------------------------ shell, python

shfiles=""
pyfiles=""
while IFS= read -r f; do
  if [ -n "$f" ] && [ -f "$f" ]; then
    case "$f" in
      .husky/*) sh -n "$f" ;;
      *.sh) shfiles="$shfiles $f" ;;
      scripts/*.py) pyfiles="$pyfiles $f" ;;
    esac
  fi
done <<EOF
$changed
EOF
if [ -n "$shfiles" ]; then
  gate_step "changed shell scripts parse (bash -n, and shellcheck where it is installed)"
  for f in $shfiles; do
    bash -n "$f"
  done
  if command -v shellcheck >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    shellcheck -S warning $shfiles
  fi
fi
if [ -n "$pyfiles" ]; then
  gate_step "changed Python under scripts/ passes ruff (#672)"
  if command -v ruff >/dev/null 2>&1; then
    # shellcheck disable=SC2086
    ruff check --config scripts/bdtools/pyproject.toml $pyfiles
  else
    gate_note_skip "ruff over the changed Python (#672): ruff is not on PATH. Install it (pipx install ruff) and re-run."
  fi
fi

# ---------------------------------------------------------------- Go modules

# Packages whose tests need the Docker daemon. With it down they t.Skip and
# go test still says ok, so they go through gate_docker_step, which ledgers
# that instead of passing it (#160).
docker_re='(^|/)tests/(crashmatrix|sftpintegration|sshexecintegration|miniointegration|conformance|machinegate|containerhooks|adapterstacks|dockercli)$|(^|/)internal/transport/rclone$'

lint_ready=0
if command -v golangci-lint >/dev/null 2>&1; then
  lint_ready=1
  # Run under the Go the linter was built with, as the full gate does:
  # a linter cannot read export data a newer Go wrote.
  if [ -z "${GOTOOLCHAIN:-}" ]; then
    _built=$(golangci-lint --version 2>/dev/null | sed -n 's/.*built with go\([0-9][0-9.]*\).*/\1/p' | head -n 1)
    if [ -n "$_built" ]; then
      GOTOOLCHAIN="go$_built"
      export GOTOOLCHAIN
    fi
  fi
fi

for module in core apps/common distribution apps/generic apps/synology apps/ugos/backend; do
  [ -f "$module/go.mod" ] || continue

  # What in this module the change touches. apps/common/tests is a JS
  # workspace and Markdown inside a module is not code. The conformance
  # matrices and EPIC specs live under docs/ and are read by one core
  # package, so touching them touches that package.
  mod_files="$(printf '%s\n' "$changed" | grep -E "^$module/" | grep -vE '\.md$' || true)"
  if [ "$module" = "apps/common" ]; then
    mod_files="$(printf '%s\n' "$mod_files" | grep -vE '^apps/common/tests/' || true)"
  fi
  if [ "$module" = "core" ] && has '^(docs/conformance/|docs/EPIC-[A-Z]-)'; then
    mod_files="$mod_files
core/tests/compat/matrix_test.go"
  fi

  whole=0
  pkgs=""
  while IFS= read -r f; do
    [ -n "$f" ] || continue
    case "$f" in
      "$module/go.mod" | "$module/go.sum")
        whole=1
        continue
        ;;
    esac
    d="$(dirname "$f")"
    while [ "$d" != "$module" ] && ! compgen -G "$d/*.go" >/dev/null; do
      d="$(dirname "$d")"
    done
    if [ "$d" = "$module" ]; then
      compgen -G "$module/*.go" >/dev/null || continue
      rel="."
    else
      rel="./${d#"$module"/}"
    fi
    case " $pkgs " in
      *" $rel "*) ;;
      *) pkgs="$pkgs $rel" ;;
    esac
  done <<EOF
$mod_files
EOF

  if [ "$whole" = 1 ]; then
    gate_step "$module go vet ./... (go.mod or go.sum changed)"
    (cd "$module" && GOWORK=off go vet ./...)
    gate_note_skip "$module golangci-lint and go test after a go.mod/go.sum change: a dependency bump can break any package, and only the full gate runs the whole module (docs/rclone-upgrade.md for core)"
    continue
  fi
  [ -n "$pkgs" ] || continue

  gate_step "$module go vet, touched packages:$pkgs"
  # shellcheck disable=SC2086
  (cd "$module" && GOWORK=off go vet $pkgs)

  if [ "$lint_ready" = 1 ]; then
    gate_step "$module golangci-lint, touched packages:$pkgs"
    # shellcheck disable=SC2086
    gate_lint "$module" $pkgs
  else
    gate_note_skip "$module golangci-lint on the touched packages:$pkgs. golangci-lint is not on PATH; build the pinned one (scripts/ci/pins.env)."
  fi

  plain=""
  dock=""
  for rel in $pkgs; do
    case "$rel" in
      ./cmd/gotestwatch)
        gate_note_skip "$module cmd/gotestwatch: it is compiled and run under its own exit-status contract by the full gate (#533), not by go test"
        ;;
      *)
        if printf '%s\n' "$rel" | grep -Eq "$docker_re"; then
          dock="$dock $rel"
        else
          plain="$plain $rel"
        fi
        ;;
    esac
  done
  if [ -n "$plain" ]; then
    gate_step "$module go test, touched packages:$plain"
    # shellcheck disable=SC2086
    (cd "$module" && GOWORK=off go test -timeout 20m $plain)
  fi
  if [ -n "$dock" ]; then
    gate_docker_step "$module go test, Docker-backed touched packages:$dock"
    # shellcheck disable=SC2086
    (cd "$module" && GOWORK=off go test -timeout 45m $dock)
  fi
done

# ----------------------------------------------------------------------- JS

# Only a workspace a staged path is in. gate_require_js_deps names the
# uninstalled ones and refuses, unless CI_LOCAL_SKIP_JS=1 chose the skip.
for ws in ui/shared apps/common/tests; do
  has "^$ws/" || continue
  if [ "$(gate_workspace_state "$ws")" = uninstalled ] && [ "${CI_LOCAL_FAST_JS_INSTALL:-0}" = "1" ]; then
    gate_step "$ws install (CI_LOCAL_FAST_JS_INSTALL=1)"
    eval "$(gate_install_hint "$ws")"
  fi
  gate_require_js_deps "$ws"
  case "$(gate_workspace_state "$ws")" in
    installed)
      gate_step "$ws typecheck"
      (cd "$ws" && npm run --silent lint)
      gate_step "$ws eslint"
      (cd "$ws" && npm run --silent eslint)
      if [ "$ws" = "ui/shared" ]; then
        gate_step "ui/shared typecheck every provider"
        (cd ui/shared && npm run --silent typecheck:providers)
        gate_step "ui/shared tests"
        (cd ui/shared && npm test --silent)
      fi
      ;;
    uninstalled)
      gate_note_skip "$ws typecheck and eslint ($(gate_install_hint "$ws")); skipped out loud with CI_LOCAL_SKIP_JS=1"
      ;;
  esac
done

# Never a bare echo: "ok" has to be earned by an empty skip ledger.
gate_summary

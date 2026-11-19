#!/usr/bin/env bash
# Every self-test and guard script in the tree is a step of scripts/ci-local.sh
# (#1530, #1531, #1533), or is named, with an open issue, in
# scripts/tests/testdata/unwired-guards.txt.
#
# scripts/ci-local.sh is the only gate (#1260), so a self-test that is not a
# step there runs nowhere, and its PASS rows rest on one run. Three of them sat
# in the tree like that for a release. This looks at four shapes of guard:
#
#   scripts/<dir>/selftest.sh       a mutation self-test
#   scripts/<dir>/ui-selftest.sh    the browser-side one
#   scripts/tests/*.test.sh         a guard's own test
#   ui/shared/e2e/playwright.config.ts   the browser specs
#
# and asks whether the gate names its path on a line that is not a comment. A
# path in the exemption file is allowed, and says why: `path <TAB> why #issue`,
# with an issue number, because "unwired for now" with no owner is how these
# were lost. An entry for a file that is gone, or that the gate now runs, is
# refused too, so the list can only shrink.
#
# The checker runs against throwaway trees first, so a guard that has stopped
# guarding fails here: an unwired script is named, a wired one is not, a path
# that only a comment mentions is still unwired, and a bad exemption is refused.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }

# problems <root>: one line per thing wrong, nothing when the tree is fine.
problems() {
  local root=$1 gate="$1/scripts/ci-local.sh" exempt="$1/scripts/tests/testdata/unwired-guards.txt" f path why
  local code
  code="$(grep -vE '^[[:space:]]*#' "$gate" 2>/dev/null || true)"
  local guards
  guards="$(cd "$root" && {
    ls scripts/*/selftest.sh scripts/*/ui-selftest.sh scripts/tests/*.test.sh ui/shared/e2e/playwright.config.ts 2>/dev/null
  } | sort -u)"
  for f in $guards; do
    if printf '%s\n' "$code" | grep -qF -- "$f"; then continue; fi
    if [ -f "$exempt" ] && grep -qE "^$(printf '%s' "$f" | sed 's/[.[\*^$/]/\\&/g')"$'\t' "$exempt"; then continue; fi
    echo "$f is in the tree and is no step of scripts/ci-local.sh (wire it, or name it in scripts/tests/testdata/unwired-guards.txt with an issue)"
  done
  [ -f "$exempt" ] || return 0
  while IFS=$'\t' read -r path why; do
    case "$path" in '' | '#'*) continue ;; esac
    if ! printf '%s' "$why" | grep -qE '#[0-9]+'; then
      echo "unwired-guards.txt: $path has no issue number in its reason"
    fi
    if [ ! -e "$root/$path" ]; then
      echo "unwired-guards.txt: $path is not in the tree any more, so its entry is stale"
    elif printf '%s\n' "$code" | grep -qF -- "$path"; then
      echo "unwired-guards.txt: $path is a gate step now, so its entry is stale"
    fi
  done <"$exempt"
}

# A throwaway tree: one self-test, one guard test, and a gate that names what it is given.
mk() { # mk <dir> <gate text> [exemption text]
  mkdir -p "$1/scripts/foo" "$1/scripts/tests/testdata"
  : >"$1/scripts/foo/selftest.sh"
  : >"$1/scripts/tests/bar.test.sh"
  printf '%s\n' "$2" >"$1/scripts/ci-local.sh"
  if [ -n "${3:-}" ]; then printf '%b\n' "$3" >"$1/scripts/tests/testdata/unwired-guards.txt"; fi
}

work="$(mktemp -d "${TMPDIR:-/tmp}/every-guard.XXXXXX")"
case "$work" in /*/every-guard.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

echo "==> the checker can fail (planted trees)"
mk "$work/a" 'bash scripts/foo/selftest.sh
bash scripts/tests/bar.test.sh'
if [ -z "$(problems "$work/a")" ]; then pass "control: a gate that runs both is clean"; else fail "control: a gate that runs both is clean" "$(problems "$work/a")"; fi

mk "$work/b" 'bash scripts/tests/bar.test.sh'
if problems "$work/b" | grep -q 'scripts/foo/selftest.sh is in the tree'; then pass "a self-test the gate never runs is named"; else fail "a self-test the gate never runs is named"; fi

mk "$work/c" '# bash scripts/foo/selftest.sh would go here
bash scripts/tests/bar.test.sh'
if problems "$work/c" | grep -q 'scripts/foo/selftest.sh is in the tree'; then pass "a path that only a comment names is still unwired"; else fail "a path that only a comment names is still unwired"; fi

mk "$work/d" 'bash scripts/tests/bar.test.sh' 'scripts/foo/selftest.sh\tneeds a browser, #1532'
if [ -z "$(problems "$work/d")" ]; then pass "an exemption with an issue lets it by"; else fail "an exemption with an issue lets it by" "$(problems "$work/d")"; fi

mk "$work/e" 'bash scripts/tests/bar.test.sh' 'scripts/foo/selftest.sh\tneeds a browser'
if problems "$work/e" | grep -q 'no issue number'; then pass "an exemption with no issue number is refused"; else fail "an exemption with no issue number is refused"; fi

mk "$work/f" 'bash scripts/foo/selftest.sh
bash scripts/tests/bar.test.sh' 'scripts/foo/selftest.sh\tneeds a browser, #1532'
if problems "$work/f" | grep -q 'is a gate step now'; then pass "an exemption for a script the gate now runs is stale"; else fail "an exemption for a script the gate now runs is stale"; fi

mk "$work/g" 'bash scripts/foo/selftest.sh
bash scripts/tests/bar.test.sh' 'scripts/gone/selftest.sh\twas a browser one, #1532'
if problems "$work/g" | grep -q 'not in the tree any more'; then pass "an exemption for a script that is gone is stale"; else fail "an exemption for a script that is gone is stale"; fi

echo "==> the real tree"
real="$(problems "$repo_root")"
if [ -z "$real" ]; then
  pass "every self-test and guard script is a gate step or an exemption with an issue"
else
  fail "every self-test and guard script is a gate step or an exemption with an issue" "$real"
fi

echo "every-guard-is-a-gate-step: $((checks - failures))/$checks passed"
[ "$failures" -eq 0 ]

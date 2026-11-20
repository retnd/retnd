#!/usr/bin/env bash
# Every self-test and guard script is a step of scripts/ci-local.sh
# (#1530, #1531, #1533), or is named, with an open issue, in
# scripts/tests/testdata/unwired-guards.txt.
#
# scripts/ci-local.sh is the only gate (#1260), so a self-test that is not a
# step there runs nowhere, and its PASS rows rest on one run. Three of them sat
# in the tree like that for a release. scripts/tests/gate_wiring.py reads the
# gate and says which guards it really runs: a command line (not a comment, an
# echo, a skip note, a step title or an `if [ -f ]` test) that names the path,
# in a gate_seg_* function that a gate_run_segment line puts in a shard of
# scripts/release/gate-shards.txt, with a `for t in a b` loop expanded. A script
# the release job runs itself counts too. The guards it looks for are the
# self-tests (selftest*.sh, ui-selftest.sh), the checks (check-*.sh,
# verify-*.sh), the e2e gate, scripts/tests/*.test.sh and the browser specs'
# Playwright config.
#
# An unwired guard is allowed only in the exemption file, `path <TAB> why #issue`,
# with an issue number, because "unwired for now" with no owner is how these were
# lost. An entry for a file that is gone, or that the gate now runs, is refused
# too, so the list can only shrink.
#
# The checker runs against throwaway trees first, so a guard that has stopped
# guarding fails here. It is a static step of the gate, about a second, and it
# runs in a CI_LOCAL_FAST=1 run as well on purpose: it reads text only, and the
# cheapest place to learn a guard fell out of the gate is the iteration loop.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }

# problems <root>: one line per thing wrong, nothing when the tree is fine.
problems() {
  local root=$1 exempt="$1/scripts/tests/testdata/unwired-guards.txt" wired f path why
  wired="$(python3 "$repo_root/scripts/tests/gate_wiring.py" "$root" | cut -f1)"
  for f in $(python3 "$repo_root/scripts/tests/gate_wiring.py" "$root" --guards); do
    if grep -qxF -- "$f" <<<"$wired"; then continue; fi
    if [ -f "$exempt" ] && awk -F'\t' -v f="$f" '$0 !~ /^[[:space:]]*#/ && $1 == f { found = 1 } END { exit !found }' "$exempt"; then continue; fi
    echo "$f is in the tree and is no executed step of scripts/ci-local.sh in a shard of the manifest (wire it, or name it in scripts/tests/testdata/unwired-guards.txt with an issue)"
  done
  [ -f "$exempt" ] || return 0
  while IFS=$'\t' read -r path why; do
    case "$path" in '' | '#'*) continue ;; esac
    if ! grep -qE '#[0-9]+' <<<"$why"; then
      echo "unwired-guards.txt: $path has no issue number in its reason"
    fi
    if [ ! -e "$root/$path" ]; then
      echo "unwired-guards.txt: $path is not in the tree any more, so its entry is stale"
    elif grep -qxF -- "$path" <<<"$wired"; then
      echo "unwired-guards.txt: $path is a gate step now, so its entry is stale"
    fi
  done <"$exempt"
}

# A throwaway tree: one self-test and one guard test, a manifest with the shard
# "static", and a gate whose body is given. $2 is the text of gate_seg_x's body.
mk() { # mk <dir> <body of gate_seg_x> [exemption text] [run line]
  mkdir -p "$1/scripts/foo" "$1/scripts/tests/testdata" "$1/scripts/release"
  : >"$1/scripts/foo/selftest.sh"
  : >"$1/scripts/tests/bar.test.sh"
  printf 'static\n' >"$1/scripts/release/gate-shards.txt"
  printf 'gate_seg_x() {\n%s\n}\n%s\n' "$2" "${4:-gate_run_segment \"static\" gate_seg_x}" >"$1/scripts/ci-local.sh"
  if [ -n "${3:-}" ]; then printf '%b\n' "$3" >"$1/scripts/tests/testdata/unwired-guards.txt"; fi
}
both='  bash scripts/foo/selftest.sh
  bash scripts/tests/bar.test.sh'
unwired_msg='scripts/foo/selftest.sh is in the tree'
expect_unwired() { # expect_unwired <desc> <dir>
  local out
  out="$(problems "$2")"
  if grep -q "$unwired_msg" <<<"$out"; then pass "$1"; else fail "$1"; fi
}

work="$(mktemp -d "${TMPDIR:-/tmp}/every-guard.XXXXXX")"
case "$work" in /*/every-guard.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

echo "==> the checker can fail (planted trees)"
mk "$work/a" "$both"
if [ -z "$(problems "$work/a")" ]; then pass "control: a gate that runs both is clean"; else fail "control: a gate that runs both is clean" "$(problems "$work/a")"; fi

mk "$work/b" '  bash scripts/tests/bar.test.sh'
expect_unwired "a self-test the gate never runs is named" "$work/b"

mk "$work/c" '  # bash scripts/foo/selftest.sh would go here
  bash scripts/tests/bar.test.sh'
expect_unwired "a path that only a comment names is unwired" "$work/c"

mk "$work/c2" '  echo "next: bash scripts/foo/selftest.sh"
  bash scripts/tests/bar.test.sh'
expect_unwired "a path inside an echo is unwired" "$work/c2"

mk "$work/c3" '  gate_note_skip "scripts/foo/selftest.sh (CI_LOCAL_SKIP=1)"
  bash scripts/tests/bar.test.sh'
expect_unwired "a path inside a skip note is unwired" "$work/c3"

mk "$work/c4" '  if [ -f scripts/foo/selftest.sh ]; then
    echo "present"
  fi
  bash scripts/tests/bar.test.sh'
expect_unwired "a path that only an if [ -f ] tests is unwired" "$work/c4"

mk "$work/c5" "$both" "" 'gate_run_segment "other" gate_seg_x'
expect_unwired "a step in a shard the manifest does not list is unwired" "$work/c5"

mk "$work/c6" "$both" "" '# nobody runs it'
expect_unwired "a step in a segment nothing runs is unwired" "$work/c6"

mk "$work/c7" '  bash scripts/tests/bar.test.sh
  for t in selftest other; do
    bash scripts/foo/$t.sh
  done'
if [ -z "$(problems "$work/c7")" ]; then pass "a for loop is expanded, so a wired script in one is seen"; else fail "a for loop is expanded, so a wired script in one is seen" "$(problems "$work/c7")"; fi

mk "$work/c8" '  bash scripts/tests/bar.test.sh'
mkdir -p "$work/c8/.github/workflows"
printf '      run: bash scripts/foo/selftest.sh\n' >"$work/c8/.github/workflows/release.yml"
if [ -z "$(problems "$work/c8")" ]; then pass "a script the release job runs counts as wired"; else fail "a script the release job runs counts as wired" "$(problems "$work/c8")"; fi

# Only a line that runs the script as a command counts (the #1538 review, B4).
mk "$work/c9" '  bash scripts/foo/selftest.sh --check-anchors
  bash scripts/tests/bar.test.sh'
expect_unwired "an anchors-only run (--check-anchors) is not a run of the controls" "$work/c9"

mk "$work/c10" '  gate_docker_step "runs scripts/foo/selftest.sh"
  bash scripts/tests/bar.test.sh'
expect_unwired "a gate_docker_step title is not a run" "$work/c10"

mk "$work/c11" '  [[ -f scripts/foo/selftest.sh ]] && echo yes
  bash scripts/tests/bar.test.sh'
expect_unwired "a [[ -f ]] test is not a run" "$work/c11"

mk "$work/c12" '  st=scripts/foo/selftest.sh
  bash scripts/tests/bar.test.sh'
expect_unwired "an assignment is not a run" "$work/c12"

mk "$work/c13" '  shellcheck scripts/foo/selftest.sh
  bash scripts/tests/bar.test.sh'
expect_unwired "shellcheck over the script is not a run" "$work/c13"

mk "$work/c14" '  false && bash scripts/foo/selftest.sh
  bash scripts/tests/bar.test.sh'
expect_unwired "a run behind false && is not a run" "$work/c14"

mk "$work/c15" '  "$pw_lock" bash scripts/foo/selftest.sh
  CI_LOCAL_SELFTEST=1 bash scripts/tests/bar.test.sh'
if [ -z "$(problems "$work/c15")" ]; then pass "control: a run through a lock wrapper or behind an env assignment counts"; else fail "control: a run through a lock wrapper or behind an env assignment counts" "$(problems "$work/c15")"; fi

mk "$work/c16" '  gate_selftest_with_ledger scripts/foo/selftest.sh
  bash scripts/tests/bar.test.sh'
if [ -z "$(problems "$work/c16")" ]; then pass "control: a run through gate_selftest_with_ledger counts"; else fail "control: a run through gate_selftest_with_ledger counts" "$(problems "$work/c16")"; fi

# The Chromium lane's steps (#1537, #1543): a wrapper first, then the lane,
# and the guard is what the lane runs.
mk "$work/c17" '  gate_ui_browser "the foo self-test" bash scripts/e2e/ui-browser.sh run bash scripts/foo/selftest.sh
  gate_pw_locked bash scripts/tests/bar.test.sh'
if [ -z "$(problems "$work/c17")" ]; then pass "control: a run through gate_ui_browser and the lane, or gate_pw_locked, counts"; else fail "control: a run through gate_ui_browser and the lane, or gate_pw_locked, counts" "$(problems "$work/c17")"; fi

mk "$work/c18" '  gate_ui_browser "the foo self-test" bash scripts/e2e/ui-browser.sh run bash scripts/foo/selftest.sh --check-anchors
  bash scripts/tests/bar.test.sh'
expect_unwired "an anchors-only run through the lane is still not a run" "$work/c18"

mk "$work/h" '  bash scripts/tests/bar.test.sh' '# scripts/foo/selftest.sh\tneeds a browser, #1532'
expect_unwired "a commented-out exemption does not exempt" "$work/h"

mk "$work/d" '  bash scripts/tests/bar.test.sh' 'scripts/foo/selftest.sh\tneeds a browser, #1532'
if [ -z "$(problems "$work/d")" ]; then pass "an exemption with an issue lets it by"; else fail "an exemption with an issue lets it by" "$(problems "$work/d")"; fi

mk "$work/e" '  bash scripts/tests/bar.test.sh' 'scripts/foo/selftest.sh\tneeds a browser'
out="$(problems "$work/e")"
if grep -q 'no issue number' <<<"$out"; then pass "an exemption with no issue number is refused"; else fail "an exemption with no issue number is refused"; fi

mk "$work/f" "$both" 'scripts/foo/selftest.sh\tneeds a browser, #1532'
out="$(problems "$work/f")"
if grep -q 'is a gate step now' <<<"$out"; then pass "an exemption for a script the gate now runs is stale"; else fail "an exemption for a script the gate now runs is stale"; fi

mk "$work/g" "$both" 'scripts/gone/selftest.sh\twas a browser one, #1532'
out="$(problems "$work/g")"
if grep -q 'not in the tree any more' <<<"$out"; then pass "an exemption for a script that is gone is stale"; else fail "an exemption for a script that is gone is stale"; fi

echo "==> the real tree"
real="$(problems "$repo_root")"
if [ -z "$real" ]; then
  pass "every self-test and guard script is an executed gate step or an exemption with an issue"
else
  fail "every self-test and guard script is an executed gate step or an exemption with an issue" "$real"
fi

echo "every-guard-is-a-gate-step: $((checks - failures))/$checks passed"
[ "$failures" -eq 0 ]

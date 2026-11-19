#!/usr/bin/env bash
# The agents and encryption self-tests' controls all run on a Linux gate host,
# or skip for a reason this test allows (#1543).
#
# Until #1543 about twenty of their browser, vitest and capture controls
# hard-coded paths on the maintainer's Mac and a capture image built there by
# hand, so the gate left them out with RETND_SELFTEST_SKIP_BROWSER=1 and their
# matrix rows rested on one Mac run. scripts/tests/selftest_linux_skips.py
# lists every control that would still skip and fails while any of them has no
# allowed reason. This runs it, runs its rules against made-up controls first
# so they cannot go quiet, and checks the gate itself: no step uses the old
# blanket skip, and each self-test's lane half is a step of the ui-browser
# shard while its own shard leaves that half out by name.
#
# Static, a second or two: it imports the two self-tests and reads
# scripts/ci-local.sh, and builds and runs nothing.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }

echo "==> the rules, against made-up controls"
if out="$(python3 "$repo_root/scripts/tests/selftest_linux_skips.py" --self-check 2>&1)"; then
  pass "a Mac path, the Mac lock, a hand-built image, a bare docker run and an undeclared or made-up need are skips nobody allowed"
else
  fail "a Mac path, the Mac lock, a hand-built image, a bare docker run and an undeclared or made-up need are skips nobody allowed" "$out"
fi

echo "==> the real controls"
if out="$(python3 "$repo_root/scripts/tests/selftest_linux_skips.py" 2>&1)"; then
  pass "every agents and encryption control runs on a Linux gate host or skips for an allowed reason"
  printf '%s\n' "$out" | grep -E '^==>|skip$|skips  ' | sed 's/^/         | /'
else
  fail "every agents and encryption control runs on a Linux gate host or skips for an allowed reason" "$out"
fi

echo "==> the gate"
gate="$repo_root/scripts/ci-local.sh"
# The command lines of the gate, without comments.
commands="$(grep -vE '^[[:space:]]*#' "$gate")"
if grep -q 'RETND_SELFTEST_SKIP_BROWSER' <<<"$commands"; then
  fail "no gate step leaves the browser controls out wholesale (RETND_SELFTEST_SKIP_BROWSER)" "$(grep -n 'RETND_SELFTEST_SKIP_BROWSER' "$gate" | grep -vE '^[0-9]+:[[:space:]]*#')"
else
  pass "no gate step leaves the browser controls out wholesale (RETND_SELFTEST_SKIP_BROWSER)"
fi

# body <function>: the lines of one gate_seg_* function.
body() { awk -v f="$1() {" '$0 == f { on = 1; next } on && /^}/ { exit } on' "$gate" | grep -vE '^[[:space:]]*#'; }
ui="$(body gate_seg_ui_browser)"
for st in encryption agents; do
  if grep -E "scripts/e2e/ui-browser.sh|gate_ui_browser" <<<"$ui" | grep -q "RETND_SELFTEST_LANE=only bash scripts/$st/selftest.sh"; then
    pass "the ui-browser shard runs the $st self-test's lane controls (RETND_SELFTEST_LANE=only)"
  else
    fail "the ui-browser shard runs the $st self-test's lane controls (RETND_SELFTEST_LANE=only)" "gate_seg_ui_browser:
$ui"
  fi
  own="$(body "gate_seg_selftest_$st")"
  if grep -q "RETND_SELFTEST_LANE=skip bash scripts/$st/selftest.sh" <<<"$own"; then
    pass "the selftest-$st shard leaves only the lane half out, by name (RETND_SELFTEST_LANE=skip)"
  else
    fail "the selftest-$st shard leaves only the lane half out, by name (RETND_SELFTEST_LANE=skip)" "gate_seg_selftest_$st:
$own"
  fi
done

echo "selftest-linux-skips: $((checks - failures))/$checks passed"
[ "$failures" -eq 0 ]

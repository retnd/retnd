#!/usr/bin/env bash
# The agents and encryption self-tests' controls all run on a Linux gate host,
# or skip for a reason this test allows (#1543).
#
# Until #1543 about twenty of their browser, vitest and capture controls
# needed the Playwright lock, hard-coded paths on the maintainer's Mac and a
# capture image built there by hand, so every gate host skipped them.
# scripts/tests/selftest_linux_skips.py lists every control that would still
# skip on a Linux gate host and fails while any of them has no allowed reason
# (only the retnd-tests checkout, #1548). This runs it, runs its rules against
# made-up controls first so they cannot go quiet, and checks the gate itself:
# no step uses the old blanket skip, and both self-tests run through
# gate_selftest_with_ledger, so whatever a host does skip lands in the ledger.
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
  pass "a Mac path, the Mac lock and a hand-built image are skips nobody allowed, the retnd-tests checkout is the one allowed"
else
  fail "a Mac path, the Mac lock and a hand-built image are skips nobody allowed, the retnd-tests checkout is the one allowed" "$out"
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
for st in encryption agents; do
  own="$(body "gate_seg_selftest_$st")"
  if grep -q "gate_selftest_with_ledger scripts/$st/selftest.sh" <<<"$own"; then
    pass "the selftest-$st shard runs every control its host has a lane for and ledgers the rest"
  else
    fail "the selftest-$st shard runs every control its host has a lane for and ledgers the rest" "gate_seg_selftest_$st:
$own"
  fi
done

echo "selftest-linux-skips: $((checks - failures))/$checks passed"
[ "$failures" -eq 0 ]

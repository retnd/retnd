#!/usr/bin/env bash
# docs/reference/local-gate.md is the one page that names every knob the
# local gate reads (#1260, #1261 review item 10). This test is what keeps
# it that: it reads the knobs out of the gate's own sources and refuses
# when one is missing from the page, so a variable added to the gate
# without a line in the reference ends the gate INCOMPLETE rather than
# quietly joining the undocumented ones.
#
# Besides the variables it checks the page names the two commit trailers,
# the three exit statuses, the record's schema id and the lock directory,
# because those are the other things somebody reading a refusal goes
# looking for.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$repo_root"

doc="docs/reference/local-gate.md"
checks=0
failures=0

pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || echo "         | $2"; }

sources="scripts/ci-local.sh scripts/lib/ci-local-gate.sh scripts/ci-docker.sh scripts/install-hooks.sh \
  scripts/release/record-local-gate.sh scripts/release/verify-local-gate.sh scripts/release/gate-tree.sh \
  scripts/bdtools/release/verify_local_gate.py .husky/pre-commit .husky/prepare-commit-msg \
  core/tests/dockerlease/dockerlease.go scripts/ci/pins.env"

if [ ! -f "$doc" ]; then
  fail "the reference page exists" "$doc is missing"
  echo "local-gate-reference: $checks checks, $failures failures"
  exit 1
fi
pass "the reference page exists"

# Every variable the gate reads. A `CI_LOCAL_SKIP_*` glob in prose is not a
# variable, so a name has to end on a letter or digit.
# shellcheck disable=SC2086
vars="$(grep -ohE '\b(CI_LOCAL(_[A-Z0-9]+)*|GATE_LOCK_[A-Z_]*[A-Z]|RETND_TEST_OWNER|GOLANGCI_LINT_(DIR|VERSION)|GOTOOLCHAIN|PUBLISH)\b' $sources | sort -u)"
[ -n "$vars" ] || fail "the sources name at least one variable" "the grep over the gate's sources found nothing; the pattern or the file list is wrong"
for v in $vars; do
  if grep -q "\`$v\`" "$doc"; then
    pass "the page documents $v"
  else
    fail "the page documents $v" "add a row for \`$v\` to $doc"
  fi
done

for want in 'Local-Gate:' 'Release-Gate-Override:' 'retnd.local-gate/1' 'ci-local.lock' \
  'provenance/local-gate.json' 'provenance/local-gate.log' 'scripts/ci/pins.env'; do
  if grep -qF "$want" "$doc"; then
    pass "the page names $want"
  else
    fail "the page names $want"
  fi
done

# The three verdicts, each with its exit status.
for pair in 'ok|0' 'INCOMPLETE|3' 'FAILED|1'; do
  word="${pair%%|*}"; code="${pair##*|}"
  if grep -qE "\b$word\b.*\b$code\b|\b$code\b.*\b$word\b" "$doc"; then
    pass "the page gives the exit status for $word"
  else
    fail "the page gives the exit status for $word" "expected a line with both '$word' and '$code'"
  fi
done

# The page points back at the sources, so the two cannot drift apart
# unnoticed in the other direction either.
for src in scripts/ci-local.sh scripts/lib/ci-local-gate.sh scripts/release/record-local-gate.sh scripts/release/verify-local-gate.sh; do
  if grep -qF "$src" "$doc"; then
    pass "the page cites $src"
  else
    fail "the page cites $src"
  fi
done

echo "local-gate-reference: $checks checks, $failures failures"
[ "$failures" -eq 0 ]

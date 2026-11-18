#!/usr/bin/env bash
# The whole EPIC U capture sequence, in the order the epic's rule asks for
# (#1353): ONE capture script this epic did not change runs first and its result
# is looked at, and only then the new one, twice, byte for byte.
#
# This starts a browser, so it goes through the Playwright lock, as one run and
# never bare, and only at the scratch stage:
#
#     /Users/rom/workspace/xdg-pw-lock.sh bash docs/site/tools/capture-agents-all.sh
#
# Why the unchanged script first: EPIC L's docs pass found four unrelated breaks
# in this tooling in one afternoon, and the worst of them did not throw. A
# selector photographed the whole window instead of the card, and the only signal
# was a person looking. capture-recovery-assurance.mjs shares harness.mjs with
# the new script and `--check` proves it still reproduces. If it breaks, the
# harness is what broke and nothing the new script writes is to be believed yet;
# fix what it finds in this PR. See docs/site/tools/capture-encryption-all.sh for
# the known pre-existing moves in recovery-* pictures: look at them, do not commit
# them here.
#
# It stops at the first failure. It does not commit anything.
set -euo pipefail
cd "$(cd "$(dirname "$0")/../../.." && pwd)"

echo "==> 1. the unchanged script first: capture-recovery-assurance.mjs --check"
node docs/site/tools/capture-recovery-assurance.mjs --check

echo
echo "==> its result, for a person to look at. Every recovery-* picture should be unchanged:"
git status --short -- docs/site/screens | sed 's/^/    /'

echo
echo "==> 2. this epic's clips, twice, byte for byte"
node docs/site/tools/capture-agents.mjs --check

echo
echo "==> 3. what is now in docs/site/screens/ for this epic"
git status --short -- 'docs/site/screens/agents-*' | sed 's/^/    /'
echo
echo "Next: print the embeds (node docs/site/tools/capture-agents.mjs --embeds html) into web-ui.html's Agents section,"
echo "then bash scripts/release/check-epic-u-closed.sh once every row of the matrix is decided."

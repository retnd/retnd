#!/usr/bin/env bash
# The whole EPIC N capture sequence, in the order the epic's rule asks for
# (#998): ONE capture script this epic did not change runs first and its result
# is looked at, and only then the new one, twice, byte for byte.
#
# This starts a browser, so it goes through the Playwright lock, as one run and
# never bare:
#
#     /Users/rom/workspace/xdg-pw-lock.sh bash docs/site/tools/capture-encryption-all.sh
#
# Why the unchanged script first: EPIC L's docs pass found four unrelated breaks
# in this tooling in one afternoon, and the worst of them did not throw. A
# selector photographed the whole window instead of the card, and the only
# signal was a person looking. capture-recovery-assurance.mjs shares harness.mjs
# with the new script, takes the same kind of picture (cards and dialogs from
# the mock), and `--check` proves it still reproduces. If it breaks, the harness
# is what broke, and nothing the new script writes is to be believed yet.
#
# It stops at the first failure. It does not commit anything.
set -euo pipefail
cd "$(cd "$(dirname "$0")/../../.." && pwd)"

echo "==> 1. the unchanged script first: capture-recovery-assurance.mjs --check"
node docs/site/tools/capture-recovery-assurance.mjs --check

echo
echo "==> its result, for a person to look at. Every recovery-* picture should be unchanged:"
if git diff --quiet -- docs/site/screens; then
  echo "    no picture under docs/site/screens/ differs from what is committed"
else
  git diff --stat -- docs/site/screens
  echo "    ^ these moved. Open them and decide whether the harness or the product moved them"
  echo "      BEFORE trusting anything below. Record what you found in issue #998."
fi
git status --short -- docs/site/screens | sed 's/^/    /'

echo
echo "==> 2. this epic's captures, twice, byte for byte"
node docs/site/tools/capture-encryption.mjs --check

echo
echo "==> 3. what is now in docs/site/screens/ for this epic"
git status --short -- 'docs/site/screens/encryption-*' | sed 's/^/    /'
echo
echo "Next: bash scripts/release/check-epic-n-closed.sh (closing check, needs every row decided)."

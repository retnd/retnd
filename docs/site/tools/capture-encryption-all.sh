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
# Known, pre-existing, and shown rather than hidden: the batch gate ran this
# first step on a clean main and the unchanged script moved 33 recovery-*
# pictures there too (the committed bytes were made on another machine and
# browser build). So step 1 is expected to report a diff under docs/site/screens/
# for recovery-* files even with this epic absent, and that alone is not a
# reason to stop. What matters is that the script ran to the end and said
# "reproducible", which proves the shared harness still works. The wrapper prints
# the moved files and lists them under their own heading; do not commit them as
# part of this epic, and do not touch that script here.
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
  moved="$(git diff --name-only -- docs/site/screens | grep -vc '/recovery-' || true)"
  echo "    ^ these moved: $(git diff --name-only -- docs/site/screens | grep -c '/recovery-' || true) recovery-* pictures and $moved others."
  echo "      Moves in recovery-* are the KNOWN pre-existing ones (33 on a clean main, see the"
  echo "      header of this file): note them in issue #998 and leave them uncommitted here."
  echo "      Any move OUTSIDE recovery-* means the harness or the product moved something"
  echo "      this epic did not intend: look at it BEFORE trusting anything below."
fi
git status --short -- docs/site/screens | sed 's/^/    /'

echo
echo "==> 2. this epic's captures, twice, byte for byte"
node docs/site/tools/capture-encryption.mjs --check

echo
echo "==> 3. what is now in docs/site/screens/ for this epic"
git status --short -- 'docs/site/screens/encryption-*' | sed 's/^/    /'
echo
echo "Next: bash scripts/release/check-epic-n-closed.sh and bash scripts/release/check-epic-t-closed.sh (closing checks, each needs every row of its matrix decided)."

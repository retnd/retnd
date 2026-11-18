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
# The committed recovery-* and encryption-* bytes were shot by this script in
# the pinned Playwright image (mcr.microsoft.com/playwright:v1.62.1-noble plus
# ffmpeg and ImageMagick) for the 0.8.0 release (#1331), where it went green
# twice in a row. On another machine or browser build step 1 can still report
# moved recovery-* pictures, and that alone is not a reason to stop. What
# matters is that each step ran to the end and said "reproducible", which
# proves the shared harness still works. The wrapper prints the moved files
# under their own heading.
#
# Step 1 used to move 1 to 7 of its 33 pictures between its own two passes, so
# this script never got to step 2. The release fixed that script (#1331) even
# though EPIC N had kept it out of scope, because the release gate needs this
# whole sequence deterministic.
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
  echo "      Moves in recovery-* on a machine other than the pinned image are expected (see the"
  echo "      header of this file); commit them only when the pictures legitimately changed."
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

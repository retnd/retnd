#!/usr/bin/env bash
# The release's check that EPIC N (encrypted retained copies, #967) is closed
# (#998). Run by the require-epic-n-closed job in .github/workflows/release.yml,
# and by hand with the same command:
#
#     bash scripts/release/check-epic-n-closed.sh
#
# It refuses, naming each failure, unless all of this holds:
#
#   1. the canonical container contract is an EMPTY diff against the commit the
#      epic started from: container/compose.yaml, the compose files under
#      apps/*/compose/, apps/unraid/template/, apps/truenas/catalog/, the four
#      distribution/packaging/*.json and apps/common/platform/capabilities/.
#      Nothing this epic ships changes how a container is run, and an untracked
#      file in one of those places counts as a change. The release version is set aside
#      (#1331): a cut moves the image tag in all of them and canonical.json's
#      release record (image.tag, reference, published, note), and that is
#      not a change to how a container runs, so a cut alone passes;
#   2. the epic added 0015_retained_copy_encryption.sql (and may add
#      0019_encrypt_existing_consents.sql, #1302) and no other migration, and
#      nothing else under core/migrations/ moved. (The issue says the
#      directory "ends at 0015". Main already holds 0016 and 0018, so the
#      claim that survives is the epic's own list.);
#   3. the closing mode of core/tests/encryptiongate passes: no BLOCKED row in
#      docs/conformance/epic-n-matrix.md, every proof it cites resolves, every
#      PASS row cites a mutation that exists, and every picture and GIF listed
#      in docs/site/tools/encryption-captures.json is in docs/site/screens/.
#
# It needs a checkout with history back to EPIC_N_BASE (the release job fetches
# all of it). scripts/tests/epic-n-closure.test.sh drives this script in
# throwaway repositories, once per failure and once for a clean run.
#
#   EPIC_N_ROOT     repository to check (default: the one this script is in)
#   EPIC_N_BASE     the commit the epic started from (default: bc085e06, the
#                   0.7.2 merge into main, the last commit before any of it)
#   EPIC_N_TIP      the commit the epic ended at (default: 477aec5a, the merge
#                   of #1302 that put the last of it on main). Check 2 reads the
#                   migrations ADDED between EPIC_N_BASE and this commit, so a
#                   later epic's own migration (EPIC T's 0020, #1320) is that
#                   epic's closing check's business and not a failure here. An
#                   edit to an existing migration still fails at any point.
#                   WORKTREE reads additions up to the working tree, untracked
#                   files included, which is what the self-test uses.
#   EPIC_N_SKIP_GO  1 leaves out check 3's Go run (the self-test sets it; the
#                   Go checker has its own self-test)
set -uo pipefail

root="${EPIC_N_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
base="${EPIC_N_BASE:-bc085e06}"
tip="${EPIC_N_TIP:-477aec5a}"
cd "$root" || exit 2

fails=0
bad() { fails=$((fails + 1)); printf 'epic-n-closed: FAIL %s\n' "$1" >&2; }
ok() { printf 'epic-n-closed: ok   %s\n' "$1"; }

if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "epic-n-closed: the base commit $base is not in this checkout, so nothing can be compared to it (fetch the full history)" >&2
  exit 2
fi
if [ "$tip" != "WORKTREE" ] && ! git rev-parse --verify --quiet "$tip^{commit}" >/dev/null; then
  echo "epic-n-closed: the tip commit $tip is not in this checkout, so the epic's own migrations cannot be told from a later epic's (fetch the full history)" >&2
  exit 2
fi

# 1. The canonical container contract. A release version alone is not a move
# (scripts/release/contract-unchanged.sh holds the rule for all three checks).
moved="$(bash "$(dirname "$0")/contract-unchanged.sh" "$base")"
if [ -n "$moved" ]; then
  bad "the canonical container contract moved since $base (a release version bump alone is not a move):$(printf '\n         %s' $moved)"
else
  ok "the canonical container contract is an empty diff against $base, apart from the release version"
fi

# 2. The epic's migrations, and no others. The list is 0015 (the schema, #989,
# required) and 0019 (encrypt existing consents, #1302, owner decision: it stays
# its own migration; allowed, not required, so this can merge before it). Any
# other new migration, and any change to or removal of an existing one, fails.
if [ "$tip" = "WORKTREE" ]; then
  added="$(git diff --name-only --diff-filter=A "$base" -- core/migrations; git ls-files --others --exclude-standard -- core/migrations)"
else
  added="$(git diff --name-only --diff-filter=A "$base" "$tip" -- core/migrations)"
fi
touched="$(git diff --name-only --diff-filter=MDRT "$base" -- core/migrations)"
want15="core/migrations/0015_retained_copy_encryption.sql"
want19="core/migrations/0019_encrypt_existing_consents.sql"
unexpected="$(printf '%s\n' "$added" | grep -vxF -e "$want15" -e "$want19" | grep -v '^$' || true)"
if printf '%s\n' "$added" | grep -qxF "$want15" && [ -z "$unexpected" ] && [ -z "$touched" ]; then
  ok "core/migrations gained only the epic's own (0015, and 0019 if present) and nothing else moved"
else
  bad "core/migrations must gain exactly $want15 (and may also gain $want19) and nothing else, and change nothing. Added: [$(printf '%s' "$added" | tr '\n' ' ')] Not allowed: [$(printf '%s' "$unexpected" | tr '\n' ' ')] Changed or removed: [$(printf '%s' "$touched" | tr '\n' ' ')]"
fi

# 3. The matrix and the captures.
if [ "${EPIC_N_SKIP_GO:-0}" = "1" ]; then
  echo "epic-n-closed: skipped the Go closing check (EPIC_N_SKIP_GO=1)"
else
  # No `grep -q`: under pipefail it quits at the first match, tee takes SIGPIPE on the
  # rest of the output and a passing run reads as a failure (#1407).
  if (cd core && GOWORK=off RETND_EPIC_N_CLOSURE=1 go test -count=1 -run 'TestEpicNIsClosed' ./tests/encryptiongate/ -v 2>&1 | tee /dev/stderr | grep -- '--- PASS: TestEpicNIsClosed' >/dev/null); then
    ok "no BLOCKED row, every proof resolves, every listed picture exists"
  else
    bad "the closing check in core/tests/encryptiongate did not pass (output above)"
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "epic-n-closed: $fails failure(s). EPIC N is not closed, so this release does not go out." >&2
  exit 1
fi
echo "epic-n-closed: ok"

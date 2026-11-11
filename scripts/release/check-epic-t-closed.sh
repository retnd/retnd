#!/usr/bin/env bash
# The release's check that EPIC T (encryption suite agility, #1310) is closed
# (#1320). Run by the require-epic-t-closed job in .github/workflows/release.yml,
# and by hand with the same command:
#
#     bash scripts/release/check-epic-t-closed.sh
#
# It refuses, naming each failure, unless all of this holds:
#
#   1. the canonical deployment is an EMPTY diff against the commit the epic
#      started from: container/compose.yaml, the compose files under
#      apps/*/compose/, apps/unraid/template/, apps/truenas/catalog/, the four
#      distribution/packaging/*.json and apps/common/platform/capabilities/.
#      A suite is a property of the bytes a copy holds, so nothing this epic
#      ships changes how a container is run, and an untracked file in one of
#      those places counts as a change. The release version is set aside
#      (#1331): a cut moves the image tag in all of them and canonical.json's
#      release record (image.tag, reference, published, note), and that is
#      not a change to how a container runs, so a cut alone passes;
#   2. the epic added exactly its two migrations, 0020_encryption_key_suites.sql
#      (T1.2, the key's suite) and 0021_encryption_default_suite.sql (T1.4, the
#      build default it last ran under), and nothing else under core/migrations/
#      moved;
#   3. the closing mode of core/tests/encryptiongate passes for EPIC T's
#      matrix: no BLOCKED row in docs/conformance/epic-t-matrix.md, every proof
#      it cites resolves, every PASS row cites a mutation that exists.
#
# It needs a checkout with history back to EPIC_T_BASE.
# scripts/tests/epic-t-closure.test.sh drives this script in throwaway
# repositories, once per failure and once for a clean run.
#
#   EPIC_T_ROOT     repository to check (default: the one this script is in)
#   EPIC_T_BASE     the commit the epic started from (default: 477aec5a, the
#                   merge of #1302 that put all of EPIC N on main, which is the
#                   tree the EPIC T specification was measured on)
#   EPIC_T_SKIP_GO  1 leaves out check 3's Go run (the self-test sets it; the
#                   Go checker has its own lie table)
set -uo pipefail

root="${EPIC_T_ROOT:-$(cd "$(dirname "$0")/../.." && pwd)}"
base="${EPIC_T_BASE:-477aec5a}"
cd "$root" || exit 2

fails=0
bad() { fails=$((fails + 1)); printf 'epic-t-closed: FAIL %s\n' "$1" >&2; }
ok() { printf 'epic-t-closed: ok   %s\n' "$1"; }

if ! git rev-parse --verify --quiet "$base^{commit}" >/dev/null; then
  echo "epic-t-closed: the base commit $base is not in this checkout, so nothing can be compared to it (fetch the full history)" >&2
  exit 2
fi

# 1. The canonical deployment.
paths=(
  container/compose.yaml
  ':(glob)apps/*/compose/**'
  apps/unraid/template
  apps/truenas/catalog
  distribution/packaging/canonical.json
  distribution/packaging/compliance.json
  distribution/packaging/conformance.json
  distribution/packaging/submission.json
  apps/common/platform/capabilities
)
changed="$(git diff --name-only "$base" -- "${paths[@]}"; git ls-files --others --exclude-standard -- "${paths[@]}")"
# A release cut moves the image tag in every one of these files and
# canonical.json's release record (image.tag, image.reference,
# image.published, image.note), and none of that changes how a container is
# run (#1331). So a file counts as moved when it is new, gone, or differs from
# its base once those are set aside: every ghcr.io/retnd/retnd:X.Y.Z (or
# retnd/retnd:X.Y.Z) reference reads as one placeholder, and canonical.json's
# four release fields are dropped before the two sides are compared. Anything
# else, a port, a path, a registry, a tag that is not a release version, a
# field outside the release record, is still a move and is named.
moved=""
for f in $changed; do
  if ! git cat-file -e "$base:$f" 2>/dev/null || [ ! -e "$f" ]; then
    moved="$moved $f"
    continue
  fi
  if ! git show "$base:$f" | python3 -c '
import json, re, sys
release = re.compile(r"((?:ghcr\.io/)?retnd/retnd):[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.]+)?(?![0-9A-Za-z.-])")
def norm(text, name):
    if name.endswith("distribution/packaging/canonical.json"):
        try:
            doc = json.loads(text)
        except ValueError:
            return text
        image = doc.get("image")
        if isinstance(image, dict):
            for k in ("tag", "reference", "published", "note"):
                image.pop(k, None)
        text = json.dumps(doc, sort_keys=True, indent=1)
    return release.sub(r"\1:<release>", text)
name = sys.argv[1]
with open(name, encoding="utf-8", errors="surrogateescape") as fh:
    now = fh.read()
sys.exit(0 if norm(sys.stdin.read(), name) == norm(now, name) else 1)
' "$f"; then
    moved="$moved $f"
  fi
done
if [ -n "$moved" ]; then
  bad "the canonical container contract moved since $base (a release version bump alone is not a move):$(printf '\n         %s' $moved)"
else
  ok "the canonical container contract is an empty diff against $base, apart from the release version"
fi

# 2. The epic's two migrations, and no other.
added="$(git diff --name-only --diff-filter=A "$base" -- core/migrations; git ls-files --others --exclude-standard -- core/migrations)"
touched="$(git diff --name-only --diff-filter=MDRT "$base" -- core/migrations)"
want20="core/migrations/0020_encryption_key_suites.sql"
want21="core/migrations/0021_encryption_default_suite.sql"
unexpected="$(printf '%s\n' "$added" | grep -vxF -e "$want20" -e "$want21" | grep -v '^$' || true)"
if printf '%s\n' "$added" | grep -qxF "$want20" && printf '%s\n' "$added" | grep -qxF "$want21" && [ -z "$unexpected" ] && [ -z "$touched" ]; then
  ok "core/migrations gained only the epic's own 0020 and 0021 and nothing else moved"
else
  bad "core/migrations must gain exactly $want20 and $want21 and nothing else, and change nothing. Added: [$(printf '%s' "$added" | tr '\n' ' ')] Not allowed: [$(printf '%s' "$unexpected" | tr '\n' ' ')] Changed or removed: [$(printf '%s' "$touched" | tr '\n' ' ')]"
fi

# 3. The matrix.
if [ "${EPIC_T_SKIP_GO:-0}" = "1" ]; then
  echo "epic-t-closed: skipped the Go closing check (EPIC_T_SKIP_GO=1)"
else
  if (cd core && GOWORK=off RETND_EPIC_T_CLOSURE=1 go test -count=1 -run 'TestEpicTIsClosed' ./tests/encryptiongate/ -v 2>&1 | tee /dev/stderr | grep -q -- '--- PASS: TestEpicTIsClosed'); then
    ok "no BLOCKED row in the EPIC T matrix, and every proof resolves"
  else
    bad "the EPIC T closing check in core/tests/encryptiongate did not pass (output above)"
  fi
fi

if [ "$fails" -ne 0 ]; then
  echo "epic-t-closed: $fails failure(s). EPIC T is not closed, so this release does not go out." >&2
  exit 1
fi
echo "epic-t-closed: ok"

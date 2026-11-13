#!/usr/bin/env bash
# Shared by check-epic-{n,t,u}-closed.sh (#1407): names the files of the
# canonical container contract that moved since a base commit, one per line,
# and prints nothing when none did. Run from inside the repository to check:
#
#     bash scripts/release/contract-unchanged.sh <base>
#
# A release cut moves the image tag in every one of these files and
# canonical.json's release record (image.tag, image.reference,
# image.published, image.note), and none of that changes how a container is
# run (#1331). So a file counts as moved when it is new, gone, or differs from
# its base once those are set aside: every ghcr.io/retnd/retnd:X.Y.Z (or
# retnd/retnd:X.Y.Z) reference reads as one placeholder, and canonical.json's
# four release fields are dropped before the two sides are compared. Anything
# else, a port, a path, a registry, a tag that is not a release version, a
# field outside the release record, is still a move and is named. An untracked
# file in one of those places counts as a change.
set -uo pipefail

base="${1:?usage: contract-unchanged.sh <base commit>}"

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
for f in $changed; do
  if ! git cat-file -e "$base:$f" 2>/dev/null || [ ! -e "$f" ]; then
    echo "$f"
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
    echo "$f"
  fi
done
exit 0

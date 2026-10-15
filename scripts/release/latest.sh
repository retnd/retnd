#!/usr/bin/env bash
# The image's `latest` tag (issue #1168).
#
# ghcr.io/retnd/retnd:latest is a convenience for humans and quick pulls. It
# is not a thing to install a host from: compose files, the installer and
# the docs that name an image keep pinning the version's index digest, and
# docs/install.md says so. What this script guarantees is narrower:
#
#   * `latest` is the SAME index digest as the version tag it follows, moved
#     with `docker buildx imagetools create`, never by rebuilding, so the
#     cosign signature and SBOM attestation (both keyed by digest) verify
#     for the two names alike;
#   * `latest` only moves forward. A version takes it only when it is the
#     highest non-prerelease semver among the published GitHub Releases
#     (itself included), so a 0.5.x backport cut after 0.6.x never takes it
#     from 0.6.x, and a prerelease never takes it at all.
#
# Subcommands, each taking the bare version (0.6.4, no leading v):
#
#   should-be-latest V  print `true` or `false` on stdout. release.yml uses
#                       the same answer for the image and for the GitHub
#                       Release's --latest flag, so the two cannot disagree.
#   move V              the publish path. Apply the rule, and when V wins,
#                       point latest at the digest of the V tag in the
#                       registry. When V does not win, say so and change
#                       nothing.
#   repoint V           the workflow_dispatch backfill (tag_latest_for). The
#                       same move without a build, and stricter: it refuses
#                       a V that is not the highest release, a V with no
#                       published image, and a V whose registry digest is not
#                       the index_digest container/release-manifest.json
#                       records for it.
#   verify V            the post-publish check. Exit 1 unless latest resolves
#                       to the same digest as the V tag.
#
# Exit status: 0 done (or nothing to do), 1 a refusal or a failed check,
# 2 could not decide (the release list or the registry could not be read, or
# V is not a version). Nothing here guesses: an unreadable answer refuses.
#
# Environment: IMAGE_REPO (default ghcr.io/retnd/retnd), LATEST_MANIFEST
# (default container/release-manifest.json in this checkout), and gh's own
# GH_TOKEN / GH_REPO. Needs gh, docker buildx and python3.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
IMAGE_REPO="${IMAGE_REPO:-ghcr.io/retnd/retnd}"
MANIFEST="${LATEST_MANIFEST:-$REPO_ROOT/container/release-manifest.json}"

die() { # <status> <message>
  local rc="$1"; shift
  printf 'latest.sh: %s\n' "$*" >&2
  exit "$rc"
}

version="${2:-}"
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] \
  || die 2 "'${version}' is not a version (want X.Y.Z or X.Y.Z-pre, no leading v)"

# digest_of <tag>: the index digest the registry holds for that tag, or
# nothing when there is no such tag.
digest_of() {
  local out
  out="$(docker buildx imagetools inspect "${IMAGE_REPO}:$1" --format '{{.Manifest.Digest}}' 2>/dev/null)" || return 1
  out="$(printf '%s' "$out" | tr -d '[:space:]')"
  [[ "$out" =~ ^sha256:[0-9a-f]{64}$ ]] || return 1
  printf '%s' "$out"
}

# is_highest <version>: print true/false. A prerelease is false without
# asking anyone. Otherwise the answer is whether any published, non-draft,
# non-prerelease release has a higher X.Y.Z. The version's own release may
# or may not exist yet, so it is added to the field rather than relied on.
is_highest() {
  local v="$1" list
  if [[ "$v" == *-* ]]; then
    echo false
    return 0
  fi
  list="$(gh release list --limit 1000 --json tagName,isPrerelease,isDraft)" \
    || die 2 "could not read the release list, so I cannot tell whether ${v} is the highest release"
  printf '%s' "$list" | python3 -c '
import json, re, sys

def key(v):
    return tuple(int(p) for p in v.split("."))

try:
    releases = json.load(sys.stdin)
except ValueError:
    sys.exit(3)
if not isinstance(releases, list):
    sys.exit(3)
mine = key(sys.argv[1])
for r in releases:
    if r.get("isPrerelease") or r.get("isDraft"):
        continue
    m = re.fullmatch(r"v(\d+\.\d+\.\d+)", r.get("tagName", ""))
    if m and key(m.group(1)) > mine:
        print("false")
        sys.exit(0)
print("true")
' "$v" || die 2 "the release list was not the JSON array I expected"
}

# point_latest <version> <digest>
point_latest() {
  docker buildx imagetools create -t "${IMAGE_REPO}:latest" "${IMAGE_REPO}@$2"
}

case "${1:-}" in
  should-be-latest)
    is_highest "$version"
    ;;

  move)
    verdict="$(is_highest "$version")"
    if [ "$verdict" != true ]; then
      echo "${version} is not the highest published release (or is a prerelease), so latest stays where it is."
      exit 0
    fi
    digest="$(digest_of "$version")" || die 1 "no published image ${IMAGE_REPO}:${version}, so there is nothing to point latest at"
    point_latest "$version" "$digest"
    echo "latest -> ${IMAGE_REPO}@${digest} (${version})"
    ;;

  repoint)
    verdict="$(is_highest "$version")"
    [ "$verdict" = true ] \
      || die 1 "${version} is not the highest published non-prerelease release, and latest never moves backwards or onto a prerelease"
    digest="$(digest_of "$version")" || die 1 "no published image ${IMAGE_REPO}:${version}, so there is nothing to point latest at"
    [ -f "$MANIFEST" ] || die 1 "${MANIFEST} is missing, so I cannot check ${version}'s digest against what was recorded"
    recorded="$(python3 -c '
import json, sys
m = json.load(open(sys.argv[1]))
print(m.get("version", "") + " " + m.get("index_digest", ""))
' "$MANIFEST")" || die 1 "${MANIFEST} is not readable JSON"
    rec_version="${recorded%% *}"
    rec_digest="${recorded#* }"
    [ "$rec_version" = "$version" ] \
      || die 1 "container/release-manifest.json describes ${rec_version:-no version}, not ${version}, so its digest cannot vouch for ${version}. Run this from the ref that records ${version}."
    [ "$rec_digest" = "$digest" ] \
      || die 1 "the registry holds ${digest} for ${IMAGE_REPO}:${version} but container/release-manifest.json records ${rec_digest:-nothing}. Refusing to point latest at an image the manifest does not describe."
    point_latest "$version" "$digest"
    echo "latest -> ${IMAGE_REPO}@${digest} (${version}, backfilled)"
    ;;

  verify)
    want="$(digest_of "$version")" || die 1 "no published image ${IMAGE_REPO}:${version} to compare latest against"
    got="$(digest_of latest)" || die 1 "${IMAGE_REPO}:latest does not resolve; wanted ${want}"
    [ "$got" = "$want" ] \
      || die 1 "${IMAGE_REPO}:latest resolves to ${got}, not ${version}'s ${want}"
    echo "latest and ${version} are one digest: ${want}"
    ;;

  *)
    die 2 "usage: latest.sh should-be-latest|move|repoint|verify VERSION"
    ;;
esac

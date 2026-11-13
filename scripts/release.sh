#!/usr/bin/env bash
# Cut a release PR end to end (#1375). It never tags, publishes or merges.
#
#   scripts/release.sh <version> --issue <N> [--dry-run] [--yes] [--allow-downgrade]
#
# <version> is X.Y.Z, optionally with a prerelease (0.8.0-rc.1). In order,
# stopping at the first failure:
#
#   1. preconditions: version shape; tag v<version> absent locally and on
#      origin; version greater than image.tag in canonical.json (unless
#      --allow-downgrade); clean tree; on main (or already on the release
#      branch) containing origin/main's tip; a CHANGELOG.md section for the
#      version; the signing key present (path checked, never read) and the
#      real public key committed.
#   2. branch release-prep/v<version> and ONE commit "Release v<version>" made by
#      scripts/release/bump-version.sh (override: RELEASE_BUMP_CMD), before the
#      e2e, so the e2e and the attestation cover the tree that gets released.
#   3. the FULL e2e: scripts/release-e2e.sh (fenced docker, writes the stamp).
#   4. the signed attestation (scripts/release-e2e/attest.sh sign), then
#      verify --expect-version, before anything is committed.
#   5. commit release-attest/, ask once (skip with --yes), push the branch,
#      open the PR "Release v<version>" into `release` with gh. No merge.
#
# The tag v<version> is NOT created here. The existing publish flow
# (docs/release-workflow.md, .github/workflows/release.yml) creates it
# when the release PR merges; this script only prints the name it expects.
#
# The prep branch is release-prep/v<version>, opened INTO the long-lived
# `release` branch. A branch named release/<anything> cannot exist next to
# `release` in git, so the script refuses to run while one does.
#
# --dry-run runs the read-only checks of step 1 and prints the rest; it writes,
# pushes and opens nothing. Test-only: RELEASE_E2E_TEST_CMD (see
# scripts/release-e2e.sh), RELEASE_BUMP_CMD, RELEASE_REMOTE.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"; cd "$root"
# shellcheck disable=SC1091
. "$root/scripts/release-e2e/lib.sh"

die() { echo "release: $*" >&2; exit 1; }
say() { echo "==> release: $*"; }

remote="${RELEASE_REMOTE:-origin}"
version=""; issue=""; dry=0; yes=0; downgrade=0
while [ $# -gt 0 ]; do
  case "$1" in
    --issue) issue="${2:-}"; shift 2 ;;
    --dry-run) dry=1; shift ;;
    --yes) yes=1; shift ;;
    --allow-downgrade) downgrade=1; shift ;;
    -*) die "unknown option $1" ;;
    *) [ -z "$version" ] || die "one version only"; version="$1"; shift ;;
  esac
done
[ -n "$version" ] || die "usage: scripts/release.sh <version> --issue <N> [--dry-run] [--yes] [--allow-downgrade]"
printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$' \
  || die "[$version] is not X.Y.Z or X.Y.Z-prerelease"
if [ -z "$issue" ]; then
  [ "$dry" = 1 ] || die "--issue <N> is needed: the release PR closes the release issue (Closes #N)"
  issue="<N>"
else
  printf '%s' "$issue" | grep -Eq '^[0-9]+$' || die "--issue wants a number"
fi
tag="v$version"
branch="release-prep/v$version"

# git cannot hold `release` and `release/...` together: refuse, point at the rename.
clash="$( { git for-each-ref --format='%(refname:short)' 'refs/heads/release/*'; git ls-remote --heads "$remote" 'refs/heads/release/*' | sed 's|.*refs/heads/|'"$remote"'/|'; } | head -n 1)"
[ -z "$clash" ] || die "a branch named [$clash] exists, and git cannot hold it next to the release branch. Rename it to the prep name: git branch -m ${clash#"$remote"/} release-prep/${clash##*/} (and delete the old name on $remote)."

# Commits go through the repo's hooks, never --no-verify. The only thing set is
# the pre-commit hook's own documented out-loud opt-out (an INCOMPLETE gate
# run, stamped Local-Gate: incomplete): the full e2e is this script's step 3,
# and re-running the whole gate on a mechanical bump would take the better
# part of an hour. After #1370 the hook runs its fast tier and ignores these.
commit() {
  CI_LOCAL_FAST=1 CI_LOCAL_SKIP_JS=1 CI_LOCAL_SKIP_DOCKER=1 CI_LOCAL_SKIP_E2E=1 git commit -q "$@"
}

# --- 1. preconditions (read-only) ---
git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "tag $tag already exists locally"
git ls-remote --exit-code --tags "$remote" "refs/tags/$tag" >/dev/null 2>&1 && die "tag $tag already exists on $remote"

current="$(sed -n 's/^ *"tag": *"\([^"]*\)".*/\1/p' distribution/packaging/canonical.json | head -n 1)"
[ -n "$current" ] || die "no image.tag in distribution/packaging/canonical.json"
head_subject="$(git log -1 --format=%s)"
resuming=0
[ "$(git symbolic-ref --quiet --short HEAD || true)" = "$branch" ] && resuming=1
if [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ]; then
  current_for_cmp="$(git show "HEAD~1:distribution/packaging/canonical.json" | sed -n 's/^ *"tag": *"\([^"]*\)".*/\1/p' | head -n 1)"
else
  current_for_cmp="$current"
fi
greater() { # greater <new> <old>
  n="${1%%-*}"; o="${2%%-*}"
  if [ "$n" != "$o" ]; then [ "$(printf '%s\n%s\n' "$o" "$n" | sort -V | tail -n 1)" = "$n" ]; return; fi
  [ "$1" != "$2" ] || return 1
  case "$1:$2" in
    "$n:$o"*-*) return 0 ;;          # release over its own prerelease
    *-*:"$o") return 1 ;;            # prerelease under its release
    *) [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | tail -n 1)" = "$1" ] ;;
  esac
}
if ! greater "$version" "$current_for_cmp"; then
  [ "$downgrade" = 1 ] || die "$version is not greater than the current $current_for_cmp (--allow-downgrade overrides)"
  say "WARNING: $version is not greater than $current_for_cmp, continuing because of --allow-downgrade"
fi

[ -z "$(git status --porcelain --untracked-files=all)" ] || die "the working tree is dirty"
cur_branch="$(git symbolic-ref --quiet --short HEAD || true)"
[ "$cur_branch" = main ] || [ "$cur_branch" = "$branch" ] || die "run this from main (or from $branch to resume), not [$cur_branch]"
main_sha="$(git ls-remote "$remote" refs/heads/main | cut -f1)"
[ -n "$main_sha" ] || die "could not read $remote/main"
git cat-file -e "$main_sha^{commit}" 2>/dev/null || die "$remote/main is at $main_sha, which is not here yet. git fetch $remote main"
git merge-base --is-ancestor "$main_sha" HEAD || die "HEAD does not contain $remote/main ($main_sha): update first"
grep -Eq "^## \[$(printf '%s' "$version" | sed 's/[.]/\\./g')\]" CHANGELOG.md || die "CHANGELOG.md has no '## [$version]' section"

key="${RELEASE_ATTEST_KEY:-$HOME/.ssh/retnd-release-attest}"
[ -f "$key" ] || die "the signing key $key is missing (docs/release-workflow.md says how to provision it)"
grep -Eq '^ssh-ed25519 AAAA' scripts/release-e2e/release-attest.pub || die "scripts/release-e2e/release-attest.pub is still the placeholder: commit the real public key to main first"
[ "$dry" = 1 ] || command -v gh >/dev/null || die "gh is not on PATH"
say "preconditions ok: $current_for_cmp -> $version, branch $branch, expected tag $tag (this script does not create it)"

if [ "$dry" = 1 ]; then
  say "DRY RUN. Nothing below runs."
  [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ] || say "would create $branch and commit 'Release $tag' via ${RELEASE_BUMP_CMD:-scripts/release/bump-version.sh $version}"
  say "would run the full e2e: bash scripts/release-e2e.sh"
  say "would sign and verify: scripts/release-e2e/attest.sh sign --version $version; verify --expect-version $version"
  say "would commit release-attest/, push $branch to $remote, and run: gh pr create --base $RELEASE_BRANCH --title 'Release $tag' (Closes #$issue)"
  exit 0
fi

# --- 2. branch and the one bump commit ---
if [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ]; then
  say "resuming on $branch, the bump commit is already there"
else
  [ "$resuming" = 1 ] || git switch -c "$branch"
  bump="${RELEASE_BUMP_CMD:-bash scripts/release/bump-version.sh $version}"
  say "bumping: $bump"
  sh -c "$bump"
  git add -A
  commit -m "Release $tag"
fi

# --- 3. the full e2e ---
say "full e2e"
bash scripts/release-e2e.sh

# --- 4. attestation, verified before it goes anywhere ---
say "signing the attestation"
bash scripts/release-e2e/attest.sh sign --version "$version"
bash scripts/release-e2e/attest.sh verify --expect-version "$version"

# --- 5. commit, confirm, push, open the PR ---
tree="$(re_tree_id HEAD)"
stamp="$(re_stamp_file "$tree")" || die "no green e2e stamp for the tree being attested ($tree)"
grep -qx "tree=$tree" "$stamp" || die "the stamp and the attested tree differ"
host="$(sed -n 's/^host=//p' "$stamp")"; when="$(sed -n 's/^time=//p' "$stamp")"
git add -- release-attest
commit -m "Add the signed e2e attestation for $tag" -m "Tree $tree, e2e on $host at $when."
if [ "$yes" != 1 ]; then
  printf 'Push %s to %s and open the release PR into %s? [y/N] ' "$branch" "$remote" "$RELEASE_BRANCH"
  read -r ans || ans=n
  case "$ans" in y|Y|yes) ;; *) die "stopped before the push. The attestation commit is local on $branch." ;; esac
fi
git push -u "$remote" "$branch"
body="$(mktemp "${TMPDIR:-/tmp}/release-pr.XXXXXX")"; trap 'rm -f "$body"' EXIT
{
  echo "I cut $tag from main on $branch: one commit bumps the version, the full e2e ran on that tree, and the last commit adds the signed attestation (release-attest/e2e.json, checked against the key in scripts/release-e2e/release-attest.pub)."
  echo
  echo "E2E stamp: tree $tree, host $host, $when. Merging this is what lets the existing publish flow create the tag $tag; I did not tag or publish anything."
  echo
  echo "Closes #$issue"
} >"$body"
gh pr create --base "$RELEASE_BRANCH" --head "$branch" --title "Release $tag" --body-file "$body"
say "done. Review the PR, do not merge from here. Expected tag after the publish flow: $tag"

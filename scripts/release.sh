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
#      key line for it committed in provenance/allowed_signers.
#   2. branch release-prep/v<version> and ONE commit "Release v<version>" made by
#      scripts/release/bump-version.sh (override: RELEASE_BUMP_CMD), before the
#      e2e, so the e2e and the attestation cover the tree that gets released.
#      PREPARED ON MAIN: when canonical.json already says <version> (unpublished)
#      and CHANGELOG.md has its section, as 0.8.0 does once its cut is on main,
#      there is nothing to bump. The branch is created at main's tip with no bump
#      commit, so the release PR's gate tree is main's and the local-gate record
#      made on main's tip stays valid. Only release-attest/ is committed on it.
#      Still in step 1, before anything is written, it FAILS FAST on the two
#      things that would otherwise surface only after the e2e or in the hosted
#      run (#1672): scripts/release/precut.sh --version <version> (parity,
#      compliance drift, the packaging suite, the EPIC closing checks, the
#      manifest commit), and PUBLISH=true scripts/release/verify-local-gate.sh
#      on the tree being released. With a bump commit that tree only exists
#      after step 2, so there the record is checked right after the bump.
#   3. the FULL e2e: scripts/release-e2e.sh (fenced docker, writes the stamp).
#      REUSED instead when a green stamp already exists for exactly this tree
#      (scripts/release-e2e/lib.sh re_stamp_valid: the default command, a real
#      host, this gate), in the real stamp directory (never the test-only one),
#      and younger than STAMP_MAX_AGE_H below. That is what lets the runbook
#      start scripts/release-e2e.sh at the freeze, beside the gate.
#   4. the signed attestation (scripts/release-e2e/attest.sh sign), then
#      verify --expect-version, before anything is committed.
#   5. commit release-attest/, ask once (skip with --yes), push the branch,
#      open the PR "Release v<version>" into `release` with gh. No merge.
#
# The tag v<version> is NOT created here. It is created by the publish flow of
# .github/workflows/release.yml, which only runs when Actions is enabled
# (docs/release-workflow.md); this script only prints the name it expects.
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
# How old a green e2e stamp may be and still stand in for the e2e (step 3).
STAMP_MAX_AGE_H=24
e2e_why="$(mktemp "${TMPDIR:-/tmp}/release-why.XXXXXX")"; trap 'rm -f "$e2e_why"' EXIT
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

# Commits go through the repo's hooks, plainly: the hook runs its fast tier and
# stamps the commit. The full e2e is this script's step 3.
commit() {
  git commit -q "$@"
}

# --- 1. preconditions (read-only) ---
git rev-parse -q --verify "refs/tags/$tag" >/dev/null && die "tag $tag already exists locally"
git ls-remote --exit-code --tags "$remote" "refs/tags/$tag" >/dev/null 2>&1 && die "tag $tag already exists on $remote"

current="$(sed -n 's/^ *"tag": *"\([^"]*\)".*/\1/p' distribution/packaging/canonical.json | head -n 1)"
[ -n "$current" ] || die "no image.tag in distribution/packaging/canonical.json"
head_subject="$(git log -1 --format=%s)"
resuming=0
[ "$(git symbolic-ref --quiet --short HEAD || true)" = "$branch" ] && resuming=1
# Prepared on main: the version is already set, unpublished, and has a changelog section.
prepared=0
if [ "$current" = "$version" ] && ! grep -Eq '^ *"published": *true' distribution/packaging/canonical.json \
   && grep -Eq "^## \[$(printf '%s' "$version" | sed 's/[.]/\\./g')\]" CHANGELOG.md; then
  prepared=1
fi
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
if [ "$prepared" = 1 ]; then
  say "$version is already set in canonical.json: prepared on main, so no bump commit"
elif ! greater "$version" "$current_for_cmp"; then
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
[ -f "$key" ] || die "the signing key $key is missing (docs/release-workflow.md says where it lives)"
grep -Eq '^retnd-release namespaces="[^"]*retnd-e2e-attest[^"]*" ssh-ed25519 AAAA' provenance/allowed_signers || die "provenance/allowed_signers has no key line for retnd-release and the retnd-e2e-attest namespace: commit the public key to main first"
[ "$dry" = 1 ] || command -v gh >/dev/null || die "gh is not on PATH"
say "preconditions ok: $current_for_cmp -> $version, branch $branch, expected tag $tag (this script does not create it)"

# Fail fast (#1672): what would otherwise be found after the e2e, or by the hosted run.
say "precut: bash scripts/release/precut.sh --version $version --base $main_sha"
bash scripts/release/precut.sh --version "$version" --base "$main_sha" || die "precut.sh is red (above). Fix it on main first: nothing was branched, run or pushed."
gate_ok=0
check_gate() {
  say "local gate record: PUBLISH=true bash scripts/release/verify-local-gate.sh $(git rev-parse HEAD)"
  PUBLISH=true bash scripts/release/verify-local-gate.sh "$(git rev-parse HEAD)" \
    || die "the signed local gate record does not cover this tree (above), so the release would be refused at publish. Record the gate for this tree first (docs/release-workflow.md)."
  gate_ok=1
}
# The tree being released is HEAD's when nothing is bumped (prepared on main, or resuming after the bump).
if [ "$prepared" = 1 ] || { [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ]; }; then check_gate; fi

if [ "$dry" = 1 ]; then
  say "DRY RUN. Nothing below runs."
  if [ "$prepared" = 1 ]; then say "would create $branch at the tip of main with no bump commit (prepared on main)"
  else [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ] || say "would create $branch and commit 'Release $tag' via ${RELEASE_BUMP_CMD:-scripts/release/bump-version.sh $version}"; fi
  [ "$gate_ok" = 1 ] || say "would check the local gate record on the bump commit: PUBLISH=true bash scripts/release/verify-local-gate.sh HEAD"
  say "would reuse a green e2e stamp for the release tree when one is under ${STAMP_MAX_AGE_H}h old, else run the full e2e: bash scripts/release-e2e.sh"
  say "would sign and verify: scripts/release-e2e/attest.sh sign --version $version; verify --expect-version $version"
  say "would commit release-attest/, push $branch to $remote, and run: gh pr create --base $RELEASE_BRANCH --title 'Release $tag' (Closes #$issue)"
  exit 0
fi

# --- 2. branch and the one bump commit ---
if [ "$prepared" = 1 ]; then
  if [ "$resuming" != 1 ]; then
    [ "$(git rev-parse HEAD)" = "$main_sha" ] || die "prepared on main means branching at $remote/main's tip ($main_sha), and HEAD is $(git rev-parse HEAD)"
    git switch -c "$branch"
  fi
  say "prepared on main: $branch is at the main tip, no bump commit"
elif [ "$resuming" = 1 ] && [ "$head_subject" = "Release $tag" ]; then
  say "resuming on $branch, the bump commit is already there"
else
  [ "$resuming" = 1 ] || git switch -c "$branch"
  bump="${RELEASE_BUMP_CMD:-bash scripts/release/bump-version.sh $version}"
  say "bumping: $bump"
  sh -c "$bump"
  git add -A
  commit -m "Release $tag"
fi

[ "$gate_ok" = 1 ] || check_gate

# --- 3. the full e2e, or a green stamp for exactly this tree ---
reusable_stamp() { # prints the stamp path, or why there is none on stderr
  local t="$1" f when then_s now_s
  f="$(re_stamp_dir)/$t.stamp"
  [ -f "$f" ] || { echo "none for tree $t in $(re_stamp_dir)" >&2; return 1; }
  re_stamp_valid "$f" "$t" 0 || { echo "$f is not a valid green stamp for this tree, the default command and this gate" >&2; return 1; }
  when="$(sed -n 's/^time=//p' "$f")"
  printf '%s' "$when" | grep -Eq '^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$' || { echo "$f has a time I cannot read ($when)" >&2; return 1; }
  then_s="$(date -u -d "$when" +%s 2>/dev/null || date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$when" +%s 2>/dev/null)" \
    || { echo "$f has a time I cannot read ($when)" >&2; return 1; }
  now_s="$(date -u +%s)"
  [ "$then_s" -le $((now_s + 300)) ] || { echo "$f is dated in the future ($when)" >&2; return 1; }
  [ $((now_s - then_s)) -le $((STAMP_MAX_AGE_H * 3600)) ] || { echo "$f is older than ${STAMP_MAX_AGE_H}h ($when)" >&2; return 1; }
  printf '%s' "$f"
}
e2e_tree="$(re_tree_id HEAD)"
if reuse="$(reusable_stamp "$e2e_tree" 2>"$e2e_why")"; then
  say "reusing the green e2e stamp $reuse (host $(sed -n 's/^host=//p' "$reuse"), $(sed -n 's/^time=//p' "$reuse")): no second e2e for the same tree"
else
  say "no reusable e2e stamp ($(cat "$e2e_why")), so running the full e2e"
  bash scripts/release-e2e.sh
fi

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
body="$(mktemp "${TMPDIR:-/tmp}/release-pr.XXXXXX")"; trap 'rm -f "$body" "$e2e_why"' EXIT
{
  if [ "$prepared" = 1 ]; then
    echo "I cut $tag from main on $branch: the version was already set on main, so there is no bump commit, the full e2e ran on main's tree, and the one commit adds the signed attestation (release-attest/e2e.json, checked against the key in provenance/allowed_signers)."
  else
    echo "I cut $tag from main on $branch: one commit bumps the version, the full e2e ran on that tree, and the last commit adds the signed attestation (release-attest/e2e.json, checked against the key in provenance/allowed_signers)."
  fi
  echo
  echo "E2E stamp: tree $tree, host $host, $when. Merging this does not tag or publish by itself: the publish flow of release.yml does that, and only when Actions is enabled. Before merging, run both verifies by hand (docs/release-branch.md). I did not tag or publish anything."
  echo
  echo "Closes #$issue"
} >"$body"
gh pr create --base "$RELEASE_BRANCH" --head "$branch" --title "Release $tag" --body-file "$body"
say "done. Review the PR, do not merge from here. Expected tag after the publish flow: $tag"

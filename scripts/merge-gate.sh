#!/usr/bin/env bash
# The pre-merge check (#1361): run the fast tier against a pull request's
# head and post the result as a commit status, from this machine.
#
#   scripts/merge-gate.sh <pr-number>                 # run, then post the status
#   scripts/merge-gate.sh <pr-number> --dry-run       # run, print what would be posted
#   scripts/merge-gate.sh <pr-number> --repo OWNER/NAME --context NAME
#
# GitHub Actions is off for this repository on purpose, so nothing on
# GitHub runs a check in front of `main`. All 28 PRs of EPIC N and EPIC T
# went in through the merge button carrying `Local-Gate: unverified`.
# Branch protection can only require a status somebody posts, so this is
# the somebody: it fetches the PR head, checks it out in a throwaway
# worktree, runs scripts/ci-fast.sh over the paths the PR changed against
# its merge base (plus the anchors and contract drift checks, which always
# run), and posts `retnd/local-gate` on the head commit through `gh`.
#
#   success   the fast tier ended "ci-local: ok" (exit 0)
#   failure   it failed (exit 1), or ended INCOMPLETE (exit 3): a check
#             that applied could not run, and an incomplete run is not a
#             pass. The description says which.
#
# The script from THIS checkout runs the checks, not the PR's copy of
# scripts/ci-fast.sh, so a PR cannot loosen the gate that judges it. The
# check scripts it calls (check-anchors.sh and the rest) are the PR's own,
# because they are what the PR's tree has to satisfy.
#
# It never touches branch protection or any repository setting: turning the
# requirement on is the owner's one command, in docs/reference/merge-gate.md.
# Posting needs a `gh` login that can write commit statuses on the repository.
#
# The status is on the PR HEAD commit. A push to the PR makes a new head and
# the old status does not carry over, so run it again after the last push.
#
# Exit status: 0 when the state is success, 1 when it is failure, 2 for a
# usage or environment problem. The worktree is removed on every exit.
set -euo pipefail

usage() {
  echo "usage: scripts/merge-gate.sh <pr-number> [--dry-run] [--repo OWNER/NAME] [--context NAME]" >&2
  exit 2
}

pr=""
repo=""
context="retnd/local-gate"
dry=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) dry=1 ;;
    --repo)
      [ $# -ge 2 ] || usage
      repo="$2"
      shift
      ;;
    --context)
      [ $# -ge 2 ] || usage
      context="$2"
      shift
      ;;
    -h | --help) usage ;;
    -*) usage ;;
    *)
      [ -z "$pr" ] || usage
      pr="$1"
      ;;
  esac
  shift
done
case "$pr" in '' | *[!0-9]*) usage ;; esac

here="$(cd "$(dirname "$0")" && pwd)"
cd "$here/.."

for tool in git gh; do
  command -v "$tool" >/dev/null 2>&1 || { echo "merge-gate: $tool is not on PATH." >&2; exit 2; }
done
gh auth status >/dev/null 2>&1 || { echo "merge-gate: gh is not logged in (gh auth status)." >&2; exit 2; }

if [ -z "$repo" ]; then
  repo="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
fi

state="$(gh pr view "$pr" -R "$repo" --json state -q .state)"
if [ "$state" != "OPEN" ]; then
  echo "merge-gate: PR #$pr is $state, not OPEN; there is nothing to gate." >&2
  exit 2
fi
head_oid="$(gh pr view "$pr" -R "$repo" --json headRefOid -q .headRefOid)"
base_ref="$(gh pr view "$pr" -R "$repo" --json baseRefName -q .baseRefName)"

# The head as GitHub holds it, checked against what the API just said, so a
# push between the two cannot post a verdict about a commit that was not run.
git fetch -q origin "pull/$pr/head"
head_sha="$(git rev-parse FETCH_HEAD)"
if [ "$head_sha" != "$head_oid" ]; then
  echo "merge-gate: PR #$pr moved while it was being fetched ($head_oid, then $head_sha). Run it again." >&2
  exit 2
fi
git fetch -q origin "$base_ref"
base_sha="$(git rev-parse FETCH_HEAD)"
merge_base="$(git merge-base "$base_sha" "$head_sha")"

scratch="$(mktemp -d "${TMPDIR:-/tmp}/merge-gate-pr$pr.XXXXXX")"
tree="$scratch/tree"
cleanup() {
  git worktree remove --force "$tree" >/dev/null 2>&1 || true
  rmdir "$scratch" 2>/dev/null || true
}
trap cleanup EXIT

git worktree add -q --detach "$tree" "$head_sha"
echo "==> merge-gate: PR #$pr, head ${head_sha:0:12}, base $base_ref (merge base ${merge_base:0:12}), checked out at $tree"

log="$scratch/run.log"
set +e
CI_LOCAL_FAST_JS_INSTALL=1 bash "$here/ci-fast.sh" --root "$tree" --base "$merge_base" 2>&1 | tee "$log"
status="${PIPESTATUS[0]}"
set -e

if [ "$status" -eq 0 ]; then
  gh_state=success
  description="Fast tier green on ${head_sha:0:12}"
elif [ "$status" -eq 3 ]; then
  gh_state=failure
  first="$(sed -n 's/^        - //p' "$log" | head -n 1 | tr -d '\r')"
  description="Fast tier INCOMPLETE: ${first:-something that applied did not run}"
else
  gh_state=failure
  step="$(sed -n 's/^==> ci-local: FAILED (\(.*\))$/\1/p' "$log" | tail -n 1)"
  description="Fast tier FAILED: ${step:-see the local run}"
fi
# GitHub caps a status description at 140 characters.
description="$(printf '%s' "$description" | tr '\n' ' ' | cut -c1-140)"

echo "==> merge-gate: $context is $gh_state on ${head_sha:0:12}: $description"
if [ "$dry" = 1 ]; then
  echo "==> merge-gate: --dry-run, so nothing was posted. It would run:"
  echo "    gh api -X POST repos/$repo/statuses/$head_sha -f state=$gh_state -f context=$context -f description=<above>"
else
  gh api -X POST "repos/$repo/statuses/$head_sha" \
    -f state="$gh_state" -f context="$context" -f description="$description" >/dev/null
  echo "==> merge-gate: posted."
fi

[ "$gh_state" = success ]

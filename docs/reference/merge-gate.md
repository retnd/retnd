# The pre-merge check

Nothing on GitHub runs a check in front of `main`: Actions is off on
purpose (#1260), and `main` has no branch protection. Between releases the
only gate was the pre-commit hook, and a hook can be bypassed. All 28 PRs of
EPIC N and EPIC T went in through the merge button with
`Local-Gate: unverified` in their body, and `main` went red (#1361).

`scripts/merge-gate.sh <pr>` is the one check that stands between a PR and
`main` without Actions. It runs on a machine of ours, runs the fast tier
(`scripts/ci-fast.sh`, see "Tiers" in `docs/reference/local-gate.md`) over
what the PR changes, and posts the result as a commit status through `gh`.
Branch protection can then require that status.

It checks the merge result, not the head on its own: the script merges the
PR's base branch into a throwaway worktree of the PR head and runs the fast
tier on that, so a PR that is green alone but breaks once the base has moved
(or once another PR landed) is red. A base that does not merge cleanly is a
failure without running anything. The status itself is posted against the PR
head commit, which is the one protection looks at.

## Running it

```
scripts/merge-gate.sh 1363                # run, then post retnd/local-gate on the PR head
scripts/merge-gate.sh 1363 --dry-run      # run, print what would be posted
```

It fetches `pull/<n>/head`, checks it out in a throwaway worktree (removed on
every exit), merges the base into it, and runs the checks from this checkout's copy of
`scripts/ci-fast.sh`, so a PR cannot loosen the gate that judges it. The
paths it checks are the ones the PR changes relative to the target branch; the anchors check and the contract drift check run whatever
they are. A JS workspace the PR touches is installed in that worktree first
(`CI_LOCAL_FAST_JS_INSTALL=1`).

| state | when |
| --- | --- |
| `success` | the fast tier ended `ci-local: ok` |
| `failure` | it failed, or ended INCOMPLETE (a check that applied could not run: no `golangci-lint`, a Docker-backed package with no daemon, a `go.mod` or `go.sum` change). The description says which |

The status is on the PR head commit, so a push needs a new run. It needs a
`gh` login that can write commit statuses on the repository, and it
changes nothing else: no branch protection, no repository setting.

The live-issue citation test in `core/tests/compat` does not run here. It
belongs to the full gate on the release PR.

## Turning the requirement on (owner, once)

This is deliberately not done by the script and not part of any PR. Until
you run it, `merge-gate.sh` posts a status nobody requires.

Classic branch protection, requiring the status and nothing else (no review
requirement, admins can still override with the merge button):

```
gh api -X PUT repos/retnd/retnd/branches/main/protection --input - <<'JSON'
{
  "required_status_checks": { "strict": true, "contexts": ["retnd/local-gate"] },
  "enforce_admins": false,
  "required_pull_request_reviews": null,
  "restrictions": null
}
JSON
```

Or in the web UI: Settings, Branches, add a rule for `main`, tick "Require
status checks to pass before merging", and add `retnd/local-gate`. (The
status only appears in the picker after the script has posted it once.)

Things worth knowing before you do:

- Keep `"strict": true` (the "Require branches to be up to date before
  merging" box in the web UI). The status was earned by a merge with the
  base as it was when the script ran. Without strict, two stacked or parallel
  PRs that are each green can still break `main` together, which is the
  pattern #1361 describes. With it, GitHub refuses the merge button until the
  PR is current with `main`, and you run the script again on the new head.

- Nothing posts the status by itself. Whoever is about to merge runs
  `scripts/merge-gate.sh <pr>` first, after the PR's last push. A PR with no
  status cannot be merged while the rule is on.
- `"enforce_admins": true` makes the rule bind you too. Leave it `false`
  while you are getting used to it.
- To switch it off again:
  `gh api -X DELETE repos/retnd/retnd/branches/main/protection`.
- A stacked PR (base is another PR's branch, not `main`) is not covered by a
  rule on `main`; run the script on it anyway, it gates against its own base.
- If Actions ever comes back, the same `scripts/ci-fast.sh` as a one-job
  workflow posting the same context replaces this script without changing the
  rule.

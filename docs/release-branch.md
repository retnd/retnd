# The `release` branch

Issue #258. This is the policy the rest of the release path is built on, so it is
worth reading before `.github/workflows/release.yml` or
`scripts/release/publish-image.sh`, both of which assume it.

For the ordered maintainer checklist, commands, automated post-publish recording
and failure handling, use [`release-workflow.md`](release-workflow.md). This document
defines the branch policy that checklist must preserve.

## What it is

`release` is the publish branch. A push to it runs the release pipeline, and that
pipeline publishes: it pushes the canonical multi-architecture image to `ghcr.io`,
signs it keylessly and attests its SBOM. Nothing else in this repository triggers on
a push.

It is also the pointer at what has actually been published. `git checkout release`
gives you the tree the current release was cut from, and `container/release-manifest.json`
on it names the commit the shipped binaries were built from.

## Three rules

**1. Append-only.** Never force-pushed, never rebased, never squash-merged into.

This is the rule the other two exist to protect, and it is the direct answer to
issue #174. That issue was a manifest pinning `c51a07f`, a commit made on a feature
branch that GitHub's squash merge rewrote out of existence, leaving the manifest
naming a SHA nobody could check out. Every parity check stated as "matches the
release manifest" was comparing against a build that could not be reproduced.

The fix was an ancestry rule against `origin/main`, and `origin/main` works for that
purpose for one reason only: it does not rewrite. `release` is in the ancestry list
(`releaseAncestryRefPreference` in `distribution/packaging/matrix.go`) because this
rule gives it the same property. Break this rule and the manifest's reachability
check becomes a check against a moving target, which is worse than no check, because
it looks like one.

**2. Nothing is branched off it.** Work is cut from `main` and merged to `main`.
`release` is a destination, never a starting point.

**3. Only a release cut lands on it.** The version bump in
`distribution/packaging/canonical.json` and the adapter metadata `derive.go` holds to
it, the regenerated `container/release-manifest.json`, and the regenerated compliance
bundle under `provenance/`. Nothing else.

A commit on `release` that changes engine, adapter or UI source is a commit that
publishes something nobody reviewed on `main`, and the push trigger means it does so
without anyone typing anything. Rule 3 is what makes rule 1's audit trail worth
having: merging to `release` is a deliberate act with a reviewable diff, which is
what the pipeline's typed-confirmation input used to stand in for while a manual
dispatch was the only way in.

## What is enforced, and where

- `TestReleaseManifestPinsACommitThisHistoryCanReach` (`distribution/packaging`) fails
  the build when the committed manifest pins a commit no rewrite-free ref can reach.
  It asks `origin/main`, `origin/release`, the local `main` and `release`, and then
  `HEAD` as the weakest form, and it names which one answered so a fallback never
  reads as the full-strength check.
- `TestResolveReachableAncestryRef_AsksEveryRewriteFreeRef` is its control. It builds
  a commit `origin/main` cannot reach and `origin/release` can, which is the exact
  shape of a first release cut, and requires `origin/release` to answer.
- `scripts/release/publish-image.sh`'s guard 2b asks the same question again at
  publish time, because a registry tag is the one artifact that regenerating a file
  cannot correct.
- `scripts/release/verify-manifest-parity.sh` rebuilds each architecture and compares
  the extracted binaries' SHA-256 against the manifest, before the push. That is the
  half a SHA comparison cannot answer, and it is what replaced the guard that
  compared the manifest's commit to `HEAD` (#260).

## What is policy rather than code

Rules 1 and 2 are branch protection, not a test. A clone cannot observe that the
remote branch was force-pushed yesterday, so no check in this repository can enforce
them.

They are a GitHub ruleset instead, named "release branch is append-only", active,
targeting `refs/heads/release`, carrying `deletion`, `non_fast_forward` and
`pull_request` (merge commits only). `deletion` and
`non_fast_forward` block a force push and block deleting the branch. `pull_request`
is what makes rule 3 an enforced fact rather than a stated one: it blocks every direct
push to `release`, ordinary ones included, so the only way a commit lands there is
through a pull request, and it restricts the merge method to a real
merge commit, so merging a PR can never become the squash or rebase merge rule 1
forbids.

Its approval count is zero, and this paragraph used to say one. Zero is the honest
setting on a repository with one maintainer, because GitHub will not let an author
approve their own pull request and a count of one would deadlock every release cut;
what makes rule 3 hold is the pull request being required at all, not a review nobody
can give. The number is written down here rather than described, so that turning it up
the day there is a second maintainer is a decision somebody makes on purpose.

There is no `required_status_checks` rule any more, and this paragraph used to
describe one. Issue #575 made `release gate`, the aggregating job of
`.github/workflows/ci.yml`, a required check on every pull request into `release`.
#1260 deleted that workflow: GitHub Actions gates nothing here, the pre-commit hook
running `scripts/ci-local.sh` is the only gate, for `main` and for releases alike,
and a required context naming a check no run produces would block the merge button
for good. The ruleset keeps the three rules above and drops that one.

### The half that is not a settings page

What makes any of this mean anything about the code now lives in the repository
rather than in a settings page. Release prep ends by running
`scripts/release/record-local-gate.sh` on the `main` tip being released. That runs
the full gate in a fresh detached worktree at `HEAD`, with `CI=1`, and writes
`provenance/local-gate.json` and `provenance/local-gate.log` only when the run exits
0 with an empty skip ledger. The record names the tree it ran on (the commit's tree
with exactly those two files left out, computed by `scripts/release/gate-tree.sh`),
the tool versions, the duration and the hash of the log.

`.github/workflows/release.yml` reads that record at publish time. Its
`require-release-gate` job takes `provenance/local-gate.json` off the commit being
published, recomputes the gate tree of that commit with the same script, and refuses
to let `publish` run unless the two agree, the ledger is empty, the schema is the one
it reads, the committed log hashes to what the record says, and the recorded Go is
the Go the workflow builds with (same major.minor). It compares trees rather than
commits because the release pull request lands as GitHub's own merge commit, which
no hook ever ran on; its tree is `main`'s tree whenever `release` is already an
ancestor of `main`, which `merge-back-to-main` keeps true. If anything lands on
`main` between the record and the cut, the hashes differ and the release is refused,
naming both. That is the check working, and the answer is to re-run the recorder on
the new tip.

What it proves is that somebody with push rights ran the full suite on those bytes
and it passed. It is self-asserted: anyone who can write a JSON file can write this
one, the same way anyone could once dispatch `ci.yml` by hand on any ref. What stands
against a forged record is review of the prep pull request, where the record and its
log arrive as a diff, and the log itself, which the record hashes and
`github-release` attaches to the GitHub Release, so forging the record means forging
twenty-five minutes of output rather than one line. Signing the prep commit is the
upgrade path if that is ever not enough. Whether a second person looked is rule 3's
question, and the ruleset is what answers it.

`scripts/tests/release-refuses-an-ungated-publish.test.sh` pulls that job's script out
of the workflow and runs it in throwaway repositories, once per outcome (a matching
record, no record, a different tree, an INCOMPLETE ledger, the wrong Go, an unknown
schema, a log that does not match, overridden, and overridden with no reason), and
`scripts/tests/local-gate-record.test.sh` drives the recorder's refusals and then runs
that same script over what the recorder wrote. Actions is the only thing that ever runs
`release.yml`, so without those the first time any refusal ran would be the day one of
them mattered.

### The emergency override

`Release-Gate-Override: <why>` in the message of the commit being published, with a
reason of at least twelve characters, publishes anyway. The run reports `release gate:
OVERRIDDEN` in its summary, annotates itself with a warning carrying the reason, and
still prints everywhere it looked and what it found there.

That exists because a release process with no override is a release process people
route around, and the route around this one is deleting the job. It is a commit
trailer rather than a dispatch input for the same reason merging beats typing a
string: `release` is append-only, so the trailer and its reason sit in the published
history for good, next to the commit they excused, while a dispatch input lives in a
run log that ages out. It also cannot be added after a commit has landed, and that is
the point rather than a limitation. Overriding is a decision made while cutting, not a
button found afterwards.

A trailer on a commit whose record matches is noise rather than a standing permission.
The record is what allows that release; the override is only ever read when the record
does not pass.

That is a change from the branch's first weeks, when the ruleset carried only
`deletion` and `non_fast_forward` and an ordinary push to `release` was possible with
no diff reviewed by anyone: the redesign's safety argument ("merging is a stronger
act than typing a string") held in the doc but not in the ruleset. Cutting `v0.1.0`
happened under that gap; every cut after it goes through the pull request the
ruleset now requires.

What the tests above enforce is the consequence of the rules holding, which is the
useful half. If the ruleset were removed and the branch rewritten, the reachability
check starts failing, loudly, on the next run rather than months later.

## Cutting a release

1. Get the commit you want to publish onto `main`, green under the pre-commit hook
   (`bash scripts/install-hooks.sh` once per clone if it is not installed).
2. Regenerate the manifest from a clean checkout of that commit:
   `VERSION=<x.y.z> scripts/release/record-release-hashes.sh`. It refuses a commit
   that is not already on main, for #174's reason.
3. Bump the version in `distribution/packaging/canonical.json` and let `derive.go`
   tell you every adapter that has to follow.
4. Regenerate the compliance bundle: `(cd distribution && go run ./cmd/provenance -write)`.
5. Merge all of that to `main`, then on that tip, last, record the gate run:
   `bash scripts/release/record-local-gate.sh`. It refuses a dirty tree, any
   `CI_LOCAL_FAST` or `CI_LOCAL_SKIP_*` variable, and a clone without the hook, runs
   the full gate in a detached worktree, and writes `provenance/local-gate.json` and
   `provenance/local-gate.log` only on a complete green run. Merge those two files to
   `main` through a pull request; that review is what stands behind the record.
6. Prove it before you publish it: dispatch the Release workflow with `publish: false`.
   That path runs every guard and the parity rebuild against the real tree and stops
   before the registry.
7. Open a pull request from `main` into `release` and get it approved. The ruleset
   refuses a direct push, so this is the only way in; merge it with a real merge
   commit (the ruleset refuses squash and rebase, which would break rule 1). There is
   no GitHub check to wait for. Merging is what publishes, and the release run reads
   the record off the merge commit before it pushes anything, so a tree the gate never
   ran on in full, or one that moved after the record was written, stops there rather
   than shipping.
8. Observe `.github/workflows/release.yml` through `merge-back-to-main`. After
   the push, that job reads the registry digests back, records them in the
   manifest, flips `image.published` to true, updates the installer's carried
   digest, regenerates and verifies the bundle, and lands one merge commit on
   `main`. There is no manual post-publish metadata pull request.
9. The same run then builds the CLI tarballs (`cli-binaries`), smoke-tests the macOS
   one (`cli-darwin-smoke`) and creates the `v<x.y.z>` GitHub Release with the four
   tarballs, `SHA256SUMS` and the local-gate log (`github-release`). That release is
   the first and only place the Git tag appears, so nobody tags by hand.
10. Once `publish` has signed and attested the image, `tag-latest` moves
   `ghcr.io/retnd/retnd:latest` to the version's index digest with
   `docker buildx imagetools create` (a copy by digest, so `latest` and the version tag
   are one digest and the signature covers both) and then checks that `latest` resolves
   to that digest, failing the run if it does not. It only does this when the version is
   the highest published non-prerelease release; `github-release` applies the same rule
   to the release page's "Latest" badge (`--latest` or `--latest=false`). Both come from
   `scripts/release/latest.sh`, and `scripts/tests/release-moves-latest.test.sh` drives it.

### Re-pointing `latest` without a release

To move `latest` for a version that is already published (it is how a release cut
before this existed gets its tag), dispatch the Release workflow with `tag_latest_for`
set, from the ref whose `container/release-manifest.json` records that version, which
is `main` once the release has merged back:

```
gh workflow run release.yml --ref main -f tag_latest_for=<x.y.z>
```

Nothing is built, published or signed: `decide` forces `publish` off, the gate and the
compliance job skip, and only `repoint-latest` runs. It refuses a version with no
published image, a version whose registry digest differs from the manifest's
`index_digest`, a version the manifest does not describe, and a version that is not the
highest non-prerelease release (so it cannot move `latest` backwards). It holds
`packages: write` and `contents: read` and no `id-token`, because nothing is signed.

## The first cut, and why it was different

`v0.1.0` could not follow step 1 exactly, and the reason is worth writing down rather
than hiding.

The pipeline that publishes a release is itself part of what `v0.1.0` shipped: the
push trigger (#259), the guard that made the publish script satisfiable at all
(#260), and this policy (#258). None of it was on `main` when the release was cut, so
the tree that published `v0.1.0` was a branch under review rather than `main`.

What was kept, because it is the part that matters: the manifest pins `13ed710`,
which is a commit on `main`, and the parity rebuild proves the published binaries are
that commit's binaries. So the released bytes are reproducible from `main` even
though the tree that pushed them was not yet. Every later cut follows step 1 as
written.

Step 7 also went slightly differently, and for a reason worth keeping. The digests
`ghcr.io` assigned are recorded on the pull request into `main` rather than pushed
straight back to `release`, because a push to `release` publishes: it would rebuild,
re-push bytes the registry already holds under the same digest, and add a second
Sigstore signature for nothing. So `release` records the tree that published, and
`main` records what the registry answered. The next cut fast-forwards past both.

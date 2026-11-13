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

## The local e2e gate on `release` (#1375)

Actions is off for this repository, so the full e2e for a release runs from
committed git hooks, on another machine, in a fenced container. None of it is a
workflow, and no workflow trigger was added or changed.

**The flow.** Work is cut from `main` as before. A release candidate lives on a
prep branch named `release-prep/v<version>`, cut by `scripts/release.sh`, and it
merges into the long-lived `release` branch by pull request. (git cannot hold
`release` together with any branch named `release/...`, so `scripts/release.sh`
refuses to run while one exists, locally or on origin, and tells you the rename.)
If `release` does not exist yet, create it once from the default branch:
`gh api repos/retnd/retnd/git/refs -f ref=refs/heads/release -f sha=$(git rev-parse origin/main)`,
then append a line saying why the gate was skipped to
`$(git rev-parse --git-common-dir)/release-e2e/overrides.log`. What "full e2e" means
here is the repo's own step from `scripts/ci-local.sh`: the retnd-tests browser
suite plus the CLI smoke slice at the pinned sha, `bash scripts/e2e/run-tests-repo-gate.sh`.
That command lives in exactly one place, `RELEASE_E2E_CMD_DEFAULT` in
`scripts/release-e2e/lib.sh`; the runner and this page both refer to it. The
two-machine privileged proof is not part of it (it stays in the full gate).

**What runs when.**

| When | What | Where it lives |
| --- | --- | --- |
| commit on a feature branch | the pre-commit hook's own checks (the fast tier from #1370, with its hooks-path guard). I did not touch it | `.husky/pre-commit` |
| local merge into `release` that auto-commits | the full e2e through the runner; a red run aborts the merge; a green stamp for the same tree is reused | `.husky/pre-merge-commit` |
| push of `refs/heads/release` | refused unless a green stamp exists for the exact tree being pushed | `.husky/pre-push` |
| any other push | passes straight through | `.husky/pre-push` |

`pre-merge-commit` does not run for `merge --no-commit`, squash merges or
fast-forwards. `pre-push` is what covers those. Note that the ruleset on `release`
already forces every real change through a pull request, so in practice the stamp
is what you hold when you push a candidate and what `scripts/release.sh` records.

**The runner** is `scripts/release-e2e.sh`. By hand: `bash scripts/release-e2e.sh`
on a clean checkout of the commit. It refuses a dirty tree, picks the least loaded
x86 host with `/Users/rom/workspace/dh.sh pick x86` (mars or the NAS, never a Mac, so
Playwright never runs on this laptop), and runs the command through
`scripts/ci-docker.sh --host`, which copies the tree to a scratch directory on that
host, runs the container and removes the scratch directory, the image and the cache
volumes on every exit path. The container is fenced: memory is 60% of the host's RAM
capped at 16g, `--memory-swap` equal to it (no swap), and CPUs are two thirds of the
host's (`RELEASE_E2E_MEMORY` and `RELEASE_E2E_CPUS` override). The fence covers the
container the command runs in; the sibling containers the e2e itself starts on that
host's daemon are not inside it. I have not timed a real run on a host yet (no full e2e was run while building this), so the
first one should be watched for duration and peak memory.

**The stamp** is written only on a green exit, to
`$(git rev-parse --git-common-dir)/release-e2e/<tree>.stamp`: tree, commit, command,
host and time. It is never committed. The tree is the one from
`scripts/release/gate-tree.sh`, which leaves out `release-attest/` and
`provenance/local-gate.*`, so adding the attestation or the gate record does not
invalidate it.

**Override.** There is no new flag and nobody should use `--no-verify`. The
existing trailer applies: `Release-Gate-Override: <reason, at least 12 characters>`
in the merge message (pre-merge-commit) or in the message of the commit being
pushed (pre-push). It prints loudly and is appended to
`$(git rev-parse --git-common-dir)/release-e2e/overrides.log`. It is legitimate for
the same reasons it is at publish time (an emergency fix while the e2e hosts are
down), and the reason stays in the append-only history.

**Failure modes.** Dirty tree: the runner refuses (exit 2). Docker host down:
`dh.sh` or `docker info` does not answer, the runner stops with exit 2 and names
`RELEASE_E2E_HOST=mars|nas`. OOM: the container hits its cap, the log says so, the
run is red and no stamp is written; report it, do not retry in a loop. A red run or
Ctrl-C leaves no stamp.

**How `pre-push` judges.** It reads each stdin line from the end (the local ref
can contain spaces, as in `HEAD^{/some message}`, which shifts every field from
the front), and for a push of `refs/heads/release` it takes the gate files from
the REMOTE release tip, never from the tree being pushed: the runner, `lib.sh`
(which holds the e2e command), `attest.sh`, `gate-tree.sh` and `ci-docker.sh`,
extracted with `git archive` to a tar file in a temp directory that a trap
removes. A stamp counts only if it is non-empty, has exactly the eight keys once
each, names the tree it is filed under, says `green`, records the trusted command,
a host from the allowlist (`mars`, `nas`, or several joined with `+`) and the
`gate_sha256` of those files. Refused unless skipped as below: a stamp that fails
any of that; a push that changes a gate file or the hooks; a push that touches
`release-attest/` beyond `e2e.json` and `e2e.json.sig`; a remote with no `release`
branch (the one-time bootstrap); a remote tip that is not in this clone ("fetch
first"); a remote tip with no gate in it (a first adoption of this gate needs the
skip). `DH_UNFENCED` is unset by the runner and the hook.

**Copies of the gate are raw.** The hooks read the gate files as blobs
(`git ls-tree` plus `git cat-file blob`, modes and symlinks kept), never with
`git archive` or `git checkout-index`, because those obey the tree's own
`.gitattributes`: `export-ignore` could drop a file and `export-subst` or an eol
filter could rewrite one. The runner ships the tree with `git ls-files`, which
those attributes do not touch.

**Malformed ref lists.** A newline inside a refspec can split one stdin line in
two and plant a fake `refs/heads/release` line naming a local-only commit whose
gate says `exit 0`. `pre-push` refuses any line with fewer than four fields and
any push where more than one line names `refs/heads/release`. No skip lifts
either.

**The skip.** `RELEASE_E2E_SKIP='<reason, 12+ characters>'` in the environment, or
the existing `Release-Gate-Override` trailer on the pushed commit (or in the merge
message for `pre-merge-commit`). Each hook parses it and writes the record itself,
inline, sourcing nothing from the working tree or the pushed tree, so a topic
cannot neuter the logging. One line is appended to
`$(git rev-parse --git-common-dir)/release-e2e/overrides.log` with `time`, `hook`,
`reason`, `sha`, `old_tip`, `ref`, `waived` (what the skip bypassed), `user` and
`branch`; control characters and newlines in the reason are flattened to spaces,
so a reason cannot forge a second line. A skip waives the e2e as well as the
gate checks.

**An existing `release` without the gate (this repo's case).** `release` already
exists on origin and carries none of these files, so the first push or merge that
brings the gate in is refused ("no release gate to judge with" for `pre-push`,
"HEAD does not carry the release gate scripts" for `pre-merge-commit`) until it is
skipped once and recorded: `RELEASE_E2E_SKIP='adopt the release e2e gate on the
existing release branch' git push origin <branch>:release`, or the trailer. After
that merge, every later push is judged by the gate on the remote tip.

**Install.** `bash scripts/install-hooks.sh` (it checks and installs all four
hooks). `RELEASE_E2E_TEST_CMD` replaces the command and runs it locally for the
self-test only. It prints a TEST-ONLY warning and writes its stamp to
`release-e2e-TEST-ONLY/`, a directory the hooks never read. Only the self-test
sets `RELEASE_E2E_ALLOW_TEST_STAMPS` (to the repository's git dir, a bare `1` does
nothing), which is what makes such a stamp count. Green is the command's exit
status and nothing else: no output is parsed.

**Residuals, stated plainly.**
- The hooks come from your working tree (`core.hooksPath=.husky`). A topic branch
  that edits `.husky/pre-push` or `.husky/pre-merge-commit` can change what runs on
  the next push from that checkout; the gate-file check catches it only when that
  topic is what gets pushed to `release`, and a merge of such a topic into `main` is
  not gated at all.
- `pre-merge-commit` runs the gate scripts from `HEAD` of `release`, but the hook
  file itself is the working-tree copy.
- The skip waives the e2e too. It is logged, not prevented.
- A merge made in the GitHub web UI runs no local hook.
- The real boundary is GitHub branch protection on `release` (pull request
  required, no force push) plus `release.yml`'s `require-release-gate`, which
  refuses to publish a tree without a matching gate record (see above). The local
  hooks and the signed attestation make a bad merge hard to do by accident and easy
  to spot in review; they do not replace that.
- `release-attest/` and `provenance/local-gate.*` are outside the stamp key on
  purpose, so adding the attestation or the gate record does not invalidate the e2e.
- The only pre-push logic in this repo is the `release` check. It starts no
  Playwright and no suite, so `scripts/release.sh`'s push needs no extra skip. The two
  commits `scripts/release.sh` makes go through the pre-commit hook with its
  documented out-loud opt-out (`CI_LOCAL_FAST`, `CI_LOCAL_SKIP_JS`,
  `CI_LOCAL_SKIP_DOCKER`, `CI_LOCAL_SKIP_E2E`), never `--no-verify`.

Self-test: `bash scripts/tests/release-e2e.test.sh` (fake command, throwaway key,
stand-in `gh`, `dh.sh` and `ci-docker.sh`; it never starts the real suite).

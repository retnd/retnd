# Release workflow

This is the maintainer runbook for cutting and publishing a retnd release. It
turns the policy in [`release-branch.md`](release-branch.md) and the supply-chain
design in
[`compliance/release-provenance.md`](compliance/release-provenance.md) into one
ordered procedure.

## Release model

A release crosses three states:

1. **Prepared on `main`.** The version, image references, binary hashes and
   compliance bundle describe a candidate, but no registry identity exists yet.
   `image.published` is `false`; every `registry_digest` and `index_digest` in
   `container/release-manifest.json` is `null`.
2. **Published from `release`.** A merge commit reaching `release` starts
   `.github/workflows/release.yml`. The workflow requires the `release gate`,
   rebuilds both architectures, checks manifest parity, pushes the image and
   signs each pushed package path with the workflow's keyless OIDC identity.
3. **Recorded on `main`.** Registry digests are read back, committed with
   `image.published: true`, copied into the installer pin, and folded into a
   regenerated provenance bundle.

`main` is where work and release metadata are prepared. `release` is an
append-only publish destination: never branch from it, rebase it, force-push it
or put product changes directly on it. A semantic version already pushed to the
registry is immutable by policy. A correction gets a new version.

## Files that define a release

| File | Authority |
| --- | --- |
| `distribution/packaging/canonical.json` | Version, canonical image reference, optional mirror, architectures and published state |
| `container/release-manifest.json` | Source commit, binary hashes, per-architecture registry digests and index digest |
| `provenance/release-provenance.json` | Derived release, signing and compliance record |
| `provenance/sbom.spdx.json` | SPDX SBOM distributed with the release |
| `provenance/checksums.txt` | Digests of the distributed artifacts |
| `scripts/install/install_docker_host.py` | Installer-carried release version and immutable index digest |
| `CHANGELOG.md` | Operator-visible release notes and upgrade effects |

Do not edit generated provenance files independently. Generate them together
with `distribution/cmd/provenance`.

## 1. Prepare the candidate on `main`

Start from a full, clean checkout. The hash recorder and parity checks need
complete history and Docker Buildx.

```bash
git fetch origin main release
git switch main
git pull --ff-only origin main
git status --short
export VERSION=x.y.z
```

`git status --short` must be empty. Use the version without a leading `v`.
Create or identify the release issue before opening either release-related pull
request; every pull request body uses `Closes #N`.

Review `CHANGELOG.md`, then update the image block in
`distribution/packaging/canonical.json`:

- `image.tag` is `$VERSION`;
- `image.reference` ends in `:$VERSION`;
- `image.published` is `false`;
- if `image.mirror` exists, its reference carries the same tag.

Do not copy registry digests from the preceding release. A candidate has no
registry identity until this candidate is pushed.

Record the binaries built from the selected `main` commit:

```bash
VERSION="$VERSION" bash scripts/release/record-release-hashes.sh
```

The command builds `linux/amd64` and `linux/arm64`, extracts `/retnd` and
`/retnd-web`, and writes `container/release-manifest.json`. It refuses a commit
that is not reachable from `origin/main`, a dirty image input, missing history,
or a non-`HEAD` `COMMIT`. The generated registry and index digests remain
`null`.

Run the packaging suite. It names every adapter whose checked metadata still
disagrees with `canonical.json`; update those adapter files rather than weakening
the derivation check.

```bash
(
  cd distribution
  go test ./packaging -count=1
  go run ./cmd/provenance -write
  go run ./cmd/provenance
)
bash scripts/release/check-published-provenance.sh
sha256sum -c provenance/checksums.txt
```

Commit this preparation on a branch, open a pull request into `main`, and let the
normal CI gate pass. The manifest may pin the `main` commit immediately before
the metadata commit: that is the reviewed source tree whose binaries it records.
After the preparation pull request merges, work from the resulting `main` tip.

## 2. Run the hosted dry run

Dispatch the Release workflow against `main` without publishing:

```bash
gh workflow run release.yml --ref main \
  -f publish=false \
  -f confirm=""
```

Watch the run in GitHub Actions or with `gh run watch <run-id> --exit-status`.
The dry run:

- regenerates and compares the compliance bundle;
- verifies `provenance/checksums.txt`;
- runs the distribution supply-chain tests;
- rebuilds both architectures and verifies binary parity with the manifest;
- executes every publish guard with `DRY_RUN=1`.

It does **not** log in to GHCR, install Cosign, push, sign, or merge anything
back to `main`. A dry-run failure is still private: fix it on `main`, merge that
fix, and dispatch a new dry run.

## 3. Open and merge the release pull request

Open a pull request whose head is `main` and whose base is `release`:

```bash
gh pr create --base release --head main \
  --title "Release ${VERSION}" \
  --body "Closes #N"
```

The diff must be a release cut already reviewed on `main`. Do not add product,
adapter or UI changes directly to this pull request.

Wait for the required check named exactly `release gate`. Merge with a real merge
commit. Squash and rebase merges violate the append-only ancestry contract and
are blocked by the release-branch ruleset.

Merging is the publish action. The push to `release` starts the Release workflow;
do not follow it with a `workflow_dispatch` publish of the same version.

## 4. Observe the publishing run

The jobs run in this order:

1. `decide` sets `publish=true` for the push to `release`.
2. `require-release-gate` finds a successful `release gate` check on the
   published commit or its pull-request parent.
3. `compliance-artifacts` regenerates the compliance bundle, refuses drift and
   uploads the bundle as a workflow artifact.
4. `publish` verifies manifest parity, runs the publish guards, performs one
   multi-platform Buildx push for every declared package path, reads the index
   digest back and signs each path with Cosign.
5. `merge-back-to-main` merges the published release commit into `main` after a
   successful publish. A conflict fails rather than choosing a resolution.

Save the run URL and its digest output on the release issue. The publisher emits
the index digest and candidate platform digests, but the committed record is
based on a fresh registry read, not on the push's stdout.

### Current SBOM-attestation boundary

The workflow and provenance design require an SPDX attestation, but the current
`publish_image.py` implementation invokes `cosign sign` and contains no
`cosign attest` invocation. A green publishing run therefore proves that the
compliance bundle was generated and uploaded and that the image was signed; it
does **not** by itself prove that an SBOM attestation exists in the registry.
Check for the attestation explicitly before declaring that acceptance item
complete. Do not convert the uploaded workflow artifact into a claim about a
registry attestation.

## 5. Record what the registry holds

Inspect the canonical image after the push:

```bash
IMAGE="ghcr.io/retnd/retnd:${VERSION}"
docker buildx imagetools inspect "$IMAGE"
docker buildx imagetools inspect "$IMAGE" --raw
```

Read the index digest and each architecture's manifest digest from the registry.
Then update these values together on a branch from the current `main`:

1. `container/release-manifest.json`
   - set each architecture's `registry_digest`;
   - set `index_digest`.
2. `distribution/packaging/canonical.json`
   - set `image.published` to `true`.
3. `scripts/install/install_docker_host.py`
   - set `CARRIED_RELEASE` to `$VERSION`;
   - set `CARRIED_RELEASE_DIGEST` to the manifest's `index_digest`.
4. Regenerate the compliance bundle:

   ```bash
   (cd distribution && go run ./cmd/provenance -write)
   ```

Run the focused consistency checks:

```bash
(
  cd distribution
  go test ./packaging -count=1
  go run ./cmd/provenance
)
python3 scripts/install/test_install_docker_host.py
bash scripts/release/check-published-provenance.sh
sha256sum -c provenance/checksums.txt
```

The packaging tests refuse all half-recorded states: published without digests,
digests while unpublished, a version mismatch, or an installer pin different
from the manifest's index digest.

Open the post-publish metadata pull request into `main` with `Closes #N` in its
body. If the release issue was closed by the release pull request, use the issue
that tracks recording and verification. Once published facts have landed on
`main`, `check-published-provenance.sh` freezes the published flag, recorded
version, architectures and registry digests against later rewrites.

## 6. Verify the public release

Verify by immutable digest, not only by the mutable tag:

```bash
DIGEST="sha256:..." # container/release-manifest.json index_digest
cosign verify \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity \
  'https://github.com/retnd/retnd/.github/workflows/release.yml@refs/heads/release' \
  "ghcr.io/retnd/retnd@${DIGEST}"
```

Also confirm:

- every declared architecture appears in `imagetools inspect`;
- every declared mirror resolves to the same release digest;
- the compliance bundle workflow artifact contains `LICENSE`, `NOTICE`,
  `provenance/` and `container/release-manifest.json`;
- the installer accepts the canonical tag and pins it to the recorded index
  digest;
- the post-publish metadata pull request is green and merged.

The workflow does not create a Git tag or a GitHub Release. Do not report either
as published unless a separate, observed process created it.

## Failure and recovery

| Failure | Action |
| --- | --- |
| Dry run fails | Fix `main`; rerun the dry run. Nothing public changed. |
| `release gate` is missing, red, skipped or forged | Do not publish. Fix and rerun CI. |
| `merge-back-to-main` conflicts | Merge the published `release` commit into `main` manually with `--no-ff`; do not rewrite `release`. |
| Push succeeds but signing fails | Preserve the run and registry digest, stop, and treat recovery as a release incident. Do not rebuild or move the same semantic tag casually: a second Buildx run may produce a different OCI digest. |
| Public image is defective | Keep the published evidence immutable and cut a new patch version. Never delete or force-push `release` to hide it. |
| Emergency gate override is unavoidable | Put `Release-Gate-Override: <reason>` with at least twelve characters in the release commit before it lands. The override is audited in append-only history; it is not a post-failure button. |

A manual dispatch with `publish=true` is an exceptional recovery mechanism. It
publishes only from `refs/heads/release` and requires `confirm` to equal the tag
in `canonical.json`. Because it can rebuild an already-pushed tag, do not use it
as the normal release path or as an automatic retry.

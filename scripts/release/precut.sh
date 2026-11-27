#!/usr/bin/env bash
# What the hosted Release run has refused on before, asked locally in a few
# minutes, before the long gate and before release.sh's e2e (#1672, #1451).
#
#   bash scripts/release/precut.sh [--version X.Y.Z] [--base <ref>] [--no-parity]
#
# Every red Release run since September was one of these: manifest parity,
# compliance drift, a stale pinned packaging test, or a check that needed one
# command to see. Each one is cheap to ask here and costs an hour or more to
# learn from a hosted run. In order, after refusing a dirty tree (exit 2):
#
#   1. CHANGELOG.md has a '## [<version>]' section. <version> defaults to
#      image.tag in distribution/packaging/canonical.json.
#   2. container/release-manifest.json records <version> and a commit that is
#      an ancestor of <base> (default origin/main), as record-release-hashes.sh
#      requires and the publish job assumes.
#   3. the image inputs that moved since that commit are listed. With
#      --no-parity and nothing moved, the image build is skipped; with
#      --no-parity and something moved, that is a refusal naming the files.
#   4. compliance artifacts: (cd distribution && go run ./cmd/provenance -write)
#      must leave NOTICE and provenance/ as committed (the regenerated files
#      are put back either way), and provenance/checksums.txt must match.
#   5. (cd distribution && go test ./packaging/ -count=1), the suite the
#      compliance-artifacts job runs.
#   6. the EPIC N, T and U closing checks and check-published-provenance.sh,
#      the same commands release.yml runs.
#   7. verify-manifest-parity.sh (an amd64 image build, needs docker), unless
#      skipped by 3.
#
# Every check runs even after one is red, so one pass lists everything to fix.
# Exit 0 all green, 1 something refused, 2 it could not start. It reads no
# environment knobs; release.sh runs it in its first step.
set -uo pipefail

die() { echo "precut: $*" >&2; exit 2; }
root="$(git rev-parse --show-toplevel)" || die "not inside a git checkout"
cd "$root" || die "cannot cd to $root"

version=""; base=origin/main; parity=1
while [ $# -gt 0 ]; do
  case "$1" in
    --version) version="${2:-}"; shift 2 ;;
    --base) base="${2:-}"; shift 2 ;;
    --no-parity) parity=0; shift ;;
    *) die "unknown option $1 (usage: scripts/release/precut.sh [--version X.Y.Z] [--base <ref>] [--no-parity])" ;;
  esac
done
[ -z "$(git status --porcelain --untracked-files=all)" ] || die "the working tree is dirty. The compliance check regenerates files into it, so commit or stash first."
[ -n "$version" ] || version="$(sed -n 's/^ *"tag": *"\([^"]*\)".*/\1/p' distribution/packaging/canonical.json 2>/dev/null | head -n 1)"
[ -n "$version" ] || die "no --version and no image.tag in distribution/packaging/canonical.json"
git rev-parse -q --verify "$base^{commit}" >/dev/null || die "$base is not a commit here. git fetch origin main"

reds=0
ok() { echo "    ok   $1"; }
red() { reds=$((reds + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }
run() { # run <name> <cmd...>: one check, its output kept only when it fails
  local name="$1" out t0; shift; t0=$SECONDS
  if out="$("$@" 2>&1)"; then ok "$name ($((SECONDS - t0))s)"; else red "$name" "$(printf '%s\n' "$out" | tail -n 20)"; fi
}

echo "==> precut: version $version, base $base ($(git rev-parse --short "$base")), HEAD $(git rev-parse --short HEAD)"

# 1. changelog
if grep -Eq "^## \[$(printf '%s' "$version" | sed 's/[.]/\\./g')\]" CHANGELOG.md 2>/dev/null; then ok "CHANGELOG.md has '## [$version]'"
else red "CHANGELOG.md has no '## [$version]' section"; fi

# 2. the release manifest
manifest=container/release-manifest.json
m_version="$(sed -n 's/^  "version": *"\([^"]*\)".*/\1/p' "$manifest" 2>/dev/null | head -n 1)"
m_commit="$(sed -n 's/^  "commit": *"\([0-9a-f]*\)".*/\1/p' "$manifest" 2>/dev/null | head -n 1)"
if [ "$m_version" = "$version" ]; then ok "$manifest records version $version"
else red "$manifest records version ${m_version:-(none)}, not $version" "record the hashes on main first: bash scripts/release/record-release-hashes.sh"; fi
moved=""
if [ -z "$m_commit" ] || ! git cat-file -e "$m_commit^{commit}" 2>/dev/null; then
  red "$manifest records commit ${m_commit:-(none)}, which is not here" "git fetch origin, or record the hashes again on main"
elif git merge-base --is-ancestor "$m_commit" "$base"; then
  ok "the manifest commit $(git rev-parse --short "$m_commit") is an ancestor of $base"
  # 3. what moved in the image's inputs since then: container/Dockerfile and
  # its build context as .dockerignore leaves it (core, apps, ui/shared, the
  # licence files the runtime stage copies).
  moved="$(git diff --name-only "$m_commit" HEAD -- container/Dockerfile .dockerignore core apps ui/shared LICENSE NOTICE provenance/third-party-licenses.json)"
  if [ -z "$moved" ]; then ok "no image input moved since the manifest commit"
  else echo "    note $(printf '%s\n' "$moved" | wc -l | tr -d ' ') image input file(s) moved since the manifest commit, so only the parity build can say the image is unchanged:"; printf '%s\n' "$moved" | head -n 20 | sed 's/^/         | /'; fi
else
  red "the manifest commit $m_commit is not an ancestor of $base" "the hashes were recorded off main (#174). Record them again on main: bash scripts/release/record-release-hashes.sh"
fi

# 4. compliance artifacts, as the compliance-artifacts job checks them
prov_out="$( (cd distribution && GOWORK=off go run ./cmd/provenance -write) 2>&1)"; prov_st=$?
drift="$(git status --porcelain -- NOTICE provenance)"
git checkout -q -- NOTICE provenance 2>/dev/null
if [ "$prov_st" != 0 ]; then red "go run ./cmd/provenance -write failed (exit $prov_st)" "$(printf '%s\n' "$prov_out" | tail -n 20)"
elif [ -n "$drift" ]; then red "the committed compliance artifacts are not what this tree generates" "$drift
regenerate and commit: (cd distribution && go run ./cmd/provenance -write)"
else ok "the compliance artifacts are what this tree generates"; fi
untracked="$(git status --porcelain --untracked-files=all -- NOTICE provenance)"
[ -z "$untracked" ] || red "the regeneration left files git does not track; remove them by hand" "$untracked"
if command -v sha256sum >/dev/null 2>&1; then sums=(sha256sum -c --quiet); else sums=(shasum -a 256 -c --quiet); fi
if out="$("${sums[@]}" provenance/checksums.txt 2>&1)"; then ok "provenance/checksums.txt matches the files"
else red "provenance/checksums.txt does not match the files" "$out"; fi

# 5. the packaging suite (pinned cut tests, manifest integrity, compliance)
run "distribution/packaging suite" sh -c 'cd distribution && GOWORK=off go test ./packaging/ -count=1'

# 6. the hosted closing checks and the published-provenance guard
for c in check-epic-n-closed check-epic-t-closed check-epic-u-closed check-published-provenance; do
  run "$c" bash "scripts/release/$c.sh"
done

# 7. parity
if [ "$parity" = 1 ]; then
  run "verify-manifest-parity (image build)" bash scripts/release/verify-manifest-parity.sh
elif [ -z "$moved" ] && [ -n "$m_commit" ]; then
  echo "    skip parity skipped (--no-parity): no image input moved since the manifest commit"
else
  red "--no-parity, but image inputs moved since the manifest commit, so parity has to run" "$(printf '%s\n' "$moved" | head -n 20)"
fi

if [ "$reds" = 0 ]; then echo "precut: green. The hosted checks that refused releases before pass on this tree."; exit 0; fi
echo "precut: $reds check(s) red. Fix them on main before the gate: every fix moves the gate tree." >&2
exit 1

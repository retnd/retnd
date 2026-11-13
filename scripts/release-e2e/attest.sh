#!/usr/bin/env bash
# The signed e2e attestation a release PR carries (#1375).
#
#   scripts/release-e2e/attest.sh sign   --version X.Y.Z
#   scripts/release-e2e/attest.sh verify [--commit <rev>] [--expect-version X.Y.Z]
#
# sign: needs a green stamp (scripts/release-e2e.sh) for the tree of HEAD,
# and the private key at $RELEASE_ATTEST_KEY (default ~/.ssh/retnd-release-attest).
# It only checks the key file exists and hands its PATH to ssh-keygen -Y sign;
# it never reads, prints or creates a private key. Writes release-attest/e2e.json
# and release-attest/e2e.json.sig.
#
# verify: decides from committed files alone. The public key is read from
# scripts/release-e2e/release-attest.pub AT THE COMMIT being verified, and the
# placeholder there fails closed. Checks: signature, verdict green, the tree
# in the attestation equals the commit's tree (release-attest/ and
# provenance/local-gate.* left out, scripts/release/gate-tree.sh), and the
# version in the attestation equals --expect-version AND the image tag in
# distribution/packaging/canonical.json at that commit. Without --commit it
# reads the files in the working tree against HEAD (pre-commit check).
# Exit 0 valid, 1 invalid (every reason printed), 2 usage or missing input.
#
# It proves a holder of the key signed a green run of that tree. It cannot
# stop someone with write access editing this script in the same PR; the
# reviewer of the release PR is that check.
set -euo pipefail
root="$(git rev-parse --show-toplevel)"
cd "$root"
# shellcheck disable=SC1091
. "$root/scripts/release-e2e/lib.sh"
NS=retnd-e2e-attest
IDENT=retnd-release-attest
DIR=release-attest
PUB=scripts/release-e2e/release-attest.pub
die() { echo "attest: $*" >&2; exit 2; }

jget() { sed -n "s/^  \"$1\": \"\(.*\)\",\{0,1\}\$/\1/p" | head -n 1; }
esc() { printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'; }

# The one public-key line from <rev>:PUB, or nothing when it is still the placeholder.
pubkey_at() {
  git show "$1:$PUB" 2>/dev/null | grep -E '^ssh-ed25519 AAAA[A-Za-z0-9+/=]+( .*)?$' | head -n 1 || true
}

cmd="${1:-}"; shift || true
case "$cmd" in
  sign)
    version=""
    while [ $# -gt 0 ]; do case "$1" in --version) version="$2"; shift 2 ;; *) die "unknown option $1" ;; esac; done
    [ -n "$version" ] || die "sign needs --version"
    key="${RELEASE_ATTEST_KEY:-$HOME/.ssh/retnd-release-attest}"
    [ -f "$key" ] || die "the signing key $key is missing. Provision it: ssh-keygen -t ed25519 -f $key, commit the .pub as $PUB."
    [ -n "$(pubkey_at HEAD)" ] || die "$PUB at HEAD is still the placeholder, so nothing could verify a signature. Commit the real public key first."
    [ -z "$(git status --porcelain --untracked-files=all)" ] || die "the working tree is dirty."
    tree="$(re_tree_id HEAD)"
    stamp="$(re_stamp_file "$tree")" || die "no green e2e stamp for tree $tree. Run scripts/release-e2e.sh first."
    mkdir -p "$DIR"
    {
      echo '{'
      echo '  "schema": 1,'
      echo '  "kind": "retnd-e2e-attestation",'
      echo '  "tree_algo": "scripts/release/gate-tree.sh",'
      echo "  \"tree\": \"$tree\","
      echo "  \"commit\": \"$(git rev-parse HEAD)\","
      echo "  \"version\": \"$(esc "$version")\","
      echo "  \"command\": \"$(esc "$(sed -n 's/^command=//p' "$stamp")")\","
      echo "  \"host\": \"$(esc "$(sed -n 's/^host=//p' "$stamp")")\","
      echo "  \"e2e_time\": \"$(esc "$(sed -n 's/^time=//p' "$stamp")")\","
      echo '  "verdict": "green"'
      echo '}'
    } >"$DIR/e2e.json"
    rm -f "$DIR/e2e.json.sig"
    ssh-keygen -Y sign -q -f "$key" -n "$NS" "$DIR/e2e.json" >/dev/null || die "ssh-keygen could not sign (passphrase needs ssh-agent?)."
    echo "attest: signed tree $tree for version $version -> $DIR/e2e.json(.sig)"
    ;;
  verify)
    rev=""; expect=""
    while [ $# -gt 0 ]; do case "$1" in --commit) rev="$2"; shift 2 ;; --expect-version) expect="$2"; shift 2 ;; *) die "unknown option $1" ;; esac; done
    work="$(mktemp -d "${TMPDIR:-/tmp}/attest-verify.XXXXXX")"; trap 'rm -rf "$work"' EXIT
    at="${rev:-HEAD}"
    if [ -n "$rev" ]; then
      git show "$rev:$DIR/e2e.json" >"$work/e2e.json" 2>/dev/null || die "$DIR/e2e.json is not in $rev"
      git show "$rev:$DIR/e2e.json.sig" >"$work/e2e.json.sig" 2>/dev/null || die "$DIR/e2e.json.sig is not in $rev"
    else
      [ -f "$DIR/e2e.json" ] && [ -f "$DIR/e2e.json.sig" ] || die "$DIR/e2e.json(.sig) not found in the working tree"
      cp "$DIR/e2e.json" "$DIR/e2e.json.sig" "$work/"
    fi
    bad=0
    fail() { echo "attest: INVALID: $*" >&2; bad=1; }
    pub="$(pubkey_at "$at")"
    if [ -z "$pub" ]; then
      fail "$PUB at $at holds no ssh-ed25519 key (placeholder fails closed)"
    else
      echo "$IDENT $pub" >"$work/signers"
      ssh-keygen -Y verify -f "$work/signers" -I "$IDENT" -n "$NS" -s "$work/e2e.json.sig" <"$work/e2e.json" >/dev/null 2>&1 \
        || fail "the signature does not verify against $PUB"
    fi
    j="$work/e2e.json"
    [ "$(jget kind <"$j")" = retnd-e2e-attestation ] || fail "kind is not retnd-e2e-attestation"
    [ "$(jget verdict <"$j")" = green ] || fail "verdict is not green"
    want_tree="$(re_tree_id "$at")"
    [ "$(jget tree <"$j")" = "$want_tree" ] || fail "tree $(jget tree <"$j") is not this commit's tree $want_tree: the code changed after the run"
    v="$(jget version <"$j")"
    [ -n "$v" ] || fail "the attestation records no version"
    if [ -n "$expect" ] && [ "$v" != "$expect" ]; then fail "attestation version $v is not the expected $expect"; fi
    tag="$(git show "$at:distribution/packaging/canonical.json" 2>/dev/null | sed -n 's/^ *"tag": *"\([^"]*\)".*/\1/p' | head -n 1)"
    [ "$tag" = "$v" ] || fail "attestation version [$v] disagrees with image.tag [$tag] in distribution/packaging/canonical.json"
    [ "$bad" = 0 ] || exit 1
    echo "attest: valid. tree $want_tree, version $v, host $(jget host <"$j"), time $(jget e2e_time <"$j")"
    ;;
  *) die "usage: attest.sh sign --version X.Y.Z | verify [--commit REV] [--expect-version X.Y.Z]" ;;
esac

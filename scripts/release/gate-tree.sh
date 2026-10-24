#!/usr/bin/env bash
# The tree hash a local-gate record is about (#1260).
#
#   bash scripts/release/gate-tree.sh [<commit-ish>]     # default HEAD
#
# Prints the tree hash of the commit with exactly two paths left out:
# provenance/local-gate.json (the record) and provenance/local-gate.log
# (the gate output the record hashes). Nothing else. The record describes
# the tree the gate ran on, and the record is committed INTO that tree
# afterwards, so the hash has to be taken with the record itself out of
# the picture, or writing the record would move the hash it records.
#
# Why only those two, and not release.yml's whole paths-ignore list: that
# list also carries scripts/install/install_docker_host.py, which is code
# the release ships, and the manifest and canonical.json, which are data
# the release ships. Leaving those out of the comparison would let any of
# them change after the gate ran and still publish. So release prep writes
# the record LAST, after the hashes and the provenance bundle, and
# everything written before it is inside the hash.
#
# This is the ONE implementation, used by both ends:
# scripts/release/record-local-gate.sh writes gate_tree with it, and
# require-release-gate in .github/workflows/release.yml recomputes it on
# the commit being published and refuses on a mismatch. Two copies would
# drift, and a drift here is a refused release on the day of the release.
#
# Git alone, no Go and no Node, so it runs the same on a laptop and on a
# runner. A temporary index is loaded from the commit's tree, the two
# paths are dropped from it, and the result is written back out as a tree
# object. That works on a depth-1 checkout, because it needs the tree and
# blob objects of the one commit and nothing from history. The temporary
# index lives in the git dir rather than in $TMPDIR, so nothing this
# script creates sits outside the repository, and it is removed on every
# exit.
set -euo pipefail

commit="${1:-HEAD}"

# The two paths, named once. The recorder and the release job both call
# this script rather than carrying the list, and
# scripts/tests/local-gate-record.test.sh pins that a change to any other
# file under provenance/ DOES move the hash.
EXCLUDED="provenance/local-gate.json provenance/local-gate.log"

tree="$(git rev-parse --verify --quiet "${commit}^{tree}")" || {
  echo "gate-tree: ${commit} does not name a commit in this repository" >&2
  exit 2
}

git_dir="$(git rev-parse --path-format=absolute --git-dir)"
index="$git_dir/gate-tree-index.$$"
trap 'rm -f "$index"' EXIT

export GIT_INDEX_FILE="$index"
git read-tree "$tree"
# update-index --force-remove takes exact paths, where `git rm --cached`
# takes pathspecs and would also drop a DIRECTORY carrying one of these
# names, which is content and must stay in the hash (#1261 review, item 8).
# shellcheck disable=SC2086
git update-index --force-remove -- $EXCLUDED
git write-tree

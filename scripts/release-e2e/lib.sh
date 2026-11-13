# shellcheck shell=sh disable=SC2034
# Shared by the release hooks, the e2e runner, the attestation tool and
# scripts/release.sh (#1375). POSIX sh, sourced, no side effects.
#
# THE ONE PLACE THE FULL E2E COMMAND LIVES. The runner and
# docs/release-branch.md both point here. It is the repo's own e2e step from
# scripts/ci-local.sh ("browser e2e + CLI smoke, from retnd-tests at the
# pinned sha"), not a new suite.
RELEASE_E2E_CMD_DEFAULT='bash scripts/e2e/run-tests-repo-gate.sh'
RELEASE_BRANCH=release
# retnd's existing override convention (docs/release-branch.md, release.yml):
# a trailer with a reason of at least 12 characters.
OVERRIDE_TRAILER='Release-Gate-Override'
OVERRIDE_MIN=12

re_root() { git rev-parse --show-toplevel; }

# Where the gate's own scripts come from. Unset: the working tree. The
# pre-merge-commit hook sets it to a copy extracted from HEAD, so a branch
# being merged cannot swap the gate for one that says green.
re_tools() { printf '%s' "${RE_TOOLS:-$(re_root)}"; }

# Stamps live in the shared git dir, never in a working tree and never committed.
re_stamp_dir() { printf '%s/release-e2e' "$(git rev-parse --path-format=absolute --git-common-dir)"; }

# The tree a stamp and an attestation are about: scripts/release/gate-tree.sh,
# which leaves out release-attest/ and provenance/local-gate.*. $1 is a
# tree-ish; with none, the index (what a merge is about to commit).
re_tree_id() {
  bash "$(re_tools)/scripts/release/gate-tree.sh" "${1:-$(git write-tree)}"
}

# stdin: a commit message. Prints the override reason when it is long enough.
re_override_reason() {
  r="$(sed -n "s/^$OVERRIDE_TRAILER:[[:space:]]*//p" | tail -n 1)"
  [ "${#r}" -ge "$OVERRIDE_MIN" ] && printf '%s' "$r"
  return 0
}

# The files that ARE the gate. A stamp records their sha256 (gate_sha256) and
# the pre-push hook judges with the copies from the REMOTE release tip, so a
# branch that edits the gate cannot vouch for itself. RE_GATE_FILES is what is
# hashed and extracted; RE_GATE_WATCHED adds the hooks, which are read from the
# working tree and so can only be watched, not replaced.
RE_GATE_FILES='scripts/release-e2e.sh scripts/release-e2e/lib.sh scripts/release-e2e/attest.sh scripts/release/gate-tree.sh scripts/ci-docker.sh'
RE_GATE_WATCHED="$RE_GATE_FILES scripts/release-e2e/release-attest.pub .husky/pre-push .husky/pre-merge-commit"
# The real hosts the runner can name (dh.sh pick x86). A stamp may join several with +.
RE_HOSTS_ALLOWED='mars nas'

re_gate_hash() { # hash of the gate files in $(re_tools)
  t="$(re_tools)"
  for gf in $RE_GATE_FILES; do printf '%s\n' "$gf"; cat "$t/$gf" || return 1; done \
    | if command -v sha256sum >/dev/null 2>&1; then sha256sum; else shasum -a 256; fi | cut -d' ' -f1
}

# Stamps from the test-only command override go to a different directory that
# nothing reads, unless RELEASE_E2E_ALLOW_TEST_STAMPS names this repository's
# git dir exactly (a bare 1 is not enough). Only scripts/tests/release-e2e.test.sh
# sets it; setting RELEASE_E2E_TEST_CMD never makes a stamp count.
re_test_stamp_dir() { printf '%s/release-e2e-TEST-ONLY' "$(git rev-parse --path-format=absolute --git-common-dir)"; }
re_test_stamps_allowed() {
  [ -n "${RELEASE_E2E_ALLOW_TEST_STAMPS:-}" ] || return 1
  [ "$(cd "$RELEASE_E2E_ALLOW_TEST_STAMPS" 2>/dev/null && pwd -P)" = "$(cd "$(git rev-parse --git-common-dir)" && pwd -P)" ]
}

# re_stamp_valid <file> <tree> <test 0|1>: non-empty, exactly these eight keys
# once each and nothing else, filed under its own tree, green, bound to this
# gate, the trusted command, and a real host. A test stamp skips only the
# command and host checks.
re_stamp_valid() {
  f="$1"
  [ -s "$f" ] || return 1
  [ "$(wc -l <"$f" | tr -d ' ')" = 8 ] || return 1
  for k in schema status tree commit command host time gate_sha256; do
    [ "$(grep -c "^$k=" "$f")" = 1 ] || return 1
  done
  [ "$(sed -n 's/^schema=//p' "$f")" = 1 ] || return 1
  [ "$(sed -n 's/^status=//p' "$f")" = green ] || return 1
  [ "$(sed -n 's/^tree=//p' "$f")" = "$2" ] || return 1
  [ -n "$(sed -n 's/^time=//p' "$f")" ] && [ -n "$(sed -n 's/^commit=//p' "$f")" ] || return 1
  [ "$(sed -n 's/^gate_sha256=//p' "$f")" = "$(re_gate_hash)" ] || return 1
  [ "$3" = 1 ] && return 0
  [ "$(sed -n 's/^command=//p' "$f")" = "$RELEASE_E2E_CMD_DEFAULT" ] || return 1
  hosts="$(sed -n 's/^host=//p' "$f")"; [ -n "$hosts" ] || return 1
  for h in $(printf '%s' "$hosts" | tr '+' ' '); do
    case " $RE_HOSTS_ALLOWED " in *" $h "*) ;; *) return 1 ;; esac
  done
}

re_stamp_ok() { re_stamp_file "$1" >/dev/null; }

# re_stamp_file <tree>: prints the path of a valid stamp, or fails.
re_stamp_file() {
  f="$(re_stamp_dir)/$1.stamp"
  if re_stamp_valid "$f" "$1" 0; then printf '%s' "$f"; return 0; fi
  if re_test_stamps_allowed; then
    f="$(re_test_stamp_dir)/$1.stamp"
    if re_stamp_valid "$f" "$1" 1; then printf '%s' "$f"; return 0; fi
  fi
  return 1
}

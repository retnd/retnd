#!/usr/bin/env bash
# The "former name is gone" guard (#1109): no tracked text file in this tree
# contains the name the product had before EPIC R (#885).
#
# check-brand-drift.sh stops NEW old-brand identifiers and carries allowlists
# for the ones a rename is still sweeping. This is the blunter sibling, and it
# has no allowlist at all: after #1109 nothing is allowed to contain the
# previous name, in code, docs, scripts, changelog or tests, so there is
# nothing to pin. Any hit is a failure, reported as file:line.
#
# The name is never written out in this file, or anywhere else in the tree.
# Every spelling is assembled below from two fragments at runtime, the same
# trick core/tests/deploymentdiff uses for apps/<provider>/. That is on
# purpose: a guard that spelled the name would trip over itself, and an
# exclusion for its own path would be the one hole a returning name could
# hide in.
#
# Three spellings, as the issue defines them:
#
#   all upper case                    anywhere, with or without a suffix
#   capitalised, trailing "d" lower   not followed by a lowercase letter
#   capitalised, trailing "D" upper   not followed by a lowercase letter
#   all lower case                    not followed by a lowercase letter
#
# "Not followed by a lowercase letter" is what keeps the look-alikes green:
# BackupDetailPage, BackupDefaultsPage, BackupDomain, openBackupDataPlane and
# backupDataPath all continue with a lowercase letter, so they are not the
# name. scripts/rename/selftest-former-name.sh plants every spelling and every
# look-alike to prove both halves.
#
# `git grep -I` skips binary files, and `--cached` reads the INDEX (#1622), so
# what is looked at is the staged blob of every tracked file, never the
# working tree. That is what the pre-commit hook is about to commit: an
# unstaged clean edit cannot hide a staged hit, and an unstaged hit cannot
# refuse a clean staged blob. In CI, and anywhere the tree is clean, the index
# and the tree are the same bytes. A file that is only intent-to-add (git add
# -N) has an empty blob in the index, so it is not scanned until it is staged.
# Untracked build directories are not in the index, so they cannot make this
# red.
#
# Exit 0: no tracked file contains the name. Exit 1: file:line for each hit.
set -euo pipefail

repo_root="$(git rev-parse --show-toplevel)"
cd "$repo_root"

# Fragments. Joined only here, only at runtime.
stem_up="BACKUP"
stem_cap="Backup"
stem_low="backup"
tail_up="D"
tail_low="d"

former_up="${stem_up}${tail_up}"
former_cap_low="${stem_cap}${tail_low}"
former_cap_up="${stem_cap}${tail_up}"
former_low="${stem_low}${tail_low}"

# POSIX ERE, no lookahead: "not followed by a lowercase letter" is
# ([^a-z]|$).
re="(${former_up}|(${former_cap_low}|${former_cap_up}|${former_low})([^a-z]|\$))"

# The first of two exceptions. core/migrations/0012_workflow_runs.sql carries the name in
# a comment, and that file is sha256-pinned in three places (migrate.go's
# ErrSchemaDrift check, migrationimmutability_test.go and
# migrationanchor_test.go), so editing it would brick every existing database.
# Allowlisted by its exact path and nothing else: no glob, no directory, so a
# new migration or a copy of the file elsewhere is still scanned.
frozen_migration="core/migrations/0012_workflow_runs.sql"

# The other one: the committed gate record's log (#1481). It is the output of
# the browser lane, which quotes spec titles from another repository, and it is
# written by the release recorder, so the tree cannot be edited to drop the
# name from it. Exact path again; provenance/local-gate.json is still scanned.
gate_log="provenance/local-gate.log"

hits="$(git grep --cached -n -I -E -- "$re" -- . ":!${frozen_migration}" ":!${gate_log}" || true)"

if [ -n "$hits" ]; then
  echo "check-no-former-name: FAILED: the previous product name is still in the tree (#1109):" >&2
  # file:line only. Echoing the matched line would write the name into the
  # CI log, which is the one place this guard has no business spelling it.
  printf '%s\n' "$hits" | cut -d: -f1,2 | sed 's/^/  /' >&2
  cat >&2 <<'MSG'
check-no-former-name: nothing may contain the name the project had before
  EPIC R (#885), in any spelling. Say "the previous name" in prose, and use
  the RETND_* / retnd names in code. There is no allowlist for this guard.
MSG
  exit 1
fi

echo "check-no-former-name: ok (no tracked file contains the previous name)"

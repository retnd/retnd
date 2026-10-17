#!/usr/bin/env bash
#
# retnd default recovery-test validator
# =====================================
#
# This is the script retnd hands out as a starting point for a custom
# recovery-test validator. Download it, read it, change the part marked
# "ADAPT HERE" to check what your backups actually contain, and upload it
# again under your own name (Settings > Assurance, or
# `retnd assurance validators add NAME --pattern REGEX --script FILE`).
#
# WHEN IT RUNS
#
#   A recovery test recovers the newest known good copy of a backup into an
#   isolated staging directory, reads it end to end and checks it against
#   the SHA-256 recorded when it was ingested. Only if that matches is a
#   validator run. So by the time this script starts, the bytes are already
#   proved to be the bytes that were backed up. Your job here is the other
#   half: are they USABLE? Does the archive open, does the dump load, does
#   the manifest list what it should.
#
#   Which validator runs for a copy, most specific first:
#     1. a custom validator scoped to the copy's backup set whose pattern
#        matches the artifact's name
#     2. a global custom validator whose pattern matches
#     3. the set's validator_id
#     4. the single script in recovery-test/<source>/<set>
#     5. the built-in structure check (gzip, zstd, tar)
#   Two patterns at the same scope that both match one name end the test as
#   "could not complete" rather than picking one.
#
# WHERE IT RUNS
#
#   As a NAME.local.sh hook: in a fresh container started by the host
#   workflow runner, with bash as the interpreter. The default image is
#   bash:5.2.37-alpine3.21, which has busybox's gzip, tar, unzip and
#   sha256sum but NOT zstd, xz, python or a database client. If your check
#   needs one of those, run the workflow runner with --hook-image naming an
#   image that has it.
#
#   The recovered file sits in the staging directory on the host
#   (assurance.staging_root, by default "recovery-staging" beside the state
#   database). The container can only see it if that directory is given to
#   the runner with --hook-mount. If it is not, RECOVERY_TEST_FILE names a
#   path that does not exist in here, and this script says so and fails.
#
#   It gets no arguments and nothing useful on stdin. Its working directory
#   is a scratch directory of its own ($RETND_WORK_DIR when the runner sets
#   it); write there, never next to the recovered file. The recovered file
#   is removed when the test ends, whatever this script does.
#
#   It has workflows.script_timeout to finish (5 minutes unless the
#   deployment or the set says otherwise). A script that runs over is killed
#   and the test is recorded as FAILED.
#
# WHAT THE UPLOAD CHECK IS NOT
#
#   An upload has to pass the check that gates saving a workflow. That check
#   refuses shell mistakes that are almost always wrong, such as rm -rf on an
#   unset variable. It is a lint and not a sandbox: it does not stop a script
#   from doing whatever its container can do. The script runs with the set's
#   merged workflow environment, the same variables its other hooks get,
#   secrets included if you put them there. Treat uploading a validator the
#   way you treat adding a hook.
#
# WHAT IT IS TOLD (environment)
#
#   RECOVERY_TEST_FILE       path to the recovered copy (read it, never write it)
#   RECOVERY_TEST_NAME       the artifact's file name, e.g. MANIFEST-20260902T153521Z.txt
#   RECOVERY_TEST_ARTIFACT   the artifact id: <source>/<set>/<name>
#   RECOVERY_TEST_MEDIUM     which copy was recovered: "local" or a storage medium id
#   RECOVERY_TEST_DIGEST     the SHA-256 (hex) the recovered bytes already matched
#   RECOVERY_TEST_SIZE       their length in bytes
#
#   Only when this script runs as a custom validator:
#   RECOVERY_TEST_VALIDATOR  the validator's name (this file is NAME.local.sh)
#   RECOVERY_TEST_PATTERN    the pattern that selected it
#   RECOVERY_TEST_SCOPE      "backup_set" or "global"
#
#   And the facts every retnd hook gets, among them:
#   RETND=1, RETND_BACKUP_SET_ID (<source>/<set>), RETND_BACKUP_SET_NAME,
#   RETND_RUN_ID, RETND_STEP_NAME, RETND_SOURCE_HOST, RETND_SOURCE_PATH,
#   RETND_DESTINATION, RETND_STARTED_AT
#
#   plus the workflow environment configured for the deployment and the set
#   (Settings > Workflow), secrets included, resolved the same way they are
#   for a backup hook.
#
# WHAT ITS EXIT STATUS MEANS
#
#   0    the copy is usable. The test is recorded as PASSED, and the copy is
#        not owed another test until the recovery-test cadence comes round.
#   anything else
#        the test is recorded as FAILED, with "the validator exited N" as the
#        reason and whatever this script printed as the evidence. A failure
#        opens an assurance episode, which runs the assurance-after scripts
#        once, and the copy is tried again within six hours (or the cadence,
#        if that is shorter).
#
#   retnd itself only tells 0 from not-0. There is no exit status that means
#   "I could not check": a test that could not look at the copy has not
#   shown it is usable, so it fails, and the output is where you say why.
#   This script uses these by convention, so the number alone tells you
#   which kind of failure it was:
#
#     1    the copy was checked and it is NOT usable
#     2    the environment is wrong: a variable is missing or the file
#          cannot be read from inside the container (see --hook-mount)
#     3    a tool the check needs is not in the hook image
#     4    this script has no check for a file with this name; adapt it
#
#   Killed by the timeout, or by a signal, is FAILED too. If the runner
#   cannot start the script at all (no runner, the script refused by the
#   save-time lint, the file missing), the test is "could not complete",
#   which says nothing about the copy.
#
# WHAT HAPPENS TO ITS OUTPUT
#
#   stdout and stderr are captured together. Values of secret environment
#   variables are redacted before anything is kept. Then:
#     - the first 16 KiB are kept and the rest is dropped
#     - control characters and invalid UTF-8 become U+FFFD
#     - at most 200 lines, each at most 1024 bytes, 32 KiB in all, are stored
#   When anything was dropped the stored output ends with retnd's own
#   truncation marker (printing the same words yourself does not fake one).
#   The output of the newest test is what Settings > Assurance and
#   `retnd recovery-test <source/set> show-output` show. So print a short
#   summary a person can read at a glance, and print the line that explains
#   a failure LAST, because the end is what a reader looks at.
#
# EXAMPLES OF THINGS TO PUT UNDER "ADAPT HERE"
#
#   A text manifest that has to list at least one file and end with a
#   checksum line:
#       grep -q . "$file" || reject "the manifest is empty"
#       tail -n 1 "$file" | grep -Eq '^sha256 [0-9a-f]{64}$' || reject "no checksum line"
#
#   A PostgreSQL custom-format dump (needs an image with pg_restore):
#       need pg_restore
#       pg_restore --list "$file" >/dev/null || reject "pg_restore cannot read the dump"
#
#   A SQLite database (needs an image with sqlite3):
#       need sqlite3
#       [ "$(sqlite3 "$file" 'PRAGMA integrity_check;')" = ok ] || reject "integrity_check failed"
#
#   A JSON export (needs jq):
#       need jq
#       jq -e '.items | length > 0' "$file" >/dev/null || reject "no items in the export"
#
# Keep it read-only. A validator never changes the recovered file, never
# reaches back to the source host and never deletes anything: it looks, and
# it says yes or no.

set -euo pipefail

# say prints a line of evidence.
say() { printf '%s\n' "$*"; }

# reject ends the test as FAILED because the copy is not usable.
reject() {
	say "REJECTED: $*"
	exit 1
}

# broken ends the test as FAILED because the environment is wrong.
broken() {
	say "CANNOT CHECK: $*"
	exit 2
}

# need ends the test as FAILED when a tool is missing from the hook image.
need() {
	command -v "$1" >/dev/null 2>&1 || {
		say "CANNOT CHECK: $1 is not in the hook image; run the workflow runner with a --hook-image that has it"
		exit 3
	}
}

file="${RECOVERY_TEST_FILE:-}"
name="${RECOVERY_TEST_NAME:-}"
size="${RECOVERY_TEST_SIZE:-}"
digest="${RECOVERY_TEST_DIGEST:-}"

[ -n "$file" ] || broken "RECOVERY_TEST_FILE is not set; this script is meant to run as a recovery-test validator"
[ -n "$name" ] || name="$(basename "$file")"
[ -e "$file" ] || broken "$file does not exist in the hook container; give the staging directory to the workflow runner with --hook-mount"
[ -r "$file" ] || broken "$file cannot be read from inside the hook container"

say "checking ${RECOVERY_TEST_ARTIFACT:-$name} (copy: ${RECOVERY_TEST_MEDIUM:-unknown})"

# The engine has already matched the digest. Checking the size again here is
# cheap and catches a mount that shows a different file than the one staged.
actual_size="$(wc -c <"$file" | tr -d ' ')"
if [ -n "$size" ] && [ "$actual_size" != "$size" ]; then
	broken "the file in the container is $actual_size bytes and the recovered copy was $size; the mount does not show the staged file"
fi
say "size: $actual_size bytes"

# Optional and slower: hash it again, for a validator that wants to say so in
# its own output. Uncomment to use.
# if command -v sha256sum >/dev/null 2>&1 && [ -n "$digest" ]; then
# 	got="$(sha256sum "$file" | cut -d ' ' -f 1)"
# 	[ "$got" = "$digest" ] || reject "sha256 is $got, expected $digest"
# 	say "sha256 matches"
# fi
: "$digest"

# ---- ADAPT HERE ---------------------------------------------------------
#
# Decide from the name what the file should be, and check that it is. The
# cases below are generic structure checks; replace or extend them with a
# check of your application's own data (see the examples at the top).

case "$name" in
*.tar.gz | *.tgz)
	need gzip
	need tar
	gzip -t "$file" || reject "the gzip stream is damaged"
	entries="$(tar -tzf "$file" | wc -l | tr -d ' ')" || reject "tar cannot list the archive"
	[ "$entries" -gt 0 ] || reject "the archive has no entries"
	say "tar.gz: $entries entries, gzip stream intact"
	;;
*.gz)
	need gzip
	gzip -t "$file" || reject "the gzip stream is damaged"
	say "gzip stream intact"
	;;
*.tar)
	need tar
	entries="$(tar -tf "$file" | wc -l | tr -d ' ')" || reject "tar cannot list the archive"
	[ "$entries" -gt 0 ] || reject "the archive has no entries"
	say "tar: $entries entries"
	;;
*.zst | *.zstd)
	need zstd
	zstd -tq "$file" || reject "the zstd stream is damaged"
	say "zstd stream intact"
	;;
*.zip)
	need unzip
	unzip -tq "$file" >/dev/null || reject "unzip -t reports damage"
	say "zip: every member's CRC matches"
	;;
*.txt | *.csv | *.log | *.json | *.manifest)
	[ -s "$file" ] || reject "the file is empty"
	lines="$(wc -l <"$file" | tr -d ' ')"
	say "text: $lines lines"
	;;
*)
	say "this validator has no check for a file named $name"
	say "adapt the ADAPT HERE section of the script to say what this file should contain"
	exit 4
	;;
esac

# -------------------------------------------------------------------------

say "ACCEPTED: ${RECOVERY_TEST_ARTIFACT:-$name}"
exit 0

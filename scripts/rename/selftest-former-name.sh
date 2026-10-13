#!/usr/bin/env bash
# Planted-fault self-test for scripts/rename/check-no-former-name.sh (#1109).
#
# A guard that is green on the only tree anybody runs it against has proven
# nothing, and this one is green by design once #1109 is done. So each case
# builds a throwaway git repository, plants the name in one spelling, and
# requires the guard to go red naming file:line; then plants the look-alikes
# and requires it to stay green.
#
# The name is assembled from fragments at runtime and never written out, for
# the reason the guard itself gives.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
guard="$repo_root/scripts/rename/check-no-former-name.sh"

stem_up="BACKUP"
stem_cap="Backup"
stem_low="backup"
former_up="${stem_up}D"
former_cap_low="${stem_cap}d"
former_cap_up="${stem_cap}D"
former_low="${stem_low}d"

checks=0
failures=0

pass() {
  checks=$((checks + 1))
  echo "  ok   $1"
}

fail() {
  checks=$((checks + 1))
  failures=$((failures + 1))
  echo "  FAIL $1"
  if [ -n "${2:-}" ]; then
    printf '%s\n' "$2" | sed 's/^/         /'
  fi
}

new_repo() {
  tree="$(mktemp -d)"
  (
    cd "$tree"
    git init -q
    printf 'hello\n' >README.md
    git add -A
    git -c user.email=selftest@example.invalid -c user.name=selftest \
      commit -q -m base
  )
  echo "$tree"
}

# commit <tree> <path> <content>
commit() {
  mkdir -p "$1/$(dirname "$2")"
  printf '%s\n' "$3" >"$1/$2"
  (
    cd "$1"
    git add -A
    git -c user.email=selftest@example.invalid -c user.name=selftest \
      commit -q -m case
  )
}

run_guard() {
  out="$(cd "$1" && bash "$guard" 2>&1)"
  status=$?
}

# red <label> <tree> <needle>...: must exit non-zero and name every needle,
# and must not echo the name itself into the report.
red() {
  local label="$1" tree="$2" needle
  shift 2
  run_guard "$tree"
  if [ "$status" -eq 0 ]; then
    fail "$label (the guard accepted it)" "$out"
  else
    local ok=1
    for needle in "$@"; do
      if ! printf '%s' "$out" | grep -Fq -- "$needle"; then
        fail "$label (went red, but never named '$needle')" "$out"
        ok=0
        break
      fi
    done
    [ "$ok" -eq 1 ] && pass "$label"
  fi
  rm -rf "$tree"
}

green() {
  run_guard "$2"
  if [ "$status" -eq 0 ]; then
    pass "$1"
  else
    fail "$1 (expected exit 0, got $status)" "$out"
  fi
  rm -rf "$2"
}

echo "==> former-name guard: planted-fault self-test (#1109)"

tree="$(new_repo)"
green "control: a clean tree is green" "$tree"

tree="$(new_repo)"
commit "$tree" core/env.go "package core

const Key = \"${former_up}_RUN_ID\""
red "the upper-case prefix goes red" "$tree" "core/env.go:3"

tree="$(new_repo)"
commit "$tree" core/env.go "package core

const Key = \"${former_up}\""
red "the bare upper-case name goes red" "$tree" "core/env.go:3"

tree="$(new_repo)"
commit "$tree" docs/a.md "The ${former_cap_low} daemon."
red "the capitalised spelling goes red" "$tree" "docs/a.md:1"

tree="$(new_repo)"
commit "$tree" docs/a.md "Call ${former_cap_low}"
red "the capitalised spelling at end of line goes red" "$tree" "docs/a.md:1"

tree="$(new_repo)"
commit "$tree" docs/a.md "The ${former_cap_up} daemon."
red "the capitalised spelling with an upper-case D goes red" "$tree" "docs/a.md:1"

tree="$(new_repo)"
commit "$tree" docs/a.md "/var/packages/${former_cap_low}/target"
red "a path spelling goes red" "$tree" "docs/a.md:1"

tree="$(new_repo)"
commit "$tree" scripts/x.sh "systemctl status ${former_low}.service"
red "the lower-case spelling goes red" "$tree" "scripts/x.sh:1"

tree="$(new_repo)"
commit "$tree" scripts/x.sh "echo ${former_low}"
red "the lower-case spelling at end of line goes red" "$tree" "scripts/x.sh:1"

# The guard must not exclude any path, its own scripts included.
tree="$(new_repo)"
commit "$tree" scripts/rename/check-no-former-name.sh "# ${former_cap_low}"
red "the guard's own path is not excluded" "$tree" "scripts/rename/check-no-former-name.sh:1"

tree="$(new_repo)"
commit "$tree" CHANGELOG.md "- renamed from ${former_cap_low}"
red "the changelog is not excluded" "$tree" "CHANGELOG.md:1"

# Staged but uncommitted, which is the state the pre-commit hook sees.
tree="$(new_repo)"
printf 'x %s\n' "$former_up" >"$tree/staged.txt"
(cd "$tree" && git add -N staged.txt)
red "a staged-but-uncommitted file goes red" "$tree" "staged.txt:1"

# Binary files are skipped.
tree="$(new_repo)"
printf '\000\001%s\000' "$former_up" >"$tree/blob.bin"
(cd "$tree" && git add -A && git -c user.email=s@example.invalid -c user.name=s commit -q -m bin)
green "a binary file containing the name is skipped" "$tree"

# The one frozen migration is allowlisted by exact path, and only that path.
tree="$(new_repo)"
commit "$tree" core/migrations/0012_workflow_runs.sql "-- ${former_cap_low} comment in a hash-pinned file"
green "the sha256-pinned migration is allowlisted" "$tree"

tree="$(new_repo)"
commit "$tree" core/migrations/0013_other.sql "-- ${former_cap_low}"
red "a different migration is not allowlisted" "$tree" "core/migrations/0013_other.sql:1"

tree="$(new_repo)"
commit "$tree" docs/core/migrations/0012_workflow_runs.sql "-- ${former_cap_low}"
red "the same file name under another directory is not allowlisted" "$tree" "docs/core/migrations/0012_workflow_runs.sql:1"

tree="$(new_repo)"
commit "$tree" core/migrations/0012_workflow_runs.sql.bak "-- ${former_cap_low}"
red "a sibling of the pinned file is not allowlisted" "$tree" "0012_workflow_runs.sql.bak:1"

# The look-alikes: real identifiers that begin with the same letters.
tree="$(new_repo)"
commit "$tree" ui/pages.tsx 'export function BackupDetailPage() {}
export function BackupDefaultsPage() {}
export const BackupDomain = {};
export function openBackupDataPlane() {}
export const backupDataPath = "x";
export const slug = "backup-set";
export const key = "BACKUP_DIR";'
green "BackupDetailPage, BackupDefaultsPage, openBackupDataPlane, BackupDomain, backupDataPath stay green" "$tree"

echo "==> former-name guard self-test: $checks checks, $failures failure(s)"
[ "$failures" -eq 0 ] || exit 1

#!/usr/bin/env bash
# Install the pre-commit gate in this clone, once, for every worktree (#1260).
#
#   bash scripts/install-hooks.sh
#
# The hooks live in .husky/ and are plain sh (no husky runtime, no
# package.json `prepare`, nothing to npm install), so installing them is
# one git setting: core.hooksPath=.husky. It goes in the SHARED config,
# the one `git config` writes to from any worktree of this repository, and
# the relative path resolves against each worktree's own root, so one
# call here covers every lane under retnd-lanes/ and every worktree added
# later. Each lane runs the hook from its own branch, which is right (a
# branch that changes the hook tests its own hook) and worth knowing (an
# old branch runs an old hook).
#
# Why this exists at all: when #1260 was opened, core.hooksPath was unset
# in the main checkout and in every worktree, and nothing in the tree set
# it, so the gate the repository says it relies on had not been running on
# commits anywhere. Nothing in git can force a hook on a clone, so the two
# things that can notice a missing one both say so and point here:
# scripts/ci-local.sh warns, and scripts/release/record-local-gate.sh
# refuses.
#
# Idempotent, and it prints what it set.
set -euo pipefail

unset GIT_INDEX_FILE GIT_DIR GIT_WORK_TREE GIT_OBJECT_DIRECTORY GIT_COMMON_DIR GIT_PREFIX

toplevel="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "install-hooks: not inside a git checkout; run it from the clone the hook should guard." >&2
  exit 2
}
cd "$toplevel"

for hook in pre-commit prepare-commit-msg; do
  if [ ! -f ".husky/$hook" ]; then
    echo "install-hooks: .husky/$hook is not in this tree, so there is nothing to install. Check out a branch that has it." >&2
    exit 2
  fi
  if [ ! -x ".husky/$hook" ]; then
    echo "install-hooks: .husky/$hook is not executable; git would skip it silently. Fix: chmod +x .husky/$hook (and commit the mode)." >&2
    exit 2
  fi
done

before="$(git config --get core.hooksPath || true)"
git config core.hooksPath .husky
config_file="$(git rev-parse --path-format=absolute --git-common-dir)/config"

if [ "$before" = ".husky" ]; then
  echo "install-hooks: core.hooksPath was already .husky in $config_file; nothing to change."
else
  echo "install-hooks: set core.hooksPath=.husky in $config_file${before:+ (was: $before)}."
fi
echo "    That is the shared config, so it covers every worktree of this repository, now and later."
echo "    .husky/pre-commit runs scripts/ci-local.sh on every commit; .husky/prepare-commit-msg stamps"
echo "    a commit the gate did not check. CI_LOCAL_RED=\"<why>\" commits red tests ahead of their fix."

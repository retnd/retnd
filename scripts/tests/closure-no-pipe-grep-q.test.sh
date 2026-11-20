#!/usr/bin/env bash
# The N, T, U and O closure checks and their self-tests run under pipefail. A
# `printf ... | grep -q X` there is a flake: grep quits on the first match,
# printf takes SIGPIPE on the rest, and a busy machine reads a found match as a
# failure (#1407, #1482). A here-string (`grep -q X <<<"$x"`) has no pipe to
# break. This fails while any of those files still pipes into grep -q.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT" || exit 2

echo "==> the closure checks do not pipe into grep -q under pipefail (#1482)"

bad=0
for e in n t u o; do
  for f in "scripts/release/check-epic-$e-closed.sh" "scripts/tests/epic-$e-closure.test.sh"; do
    if [ ! -f "$f" ]; then echo "    FAIL $f is missing"; bad=$((bad + 1)); continue; fi
    hits="$(grep -nE '\| *grep -[a-zA-Z]*q' "$f" || true)"
    if [ -n "$hits" ]; then
      echo "    FAIL $f pipes into grep -q:"; printf '%s\n' "$hits" | cut -c1-140 | sed 's/^/         | /'
      bad=$((bad + 1))
    else
      echo "    ok   $f has no pipe into grep -q"
    fi
  done
done

echo "closure-no-pipe-grep-q: $bad failures"
[ "$bad" -eq 0 ]

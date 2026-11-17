#!/usr/bin/env bash
# `--shard i/n` on the recovery and assurance mutation self-tests (#1455, plan item H).
#
# Every case runs with --check-anchors, which plants nothing and builds nothing, so
# the whole file takes a second or two and needs no Docker.
#
# What this holds:
#   - no flag: no shard line, and the output equals `--shard 1/1` minus its one header line
#   - the union of all n slices is the whole list, with no mutant in two slices
#     (and the clean-tree controls run in exactly one slice)
#   - an empty, malformed or out-of-range slice is refused with exit 2 and runs nothing
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/selftest-shard.XXXXXX")"
trap 'rm -rf "$work"' EXIT

fail=0
checks=0
check() { # check <description> <status 0|1>
  checks=$((checks + 1))
  if [ "$2" -ne 0 ]; then
    echo "FAIL: $1" >&2
    fail=$((fail + 1))
  fi
}

# labels <out-file>: the "==> label" lines of mutants, without the clean-control header
labels() { grep '^==> ' "$1" | grep -v '^==> negative controls' | grep -v '^==> shard ' || true; }
cleans() { grep 'on an unmutated tree' "$1" || true; }

for name in recovery assurance; do
  script="$repo_root/scripts/$name/selftest.sh"

  bash "$script" --check-anchors >"$work/$name.whole" 2>&1
  check "$name: the unsharded run succeeds" $?
  grep -qi 'shard' "$work/$name.whole"
  check "$name: the unsharded output never mentions a shard" $((1 - $?))

  bash "$script" --check-anchors --shard 1/1 >"$work/$name.one" 2>&1
  check "$name: --shard 1/1 succeeds" $?
  grep -v '^==> shard ' "$work/$name.one" | grep -v '^OK: ' >"$work/$name.one.body"
  grep -v '^OK: ' "$work/$name.whole" >"$work/$name.whole.body"
  cmp -s "$work/$name.one.body" "$work/$name.whole.body"
  check "$name: --shard 1/1 prints what the unsharded run prints (bar its header)" $?

  labels "$work/$name.whole" | sort >"$work/$name.want"
  cleans "$work/$name.whole" | sort >"$work/$name.want.clean"
  total=$(wc -l <"$work/$name.want" | tr -d ' ')
  [ "$total" -gt 10 ]
  check "$name: the whole list has more than 10 mutants (got $total)" $?

  for n in 2 3 7 "$total"; do
    : >"$work/$name.got"
    : >"$work/$name.got.clean"
    biggest=0
    for i in $(seq 1 "$n"); do
      bash "$script" --check-anchors --shard "$i/$n" >"$work/$name.s" 2>&1
      check "$name: --shard $i/$n succeeds" $?
      k=$(labels "$work/$name.s" | tee -a "$work/$name.got" | wc -l | tr -d ' ')
      cleans "$work/$name.s" >>"$work/$name.got.clean"
      [ "$k" -gt 0 ]
      check "$name: --shard $i/$n holds at least one mutant" $?
      [ "$k" -gt "$biggest" ] && biggest=$k
    done
    sort "$work/$name.got" >"$work/$name.got.sorted"
    cmp -s "$work/$name.got.sorted" "$work/$name.want"
    check "$name: the union of $n slices is the whole list, no overlap" $?
    sort "$work/$name.got.clean" >"$work/$name.got.clean.sorted"
    cmp -s "$work/$name.got.clean.sorted" "$work/$name.want.clean"
    check "$name: the clean-tree controls run once across $n slices" $?
    # balanced: no slice carries more than ceil(total / n)
    [ "$biggest" -le $(((total + n - 1) / n)) ]
    check "$name: $n slices are balanced (largest $biggest of $total)" $?
  done

  for bad in 0/3 4/3 1/0 3/2 -1/3 a/b 1 1/ /3 1/2/3 "$((total + 1))/$((total + 1))"; do
    bash "$script" --check-anchors --shard "$bad" >"$work/$name.bad" 2>&1
    rc=$?
    [ "$rc" -eq 2 ]
    check "$name: --shard $bad is refused with exit 2 (got $rc)" $?
    grep -q '^==> ' "$work/$name.bad"
    check "$name: --shard $bad ran nothing" $((1 - $?))
  done

  bash "$script" --check-anchors --shard >"$work/$name.bad" 2>&1
  [ $? -eq 2 ]
  check "$name: --shard with no value is refused" $?
done

if [ "$fail" -ne 0 ]; then
  echo "selftest-shard: $fail of $checks checks failed" >&2
  exit 1
fi
echo "selftest-shard: ok ($checks checks)"

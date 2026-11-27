#!/usr/bin/env bash
# Every step of scripts/ci-local.sh has its line in the serial golden, and the
# golden names no step the gate does not have (#1642).
#
# scripts/tests/testdata/ci-local-serial-steps.txt is the heading list of a
# full fixture run, and W4 of scripts/tests/ci-local-gate.test.sh compares a
# real serial run against it. That case takes minutes and lives in the full
# gate, so #1631 added a step without its golden line, the commit hook never
# looked, and main stayed red until #1641. This is the same comparison done
# statically, in about a second, so the fast tier can run it on any commit that
# touches ci-local.sh, the golden or the list below.
#
# A step the fixture run never prints (one that only runs when an app module
# exists, under CI_LOCAL_FAST=1, or in one shard) is named in
# scripts/tests/testdata/ci-local-steps-not-in-golden.txt with a reason. A step
# title built from a variable cannot be checked here, so it has to be listed
# there too.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }

# titles <ci-local.sh>: every gate_step / gate_docker_step title, one per line,
# as it is quoted (a title with a variable in it comes out with the \$ in it).
titles() {
  sed -nE 's/^[[:space:]]*gate_(docker_)?step "(([^"\\]|\\.)*)".*/\2/p' "$1"
}

# problems <root>: one line per thing wrong, nothing when the three agree.
problems() {
  local root=$1 gate="$1/scripts/ci-local.sh" golden="$1/scripts/tests/testdata/ci-local-serial-steps.txt"
  local allow="$1/scripts/tests/testdata/ci-local-steps-not-in-golden.txt"
  local steps gold listed t why
  steps="$(titles "$gate")"
  gold="$(sed -n 's/^==> //p' "$golden" 2>/dev/null)"
  listed="$( [ -f "$allow" ] && grep -vE '^[[:space:]]*(#|$)' "$allow" | cut -f1)"
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    if grep -qxF -- "$t" <<<"$gold" || grep -qxF -- "$t" <<<"$listed"; then continue; fi
    case "$t" in
      *'$'*) echo "step [$t] has a variable in its title, so it has to be named in ci-local-steps-not-in-golden.txt" ;;
      *) echo "step [$t] is in scripts/ci-local.sh and has no line in ci-local-serial-steps.txt (add ==> $t where a serial run prints it, or list it with a reason)" ;;
    esac
  done <<<"$steps"
  while IFS= read -r t; do
    [ -n "$t" ] || continue
    grep -qxF -- "$t" <<<"$steps" || echo "golden line [==> $t] is no step of scripts/ci-local.sh"
  done <<<"$gold"
  [ -f "$allow" ] || return 0
  while IFS=$'\t' read -r t why; do
    case "$t" in '' | '#'*) continue ;; esac
    [ -n "$why" ] || echo "ci-local-steps-not-in-golden.txt: [$t] has no reason"
    if ! grep -qxF -- "$t" <<<"$steps"; then
      echo "ci-local-steps-not-in-golden.txt: [$t] is no step any more, so its entry is stale"
    elif grep -qxF -- "$t" <<<"$gold"; then
      echo "ci-local-steps-not-in-golden.txt: [$t] is in the golden now, so its entry is stale"
    fi
  done <"$allow"
}

work="$(mktemp -d "${TMPDIR:-/tmp}/serial-steps-golden.XXXXXX")"
case "$work" in /*/serial-steps-golden.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

copy() { # copy <name> -> a tree with the real three files
  local d="$work/$1"
  mkdir -p "$d/scripts/tests/testdata"
  cp "$repo_root/scripts/ci-local.sh" "$d/scripts/"
  cp "$repo_root/scripts/tests/testdata/ci-local-serial-steps.txt" "$d/scripts/tests/testdata/"
  cp "$repo_root/scripts/tests/testdata/ci-local-steps-not-in-golden.txt" "$d/scripts/tests/testdata/"
  echo "$d"
}
expect() { # expect <label> <needle> <tree>
  local out
  out="$(problems "$3")"
  if grep -qF -- "$2" <<<"$out"; then pass "$1"; else fail "$1" "$out"; fi
}

echo "==> planted copies go red"
d="$(copy p1)"
printf '\ngate_step "a planted step with no golden line (#1642)"\ntrue\n' >>"$d/scripts/ci-local.sh"
expect "P1 a step without its golden line is named (the #1631 shape)" "step [a planted step with no golden line (#1642)] is in scripts/ci-local.sh and has no line" "$d"
d="$(copy p2)"
printf '==> a golden line for a step that is gone\n' >>"$d/scripts/tests/testdata/ci-local-serial-steps.txt"
expect "P2 a golden line without its step is named" "golden line [==> a golden line for a step that is gone] is no step" "$d"
d="$(copy p3)"
printf '\n  gate_docker_step "a planted docker step (#1642)" --stall-min 5\n' >>"$d/scripts/ci-local.sh"
expect "P3 a gate_docker_step with options is read too" "step [a planted docker step (#1642)]" "$d"
d="$(copy p4)"
# shellcheck disable=SC2016 # the planted title carries a literal $x
printf '\ngate_step "built from $x"\n' >>"$d/scripts/ci-local.sh"
expect "P4 a title built from a variable has to be listed" 'step [built from $x] has a variable in its title' "$d"
d="$(copy p5)"
printf 'a step that does not exist\tno reason needed\n' >>"$d/scripts/tests/testdata/ci-local-steps-not-in-golden.txt"
expect "P5 a listed step that is gone is stale" "[a step that does not exist] is no step any more" "$d"
d="$(copy p6)"
head -n 1 "$d/scripts/tests/testdata/ci-local-serial-steps.txt" | sed 's/^==> //' | sed 's/$/\tplanted/' >>"$d/scripts/tests/testdata/ci-local-steps-not-in-golden.txt"
expect "P6 a listed step the golden has is stale" "is in the golden now, so its entry is stale" "$d"
d="$(copy p7)"
t="$(grep -vE '^[[:space:]]*(#|$)' "$d/scripts/tests/testdata/ci-local-steps-not-in-golden.txt" | head -n 1 | cut -f1)"
grep -vF -- "$t" "$d/scripts/tests/testdata/ci-local-steps-not-in-golden.txt" >"$d/x" && mv "$d/x" "$d/scripts/tests/testdata/ci-local-steps-not-in-golden.txt"
expect "P7 a conditional step taken off the list is named again" "step [$t] is in scripts/ci-local.sh and has no line" "$d"

echo "==> the tree as it stands"
out="$(problems "$repo_root")"
if [ -z "$out" ]; then pass "T1 ci-local.sh, the serial golden and the list agree"; else fail "T1 ci-local.sh, the serial golden and the list agree" "$out"; fi
n="$(titles "$repo_root/scripts/ci-local.sh" | grep -c .)"
if [ "$n" -ge 90 ]; then pass "T2 the reader finds the gate's steps ($n)"; else fail "T2 the reader finds the gate's steps (only $n)"; fi

echo "==> serial-steps-golden: $checks checks, $failures failed"
[ "$failures" -eq 0 ]

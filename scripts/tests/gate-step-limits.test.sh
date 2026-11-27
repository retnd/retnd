#!/usr/bin/env bash
# The stall watchdog's limits for the long, quiet steps are in the tree
# (#1672, P0.5), not in the shell of whoever runs the gate.
#
# The watchdog (#1540) stops a step that prints nothing for
# RETND_GATE_STALL_MIN minutes (10 by default). The mutation self-tests print
# one block per control when that control ends, and each control bounds itself
# with its own go test -timeout, up to an hour. So a healthy control could sit
# quiet for longer than the default window, and in the 0.8.1 cut eight shard
# attempts (about 2.9 hours) died that way. The way round was
# RETND_GATE_STALL_MIN=90 and RETND_GATE_STEP_CAP_MIN=480 in the Mac's shell,
# which scripts/ci-docker.sh never hands to a remote host, so those shards could
# only run on the Mac. The browser lane is the same shape: it has its own stall
# check and cap, and the gate's record-based cap (1.5 times the longest run)
# fired 13 seconds before the lane's own cap on MARS, so the run ended with the
# watchdog's verdict instead of the lane's.
#
# The rule this holds: each of those steps carries its own --stall-min (and the
# browser steps a --cap-min) on its gate_step line, above the bound the step's
# command already sets for itself. The inner bound then always fires first and
# names what hung, and the watchdog stays the backstop. The bounds are read
# from the drivers, so raising a control's timeout without moving the step's
# window turns this red.
#
# Static, a second or two, no Docker. Every check is run against a planted copy
# first, so a check that cannot fail is caught here too.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }

# step_opt <ci-local.sh> <heading> <--stall-min|--cap-min> -> the minutes, or
# nothing when the step line has no such option. The heading is matched whole.
step_opt() {
  awk -v h="$2" -v o="$3" '
    {
      line = $0
      sub(/^[ \t]+/, "", line)
      if (line !~ /^gate_(docker_)?step "/) next
      rest = substr(line, index(line, "\"") + 1)
      if (substr(rest, 1, length(h) + 1) != h "\"") next
      rest = substr(rest, length(h) + 2)
      n = split(rest, w, /[ \t]+/)
      for (i = 1; i < n; i++) if (w[i] == o) { print w[i + 1]; exit }
    }' "$1"
}

# has_step <ci-local.sh> <heading> -> 0 when a gate_step line has that heading.
has_step() {
  grep -qE '^[[:space:]]*gate_(docker_)?step "' "$1" && grep -F -- "step \"$2\"" "$1" | grep -qE '^[[:space:]]*gate_(docker_)?step "'
}

# max_control_timeout <driver dir> -> the longest per-control go test -timeout,
# in minutes, that the driver's python files declare (the Control field and its
# default), or nothing.
max_control_timeout() {
  grep -ohE 'timeout(: str)? *= *"[0-9]+m"' "$1"/*.py 2>/dev/null | grep -oE '[0-9]+' | sort -n | tail -n 1
}

# max_argv_timeout <driver file> -> the longest "-timeout", "<n>m" pair in it,
# else 10: go test's own default when the driver sets none.
max_argv_timeout() {
  m="$(grep -ohE '"-timeout", *"[0-9]+m"' "$1" 2>/dev/null | grep -oE '[0-9]+' | sort -n | tail -n 1)"
  echo "${m:-10}"
}

# lane_default <ui_browser.py> <specs|run|stall|slack> -> seconds, from the
# lane's own defaults: the cap per mode, the stall window, and the slack the
# outer subprocess timeout adds to the cap for the image start and the install.
lane_default() {
  case "$2" in
    specs) sed -nE 's/.*setting\("RETND_UI_BROWSER_TIMEOUT", ([0-9]+) if mode == "specs" else ([0-9]+)\).*/\1/p' "$1" | head -n 1 ;;
    run) sed -nE 's/.*setting\("RETND_UI_BROWSER_TIMEOUT", ([0-9]+) if mode == "specs" else ([0-9]+)\).*/\2/p' "$1" | head -n 1 ;;
    stall) sed -nE 's/.*setting\("RETND_UI_BROWSER_STALL", ([0-9]+)\).*/\1/p' "$1" | head -n 1 ;;
    slack) sed -nE 's/.*timeout=cap \+ ([0-9]+)\).*/\1/p' "$1" | head -n 1 ;;
  esac
}

ENC="the encryption and suite guards can actually fail (mutation self-test, #1531)"
AGENTS="the agent (MCP) guards can actually fail (mutation self-test, #1530)"
RECOVERY="the recovery guards can actually fail (mutation self-test, #976)"
API="the /api/v1 contract gates can actually fail (mutation self-test)"
SPECS="ui/shared browser specs (ui/shared/e2e), every file, in the pinned Playwright image (#1537)"
UISELF="the assurance UI guards can actually fail (browser mutation self-test, #1532)"

# above <label> <have minutes or empty> <inner seconds> -> one problem line
# unless have*60 is strictly more than the inner bound.
above() {
  if [ -z "$3" ]; then
    echo "$1: the bound it has to sit above could not be read from the tree"
  elif [ -z "$2" ]; then
    echo "$1: no option on its gate_step line, and it has to sit above the step's own bound of $3s"
  elif [ $(( $2 * 60 )) -le "$3" ]; then
    echo "$1: $2 min is not above the step's own bound of $3s"
  fi
}

# problems <root>: one line per step whose limits do not sit outside its own
# bound, nothing when the tree is fine.
problems() {
  local root=$1 gate="$1/scripts/ci-local.sh" lane="$1/scripts/bdtools/e2e/ui_browser.py" h inner
  for h in "$ENC" "$AGENTS" "$RECOVERY" "$API" "$SPECS" "$UISELF"; do
    has_step "$gate" "$h" || echo "no gate_step line has the heading [$h] any more; move this test with it"
  done
  inner="$(max_control_timeout "$root/scripts/bdtools/encryption")"
  above "encryption self-test --stall-min" "$(step_opt "$gate" "$ENC" --stall-min)" "${inner:+$((inner * 60))}"
  inner="$(max_control_timeout "$root/scripts/bdtools/agents")"
  above "agents self-test --stall-min" "$(step_opt "$gate" "$AGENTS" --stall-min)" "${inner:+$((inner * 60))}"
  inner="$(max_argv_timeout "$root/scripts/bdtools/recovery/selftest.py")"
  above "recovery self-test --stall-min" "$(step_opt "$gate" "$RECOVERY" --stall-min)" "$((inner * 60))"
  inner="$(max_argv_timeout "$root/scripts/bdtools/api/selftest.py")"
  above "/api/v1 self-test --stall-min" "$(step_opt "$gate" "$API" --stall-min)" "$((inner * 60))"
  local stall specs run slack
  stall="$(lane_default "$lane" stall)"
  specs="$(lane_default "$lane" specs)"
  run="$(lane_default "$lane" run)"
  slack="$(lane_default "$lane" slack)"
  above "browser specs --stall-min" "$(step_opt "$gate" "$SPECS" --stall-min)" "$stall"
  above "browser specs --cap-min" "$(step_opt "$gate" "$SPECS" --cap-min)" "${specs:+${slack:+$((specs + slack))}}"
  above "assurance UI self-test --stall-min" "$(step_opt "$gate" "$UISELF" --stall-min)" "$stall"
  above "assurance UI self-test --cap-min" "$(step_opt "$gate" "$UISELF" --cap-min)" "${run:+${slack:+$((run + slack))}}"
}

work="$(mktemp -d "${TMPDIR:-/tmp}/gate-step-limits.XXXXXX")"
case "$work" in /*/gate-step-limits.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT

# copy <name>: the files problems reads, from the real tree, into $work/<name>.
copy() {
  local d="$work/$1"
  mkdir -p "$d/scripts/bdtools/encryption" "$d/scripts/bdtools/agents" "$d/scripts/bdtools/recovery" "$d/scripts/bdtools/api" "$d/scripts/bdtools/e2e"
  cp "$repo_root/scripts/ci-local.sh" "$d/scripts/"
  cp "$repo_root"/scripts/bdtools/encryption/*.py "$d/scripts/bdtools/encryption/"
  cp "$repo_root"/scripts/bdtools/agents/*.py "$d/scripts/bdtools/agents/"
  cp "$repo_root/scripts/bdtools/recovery/selftest.py" "$d/scripts/bdtools/recovery/"
  cp "$repo_root/scripts/bdtools/api/selftest.py" "$d/scripts/bdtools/api/"
  cp "$repo_root/scripts/bdtools/e2e/ui_browser.py" "$d/scripts/bdtools/e2e/"
  echo "$d"
}

# strip <file> <heading>: that step's line loses every option after its heading.
strip() {
  H="$2" perl -0pi -e 's/(gate_(?:docker_)?step "\Q$ENV{H}\E")[^\n]*/$1/' "$1"
}

echo "==> the readers find what the drivers declare"
inner="$(max_control_timeout "$repo_root/scripts/bdtools/encryption")"
if [ -n "$inner" ] && [ "$inner" -ge 15 ]; then pass "R1 the encryption controls' longest timeout reads as $inner min"; else fail "R1 the encryption controls' longest timeout reads as a number of at least 15 (the Control default), got [$inner]"; fi
inner="$(max_control_timeout "$repo_root/scripts/bdtools/agents")"
if [ -n "$inner" ] && [ "$inner" -ge 15 ]; then pass "R2 the agents controls' longest timeout reads as $inner min"; else fail "R2 the agents controls' longest timeout reads as a number of at least 15, got [$inner]"; fi
inner="$(grep -cE '"-timeout", *"[0-9]+m"' "$repo_root/scripts/bdtools/recovery/selftest.py")"
if [ "$inner" -ge 1 ]; then pass "R3 the recovery driver's go test -timeout is found"; else fail "R3 the recovery driver's go test -timeout is found"; fi
for k in specs run stall slack; do
  v="$(lane_default "$repo_root/scripts/bdtools/e2e/ui_browser.py" "$k")"
  if [ -n "$v" ]; then pass "R4 the browser lane's $k default reads as ${v}s"; else fail "R4 the browser lane's $k default is found in ui_browser.py"; fi
done

echo "==> planted copies go red"
d="$(copy p1)"; strip "$d/scripts/ci-local.sh" "$ENC"
out="$(problems "$d")"
if grep -qF "encryption self-test --stall-min: no option" <<<"$out"; then pass "P1 the encryption step without its --stall-min is named"; else fail "P1 the encryption step without its --stall-min is named" "$out"; fi
d="$(copy p2)"; perl -pi -e 's/timeout="60m"/timeout="240m"/' "$d"/scripts/bdtools/encryption/*.py
out="$(problems "$d")"
if grep -qF "encryption self-test --stall-min:" <<<"$out" && grep -qF "14400s" <<<"$out"; then pass "P2 a control timeout raised past the step's window is named"; else fail "P2 a control timeout raised past the step's window is named" "$out"; fi
d="$(copy p3)"; strip "$d/scripts/ci-local.sh" "$SPECS"
out="$(problems "$d")"
if grep -qF "browser specs --cap-min: no option" <<<"$out" && grep -qF "browser specs --stall-min: no option" <<<"$out"; then pass "P3 the browser specs step without its limits is named, both of them"; else fail "P3 the browser specs step without its limits is named, both of them" "$out"; fi
d="$(copy p4)"; perl -pi -e 's/setting\("RETND_UI_BROWSER_TIMEOUT", 5400/setting("RETND_UI_BROWSER_TIMEOUT", 9000/' "$d/scripts/bdtools/e2e/ui_browser.py"
out="$(problems "$d")"
if grep -qF "browser specs --cap-min:" <<<"$out"; then pass "P4 a lane cap raised past the step's cap is named"; else fail "P4 a lane cap raised past the step's cap is named" "$out"; fi
d="$(copy p5)"; perl -pi -e 's/"-timeout", "10m"/"-timeout", "45m"/' "$d/scripts/bdtools/recovery/selftest.py"
out="$(problems "$d")"
if grep -qF "recovery self-test --stall-min:" <<<"$out"; then pass "P5 a recovery go test -timeout raised past the step's window is named"; else fail "P5 a recovery go test -timeout raised past the step's window is named" "$out"; fi
d="$(copy p6)"; H="$API" perl -pi -e 's/\Q$ENV{H}\E/the API self-test, renamed/' "$d/scripts/ci-local.sh"
out="$(problems "$d")"
if grep -qF "no gate_step line has the heading [$API]" <<<"$out"; then pass "P6 a renamed step is named, not silently unchecked"; else fail "P6 a renamed step is named, not silently unchecked" "$out"; fi

echo "==> the tree as it stands"
out="$(problems "$repo_root")"
if [ -z "$out" ]; then pass "T1 every long, quiet step carries limits above its own bound"; else fail "T1 every long, quiet step carries limits above its own bound" "$out"; fi

echo "==> gate-step-limits: $checks checks, $failures failed"
[ "$failures" -eq 0 ]

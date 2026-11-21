#!/usr/bin/env bash
# The Chromium lane's own cases (#1532, #1537): scripts/e2e/ui-browser.sh
# against a stand-in docker, so what it hands the daemon and what it makes of
# each answer are measured without a daemon, a browser or a network.
#
# The lane is the only thing that runs the spec files in ui/shared/e2e and the
# assurance UI self-test, so its exit status is the whole verdict the gate
# sees. The cases hold the three answers apart: 0 for a run that passed, 1 for
# a run that failed, stalled or hit its cap, 3 only for a machine that could
# not run the lane at all. A 3 that leaked out of the command inside, or a
# stall that read as "could not run", would turn a red spec into an
# INCOMPLETE run that nobody looks at twice.
#
# What the container does inside (the copy, the install, the stall and cap
# watch) is shell that only runs in the Playwright image; the pull request
# that added the lane shows it running there.
set -uo pipefail

repo_root="$(cd "$(dirname "$0")/../.." && pwd)"
lane="$repo_root/scripts/e2e/ui-browser.sh"
config=ui/shared/e2e/playwright.config.ts
checks=0
failures=0
pass() { checks=$((checks + 1)); echo "    ok   $1"; }
fail() { checks=$((checks + 1)); failures=$((failures + 1)); echo "    FAIL $1"; [ -z "${2:-}" ] || printf '%s\n' "$2" | sed 's/^/         | /'; }
has() { if grep -qF -- "$3" <<<"$2"; then pass "$1"; else fail "$1" "$2"; fi; }
hasnt() { if grep -qF -- "$3" <<<"$2"; then fail "$1" "$2"; else pass "$1"; fi; }
is() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (want $2, got $3)" "${4:-}"; fi; }

work="$(mktemp -d "${TMPDIR:-/tmp}/ui-browser-lane.XXXXXX")"
case "$work" in /*/ui-browser-lane.*) ;; *) echo "refusing odd sandbox $work" >&2; exit 1 ;; esac
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
# The stand-in daemon: every call is logged, and each verb answers what the
# case asks for.
cat >"$work/bin/docker" <<'STUB'
#!/usr/bin/env bash
# One line per call: the container's script is multi-line, so its newlines are
# written as \n.
printf 'docker' >>"$STUB_LOG"
for a in "$@"; do printf ' [%s]' "${a//$'\n'/\\n}" >>"$STUB_LOG"; done
printf '\n' >>"$STUB_LOG"
case "$1" in
  info) if [ "${2:-}" = --format ]; then echo "${STUB_ARCH:-x86_64}"; fi; exit "${STUB_INFO:-0}" ;;
  image) exit "${STUB_INSPECT:-0}" ;;
  pull) exit "${STUB_PULL:-0}" ;;
  run) exit "${STUB_RUN:-0}" ;;
  rm) exit 0 ;;
esac
exit 0
STUB
chmod +x "$work/bin/docker"

# lane <env assignments...> -- <lane args...>: sets $out, $status and $calls.
lane_run() {
  local envs=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do envs+=("$1"); shift; done
  shift
  : >"$work/log"
  out="$(env -u RETND_UI_BROWSER_IMAGE -u RETND_UI_BROWSER_CPUS -u RETND_UI_BROWSER_MEMORY -u RETND_UI_BROWSER_TIMEOUT \
    -u RETND_UI_BROWSER_STALL -u RETND_UI_BROWSER_TEST_TIMEOUT -u RETND_UI_BROWSER_PLATFORM PATH="$work/bin:$PATH" STUB_LOG="$work/log" \
    ${envs[@]+"${envs[@]}"} bash "$lane" "$@" 2>&1)"
  status=$?
  calls="$(cat "$work/log")"
}

echo "==> a passing run"
lane_run -- specs "$config"
is "L1 a passing run exits 0" 0 "$status" "$out"
run_line="$(grep '^docker \[run\]' <<<"$calls")"
has "L1 the tree is mounted read-only at its own path" "$run_line" "[-v] [$repo_root:$repo_root:ro]"
has "L1 the container is fenced to 4 cpus by default" "$run_line" "[--cpus] [4]"
has "L1 and to 4g with no swap by default" "$run_line" "[--memory] [4g] [--memory-swap] [4g]"
has "L1 it runs the pinned Playwright image" "$run_line" "[mcr.microsoft.com/playwright:v1.62.1-noble@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e]"
has "L1 it installs the @playwright/test that matches the image" "$run_line" "[LANE_PW=1.62.1]"
has "L1 it carries a per-test timeout" "$run_line" "[LANE_TEST_TIMEOUT=60]"
has "L1 it carries a stall limit of ten minutes" "$run_line" "[LANE_STALL=600]"
has "L1 and runs Python unbuffered, so a self-test's progress reaches the stall check" "$run_line" "[PYTHONUNBUFFERED=1]"
# A test.only left in one spec would otherwise run that one test, exit 0 and
# read as the whole suite passing (#1549 review). CI=1 alone does not refuse
# it: Playwright does only with --forbid-only or a config's forbidOnly.
has "L1 the specs refuse a focused test (--forbid-only)" "$run_line" "--forbid-only"
if grep -q 'forbidOnly: !!process.env.CI' "$repo_root/$config"; then
  pass "L1 and the specs' own config refuses one under CI, for a run outside the lane"
else
  fail "L1 and the specs' own config refuses one under CI, for a run outside the lane"
fi
has "L1 it carries a hard cap" "$run_line" "[LANE_CAP=5400]"
has "L1 it hands the container the mode and the config" "$run_line" "[specs] [$config]"
name="$(sed -n 's/.*\[--name\] \[\([^]]*\)\].*/\1/p' <<<"$run_line")"
case "$name" in retnd-ui-browser-*) pass "L2 the container is named" ;; *) fail "L2 the container is named" "$run_line" ;; esac
has "L2 and removed by that name on the way out" "$calls" "docker [rm] [-f] [$name]"

echo "==> runs that did not hold"
lane_run STUB_RUN=1 -- specs "$config"
is "L3 a red suite exits 1" 1 "$status" "$out"
has "L3 and the container is still removed" "$calls" "docker [rm] [-f]"
lane_run STUB_RUN=98 -- specs "$config"
is "L4 a stalled run exits 1, never 3" 1 "$status" "$out"
has "L4 and says it stalled" "$out" "STALLED"
lane_run STUB_RUN=124 -- specs "$config"
is "L5 a run that hit its cap exits 1" 1 "$status" "$out"
has "L5 and names the cap" "$out" "cap"
# 126 and 127 from the command inside (a renamed self-test, a missing tool)
# are a defect in the tree, not a machine that cannot run the lane, so the
# container says 95 for them and the lane fails.
lane_run STUB_RUN=95 -- run bash scripts/assurance/ui-selftest.sh
is "L5b a command inside that could not be run (126 or 127) fails, never INCOMPLETE" 1 "$status" "$out"
has "L5b and says so" "$out" "could not be run"
lane_run STUB_RUN=3 -- run bash scripts/assurance/ui-selftest.sh
is "L6 a 3 from the command inside is a failure, not the machine verdict" 1 "$status" "$out"

echo "==> a machine that cannot run the lane"
lane_run STUB_RUN=96 -- specs "$config"
is "L7 a container that could not be prepared exits 3" 3 "$status" "$out"
lane_run STUB_RUN=125 -- specs "$config"
is "L8 a container docker could not start exits 3" 3 "$status" "$out"
lane_run STUB_INFO=1 -- specs "$config"
is "L9 no daemon exits 3" 3 "$status" "$out"
hasnt "L9 and starts nothing" "$calls" "docker [run]"
lane_run STUB_INSPECT=1 STUB_PULL=1 -- specs "$config"
is "L10 an image that is not here and cannot be pulled exits 3" 3 "$status" "$out"
lane_run STUB_INSPECT=1 -- specs "$config"
is "L10 an image that is pulled first runs" 0 "$status" "$out"
has "L10 and was pulled" "$calls" "docker [pull]"

echo "==> what the caller asks for"
lane_run -- run bash scripts/assurance/ui-selftest.sh
is "L11 run mode passes" 0 "$status" "$out"
has "L11 and hands the command to the container" "$calls" "[run] [bash] [scripts/assurance/ui-selftest.sh]"
has "L11 with its own default cap" "$calls" "[LANE_CAP=2700]"
lane_run RETND_UI_BROWSER_MEMORY=1g RETND_UI_BROWSER_CPUS=1 RETND_UI_BROWSER_TIMEOUT=120 RETND_UI_BROWSER_STALL=30 RETND_UI_BROWSER_TEST_TIMEOUT=20 -- specs "$config" 1351-agents-card.spec.ts
has "L12 the fence moves with its knobs" "$calls" "[--cpus] [1] [--memory] [1g] [--memory-swap] [1g]"
has "L12 so do the cap, the stall limit and the per-test timeout" "$calls" "[LANE_CAP=120] [-e] [LANE_STALL=30] [-e] [LANE_TEST_TIMEOUT=20]"
has "L12 extra arguments reach Playwright" "$calls" "[$config] [1351-agents-card.spec.ts]"
lane_run STUB_INSPECT=1 -- specs "$config"
has "L16 the daemon's own platform is named on the pull" "$calls" "docker [pull] [--platform] [linux/amd64]"
has "L16 and on the run" "$calls" "[--platform] [linux/amd64] [--cpus]"
lane_run STUB_ARCH=aarch64 -- specs "$config"
has "L16 an arm64 daemon gets linux/arm64, never an emulated image" "$calls" "[--platform] [linux/arm64] [--cpus]"
lane_run STUB_ARCH=aarch64 RETND_UI_BROWSER_PLATFORM=linux/amd64 STUB_INSPECT=1 -- specs "$config"
has "L16 RETND_UI_BROWSER_PLATFORM wins on the pull" "$calls" "docker [pull] [--platform] [linux/amd64]"
has "L16 and on the run" "$calls" "[--platform] [linux/amd64] [--cpus]"
# A platform the lane cannot name is refused rather than left to the daemon:
# on a shared host an unnamed pull is what swaps a tag for everyone (#1549
# review). RETND_UI_BROWSER_PLATFORM names one by hand.
lane_run STUB_ARCH=riscv -- specs "$config"
is "L16 an architecture it cannot name is refused as a machine that cannot run the lane" 3 "$status" "$out"
hasnt "L16 and starts nothing" "$calls" "docker [run]"
lane_run STUB_ARCH=riscv RETND_UI_BROWSER_PLATFORM=linux/riscv64 -- specs "$config"
has "L16 unless RETND_UI_BROWSER_PLATFORM names one" "$calls" "[--platform] [linux/riscv64] [--cpus]"
lane_run RETND_UI_BROWSER_STALL=soon -- specs "$config"
is "L13 a stall limit that is not a number is refused" 1 "$status" "$out"
hasnt "L13 before anything starts" "$calls" "docker [run]"
lane_run -- specs ui/shared/e2e/no-such.config.ts
is "L14 a config that is not in the tree is refused" 1 "$status" "$out"
lane_run --
is "L15 no arguments is a usage error" 2 "$status" "$out"
lane_run -- shell
is "L15 an unknown mode is a usage error" 2 "$status" "$out"

echo "==> the skip guard (#1588)"
# A spec run that quietly runs less must not read as green: the lane writes a
# JSON report beside the list reporter and refuses any skip that is not the
# E2E_VIDEO-gated recording (the walkthroughs' own reason says so).
has "S1 the specs run writes a JSON report beside the list reporter" "$run_line" "--reporter=list,json"
has "S1 and the container checks it with the skip guard after the run" "$run_line" "ui_skips.py"
skips="$repo_root/scripts/bdtools/e2e/ui_skips.py"
report() { # report <file> <status> <annotation json>: one spec with one test
  python3 - "$1" "$2" "$3" <<'PY'
import json, sys
path, status, ann = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
t = {"status": status, "annotations": ann, "results": []}
spec = {"file": "x.spec.ts", "title": "a test", "tests": [t]}
json.dump({"suites": [{"file": "x.spec.ts", "suites": [{"title": "d", "specs": [spec]}], "specs": []}]}, open(path, "w"))
PY
}
guard() { out="$(python3 "$skips" "$1" 2>&1)"; status=$?; }
report "$work/r1.json" expected '[]'
guard "$work/r1.json"
is "S2 a report with no skip passes" 0 "$status" "$out"
report "$work/r2.json" skipped '[{"type":"skip","description":"recording only, run with E2E_VIDEO=1"}]'
guard "$work/r2.json"
is "S3 an E2E_VIDEO-gated skip passes" 0 "$status" "$out"
report "$work/r3.json" skipped '[{"type":"skip"}]'
guard "$work/r3.json"
is "S4 a bare test.skip is refused" 1 "$status" "$out"
has "S4 and names the file" "$out" "x.spec.ts"
report "$work/r4.json" skipped '[{"type":"fixme","description":"flaky"}]'
guard "$work/r4.json"
is "S5 a test.fixme is refused" 1 "$status" "$out"
report "$work/r5.json" skipped '[]'
guard "$work/r5.json"
is "S6 a skip with no reason at all is refused" 1 "$status" "$out"
report "$work/r7.json" skipped '[{"type":"skip","description":"flaky, E2E_VIDEO=1 would fix it"}]'
guard "$work/r7.json"
is "S9 a reason that only mentions E2E_VIDEO is refused, the exact recording form is required" 1 "$status" "$out"
report "$work/r8.json" skipped null
guard "$work/r8.json"
is "S10 annotations: null is read as no reason, a refused skip" 1 "$status" "$out"
hasnt "S10 with no traceback" "$out" "Traceback"
printf '{"suites": 3}' >"$work/r9.json"
guard "$work/r9.json"
is "S11 a report of the wrong shape is a clean refusal" 1 "$status" "$out"
hasnt "S11 with no traceback" "$out" "Traceback"
guard "$work/no-such-report.json"
is "S7 a missing report is refused, never read as no skips" 1 "$status" "$out"
printf 'not json' >"$work/r6.json"
guard "$work/r6.json"
is "S8 a report that does not parse is refused" 1 "$status" "$out"

echo "ui-browser-lane: $((checks - failures))/$checks passed"
[ "$failures" -eq 0 ]

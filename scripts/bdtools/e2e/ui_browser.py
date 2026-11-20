"""The Chromium lane for the shared UI's own browser specs (#1532, #1537).

Two things in this tree drive a real browser and, until this lane existed,
ran in no gate step: the 127 Playwright specs in `ui/shared/e2e/`, which are
the proof the Dashboard, Settings, Agents and suite UI work landed with, and
EPIC Q's browser-side mutation self-test (`scripts/assurance/ui-selftest.sh`).
Both need node, a linux install of `ui/shared`'s lockfile, `@playwright/test`
and a Chromium, and no build host has all four.

So the lane brings them along. It runs one container from the Playwright
image every other browser step already pins (the contrast check, the e2e
machines), with the tree mounted READ-ONLY at its own path, which is what
makes it work from inside `scripts/ci-docker.sh` on a remote daemon as well
as on a laptop: the daemon resolves the path on its own disk, where the
shipped tree is. Inside, `ui/` and `scripts/` are copied to the container's
own /tmp and everything else is linked, `npm ci` makes a linux install there
(never the host's node_modules, which on a Mac hold darwin binaries), and
the command runs. Nothing is written to the tree, and `--rm` takes the rest.

    ui_browser.py specs <playwright config> [playwright args...]
    ui_browser.py run <command...>

`specs` runs `npx playwright test -c <config>` from the config's workspace
(the directory above the one it sits in) with a per-test timeout, a global
timeout and the list reporter. `run` runs any command from the copy's root;
the gate uses it for the assurance UI self-test.

Every run is bounded twice, because a browser that hangs is the failure the
lane is most likely to meet and the one that is least visible:

  * a hard cap (RETND_UI_BROWSER_TIMEOUT seconds), enforced inside the
    container and again around it, so a wedged daemon cannot hold the gate;
  * a stall check: when the run's output has not grown for
    RETND_UI_BROWSER_STALL seconds it is treated as stalled, the process
    table is printed, and the run is killed.

Either one is a FAILED run, never a skip: a spec that hangs is a red spec.
Output is what the stall check watches, so the container runs Python
unbuffered: the assurance UI self-test prints a line per control, and with
stdout on a pipe those lines sat in a buffer until a healthy run was killed
as stalled, six controls in.

Exit statuses, so the gate does not parse prose:
  0  the command passed
  1  it failed, hit the cap or stalled
  3  this machine could not run the lane at all (no daemon, the image could
     not be pulled, the container could not start, the install inside could
     not run). The gate ledgers it, so the run ends INCOMPLETE.

Knobs: RETND_UI_BROWSER_IMAGE (the pinned image), RETND_UI_BROWSER_CPUS (4),
RETND_UI_BROWSER_MEMORY (4g), RETND_UI_BROWSER_TIMEOUT (5400 for `specs`,
2700 for `run`), RETND_UI_BROWSER_STALL (600), RETND_UI_BROWSER_TEST_TIMEOUT
(60, seconds per test, `specs` only), RETND_UI_BROWSER_PLATFORM (the daemon's
own by default; it goes on the pull and the run).
"""

from __future__ import annotations

import os
import re
import secrets
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools import harness

PROGRAM = "ui browser lane"

# The same digest scripts/contrast/run-browser-check.sh and
# scripts/e2e/client-machine.Dockerfile pin.
IMAGE = (
    "mcr.microsoft.com/playwright:v1.62.1-noble"
    "@sha256:dcc5531e97840b9b5e794f2814476b21571c5124a3fca2267d73041f56e7580e"
)

# `docker info`'s Architecture, as the platform the daemon runs natively.
NATIVE = {"x86_64": "linux/amd64", "amd64": "linux/amd64", "aarch64": "linux/arm64", "arm64": "linux/arm64"}

# Inside the container: 96 is "the lane could not be prepared", 98 is "the
# run stalled", 124 is coreutils timeout's own "the cap was reached".
PREP_FAILED = 96
STALLED = 98
CAPPED = 124

# The container's side of the lane. Positional: the tree, the mode, then the
# mode's arguments. Settings arrive as LANE_* variables.
INNER = r"""
set -u
src=$1 mode=$2
shift 2
w=/tmp/w
mkdir -p "$w/ui" "$w/scripts" /tmp/h
# ui/ and scripts/ are copied (the self-test resolves its root from its own
# file, and ui/shared needs a node_modules of its own); everything else is
# linked, read-only, at the same relative place.
tar -C "$src/ui" --exclude=node_modules --exclude=./shared/dist --exclude=./shared/test-results -cf - . \
  | tar -C "$w/ui" -xf - || exit 96
tar -C "$src/scripts" -cf - . | tar -C "$w/scripts" -xf - || exit 96
for entry in "$src"/* "$src"/.[!.]*; do
  [ -e "$entry" ] || continue
  name=${entry##*/}
  case $name in ui|scripts|.git|node_modules) continue ;; esac
  ln -s "$entry" "$w/$name"
done
cd "$w/ui/shared" || exit 96
echo "==> ui browser lane: npm ci in a copy of ui/shared (a linux install of its lockfile, inside the container)"
npm ci --no-audit --no-fund --loglevel=error || exit 96
npm i --no-save --no-audit --no-fund --loglevel=error "@playwright/test@$LANE_PW" || exit 96

case $mode in
  specs)
    cfg=$1
    shift
    ws=${cfg%/*/*}
    cd "$w/$ws" || exit 96
    set -- npx playwright test -c "${cfg#"$ws"/}" --output /tmp/pw-out --reporter=list \
      --timeout "$((LANE_TEST_TIMEOUT * 1000))" --global-timeout "$((LANE_CAP * 1000))" "$@"
    ;;
  run)
    cd "$w" || exit 96
    ;;
esac

echo "==> ui browser lane: $* (cap ${LANE_CAP}s, stalled after ${LANE_STALL}s without output)"
log=/tmp/lane.log
: >"$log"
setsid "$@" >"$log" 2>&1 &
pid=$!
tail -n +1 -F --pid="$pid" "$log" 2>/dev/null &
tailer=$!
started=$(date +%s)
last=-1
idle=0
verdict=""
while kill -0 "$pid" 2>/dev/null; do
  sleep 5
  size=$(stat -c %s "$log")
  if [ "$size" = "$last" ]; then idle=$((idle + 5)); else idle=0; last=$size; fi
  if [ "$idle" -ge "$LANE_STALL" ]; then verdict=stalled; fi
  if [ "$(( $(date +%s) - started ))" -ge "$LANE_CAP" ]; then verdict=capped; fi
  if [ -n "$verdict" ]; then
    sleep 1
    echo "==> ui browser lane: $verdict (${idle}s without output, $(( $(date +%s) - started ))s in). The process table:"
    ps -eo pid,etimes,rss,args | cut -c1-200 | head -n 40
    kill -TERM -- "-$pid" 2>/dev/null
    sleep 10
    kill -KILL -- "-$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
    kill "$tailer" 2>/dev/null
    [ "$verdict" = stalled ] && exit 98
    exit 124
  fi
done
wait "$pid"
status=$?
wait "$tailer" 2>/dev/null
exit "$status"
"""


def setting(name: str, default: int) -> int:
    raw = os.environ.get(name, "")
    if not raw:
        return default
    if not raw.isdigit() or int(raw) <= 0:
        harness.die(f"{name}={raw!r} is not a positive number of seconds.")
    return int(raw)


def playwright_version(image: str) -> str:
    m = re.search(r":v(\d+\.\d+\.\d+)-", image)
    if not m:
        harness.die(
            f"cannot read a Playwright version out of the image {image!r}.",
            "The lane installs the @playwright/test that matches the browsers in the image, so the tag has to say "
            "which (mcr.microsoft.com/playwright:vX.Y.Z-noble@sha256:...).",
        )
    return m.group(1)


def body(root: Path, argv: list[str], name: str) -> int:
    mode, rest = argv[0], argv[1:]
    image = os.environ.get("RETND_UI_BROWSER_IMAGE", IMAGE)
    cap = setting("RETND_UI_BROWSER_TIMEOUT", 5400 if mode == "specs" else 2700)
    stall = setting("RETND_UI_BROWSER_STALL", 600)
    test_timeout = setting("RETND_UI_BROWSER_TEST_TIMEOUT", 60)
    # 4 and 4g: the whole run of the specs took 56 minutes on MARS at 3.5
    # cpus (#1537), against a 5400s cap, so the fence is not where to save.
    cpus = os.environ.get("RETND_UI_BROWSER_CPUS", "4")
    memory = os.environ.get("RETND_UI_BROWSER_MEMORY", "4g")
    version = playwright_version(image)

    if mode == "specs" and not (root / rest[0]).is_file():
        harness.die(f"{rest[0]} is not a file in this tree.")

    harness.require_docker()
    # Always a named platform on the pull and the run: the daemon's own, unless
    # RETND_UI_BROWSER_PLATFORM says otherwise. A shared host (MARS) asks every
    # pull and run to name one, so the lane can never swap a shared tag for the
    # other architecture.
    platform = os.environ.get("RETND_UI_BROWSER_PLATFORM", "")
    if not platform:
        arch = harness.sh(["docker", "info", "--format", "{{.Architecture}}"], check=False).stdout.strip()
        platform = NATIVE.get(arch, "")
    plat = ["--platform", platform] if platform else []
    if not harness.sh_ok(["docker", "image", "inspect", image]):
        harness.step(f"pulling {image}")
        if harness.sh(["docker", "pull", *plat, image], check=False, capture=False).returncode != 0:
            harness.cannot_run(f"could not pull {image}, so this machine cannot run the lane.")

    tree = str(root)
    cmd = [
        "docker", "run", "--rm", "--init", "--name", name, *plat,
        "--cpus", cpus, "--memory", memory, "--memory-swap", memory, "--shm-size", "1g", "--pids-limit", "4096",
        "-e", "CI=1", "-e", "HOME=/tmp/h", "-e", "PYTHONUNBUFFERED=1",
        "-e", f"LANE_PW={version}", "-e", f"LANE_CAP={cap}", "-e", f"LANE_STALL={stall}",
        "-e", f"LANE_TEST_TIMEOUT={test_timeout}",
        "-v", f"{tree}:{tree}:ro", "-w", "/tmp",
        image, "bash", "-c", INNER, "ui-browser-lane", tree, mode, *rest,
    ]  # fmt: skip
    harness.step(f"{mode} {' '.join(rest)} in {image.split('@', 1)[0]} ({cpus} cpus, {memory}, cap {cap}s)")
    try:
        # The outer bound covers the image start and the install as well as
        # the run the inner cap measures.
        status = subprocess.run(cmd, check=False, timeout=cap + 900).returncode
    except subprocess.TimeoutExpired:
        harness.die(f"the lane did not come back within {cap + 900}s, past its {cap}s cap; its container is removed.")

    if status == 0:
        return harness.EXIT_OK
    if status == PREP_FAILED:
        harness.cannot_run("the container could not be prepared (the copy or the npm install in it failed, see above).")
    if status in (125, 126, 127):
        harness.cannot_run(f"docker could not start the lane's container (exit {status}, see above).")
    if status == STALLED:
        harness.die(f"the run STALLED: no output for {stall}s, so it was killed (the process table is above).")
    if status == CAPPED:
        harness.die(f"the run hit its {cap}s cap and was killed (RETND_UI_BROWSER_TIMEOUT moves it).")
    harness.die(f"{' '.join(rest)} failed in the lane (exit {status}, see above).")


def main(argv: list[str]) -> int:
    harness.set_program(PROGRAM)
    usage = "usage: ui-browser.sh specs <playwright config> [playwright args...] | run <command...>"
    if len(argv) < 2 or argv[0] not in ("specs", "run"):
        print(usage, file=sys.stderr)
        return harness.EXIT_USAGE
    harness.install_signal_handlers()
    root = harness.repo_root(Path(__file__))
    # Named, so it can be removed on every way out: a docker client killed by
    # the outer bound or a signal leaves its container running otherwise.
    name = f"retnd-ui-browser-{os.getpid()}-{secrets.token_hex(3)}"

    def remove_container(_status: int) -> None:
        harness.sh_ok(["docker", "rm", "-f", name])

    return harness.finish(lambda: body(root, argv, name), teardown=remove_container)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))

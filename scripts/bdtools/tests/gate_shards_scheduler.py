#!/usr/bin/env python3
"""Self-test for the shard placement in scripts/bdtools/release/gate_shards.py
(#1497, #1672 P1.3 to P1.7 and P2.1).

The recorder used to send every shard of one run to the one --host it was
given. scripts/release/gate-hosts.txt now says where each shard may run, and
gate_shards.py places, schedules, moves and retries shards from it. This suite
drives gate_shards.py as the recorder does, through its command line, with a
stand-in runner that writes down which shard it was asked to run on which host
and plays whatever outcome its mode file names. It needs ssh-keygen (a
throwaway key signs the stand-in shards) and nothing else: no daemon, no
network, no host.

It runs as part of scripts/tests/run-gate-shard.test.sh, which is a gate step.
"""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from bdtools.tests import testkit

REPO = Path(__file__).resolve().parents[3]
GATE_SHARDS = REPO / "scripts/bdtools/release/gate_shards.py"
MANIFEST = REPO / "scripts/release/gate-shards.txt"
TABLE = REPO / "scripts/release/gate-hosts.txt"

STUB_RUNNER = r"""#!/usr/bin/env bash
# Stand-in for run-gate-shard.sh: <shard> [--host <h>]. Writes "<shard> <host>
# start|end" to $STUB_DIR/calls and plays the mode in $STUB_DIR/mode-<shard>
# (or mode-<shard>@<host>, which wins): ok, fail, incomplete, or log:<file>:<rc>;
# a|b plays a on the first call and b from then on.
shard="$1"; shift
host=auto
[ "${1:-}" = "--host" ] && host="$2"
d="$STUB_DIR"
echo "$shard $host start" >>"$d/calls"
mode="$(cat "$d/mode-$shard" 2>/dev/null || echo ok)"
[ -f "$d/mode-$shard@$host" ] && mode="$(cat "$d/mode-$shard@$host")"
# a|b|c: the first call plays a, the second b, and the last one repeats
n="$(cat "$d/count-$shard" 2>/dev/null || echo 0)"; echo $((n + 1)) >"$d/count-$shard"
IFS='|' read -r -a seq <<<"$mode"
if [ "${#seq[@]}" -gt 1 ]; then
  i=$n; [ "$i" -lt "${#seq[@]}" ] || i=$((${#seq[@]} - 1)); mode="${seq[$i]}"
fi
case "$mode" in
  *-once)
    if [ -e "$d/seen-$shard" ]; then mode=ok; else : >"$d/seen-$shard"; mode="${mode%-once}"; fi ;;
esac
sleep "${STUB_SLEEP:-0}"
echo "==> toolchain: go go1.27.0 linux/amd64"
echo "$shard $host end" >>"$d/calls"
case "$mode" in
  ok) echo "==> ci-local: ok (shard $shard)"; exit 0 ;;
  fail)
    echo "--- FAIL: TestTheSourceCapsConnections (0.4s)"
    echo "==> ci-local: FAILED (shard $shard)"; exit 1 ;;
  incomplete) echo "==> ci-local: INCOMPLETE (shard $shard). run-gate-shard: exit 255"; exit 3 ;;
  log:*)
    spec="${mode#log:}"; cat "$d/${spec%%:*}"; exit "${spec##*:}" ;;
esac
echo "stub: unknown mode $mode" >&2; exit 2
"""


class Fixture:
    """A throwaway signing key, a worktree that names a Go minor, a stand-in
    runner and an empty shards dir."""

    def __init__(self, root: Path, name: str) -> None:
        self.root = root / name
        self.stub = self.root / "stub"
        self.shards = self.root / "local-gate" / "shards" / "tree1"
        self.worktree = self.root / "wt"
        for d in (self.stub, self.shards, self.worktree / "core"):
            d.mkdir(parents=True)
        (self.worktree / "core/go.mod").write_text("module x\n\ngo 1.27\n")
        self.key = root / "key"
        if not self.key.exists():
            subprocess.run(["ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "t", "-f", str(self.key)], check=True)
        pub = (root / "key.pub").read_text().split()
        self.signers = self.root / "allowed_signers"
        self.signers.write_text(f'retnd-release namespaces="retnd-local-gate" {pub[0]} {pub[1]}\n')
        self.runner = self.root / "runner.sh"
        self.runner.write_text(STUB_RUNNER)
        self.runner.chmod(0o755)

    def manifest(self, names: list[str]) -> Path:
        p = self.root / "gate-shards.txt"
        p.write_text("\n".join(names) + "\n")
        return p

    def table(self, text: str, manifest: Path) -> Path:
        p = manifest.with_name("gate-hosts.txt")
        p.write_text(text)
        return p

    def mode(self, shard: str, mode: str, host: str = "") -> None:
        (self.stub / (f"mode-{shard}@{host}" if host else f"mode-{shard}")).write_text(mode + "\n")

    def calls(self) -> list[str]:
        p = self.stub / "calls"
        return p.read_text().splitlines() if p.is_file() else []

    def stand_ins(self) -> Path:
        """ssh, docker, uptime and df stand-ins for the host probe. Per host
        (the ssh target, or mac): $STUB_DIR/down-<h> makes ssh or docker fail,
        load-<h> holds one load per line (one per probe, the last repeats),
        free-<h> the free KiB."""
        b = self.root / "bin"
        if b.is_dir():
            return b
        b.mkdir()
        scripts = {
            "ssh": 'echo "ssh $*" >>"$STUB_DIR/probes"\nt="${@: -2:1}"\n'
                   '[ -e "$STUB_DIR/down-$t" ] && { echo "ssh: connect to host $t: No route to host" >&2; exit 255; }\n'
                   'GATE_PROBE_HOST="$t" exec bash -c "${@: -1}"\n',
            "docker": 'h="${GATE_PROBE_HOST:-mac}"\n'
                      '[ -e "$STUB_DIR/down-$h-docker" ] && '
                      '{ echo "Cannot connect to the Docker daemon" >&2; exit 1; }\n'
                      'echo 29.0.0\n',
            "uptime": 'h="${GATE_PROBE_HOST:-mac}"; f="$STUB_DIR/load-$h"; n=0\n'
                      '[ -f "$STUB_DIR/n-$h" ] && n="$(cat "$STUB_DIR/n-$h")"; echo $((n + 1)) >"$STUB_DIR/n-$h"\n'
                      'load=0.50; if [ -f "$f" ]; then load="$(sed -n "$((n + 1))p" "$f")"; '
                      '[ -n "$load" ] || load="$(tail -n 1 "$f")"; fi\n'
                      'echo " 10:00:00 up 3 days,  2 users,  load average: $load, 1.00, 1.00"\n',
            "df": 'h="${GATE_PROBE_HOST:-mac}"; free=99999999\n'
                  '[ -f "$STUB_DIR/free-$h" ] && free="$(cat "$STUB_DIR/free-$h")"\n'
                  'echo "Filesystem 1024-blocks Used Available Capacity Mounted on"\n'
                  'echo "/dev/x 999999999 1 $free 1% $2"\n',
        }
        for name, body in scripts.items():
            p = b / name
            p.write_text("#!/usr/bin/env bash\n" + body)
            p.chmod(0o755)
        return b

    def touch(self, name: str, text: str = "") -> None:
        (self.stub / name).write_text(text)

    def run(self, manifest: Path, *extra: str, env: dict[str, str] | None = None, command: str = "run",
            key: bool = True) -> tuple[str, int]:
        argv = [sys.executable, str(GATE_SHARDS), command, "--manifest", str(manifest), "--manifest-sha", "m1",
                "--dir", str(self.shards), "--gate-tree", "tree1", "--head", "h1", "--worktree", str(self.worktree),
                "--signers", str(self.signers), "--runner", str(self.runner),
                *(["--key", str(self.key)] if key else []), *extra]
        full = dict(os.environ, STUB_DIR=str(self.stub), **(env or {}))
        full["PATH"] = f"{self.stand_ins()}:{full.get('PATH', '')}"
        return testkit.run(argv, env=full, timeout=300)

    def attempts(self, shard: str) -> list[dict[str, object]]:
        p = self.shards / f"{shard}.attempts.json"
        data = json.loads(p.read_text()) if p.is_file() else []
        assert isinstance(data, list)
        return data

    def record(self, shard: str) -> dict[str, object]:
        data = json.loads((self.shards / f"{shard}.json").read_text())
        assert isinstance(data, dict)
        return data


def overlap(calls: list[str], host: str | None = None, only: set[str] | None = None) -> int:
    """The most shards that were between their start and end at once."""
    now = most = 0
    for c in calls:
        shard, h, what = c.split()
        if (host and h != host) or (only is not None and shard not in only):
            continue
        now += 1 if what == "start" else -1
        most = max(most, now)
    return most


def place(manifest: Path, *extra: str, table: Path | None = None) -> tuple[str, int]:
    argv = [sys.executable, str(GATE_SHARDS), "place", "--manifest", str(manifest)]
    if table is not None:
        argv += ["--hosts-table", str(table)]
    return testkit.run([*argv, *extra], timeout=60)


TABLE_3 = """host mars devshell@192.168.0.100
host nas rom@192.168.0.10
host mac -
shard static light mars,nas,mac est_min=5
shard core-a exclusive mars,nas est_min=9
shard ui-browser* light mars,nas est_min=20
shard ui-browser-selftest light nas est_min=3
"""
NAMES_3 = ["static", "core-a", "ui-browser-1", "ui-browser-2", "ui-browser-selftest"]


def table_cases(s: testkit.Suite, root: Path) -> None:
    # T1 the committed table places every committed shard
    out, rc = place(MANIFEST)
    lines = [ln for ln in out.splitlines() if ln and not ln.startswith(" ")]
    names = [ln.split()[0] for ln in lines]
    want = subprocess.run([sys.executable, str(GATE_SHARDS), "manifest", str(MANIFEST)], capture_output=True,
                          text=True).stdout.split()
    s.check(rc == 0 and names == want,
            "T1 the committed gate-hosts.txt places every shard of gate-shards.txt, in order",
            "T1 the committed table", f"rc={rc}\n{out}")
    s.check("mercury" not in out and any(ln.split()[:3] == ["gh-rows", "light", "mac"] for ln in lines),
            "T2 no shard can go to mercury, and gh-rows (the one that needs gh) stays on the Mac",
            "T2 committed placement", out)
    docker_heavy = {"core-kopia", "core-rest", "core-docker", "apps", "e2e", "two-machine", "structure"}
    heavy = [ln.split() for ln in lines if ln.split()[0] in docker_heavy]
    s.check(len(heavy) == len(docker_heavy) and all(h[1] == "exclusive" and "mac" not in h[2] for h in heavy),
            "T3 the Docker-heavy shards are exclusive and stay off the Mac",
            "T3 docker-heavy shards", out)

    f = Fixture(root, "table")
    m = f.manifest(NAMES_3)
    out, rc = place(m, table=f.table(TABLE_3, m))
    got = {ln.split()[0]: ln.split()[2] for ln in out.splitlines() if ln and not ln.startswith(" ")}
    s.check(rc == 0 and got.get("ui-browser-1") == "mars,nas" and got.get("ui-browser-2") == "mars,nas",
            "T4 a prefix line (ui-browser*) covers every slice of the shard", "T4 prefix", out)
    s.check(got.get("ui-browser-selftest") == "nas", "T5 an exact name beats a prefix", "T5 exact over prefix", out)
    elsewhere = f.root / "elsewhere.txt"
    elsewhere.write_text(TABLE_3.replace("ui-browser-selftest light nas", "ui-browser-selftest light mac"))
    out, rc = place(m, "--hosts-table", str(elsewhere))
    s.check(rc == 0 and " mac " in next((ln for ln in out.splitlines() if ln.startswith("ui-browser-selftest")), ""),
            "T6 --hosts-table reads another table than the one beside the manifest", "T6 --hosts-table", out)

    bad = [
        ("R1 a line for a shard the manifest does not have", TABLE_3 + "shard gone light mars est_min=1\n", "gone"),
        ("R2 a manifest shard no line covers", TABLE_3.replace("shard static light mars,nas,mac est_min=5\n", ""),
         "static"),
        ("R3 a host no host line declares",
         TABLE_3.replace("core-a exclusive mars,nas", "core-a exclusive mars,jupiter"), "jupiter"),
        ("R4 mercury as a host", TABLE_3 + "host mercury mercury\n", "mercury"),
        ("R5 mercury on a shard line", TABLE_3.replace("static light mars,nas,mac", "static light mars,mercury"),
         "mercury"),
        ("R6 a shard named twice", TABLE_3 + "shard static light mars est_min=1\n", "static"),
        ("R7 a host declared twice", TABLE_3 + "host mars x@y\n", "mars"),
        ("R8 a host twice on one line", TABLE_3.replace("static light mars,nas,mac", "static light mars,mars"), "mars"),
        ("R9 a class that is not light or exclusive", TABLE_3.replace("core-a exclusive", "core-a heavy"), "heavy"),
        ("R10 no est_min", TABLE_3.replace("core-a exclusive mars,nas est_min=9", "core-a exclusive mars,nas"),
         "est_min"),
        ("R11 a line that is neither host nor shard", TABLE_3 + "slot mars 3\n", "slot"),
    ]
    for label, text, needle in bad:
        out, rc = place(m, table=f.table(text, m))
        s.check(rc == 2 and needle in out and "REFUSED" in out, f"{label} is refused (exit 2) and named",
                f"{label}", f"rc={rc}\n{out}")
    out, rc = place(m, table=f.table(TABLE_3, m))
    s.check(rc == 0, "R12 control: the same table without the fault is accepted", "R12 control", out)


def placement_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "placement")
    m = f.manifest(NAMES_3)
    t = f.table(TABLE_3, m)

    def hosts_of(out: str) -> dict[str, str]:
        return {ln.split()[0]: ln.split()[2] for ln in out.splitlines() if ln and not ln.startswith(" ")}

    out, rc = place(m, "--place", "core-a=mac", table=t)
    s.check(rc == 0 and hosts_of(out).get("core-a") == "mac", "O1 --place puts one shard where it says, off its line",
            "O1 --place", out)
    out, rc = place(m, "--place", "core-a=nas,mac", "--place", "static=nas", table=t)
    h = hosts_of(out)
    s.check(rc == 0 and h.get("core-a") == "nas,mac" and h.get("static") == "nas", "O2 --place repeats, in order",
            "O2 --place twice", out)
    for label, args, needle in [
        ("O3 --place for a shard the manifest does not have", ["--place", "nope=mars"], "nope"),
        ("O4 --place onto mercury", ["--place", "core-a=mercury"], "mercury"),
        ("O5 --place onto a host the table does not have", ["--place", "core-a=jupiter"], "jupiter"),
        ("O6 --drain of an unknown host", ["--drain", "jupiter"], "jupiter"),
        ("O7 --host mercury", ["--host", "mercury"], "mercury"),
    ]:
        out, rc = place(m, *args, table=t)
        s.check(rc == 2 and needle in out, f"{label} is refused", label, f"rc={rc}\n{out}")
    out, rc = place(m, "--drain", "mars", table=t)
    h = hosts_of(out)
    s.check(rc == 0 and h.get("static") == "nas,mac" and h.get("core-a") == "nas",
            "O8 --drain takes a host off every line, the rest keep their order", "O8 --drain", out)
    out, rc = place(m, "--drain", "mac", "--place", "core-a=mac,nas", table=t)
    s.check(rc == 0 and hosts_of(out).get("core-a") == "nas", "O9 --drain wins over --place",
            "O9 drain over place", out)
    out, rc = place(m, "--hosts", "mars", table=t)
    s.check(rc == 2 and "ui-browser-selftest" in out and "--place" in out,
            "O10 --hosts that leaves a shard nowhere to run is refused, naming the shard and --place",
            "O10 --hosts leaves a shard homeless", f"rc={rc}\n{out}")
    out, rc = place(m, "--hosts", "mars", "--place", "ui-browser-selftest=mac", table=t)
    h = hosts_of(out)
    s.check(rc == 0 and h.get("static") == "mars" and h.get("ui-browser-selftest") == "mac",
            "O11 --hosts narrows every line and --place still wins over it", "O11 --hosts", out)
    out, rc = place(m, "--host", "mac", table=t)
    s.check(rc == 0 and set(hosts_of(out).values()) == {"mac"}, "O12 --host puts every shard on that one host",
            "O12 --host", out)


def run_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "run")
    m = f.manifest(NAMES_3)
    f.table(TABLE_3, m)
    out, rc = f.run(m, "--jobs", "1")
    calls = {c.split()[0]: c.split()[1] for c in f.calls() if c.endswith(" start")}
    s.check(rc == 0 and calls == {"static": "mars", "core-a": "mars", "ui-browser-1": "mars", "ui-browser-2": "mars",
                                  "ui-browser-selftest": "nas"},
            "X1 with a table beside the manifest, each shard is sent to the first host on its line",
            "X1 run places from the table", f"rc={rc} calls={f.calls()}\n{out}")
    s.check(rc == 0 and (f.shards / "ui-browser-selftest.json").is_file()
            and f.record("ui-browser-selftest").get("host") == "nas",
            "X2 the signed record names the host the shard ran on", "X2 record host", out)

    g = Fixture(root, "legacy")
    m2 = g.manifest(["static", "core-a"])
    out, rc = g.run(m2)
    calls2 = sorted(g.calls())
    s.check(rc == 0 and calls2 == ["core-a auto end", "core-a auto start", "static auto end", "static auto start"],
            "X3 no table beside the manifest (an older tree): no --host is passed, as before",
            "X3 legacy", f"rc={rc} calls={calls2}\n{out}")
    h = Fixture(root, "legacy-host")
    m3 = h.manifest(["static"])
    out, rc = h.run(m3, "--host", "nas")
    s.check(rc == 0 and h.calls()[:1] == ["static nas start"], "X4 and --host still sends it there",
            "X4 legacy --host", f"rc={rc} calls={h.calls()}\n{out}")


HOSTS_4 = """host mars t-mars slots=2 poll_s=0
host nas t-nas slots=1 poll_s=0
host mac - slots=1 poll_s=0
host jupiter t-jup slots=1 poll_s=0
"""


def mars_with(keys: str) -> str:
    return HOSTS_4.replace("host mars t-mars slots=2 poll_s=0", "host mars t-mars slots=2 poll_s=0 " + keys)


def schedule_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "slots")
    names = [f"l{i}" for i in range(6)]
    m = f.manifest(names)
    f.table(HOSTS_4 + "".join(f"shard {n} light mars est_min=1\n" for n in names), m)
    out, rc = f.run(m, env={"STUB_SLEEP": "0.4"})
    s.check(rc == 0 and overlap(f.calls(), "mars") == 2,
            "S1 a host never runs more shards than its slots, and does fill them (mars slots=2: 2 at once)",
            "S1 slots", f"rc={rc} most={overlap(f.calls(), 'mars')} calls={f.calls()}\n{out}")

    f = Fixture(root, "exclusive")
    m = f.manifest(["x1", "x2", "x3", "l1", "l2"])
    f.table(HOSTS_4.replace("slots=2", "slots=3")
            + "shard x* exclusive mars est_min=5\nshard l* light mars est_min=1\n", m)
    out, rc = f.run(m, env={"STUB_SLEEP": "0.4"})
    s.check(rc == 0 and overlap(f.calls(), "mars", {"x1", "x2", "x3"}) == 1,
            "S2 two exclusive shards never overlap on one host", "S2 exclusive", f"rc={rc} calls={f.calls()}\n{out}")
    s.check(overlap(f.calls(), "mars") >= 2, "S3 light shards still run beside an exclusive one",
            "S3 light beside exclusive", f"calls={f.calls()}")

    f = Fixture(root, "longest")
    m = f.manifest(["short", "long", "middle"])
    f.table(HOSTS_4.replace("slots=2", "slots=1")
            + "shard short light mars est_min=1\nshard long light mars est_min=9\n"
            + "shard middle light mars est_min=5\n", m)
    out, rc = f.run(m)
    order = [c.split()[0] for c in f.calls() if c.endswith(" start")]
    s.check(rc == 0 and order == ["long", "middle", "short"], "S4 the longest shard starts first",
            "S4 longest first", f"order={order}\n{out}")

    f = Fixture(root, "jobs")
    m = f.manifest(["a", "b", "c"])
    f.table(HOSTS_4 + "shard a light mars est_min=1\nshard b light nas est_min=1\nshard c light mac est_min=1\n", m)
    out, rc = f.run(m, "--jobs", "1", env={"STUB_SLEEP": "0.3"})
    s.check(rc == 0 and overlap(f.calls()) == 1, "S5 --jobs caps the whole run across hosts",
            "S5 --jobs", f"calls={f.calls()}\n{out}")


def failover_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "failover")
    m = f.manifest(["core-a", "static"])
    f.table(HOSTS_4 + "shard core-a exclusive mars,nas est_min=9\nshard static light mars,nas est_min=1\n", m)
    f.mode("core-a", "incomplete", "mars")
    f.touch("down-t-mars")
    out, rc = f.run(m, "--jobs", "1")
    hosts = [a["host"] for a in f.attempts("core-a")]
    s.check(rc == 0 and hosts == ["mars", "nas"] and f.record("core-a").get("host") == "nas",
            "F1 an INCOMPLETE on a host whose probe fails drains it, and the shard is signed on the next host "
            "with the first attempt kept", "F1 failover", f"rc={rc} hosts={hosts}\n{out}")
    s.check("static mars start" not in f.calls() and "static nas start" in f.calls(),
            "F2 a drained host takes no more shards in that run", "F2 drained", f"calls={f.calls()}")
    s.check(any("drained" in ln and "mars" in ln for ln in out.splitlines()), "F3 the drain is said out loud",
            "F3 drain message", out)

    f = Fixture(root, "no-move")
    m = f.manifest(["core-a"])
    f.table(HOSTS_4 + "shard core-a exclusive mars,nas est_min=9\n", m)
    f.mode("core-a", "incomplete", "mars")
    out, rc = f.run(m)
    s.check(rc == 3 and [c for c in f.calls() if c.endswith("start")] == ["core-a mars start"],
            "F4 an INCOMPLETE on a host that still answers its probe does not move", "F4 no move",
            f"rc={rc} calls={f.calls()}\n{out}")

    f = Fixture(root, "failed-stays")
    m = f.manifest(["core-a"])
    f.table(HOSTS_4 + "shard core-a exclusive mars,nas est_min=9\n", m)
    f.mode("core-a", "fail", "mars")
    f.touch("down-t-mars")
    out, rc = f.run(m)
    s.check(rc == 1 and not any(c.startswith("core-a nas") for c in f.calls()),
            "F5 a FAILED shard never moves, even when its host is down afterwards", "F5 failed stays",
            f"rc={rc} calls={f.calls()}\n{out}")

    f = Fixture(root, "two-moves")
    m = f.manifest(["core-a"])
    f.table(HOSTS_4 + "shard core-a light mars,nas,jupiter,mac est_min=9\n", m)
    for h, t in (("mars", "t-mars"), ("nas", "t-nas"), ("jupiter", "t-jup")):
        f.mode("core-a", "incomplete", h)
        f.touch(f"down-{t}")
    out, rc = f.run(m)
    tried = [c.split()[1] for c in f.calls() if c.endswith("start")]
    s.check(rc == 3 and tried == ["mars", "nas", "jupiter"], "F6 a shard moves at most twice",
            "F6 two moves", f"rc={rc} tried={tried}\n{out}")


def wait_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "busy")
    m = f.manifest(["core-a"])
    f.table(mars_with("max_load=4 wait_min=0")
            + "shard core-a light mars est_min=9\n", m)
    f.touch("load-t-mars", "9.00\n")
    out, rc = f.run(m)
    s.check(rc == 3 and not f.calls() and "load" in out and [a["exit"] for a in f.attempts("core-a")] == [3],
            "W1 a host that stays over its max_load: INCOMPLETE with the reason, the shard never starts, the wait "
            "is an attempt", "W1 load timeout", f"rc={rc} calls={f.calls()}\n{out}")

    f = Fixture(root, "quiets")
    m = f.manifest(["core-a"])
    f.table(mars_with("max_load=4 wait_min=1")
            + "shard core-a light mars est_min=9\n", m)
    f.touch("load-t-mars", "9.00\n8.00\n1.00\n")
    out, rc = f.run(m)
    s.check(rc == 0 and f.calls()[:1] == ["core-a mars start"],
            "W2 the shard starts once the load drops under max_load", "W2 load drops",
            f"rc={rc} calls={f.calls()}\n{out}")

    f = Fixture(root, "full")
    m = f.manifest(["core-a"])
    f.table(mars_with("min_free_gib=6 wait_min=0 scratch=/work")
            + "shard core-a light mars est_min=9\n", m)
    f.touch("free-t-mars", str(2 * 1024 * 1024))
    out, rc = f.run(m)
    s.check(rc == 3 and not f.calls() and "free" in out, "W3 less free space than min_free_gib: INCOMPLETE, never run",
            "W3 free space", f"rc={rc} calls={f.calls()}\n{out}")


def probe_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "probe")
    m = f.manifest(["a", "b"])
    t = f.table(HOSTS_4 + "shard a light mars est_min=1\nshard b light nas,mac est_min=1\n", m)
    env = dict(os.environ, STUB_DIR=str(f.stub), PATH=f"{f.stand_ins()}:{os.environ.get('PATH', '')}")
    argv = [sys.executable, str(GATE_SHARDS), "probe", "--manifest", str(m), "--hosts-table", str(t)]
    out, rc = testkit.run(argv, env=env, timeout=60)
    lines = sorted(ln.split()[0] + " " + ln.split()[1] for ln in out.splitlines() if ln.strip())
    s.check(rc == 0 and lines == ["mac ok", "mars ok", "nas ok"],
            "P1 probe prints one ok line per host the placement uses (jupiter is unused)", "P1 probe",
            f"rc={rc}\n{out}")
    f.touch("down-t-nas")
    out, rc = testkit.run(argv, env=env, timeout=60)
    s.check(rc == 1 and any(ln.startswith("nas refuse") and "ssh" in ln for ln in out.splitlines()),
            "P2 a host whose ssh fails is a refuse line naming ssh, and the exit is non-zero", "P2 probe refuse",
            f"rc={rc}\n{out}")
    out, rc = testkit.run([*argv, "--drain", "nas"], env=env, timeout=60)
    s.check(rc == 0 and "nas" not in out, "P3 a drained host is not probed", "P3 drain", f"rc={rc}\n{out}")


PLAYWRIGHT_RED = """Running 1144 tests using 1 worker
  ✓   396 e2e/1202-backups-row-menu.spec.ts:120:3 \u203a #1202 the Backups row Actions menu \u203a opens (1.1s)
  ✘   397 e2e/1202-backups-row-menu.spec.ts:138:3 \u203a #1202 the Backups row Actions menu \u203a \
#1202 Recover artifact (7.5s)
  ✘   870 e2e/1525-audit-table-and-collapse.spec.ts:43:3 \u203a #1525 the agent audit \u203a \
#1525 at a narrow width (779ms)

  1) e2e/1202-backups-row-menu.spec.ts:138:3 \u203a #1202 the Backups row Actions menu \u203a #1202 Recover artifact

    Error: expect(locator).toHaveCount(expected) failed

  2 failed
    e2e/1202-backups-row-menu.spec.ts:138:3 \u203a #1202 the Backups row Actions menu \u203a #1202 Recover artifact
    e2e/1525-audit-table-and-collapse.spec.ts:43:3 \u203a #1525 the agent audit \u203a #1525 at a narrow width
  3 skipped
  1139 passed (56.5m)

==> ui browser lane: FAILED. ui/shared/e2e/playwright.config.ts failed in the lane \
(exit 1, see above).
==> ci-local: FAILED (ui/shared browser specs (ui/shared/e2e), every file, in the pinned Playwright image \
(#1537), shard s)
"""

# A mutation self-test: two mutants caught (their suites' "--- FAIL:" lines are
# the proof it caught them), and one control that fired for the wrong reason,
# with the Go output of the mutant quoted under it.
SELFTEST_RED = """==> the encryption and suite guards can actually fail (mutation self-test, #1531)
SELFTEST FAIL: NV canary identity in the key created log line. it failed, but never named \
TestNoKeyMaterialOnAnySurface_996/NV.28.
    --- FAIL: TestNoKeyMaterialOnAnySurface_996 (0.27s)
        canary_test.go:510: Discover: state: insert artifact: database is locked (517)
    FAIL
==> the age/v1 status changed and the golden list left as it was (T1.1, TV.1)
  ok:   (caught): the age/v1 status changed and the golden list left as it was
      -> --- FAIL: TestSuiteIDsAreStableAgainstTheGoldenList (0.00s) | suite_test.go:37: the suite table reads
  ok:   (caught): N1.2 the engine route loses the reason
      -> --- FAIL: TestARefusalIsTheSameOnBothRoutes (0.80s); \
--- FAIL: TestARefusalIsTheSameOnBothRoutes/x (0.03s)
==> ci-local: FAILED (the encryption and suite guards can actually fail (mutation self-test, #1531), shard s)
"""

STALL_RED = """==> the recovery guards can actually fail (mutation self-test, #976)
  ok:   (caught): a mutant
      -> --- FAIL: TestRecover1193_ANonSFTPSetHasNoDefaultAndSaysWhy (0.04s)
==> stall watchdog: STALLED: the recovery guards can actually fail (mutation self-test, #976) \
(no output for 600s, past the 600s no-progress limit). Its processes were stopped. What it looked like:
        step: the recovery guards can actually fail (mutation self-test, #976)
        --- the last 40 lines it printed ---
        --- FAIL: TestQuotedInTheDump (0.10s)
==> ci-local: FAILED (STALLED: the recovery guards can actually fail (mutation self-test, #976), \
no output for 600s, shard s)
"""

GO_RED_A = "--- FAIL: TestAlertRefresh_1567 (50.16s)\n==> ci-local: FAILED (core suite, shard s)\n"
GO_RED_B = "--- FAIL: TestSFTPConnectionsAreReleasedAndBounded (25.85s)\n" \
           "    --- FAIL: TestSFTPConnectionsAreReleasedAndBounded/TheProbesCanSeeAConnection (6.11s)\n" \
           "==> ci-local: FAILED (core suite, shard s)\n"
LINT_RED = "core/x.go:1:1: undefined: y (typecheck)\n==> ci-local: FAILED (core golangci-lint, shard s)\n"


def retry_cases(s: testkit.Suite, root: Path) -> None:
    def case(name: str, logs: dict[str, str], mode: str, *extra: str) -> tuple[Fixture, str, int]:
        f = Fixture(root, name)
        m = f.manifest(["s"])
        f.table(HOSTS_4 + "shard s light mars est_min=1\n", m)
        for fname, text in logs.items():
            (f.stub / fname).write_text(text)
        f.mode("s", mode)
        out, rc = f.run(m, *extra)
        return f, out, rc

    def tests_of(f: Fixture) -> list[object]:
        return [a["failed_tests"] for a in f.attempts("s")]

    f, out, rc = case("go-flake", {"a.log": GO_RED_A}, "log:a.log:1|ok", "--retry-once")
    s.check(rc == 0 and tests_of(f) == [["TestAlertRefresh_1567"], []],
            "C1 control: a Go test that fails once and then passes is retried and recorded, the failure kept",
            "C1 go flake", f"rc={rc} {tests_of(f)}\n{out}")

    f, out, rc = case("pw-flake", {"pw.log": PLAYWRIGHT_RED}, "log:pw.log:1|ok", "--retry-once")
    want = ["spec: e2e/1202-backups-row-menu.spec.ts:138:3", "spec: e2e/1525-audit-table-and-collapse.spec.ts:43:3"]
    s.check(rc == 0 and tests_of(f) == [want, []],
            "C2 a Playwright spec that fails once is retried, and the attempt names each spec from the list "
            "reporter's summary", "C2 playwright flake", f"rc={rc} {tests_of(f)}\n{out}")

    f, out, rc = case("st-flake", {"st.log": SELFTEST_RED}, "log:st.log:1|ok", "--retry-once")
    got = tests_of(f)
    s.check(rc == 0 and got == [["selftest: NV canary identity in the key created log line"], []],
            "C3 a self-test control that fails once is retried, named by its control", "C3 self-test control",
            f"rc={rc} {got}\n{out}")
    first = got[0] if got and isinstance(got[0], list) else []
    s.check(not any("TestNoKeyMaterialOnAnySurface_996" in str(t) or "TestSuiteIDs" in str(t)
                    or "TestARefusal" in str(t) for t in first),
            "C4 the --- FAIL: lines of a caught mutant, or quoted under a failed control, are not failed tests",
            "C4 mutant lines counted as failed tests", str(first))

    f, out, rc = case("stall", {"stall.log": STALL_RED}, "log:stall.log:1|ok", "--retry-once")
    got = tests_of(f)
    s.check(rc == 0 and got == [["stalled: the recovery guards can actually fail (mutation self-test, #976)"], []],
            "C5 a stalled step is retried once, named by its step, and nothing quoted in the stall dump counts",
            "C5 stall", f"rc={rc} {got}\n{out}")

    f, out, rc = case("pw-twice", {"pw.log": PLAYWRIGHT_RED}, "log:pw.log:1", "--retry-once")
    s.check(rc == 1 and len(f.attempts("s")) == 2 and not (f.shards / "s.json").exists(),
            "C6 the same spec failing on the retry is FAILED for good: two attempts, nothing signed",
            "C6 same failure twice", f"rc={rc} attempts={len(f.attempts('s'))}\n{out}")

    f, out, rc = case("go-other", {"a.log": GO_RED_A, "b.log": GO_RED_B}, "log:a.log:1|log:b.log:1", "--retry-once")
    got = tests_of(f)
    s.check(rc == 1 and len(got) == 2 and got[1] == ["TestSFTPConnectionsAreReleasedAndBounded",
                                                      "TestSFTPConnectionsAreReleasedAndBounded/TheProbesCanSeeAConnection"],
            "C7 a different test failing on the retry is FAILED too, and both attempts name theirs",
            "C7 different failure", f"rc={rc} {got}\n{out}")

    f, out, rc = case("lint", {"l.log": LINT_RED}, "log:l.log:1|ok", "--retry-once")
    s.check(rc == 1 and len(f.attempts("s")) == 1, "C8 a lint or build failure (nothing named) is not retried",
            "C8 lint", f"rc={rc} attempts={len(f.attempts('s'))}\n{out}")

    f, out, rc = case("no-flag", {"pw.log": PLAYWRIGHT_RED}, "log:pw.log:1|ok")
    s.check(rc == 1 and len(f.attempts("s")) == 1, "C9 without --retry-once nothing is retried",
            "C9 no flag", f"rc={rc}\n{out}")


def rehearsal_cases(s: testkit.Suite, root: Path) -> None:
    f = Fixture(root, "rehearse")
    m = f.manifest(["static", "core-a"])
    f.table(HOSTS_4 + "shard static light mars est_min=1\nshard core-a light nas est_min=2\n", m)
    rdir = f.root / "local-gate" / "rehearsal" / "tree1"
    out, rc = f.run(m, "--rehearse", key=False)
    recs = {p.name: json.loads(p.read_text()) for p in rdir.glob("*.json") if not p.name.endswith(".attempts.json")}
    s.check(rc == 0 and sorted(recs) == ["core-a.json", "static.json"]
            and all(r.get("schema") == "retnd.local-gate.rehearsal/1" and r.get("result") == "ok"
                    for r in recs.values()),
            "H1 run --rehearse runs every shard with no key and writes retnd.local-gate.rehearsal/1 results under "
            "local-gate/rehearsal/<tree>/", "H1 rehearsal results", f"rc={rc} {sorted(recs)}\n{out}")
    s.check(not list(f.root.rglob("*.sig")) and not list(f.shards.iterdir()),
            "H2 a rehearsal signs nothing and leaves the shards dir alone", "H2 nothing signed",
            f"{list(f.root.rglob('*.sig'))} {list(f.shards.iterdir())}")
    last = (out.strip().splitlines() or [""])[-1]
    s.check("REHEARSAL" in last and "not a record" in last, "H3 its last line says it is a rehearsal, not a record",
            "H3 last line", out)

    f.mode("core-a", "fail")
    out, rc = f.run(m, "--rehearse", key=False)
    rec = json.loads((rdir / "core-a.json").read_text()) if (rdir / "core-a.json").is_file() else {}
    s.check(rc == 1 and rec.get("result") == "failed" and bool(rec.get("attempts")),
            "H4 a red shard in a rehearsal is written down as failed, with its attempts, and the exit is 1",
            "H4 failed rehearsal", f"rc={rc} {rec}\n{out}")

    g = Fixture(root, "planted")
    m2 = g.manifest(["static"])
    g.table(HOSTS_4 + "shard static light mars est_min=1\n", m2)
    out, rc = g.run(m2, "--rehearse", key=False)
    grdir = g.root / "local-gate" / "rehearsal" / "tree1"
    planted_ok = rc == 0 and (grdir / "static.json").is_file()
    if planted_ok:
        for name in ("static.json", "static.log"):
            shutil.copy(grdir / name, g.shards / name)
        subprocess.run(["ssh-keygen", "-Y", "sign", "-q", "-n", "retnd-local-gate", "-f", str(g.key),
                        str(g.shards / "static.json")], check=True, capture_output=True)
    before = len(g.calls())
    out, rc = g.run(m2)
    s.check(planted_ok and rc == 0 and len(g.calls()) > before and "reused" not in out,
            "H5 a rehearsal result planted in the shards dir, even signed, is never reused as a record",
            "H5 planted rehearsal reused", f"rc={rc} calls={g.calls()}\n{out}")
    if planted_ok:
        for name in ("static.json", "static.log"):
            shutil.copy(grdir / name, g.shards / name)
        subprocess.run(["ssh-keygen", "-Y", "sign", "-q", "-n", "retnd-local-gate", "-f", str(g.key),
                        str(g.shards / "static.json")], check=True, capture_output=True)
    out, rc = g.run(m2, "--out", str(g.root / "prov"), command="aggregate")
    s.check(rc != 0 and "rehearsal" in out and not (g.root / "prov" / "local-gate.json").exists(),
            "H6 aggregate refuses a rehearsal result, naming it", "H6 aggregate", f"rc={rc}\n{out}")

    h = Fixture(root, "signed-then-rehearse")
    m3 = h.manifest(["static"])
    h.table(HOSTS_4 + "shard static light mars est_min=1\n", m3)
    h.run(m3)
    before = len(h.calls())
    out, rc = h.run(m3, "--rehearse", key=False)
    s.check(rc == 0 and len(h.calls()) > before, "H7 a rehearsal runs a shard even when it is signed for the tree",
            "H7 rehearsal ignores signed shards", f"rc={rc}\n{out}")

    bad = Fixture(root, "odd-dir")
    m4 = bad.manifest(["static"])
    bad.table(HOSTS_4 + "shard static light mars est_min=1\n", m4)
    bad.shards = bad.root / "elsewhere"
    bad.shards.mkdir()
    out, rc = bad.run(m4, "--rehearse", key=False)
    s.check(rc == 2 and not bad.calls(), "H8 --rehearse refuses a --dir that is not <git dir>/local-gate/shards/<tree>",
            "H8 odd dir", f"rc={rc}\n{out}")


def main() -> int:
    s = testkit.Suite("gate-shards scheduler")
    if shutil.which("ssh-keygen") is None:
        s.bad("ssh-keygen is not on PATH; the stand-in shards cannot be signed")
        return s.finish()
    root = Path(tempfile.mkdtemp(prefix="gate-shards-sched."))
    try:
        table_cases(s, root)
        placement_cases(s, root)
        run_cases(s, root)
        schedule_cases(s, root)
        failover_cases(s, root)
        wait_cases(s, root)
        probe_cases(s, root)
        retry_cases(s, root)
        rehearsal_cases(s, root)
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return s.finish()


if __name__ == "__main__":
    sys.exit(main())

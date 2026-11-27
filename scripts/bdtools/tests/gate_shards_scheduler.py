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
# (or mode-<shard>@<host>, which wins): ok, fail, incomplete, or log:<file>:<rc>.
shard="$1"; shift
host=auto
[ "${1:-}" = "--host" ] && host="$2"
d="$STUB_DIR"
echo "$shard $host start" >>"$d/calls"
mode="$(cat "$d/mode-$shard" 2>/dev/null || echo ok)"
[ -f "$d/mode-$shard@$host" ] && mode="$(cat "$d/mode-$shard@$host")"
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

    def run(self, manifest: Path, *extra: str, env: dict[str, str] | None = None) -> tuple[str, int]:
        argv = [sys.executable, str(GATE_SHARDS), "run", "--manifest", str(manifest), "--manifest-sha", "m1",
                "--dir", str(self.shards), "--gate-tree", "tree1", "--head", "h1", "--worktree", str(self.worktree),
                "--signers", str(self.signers), "--runner", str(self.runner), "--key", str(self.key), *extra]
        full = dict(os.environ, STUB_DIR=str(self.stub), **(env or {}))
        return testkit.run(argv, env=full, timeout=300)

    def record(self, shard: str) -> dict[str, object]:
        data = json.loads((self.shards / f"{shard}.json").read_text())
        assert isinstance(data, dict)
        return data


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
    s.check(rc == 0 and f.record("ui-browser-selftest").get("host") == "nas",
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
    finally:
        shutil.rmtree(root, ignore_errors=True)
    return s.finish()


if __name__ == "__main__":
    sys.exit(main())

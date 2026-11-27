# The gate scheduler

`scripts/release/record-local-gate.sh --shards` hands the shards to
`scripts/bdtools/release/gate_shards.py run`, which decides where each shard
runs and calls `scripts/release/run-gate-shard.sh <shard> --host <host>` for
it. This page is about that placement (#1497, #1672 P1.3). How a shard is
signed and reused is in [local-gate.md](local-gate.md).

## The host table

`scripts/release/gate-hosts.txt` sits beside `gate-shards.txt` and is part of
the gate tree, so a change to it is a change to a gate file. Its header has the
format. In short:

```
host  mars devshell@192.168.0.100
shard core-rest   exclusive  mars,nas      est_min=18
shard ui-browser* light      mars,nas      est_min=65
```

- A shard line lists the hosts the shard may run on, in the order it prefers
  them. A host left off the line is never used for that shard.
- `ui-browser*` is a prefix line: it covers `ui-browser` and every slice the
  shard is split into. An exact line wins over a prefix.
- `exclusive` marks the Docker-heavy shards (their suites publish containers on
  the daemon's loopback). `est_min` is the usual wall time from the recorded
  runs. Both are for the scheduler; neither changes what a record means.
- The parser refuses a line for a shard the manifest does not have, a manifest
  shard with no line, an undeclared host, a duplicate, an unknown class, a
  missing `est_min`, and mercury anywhere.

A tree without the table (older than #1497) runs as before: every shard on
`--host`, or wherever the runner sends it when there is none.

## Overrides

The command line wins over the table, so losing a host never needs a commit.
After `--`, the recorder passes these to `gate_shards.py run` unchanged.

| Option | Effect |
|---|---|
| `--host <h>` | every shard on `<h>` (what the recorder always did) |
| `--place <shard>=<h>[,<h>...]` | that shard's hosts, off its line if need be (repeatable) |
| `--hosts <h>[,<h>...]` | only these hosts for every shard that is not `--place`d |
| `--drain <h>` | never this host, `--place` included (repeatable) |

A shard left with no host is refused before anything runs (exit 2), naming the
shard and `--place`. To see the placement without running anything:

```
python3 scripts/bdtools/release/gate_shards.py place --manifest scripts/release/gate-shards.txt --drain mars
```

It prints `<shard> <class> <hosts in order> est_min=<n>` per shard.

## Slots, order and the wait

Each host line has `slots=<n>` (default 1): MARS 3, the NAS 1, the Mac 1.
`gate_shards.py run` starts the shards longest first (by `est_min`), each on
the first host of its line with a free slot, and never runs two `exclusive`
shards on one host at once. `--jobs N` caps the whole run on top of that; it
defaults to the sum of the slots (or 3 for a tree without the table).

Before a shard starts, a host with `max_load` or `min_free_gib` is probed
(ssh, `docker info`, `uptime`, `df -Pk` under `scratch`), and the shard
waits, looking every `poll_s` seconds, until the host is under both. Past
`wait_min` minutes the shard is INCOMPLETE with the reason, and that wait is
an attempt in its record. The runner is never called.

## Failover

Only INCOMPLETE moves a shard. When one ends INCOMPLETE, its host is probed.
If the probe fails, the host is drained for the rest of the run (no shard
starts there again) and the shard goes back in the queue for the next host on
its line, at most twice. If the probe answers, the INCOMPLETE stands. A FAILED
shard never moves: a red test is the tree's, not the host's. Every attempt,
on every host, stays in the shard's signed record.

## Probe

```
python3 scripts/bdtools/release/gate_shards.py probe --manifest scripts/release/gate-shards.txt
```

prints `<host> ok load=<x> free_gib=<n>` (with `(busy: ...)` when the load is
over `max_load`) or `<host> refuse <why>` for every host the placement uses,
and exits 1 when any host refuses (no ssh, no docker, less free space than
`min_free_gib`). The recorder's preflight calls it when it is there.

## Tests

`scripts/bdtools/tests/gate_shards_scheduler.py`, run by
`scripts/tests/run-gate-shard.test.sh` (a gate step). It drives
`gate_shards.py` through its command line with a stand-in runner that writes
down which shard it was asked to run on which host.

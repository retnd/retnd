# EPIC Q recovery: what a 4 GiB recovery costs in memory

Nothing in `/api/v1` had streamed a byte before recovery, so the memory shape
of the download path was new machinery. The issue asked for it to be measured
rather than assumed, in the CLI and in the web process, with the numbers and
the host written down. This is that record.

This is a **measurement with stated ceilings, not a regression gate**. The
ceilings are asserted every time the harness runs; nothing runs it on every
push, because it writes about twelve gigabytes.

## Running it

```
cd apps/generic
RECOVERY_PERF_BYTES=4294967296 RECOVERY_PERF_OUT=/some/dir/result.json \
  go test ./tests/recoveryperf/ -run Memory -v -count=1 -timeout 40m
```

It builds the real `retnd` and `retnd-web` binaries, writes one deterministic
incompressible artifact of that size into a local-remote backup set, ingests it
with a real `retnd run` (so the journal holds its SHA-256 and a local
placement), and then:

- runs `retnd recover <id> --to -` with standard output hashed by the harness,
  and reads the process's peak resident set size from the rusage the kernel
  kept for it;
- starts `retnd-web serve`, enrolls over real HTTP, mints a recovery, downloads
  it over loopback hashing the body, stops the process and reads its peak
  resident set size the same way;
- does the web step once more **without downloading anything**, because the web
  process carries about 110 MiB of its own before it serves a byte (the
  embedded rclone and the HTTP stack), and what is attributable to the stream
  is the difference between the two runs, not the second number on its own.

Both recovered hashes are compared with the ingested artifact's, and the
staging area is checked to be empty afterwards.

## The host

```
host     MARS (devshell@192.168.0.100), shared, other containers running
cpu      Intel Core i7-14700F, 28 hardware threads
memory   32 GB
os       linux/amd64, run inside golang:1.27-bookworm (go1.27.2)
disk     ext4 on /dev/sda7, the artifact and its copies on the same filesystem
```

## The numbers (4 GiB, 2026-10-11)

| Process | Peak RSS | Control (same process, nothing recovered) | Attributable to the stream |
|---|---|---|---|
| `retnd recover --to -` | 46.7 MiB | 48.7 MiB (`retnd status`) | none measurable |
| `retnd-web serve` | 109.1 MiB | 111.3 MiB (served nothing) | none measurable (-2.1 MiB) |

Wall clock, for scale only: the CLI recovered 4 GiB in 33.9 s and the web
process staged, verified and sent it in 26.7 s including its own startup and
enrollment. The staged copy is on disk, not in memory, and is removed when the
last byte is served.

Both recovered bytes hashed to
`bccae53a3aa98301d3a5f25065aed4ccbee009e29b5fe4d917ce5be798ea19d1`, the
ingested artifact's SHA-256.

## The ceilings

| Ceiling | Value | What it separates |
|---|---|---|
| CLI peak RSS | 256 MiB | a 256 KiB copy buffer (about 50 MiB, which is the process's own baseline) from buffering any meaningful part of 4 GiB |
| Web growth over its own control | 128 MiB | the same, for the process that serves |

They are deliberately loose against the measurement and deliberately far under
a buffered implementation (which would reach 4 GiB). A change that starts
holding the artifact, or a megabyte per concurrent range, moves the number
by orders of magnitude and not by a few percent, which is the only kind of
change these ceilings exist to catch.

## A thousand and more copies due at once (#983)

The day a catalog is rebuilt, or a scheduler is switched on for a fleet that
never had one, every copy is due at the same instant. The simulation of that day is
`core/internal/recovery/budget/scale_test.go`: the real read limiter and the
real verifying engine, a simulated scheduler that hands them 50 copies at a
time and waits for a slot rather than being refused (the pass size is the
test's own, because no product code bounds a pass), and the peaks read off the
limiter rather than asserted from outside. Most of the fleet is on one destination on purpose, so the
per-destination ceiling has something to hold.

```
RECOVERY_BUDGET_COPIES=1200 RECOVERY_BUDGET_BYTES=1048576 \
  RECOVERY_BUDGET_OUT=/some/dir/result.json \
  go test ./internal/recovery/budget/ -run OneThousandDue -v -count=1
```

| | Measured | Bound |
|---|---|---|
| copies due at once | 1200, 1 MiB each (1.2 GiB read, hashed and staged) | |
| passes to finish them | 24 | 24 (1200 copies at 50 a pass) |
| most full reads running at once | 2 | 2 |
| most full reads of one destination at once | 1 | 1 |
| refusals | 0 | |
| wall clock | 0.7 s | |

Run on the host above, linux/amd64, 28 threads, under a docker container with
the usual neighbours. The wall clock is for scale only: a copy here is read
from memory, so it measures the engine's bookkeeping and the staging disk, not
a destination. The same simulation with a refusing limiter (what an operator at a
terminal gets) also never exceeds either ceiling, and refuses thousands of
attempts on the way, which is `TestAnInteractiveStyleLimiterRefusesInsteadOfWaitingAndStillNeverExceedsTheCeilings`.

## What this does not measure

- **Concurrent streams.** One recovery at a time. The concurrent-stream ceiling
  and the global bound on simultaneous full reads belong to the budgets work
  (#983).
- **A remote copy.** The artifact here is a local copy, so the read side is a
  file. A remote copy goes through `transport.MediumStore.OpenObject`, whose
  own buffering is the provider adapter's; the staging engine reads it through
  the same 256 KiB buffer, but the adapter's memory is not measured here.
- **A slow client.** Loopback drains instantly. The write-deadline behaviour for
  a client slower than the server's 30 second write timeout is covered by
  `handlers_recovery_test.go` rather than by this harness, because a
  loopback run would hide exactly that failure.
- **Time to first byte for a remote copy.** The whole copy is read and verified
  before the first byte is sent, so a browser waits for the entire
  remote-to-staging transfer before its download starts. A gateway in front of
  the engine needs a read timeout longer than that.

## Re-measured for the release gate (#988)

The release gate asks for the large-download budget as a measured number with the host it was
measured on, so it was run again on the composed EPIC Q tree, after the other lanes' work had
landed in it, rather than quoting the run above. One difference from the run above, stated rather
than hidden: it is 2 GiB, not 4 GiB, because the shared host's disk had about 14 GB free and the
4 GiB run writes about twelve, so the artifact lived on a 12 GB tmpfs instead. The ceilings below
are the harness's own and are size-independent (a fixed 256 KiB buffer either holds or it does not).

```
host     MARS (devshell@192.168.0.100), shared, other containers running
cpu      Intel Core i7-14700F, 28 hardware threads
memory   32 GB
os       linux/amd64, golang:1.27-bookworm, artifact and staging on tmpfs
command  RECOVERY_PERF_BYTES=2147483648 go test ./tests/recoveryperf/ -run Memory -v -count=1
```

| Process | Peak RSS | Control (nothing recovered) | Attributable to the stream |
|---|---|---|---|
| `retnd recover --to -` | 47.5 MiB | 45.8 MiB (`retnd status`) | none measurable |
| `retnd-web serve` | 112.1 MiB | 112.9 MiB (served nothing) | none measurable (-0.7 MiB) |

Both recovered hashes equalled the ingested artifact's. The harness builds the two binaries with
`GOWORK=off`, and on a tree that has taken the SQLite bump in `core` that build of `apps/generic`
fails (`go: updates to go.mod needed`), which is also true of a clean `main`; this run built them
under the workspace instead, by pointing the harness at `go.work`, and nothing else in it changed.

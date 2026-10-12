# EPIC Q recovery: what a 4 GiB recovery costs in memory

Nothing in `/api/v1` had streamed a byte before recovery, so the memory shape
of the download path was new machinery. The issue asked for it to be measured
rather than assumed, in the CLI and in the web process, with the numbers and
the host written down. This is that record.

This is a **measurement with stated ceilings, not a regression gate**. The
ceilings are asserted every time the harness runs; nothing runs it on every
push, because it writes about eight gigabytes (the artifact and the local copy
ingest makes of it).

> **Re-measured on the streamed path.** A download is no longer staged: it is
> streamed from the retained copy one verified 4 MiB chunk at a time, after a
> preparation read that stores only chunk digests (`recover_stream.go`). The
> first table below is the measurement of the streamed path, taken on 2026-10-12.
> The staged implementation's numbers (2026-10-11, MARS) are kept at the end as
> history and are not a claim about what ships.

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

## The host (streamed path, 2026-10-12)

```
machine  Apple M5, 10 cores, 32 GiB, macOS 27.0.1 (the laptop that ran Docker)
docker   Docker Desktop, engine 29.8.1, linuxkit kernel 7.0.14, 8 vCPUs, 15.6 GiB for the VM
image    golang:1.27-bookworm, linux/arm64 (go1.27.2), run as uid 1000 inside it
arch     arm64 (native; nothing emulated)
data     tmpfs (RAM-backed) for the artifact, its copy, the state dir and the build
         caches. The Docker VM's disk had no free space, so nothing could be put on it
```

Because the data sits in RAM, the wall-clock figures below are memory speed and
say nothing about a disk or a NAS. The resident-set figures are what this
measurement is for: a tmpfs page is shared memory, not part of a process's
resident set, so it does not flatter them.

Two things about how it was run, so it can be repeated or doubted:

- The harness builds both binaries with `GOWORK=off`. On this tree that fails
  before it starts (`go: updates to go.mod needed`): `core/go.mod` is at
  `modernc.org/sqlite` 1.60.1 and `apps/common/go.mod` and `apps/generic/go.mod`
  still say 1.58.0 and have not been tidied. It fails the same way on `origin/main`
  (1cd17849), so it is not this branch's. I removed the `GOWORK=off` line from the
  harness for the run, so the two binaries were built in workspace mode, and put it
  back afterwards. Nothing about the measurement depends on it.
- The CLI ran first and so paid the preparation read (one pass over the copy to
  build the chunk digests). The web process ran after it and found the stored
  digests, so its number is the warm path. The cold path's memory is the CLI's: it
  did the preparation read and the stream in one process.

## The numbers: 4 GiB, streamed (arm64)

Peak resident set size, read from the rusage the kernel keeps for each process
after it exits. 4 GiB is 4,294,967,296 bytes.

| Process | Peak RSS | Control (same process, nothing recovered) | Attributable to the stream |
|---|---|---|---|
| `retnd recover --to -` | 52.5 MiB (55,058,432 B) | 42.9 MiB (44,998,656 B, `retnd status`) | 9.6 MiB over its own baseline |
| `retnd-web serve` (warm digests) | 115.6 MiB (121,225,216 B) | 110.1 MiB (115,486,720 B, served nothing) | 5.5 MiB (5,738,496 B) |

Wall clock, for scale only (memory-speed storage, see above): the CLI prepared,
verified and wrote 4 GiB in 8.7 s, and the web process started, enrolled,
minted and streamed it in 5.7 s including its own 2 s settle.

Both recovered bytes hashed to
`bccae53a3aa98301d3a5f25065aed4ccbee009e29b5fe4d917ce5be798ea19d1`, the ingested
artifact's SHA-256 (the same payload, so the same digest as the staged run), and
the staging area was empty afterwards.

For scale I ran the same harness at 256 MiB straight before it. The CLI peaked at
51.3 MiB and the web process grew by 5.0 MiB. Sixteen times the data moved both
by a few MiB at most, which is the shape a bounded chunk gives and a buffer does
not.

### The per-chunk bound

What bounds the memory is one chunk. `export.VerifiedReader` holds a single
`recoveryChunkSize` buffer (4 MiB, `export.DefaultChunkSize`) and checks it
against the index before releasing any of it, so a download's resident cost is
that buffer and the HTTP and rclone machinery the process already carried. The
9.6 MiB and 5.5 MiB above are a few chunks at most. The ceilings below are far
above that on purpose.

Concurrent downloads multiply it: N streams hold about N chunks. This harness ran
one at a time; the stream ceiling (`download_streams`, #983) is what bounds N.

## The ceilings

| Ceiling | Value | What it separates |
|---|---|---|
| CLI peak RSS | 256 MiB | a 4 MiB chunk buffer (about 50 MiB, which is the process's own baseline) from buffering any meaningful part of 4 GiB |
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
  own buffering is the provider adapter's; a ranged read opens it at a chunk
  boundary and the verified reader holds one chunk of it, but the adapter's
  memory is not measured here.
- **A slow client.** Loopback drains instantly. The write-deadline behaviour for
  a client slower than the server's 30 second write timeout is covered by
  `handlers_recovery_test.go` rather than by this harness, because a
  loopback run would hide exactly that failure.
- **Time to first byte.** The first download of an artifact makes a preparation
  read of the whole copy before the first byte is sent (later downloads and
  resumes reuse the stored chunk digests), so a browser waits for that read. A gateway in front of
  the engine needs a read timeout longer than that.

## History: the staged implementation (2026-10-11, MARS)

Kept so the change is visible. These numbers describe the code that wrote the
whole copy to a staging disk before sending it, and they are not a claim about
the streamed path above.

```
host     MARS (devshell@192.168.0.100), shared, other containers running
cpu      Intel Core i7-14700F, 28 hardware threads
memory   32 GB
os       linux/amd64, run inside golang:1.27-bookworm (go1.27.2)
disk     ext4 on /dev/sda7, the artifact and its copies on the same filesystem
```

| Process | Peak RSS | Control | Attributable |
|---|---|---|---|
| `retnd recover --to -` | 46.7 MiB | 48.7 MiB | none measurable |
| `retnd-web serve` | 109.1 MiB | 111.3 MiB | none measurable (-2.1 MiB) |

The CLI recovered 4 GiB in 33.9 s and the web process staged, verified and sent
it in 26.7 s including startup and enrollment.

## 1000+ copies due at once, on the real scrub driver (#983)

`TestAThousandDueCopiesDeferPastTheScrubBoundAndTheReadCeilingsAreNeverExceeded`
runs `revalidate.Run` (the driver the serve scheduler calls) over a real SQLite
journal holding 1050 copies on three destinations, all due at the same instant,
with the per-pass bound taken from `config.Assurance.Revalidation` (so
`DefaultScrubMaxPerCycle`) and the limiter and workers at the product
defaults. The only double is the medium, which takes 2 ms per existence check
and counts how many are in flight at once, in total and per destination.
Existence checks are what an unattended pass makes of a medium copy (reading a
medium in full is operator-initiated), so the count is of those.

```
cd core
SCRUB_SCALE_OUT=/some/dir/scale.json \
  go test -count=1 -v -run AThousandDue ./internal/revalidate/
```

```
host     Apple M5 MacBook, macOS 27.0.1; Docker Desktop, linuxkit kernel 7.0.14, 8 vCPUs
image    golang:1.27-bookworm, linux/arm64 (go1.27.2), uid 1000, build cache and journal on tmpfs
```

| | Measured | Line |
|---|---|---|
| copies, all due at once | 1050 | |
| passes to work the backlog off | 21 | 21 (1050 / 50) |
| copies taken by each pass | 50 (all 21) | 50 |
| copies still due after pass 1 | 1000 | 1000 |
| copies checked in the end | 1050 | 1050 |
| peak reads in flight, overall | 2 | 2 |
| peak reads in flight, one destination | 1 | 1 |
| wall time of the 21 passes | 4.6 s (a previous run on the same host: 1.7 s) | |

The peak equals the ceiling, which is the positive control: the harness can see
concurrency, so "never exceeded" is a reading. Two more tests run the same
backlog with a bound taken away (no per-pass bound; a limiter at 12) and
require the harness to report exactly that overrun. Wall time is mostly the
2 ms of simulated latency times 1050 checks divided by two workers, and says
nothing about a real bucket. What the numbers are for is the shape: the work is
spread over passes and the ceilings hold.

# MCP and CI audit of the last three weeks (2026-11-19)

I went through everything merged to `main` since 2026-10-29 and asked two questions of each feature. Can an
agent reach it through the MCP catalogue, or is it a named refusal with a reason, and is the guard that says
so real? And is every guard, matrix check, closing check and self-test it brought a step of
`scripts/ci-local.sh`, which is the only gate there is? This is what I found, what I fixed, and what I left
alone on purpose.

## How I built the inventory

`git log origin/main --since=2026-10-29 --merges` is 412 merge commits, most of them merges of `main` into
a branch and back. The real unit is the pull request: 127 merged in the window, 100 of them into `main` and
27 into `cut/0.8.0`, `release` or a feature branch (the 0.8.0 closure work). I read each one's title and the
issue it closes, grouped them into the features below, and for each feature listed the routes, CLI verbs and
screens it added by reading the diff and the catalogue (`core/agenttools/tools.go`), not the PR text.

I found no merged pull request of EPIC Q or of the rename to retnd in the window, so they are outside it. I
looked at their guards only where a newer feature depends on them (the assurance and recovery self-tests are already gate
steps, and I used them as the model for the missing ones).

## The inventory

"Covered" means I checked it and found nothing to do. The route guard behind the MCP column is
`TestEveryAPIRouteIsAToolOrANamedRefusal` in `apps/common/webhost`, which walks both routers a deployment
mounts, so a route nobody answered fails by name.

| # | Feature | Issue and PR | MCP | CI | Defect and fix |
|---|---|---|---|---|---|
| 1 | EPIC N: encryption policy, ADR, migration 0015 | #989 (#1279), spec #968 (#1266) | `encryption_state` reads it. The policy PATCH and reset are named refusals (`whyConfigure`) | EPIC N closing check is a step; Go guards run with the core suite | Self-test not run: #1531 |
| 2 | EPIC N: managed key custody, resolver, recovery bundle | #990 (#1280) | Key routes are denied and refused (`whyKeys`), the projection drops the material | as 1 | Self-test not run: #1531 |
| 3 | EPIC N: streaming age encrypt and decrypt-verify | #991 (#1281) | no route of its own; `verify_copy` (operate) is the verify start | as 1 | Self-test not run: #1531 |
| 4 | EPIC N: lifecycle, retention, rebuild, rotation of encrypted copies | #992 (#1283) | no route of its own | as 1 | Self-test not run: #1531 |
| 5 | EPIC N: CLI, API and observability parity | #993 (#1284) | `copies_show` and `encryption_state` (read), `verify_copy` (operate); the rest refused with a reason | as 1 | Self-test not run: #1531 |
| 6 | EPIC N: design and web UI | #994, #995 (#1282, #1285) | screen only | UI controls are in the encryption self-test, which does not run | #1531; browser specs not in the gate: #1537 |
| 7 | EPIC N: e2e, docs, captures, closing check | #996, #997, #998 (#1287, #1288, #1286) | n/a | `epic-n-closure.test.sh` is a step | covered |
| 8 | `retnd decrypt` and recovery-key import | #1289 (#1300) | CLI-only verb, no API route; the import route is denied (`whyKeys`) | core suite | covered |
| 9 | External key providers write copies, dropped-reference guard, key alerts | #1292, #1304, #1294 (#1293, #1309, #1301) | `/encryption/providers/test` denied; health reaches an agent through `status` | core suite | Self-test not run: #1531 |
| 10 | Encrypt existing plaintext copies | #1291 (#1302) | `GET` and `POST /encryption/encrypt-existing` named refusals (`whyRead`, `whyMutation`) | core suite | covered |
| 11 | Progress stages and live verify progress | #1290, #1303 (#1298, #1306) | shows in `operation_show` and `activity_live` | core suite | covered, but see 36 (oversize answers) |
| 12 | Installer key directory, kopia `required` | #1295, #1296 (#1297, #1299) | n/a | installer tests are steps | covered |
| 13 | EPIC T: suite registry, `age-pq/v1`, default | #1312, #1313 (#1322, #1323) | `encryption_state` reports the suite | EPIC T closing check is a step | Self-test not run: #1531 |
| 14 | EPIC T: suite policy and CLI/API parity | #1314, #1315 (#1324, #1325) | `GET /encryption/suites` is a named refusal (`whyRead`: `encryption_state` already says which suite is active) | as 13 | Self-test not run: #1531 |
| 15 | EPIC T: suite lifecycle, re-encrypt, health | #1316, #1317 (#1327, #1326) | `POST` and `GET /encryption/reencrypt` named refusals | as 13 | Self-test not run: #1531 |
| 16 | EPIC T: suite UI | #1318 (#1328) | screen only | its specs are in `ui/shared/e2e`, which no step runs | #1537 |
| 17 | EPIC T: tests, docs, closing check, cell fixes | #1319, #1320, #1358, #1376 | n/a | closing check is a step | covered (self-test: #1531) |
| 18 | EPIC U: agent tokens, bearer authenticator, scopes | #1345 (#1357) | `/agent/tokens` denied and refused (`whyTokens`); scope vocabulary pinned against `core/service` | EPIC U closing check is a step | Self-test not run, and an anchor already stale: #1530 |
| 19 | EPIC U: the catalogue, route guard, deny list, projection | #1346 (#1368) | this is the catalogue. Guard is real: it walks the router, has a planted-route control, and a golden for descriptions | `core/tests/compat` registers the EPIC U matrix | Self-test not run: #1530 |
| 20 | EPIC U: stdio and HTTP transports, rate limit, result cap | #1347, #1378 (#1364, #1382) | result cap is where the defect is | core suite | **AGENT_RESULT_TOO_LARGE dead end: #1529** (this PR) |
| 21 | EPIC U: audit trail | #1348 (#1365) | `/agent/audit` refused (`whyAudit`) | core suite | Self-test not run: #1530 |
| 22 | EPIC U: confirmations (operate and configure) | #1349, #1379, #1385, #1387 | 7 operate and 4 configure tools, each proposes only; deciding is refused (`whyConfirmDecide`) | core suite | Self-test not run: #1530 |
| 23 | EPIC U: Agents card, design note | #1350, #1351, #1392, #1393, #1395 | screen only | design guard is a Go test; browser specs not in the gate | #1537 |
| 24 | EPIC U: adversarial tests, docs, release gate | #1352, #1353 | injection, exfiltration and boundary suites read the catalogue | `epic-u-closure.test.sh` is a step | covered (self-test: #1530) |
| 25 | Sharded gate: manifest, `--shard`, runner, recorder, verifier | #1455 to #1463, #1468, #1461 | n/a | `run-gate-shard.test.sh` and `selftest-shard.test.sh` ran nowhere | #1533 |
| 26 | Remote gate host and image parity | #1267, #1471, #1477 | n/a | `ci-docker-remote.test.sh` is a step | covered |
| 27 | Fast tier on every commit, pre-merge status | #1361 (#1370) | n/a | `ci-fast.test.sh` is a step | covered, and see the hook section below |
| 28 | Signed local gate record, release e2e hooks | #1373, #1375 | n/a | `local-gate-record.test.sh` is a step, `release-e2e.test.sh` was not | #1533 |
| 29 | Release 0.8.0: cut, hashes, two targets, gate reds | #1332, #1452, #1485 to #1499 | n/a | release gate covers the recorded shards | covered |
| 30 | Gate step clock | #1456 (#1467) | n/a | in the gate self-test | covered |
| 31 | Dashboard activity: Show in Terminal, separate sets, caption header, dropped-lines notice | #1505, #1504, #1509, #1503 | UI over existing reads (`activity_live`) | specs live in `ui/shared/e2e`, no step runs them | #1537 |
| 32 | Declare a validator from the Validation card | #1520 (#1521) | UI over the existing validators routes (named refusals, `whyConfigure`) | as 31 | #1537 |
| 33 | Settings at 360px, no layout shift on Backups | #1337, #1430 | n/a | as 31 | #1537 |
| 34 | EPIC O: observability spec and matrix | #897 (#904) | spec only; its endpoint will need a catalogue row when it lands | matrix is not registered in `core/tests/compat`, by design until O1.5 | waits on #903 |
| 35 | Site served from retnd-org | #1501 (#1502) | n/a | covered | covered |
| 36 | Every list-bearing read tool (`operations_list`, `assurance_due`, `retention_preview` and the rest) | EPIC U | over 64 KiB with no cursor was a dead end | n/a | **#1529** |
| 37 | The assurance UI self-test (EPIC Q guard, still the proof of the UI rows) | #988 | n/a | `ui-selftest.sh` is no gate step | #1532, open |
| 38 | Retire a failed backup (in flight) | #1528 | owned by the agent on `fix/1528-retire-failed-artifacts` | owned there | not touched here |

Thirty-eight rows, twenty-six of them with a gap. Six defects are issues: #1529 (MCP), and #1530, #1531,
#1532, #1533 and #1537 (CI). I fixed four of them: #1529 in PR #1534, and #1530, #1531 and #1533 in PR #1538.
#1532 and #1537 need a host with a Playwright browser to prove the new step, and the build hosts have none, so
I filed them with the fix written down and left them open instead of wiring a step I could not watch run.

## What I checked on the MCP side, and found fine

- **Every route is a tool or a named refusal.** The guard walks the configured router, the first-run router
  and the auth router, asserts four routes it must see (so it cannot pass on an empty walk), and has planted
  controls for an unanswered route, a duplicate row and a stale one. Not vacuous.
- **Scopes.** A `GET` is `read`, a mutation is `operate` or `configure`, and the shape guard fails on a mismatch.
  Eleven mutating tools, all proposing: nothing an agent calls runs without a person approving it.
- **Descriptions are static.** One AST check refuses anything but a string literal and a golden file pins every
  sentence, so a new one is a reviewed change.
- **The projection.** The deny list of field names is applied at every depth (and plurals and suffixes), the
  canary shapes (age identity and recipient, PEM private key) are replaced, `ssh_key_id` goes, and a key
  provider's `source` goes. I read every response schema of the 20 read tools in `api/v1/openapi.json` for
  names that could carry material (`key`, `recipient`, `secret`, `token`, `path`, `host` and so on). What is
  left is `encryption_key_id`, `key_id`, `trusted_host_keys` and `fingerprint`, which name a key or hold a
  public host key and are not the key. Nothing to fix.
- **The deny list and the refusals agree.** Every entry of the deny list is a refusal row. The converse does
  not hold (a refusal like `whyConfigure` is not denied) and does not need to: a bearer may only `GET` a route
  a read tool sits on (`BearerReadable`) and may only change one a tool offers (`Offered`), so a refused
  route is unreachable whether or not it is on the list.
- **Observation, not a defect:** `copies_show` needs `query.artifact` and its description does not say so (the
  engine answers not-found without it). That is a one-line change to a golden file that #1528 is also editing,
  so I left it for after that merges.

## The MCP defect, and the fix (#1529)

Only `activity` carried a `next_cursor`, and the server only paged a route that did. Every other list answer
over 64 KiB was `AGENT_RESULT_TOO_LARGE` with "narrow the call", and `operations_list` takes no query at all,
`assurance_due` has two filters and no page, and `retention_preview` has neither. A test pinned that refusal as
the intended behaviour, so nothing noticed the dead end.

I made the server page a list itself when the route cannot: whole items of the largest array that fit, and a
cursor `retnd-page:<offset>:<fingerprint of the full answer>` that comes back as `query.before`. A cursor from
an answer that has since changed is refused, so a walk never skips or repeats an item, and a route's own
cursor still wins. The guard drives every read tool that answers an array from a fake engine answering 200 KiB
and needs every item back exactly once; a list whose single item is over the cap is still a refusal, and a
control proves the guard sees it. I broke the pager three ways (no cursor, no fingerprint check, a zero budget)
and each turned tests red.

I did not give each route a `limit` and a cursor of its own. That is the better answer for the artifact lists,
which #1528 does, but for the rest it is a contract entry, regenerated bindings, a CLI flag and the
web/CLI parity guards per route, which is a feature each.

## What I left to #1528

The artifact list filters and paging (`backups_list`, `quarantine_list`: a state filter, a set filter, a page)
and the new retire tool, with its catalogue row and its gate checks. The pager above does not meet its work:
it only steps in for a route that has no `next_cursor`, and the guard drives both lists so that if #1528 gives
them a cursor, nothing here has to change.

## The CI defects, and the fix

`scripts/ci-local.sh` ran the recovery and assurance self-tests (EPIC Q) but not the encryption one (N and T,
anchors only), not the EPIC U one, not the UI one, and not the self-tests of the sharded gate itself.

- **#1530**: `scripts/agents/selftest.sh` joins a new `selftest-agents` shard, and its anchors join
  `check_anchors.py` (ten self-tests now). It had already drifted: a control anchored on a line of
  `check-epic-u-closed.sh` that #1414 rewrote, so its anchor check was red. I fixed the anchor.
- **#1531**: `scripts/encryption/selftest.sh` joins a `selftest-encryption` shard. It is the longest
  of them (131 anchors), so it has a shard of its own.
- **#1533**: `run-gate-shard.test.sh`, `selftest-shard.test.sh` and `release-e2e.test.sh` are steps of the
  gate-self-test segment. They need no daemon: 1, 6 and 18 seconds on a gate image.
- **The guard that keeps it fixed.** `scripts/tests/every-guard-is-a-gate-step.test.sh` fails when a
  `selftest.sh`, `ui-selftest.sh`, `scripts/tests/*.test.sh` or the `ui/shared/e2e` Playwright config is in
  the tree and in no non-comment line of the gate. The exemption file can only shrink, and each entry
  names an open issue. Its own planted trees show it can fail.

The two new shards are in `scripts/release/gate-shards.txt`, so a sharded record now has to carry them and
the release gate covers them. Each has a stub in `scripts/tests/ci-local-gate.test.sh` and a line in the
serial-order golden.

### Left open, on purpose

- **#1532**, the assurance UI self-test, and **#1537**, the 127 specs in `ui/shared/e2e`: both need
  Chromium and the Playwright lock. They are the entries in the exemption file, each naming its issue.
- **EPIC O's matrix** is not registered in `core/tests/compat`. It is all `BLOCKED` on code that is not
  merged, and its own text says the checks land with O1.5 (#903), so I did not invent a registration.
- **#1525** (the Audit list on the Agents card) is a UI bug someone filed, not an MCP or CI gap.

## The pre-commit hook and `go test` on this Mac (reported, not changed)

`.husky/pre-commit` runs `scripts/ci-fast.sh --staged`. That script maps every staged path to packages and
runs `go vet`, `golangci-lint` and `go test` on them, and for a staged file under `docs/conformance/` or an
`docs/EPIC-*` spec it adds `core/tests/compat/matrix_test.go` (the `has '^(docs/conformance/|docs/EPIC-[A-Z]-)'`
branch, around line 229). So a docs or spec commit still compiles and links the `compat` test binary on the
Mac. A few agents committing together means a few parallel Go builds on a laptop that is already busy.

What I would do, in this order:

1. **One Go step at a time.** Take a lock (an `mkdir` lock in the git common dir or a shared path, with the wait
   and the load check `xdg-pw-lock.sh` already has) around the Go part of the fast tier, run it under `nice`, and
   cap it (`GOMAXPROCS=2`, `-p 1`). The hook then queues instead of piling up.
2. **A docs or spec commit does not need Go in the hook.** The compat test reads Markdown and checks paths exist.
   Let the hook run a small shell or Python check of the same shape (one row per gate line, cited files exist),
   and leave the real compat package to the pre-merge status and the release gate, which `ci-fast.sh` already
   says is where the live half of it belongs.
3. **If the Mac is loaded, send the Go steps to MARS.** When `uptime` is above a threshold, run the staged
   package tests through `scripts/ci-docker.sh --host` instead of locally, which already exists (#1267).

I would do 1 and 2 first: they remove the problem without needing a network.

## What I ran, and on what

Go builds and tests ran on MARS in containers through `dh.sh`, with `DH_MEMORY=4g DH_CPUS=4` and unique `aud-`
job names, and I cleaned the volumes afterwards. The MCP packages (`agentmcp`, `agenttools`, `cliecho`, the
agent injection, exfiltration and gate suites, the agent tests of `webhost`) pass. The gate self-test ran in a
build of the pinned gate image. What I could not run: the full mutation self-tests end to end (they need
the full tree and history on a gate host), only their anchor checks and a sample of controls, and anything
that needs a browser.

MARS was shared with three other jobs the whole time. I ran at most two containers at once, each fenced at 4 GB
and 4 CPUs, so I estimated 8 CPUs and 8 GB at worst. The host's load average peaked at about 7.8 (5-minute) while
I was on it and it never ran short of memory. The gate image build and the gate self-test were the heaviest
steps (about five minutes at 4 CPUs).

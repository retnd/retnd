# EPIC T conformance matrix: encryption suite agility for retained copies

This is the list of claims EPIC T makes, each tied to the test that will go red if the claim stops being true, plus the **mutation that has to be run and watched to fail** before the row may say `PASS`. `BLOCKED` is for a claim whose proof has not landed on `main`, citing the epic (#1310, which stays open until the release) with the sub-issue that unblocks it in the last column.

**Every row is `BLOCKED` on the day this file lands, and that is the point.** Nothing of this epic exists in the tree yet. Where a row names a test, it is an existing test or file the proof will extend or stand beside, never one I made up; the guard that reads this file resolves every backticked path a `PASS` row cites, so a row that named a test before it existed would fail the moment it flipped. No row flips until the pull request that lands its proof on `main` has also had its falsification run and watched to fail.

`docs/EPIC-T-encryption-suites.md` is the contract. Its phase exit gates are the checkbox lists this file numbers `T1.n` and `T2.n`, in order, one row per line. Its section 3.6 consensus table is `TC-01` to `TC-06`. Its section 6 guard table is the `TV.n` ledger, one row per guard, in order.

## Reading a row

| Column | Means |
|---|---|
| ID | `T1.n` and `T2.n` for exit-gate lines, `TC-nn` for the review findings, `TV.n` for the planted-violation ledger |
| Claim | What is asserted, in the epic's words |
| Executable proof | What exists today that the proof will extend, as a path or `<package dir>:<TestName>`. The row says in words what the new proof has to be |
| Falsification | The mutation that must turn the proof red. Once run, the row gains the date and what caught it |
| Status | `BLOCKED (#1310)` now. `PASS` only once the falsification has been run and watched to fail, with the date. `PARTIAL` when a half cannot be shown, saying what is missing. `BY HAND` for a check nobody can automate, naming who checked what and when |
| Waits on | The sub-issue, and the pull request once one is open |

## The guards that read this file

`core/tests/compat/matrix_test.go` carries this pair in its table, registered red before the documents existed (#1311). Its five tests hold this file to the spec on every gate run: one row per exit-gate line in each phase, contiguous (`TestTheMatrixHasARowPerGateLine`); one `TV.n` row per guard in the spec's section 6 table (`TestTheViolationLedgerIsHeldToTheSpecsGuardTable`); every path a `PASS` row cites resolves (`TestTheMatrixDoesNotCiteSuitesThatDoNotExist`); a spec box is ticked exactly when its row is `PASS` (`TestTheSpecsExitGateBoxesAgreeWithTheMatrix`); and every `BLOCKED` row cites an issue GitHub says is open (`TestEveryBlockedRowCitesAnIssueThatIsStillOpen`).

---

## Phase 1 exit gate (`T1.n`)

| ID | Claim | Executable proof | Falsification (to run, and watch fail) | Status | Waits on |
|---|---|---|---|---|---|
| T1.1 | `copycrypt.Suites()` lists `age/v1` (supported) and `age-pq/v1` (default); a test pins the table and that exactly one default exists | `core/internal/copycrypt/copycrypt_test.go`, beside which a golden-list test and a one-default test land | Change a status or the default without editing the golden list; mark two defaults | BLOCKED (#1310) | #1312 (T1.2), #1313 (T1.3) |
| T1.2 | Every row in `placement_encryption` written by this build carries the suite id the registry's constant names; planting a config string there turns a row red | `core/internal/state/encryption_test.go` and `core/internal/placement/encryption_test.go`, extended so the recorded `format_id` equals `suite.ID()` | Write `cfg.Suite` text into `RecordPlacementEncryption`; `state` must refuse with `ErrUnknownSuite` | BLOCKED (#1310) | #1312 (T1.2) |
| T1.3 | A copy encrypted under `age-pq/v1` decrypts with the stock `age` CLI pinned in `scripts/ci/pins.env` using the exported bundle's material, and an `age/v1` identity cannot open it | `core/internal/keycustody/bundle_test.go` for the bundle half; the external-reader half needs the `age` pin T1.3 adds to `scripts/ci/pins.env` | Swap the identity for an `age/v1` one; the CLI must refuse. Remove the pin; the vector must skip, not pass | BLOCKED (#1310) | #1313 (T1.3) |
| T1.4 | `encryption_keys.suite_id` exists, every pre-migration key reads `age/v1`, and two active managed keys under one suite are impossible while one per suite is possible | `core/internal/state/encryption_test.go`, extended with a migration test and the per-suite active rule; `core/tests/compat` cell `03-migrated-schema` | Recreate the index over `(provider)` only; the second suite's active key must be refused and the test must say so | BLOCKED (#1310) | #1312 (T1.2) |
| T1.5 | `suite: nonsense` and `suite: <retired>` fail `Load`; `suite: default` and a known id load; a config that writes nothing is byte-identical in behaviour to EPIC N | `core/internal/config/retainedcopyencryption_test.go` and the compat fixtures in `core/tests/compat/testdata/configs` | Make `Validate` accept any non-empty id; fixture `64-invalid-` must load and the test must fail | BLOCKED (#1310) | #1314 (T1.4) |
| T1.6 | A `required` set whose policy suite has no active key refuses the copy before `SOURCE_DELETE_PENDING`, the local copy is untouched, health shows the reason, and nothing is written under another suite | `core/internal/placement/encryption_test.go` and the encrypted cells in `core/tests/movecrash/encrypted_cells_test.go` | Fall back to any active managed key; the copy must be refused anyway and the crash cell must see no medium write | BLOCKED (#1310) | #1314 (T1.4) |
| T1.7 | `retnd encryption suites [--json]`, `GET /api/v1/encryption/suites`, `policy` showing the suite, `key create --suite` and `key rotate --suite` exist with `cliecho` entries, regenerated `openapi.json`, and the route-to-verb guard green | `core/cmd/retnd/routeparity_test.go`, `core/cmd/retnd/cliecho_test.go`, `api/v1/openapi.json` | Drop the `cliecho` entry for the suites route; the guard must name it | BLOCKED (#1310) | #1315 (T1.5) |
| T1.8 | The shared vector suite is red against a planted broken adapter (truncation accepted) and green against both shipped suites | `core/internal/copycrypt/copycrypt_test.go`'s five negatives, which the shared vectors generalise | The planted adapter that returns EOF at a chunk boundary must turn group 2 red | BLOCKED (#1310) | #1313 (T1.3) |

## Phase 2 exit gate (`T2.n`)

| ID | Claim | Executable proof | Falsification (to run, and watch fail) | Status | Waits on |
|---|---|---|---|---|---|
| T2.1 | Marking a suite deprecated in the table turns health degraded with the count of active copies under it; marking it retired makes `Load` refuse the pinned id and the engine refuse a write, while decrypt-verify, recover and `decrypt` still work on its copies | `core/internal/keycustody/health_test.go` and `core/internal/placement/encryption_test.go` | Let the engine write under a retired suite; refuse to open a retired suite's copy; each must go red | BLOCKED (#1310) | #1316 (T2.1) |
| T2.2 | `retnd encryption reencrypt --suite <id> [--dry-run]` lists and re-encrypts copies under another suite through the #1291 path; the source is retired only after decrypt-verify; a kill at each phase boundary reconciles | `core/tests/movecrash/retro_cells_test.go`, which the re-encrypt cells extend | Retire the source before the new copy decrypt-verifies; the kill cell must catch it | BLOCKED (#1310) | #1317 (T2.2) |
| T2.3 | The Settings card shows the suite, its `Describe()` sentence and status, and changing it leads through rotate, export and confirm; per-copy chips show the suite id; every new text pair meets WCAG AA in both themes with ratios recorded | The Playwright specs under `ui/` that T2.3 adds, run through the workspace lock | Let the select save without the rotate step; the spec must fail. Drop a text pair below 4.5:1; the contrast check must fail | BLOCKED (#1310) | #1318 (T2.3) |
| T2.4 | retnd-tests suites cover two suites end to end, the crypto negatives per suite, the cross-suite key refusal, rotation across suites, catalog loss and rebuild, and a clean-machine recovery per suite, and the pin moves in lockstep | `core/tests/encryptedretention/retention_test.go`, which the two-suite cases extend | Remove one suite from the table the suites iterate; the coverage check must name it | BLOCKED (#1310) | #1319 (T2.4) |
| T2.5 | `docs/encrypted-copies.md`, `recovery-without-a-terminal.md`, ADR 0027's one-line note, the CHANGELOG one-way door, captures and the `gate_step` are in; the matrix has no `BLOCKED` row and the canonical deployment is byte-identical | `docs/encrypted-copies.md`, `scripts/ci-local.sh` | Leave a `BLOCKED` row; the release check must refuse | BLOCKED (#1310) | #1320 (T2.5) |

## The review consensus (`TC-nn`)

| ID | Finding | Proof | Falsification | Status | Waits on |
|---|---|---|---|---|---|
| TC-01 | `Describe()` is the only text about a suite, states the level, and carries no marketing word | `core/internal/copycrypt/copycrypt_test.go`, beside which the sentence pin lands | Add "military-grade" to a sentence | BLOCKED (#1310) | #1313 (T1.3) |
| TC-02 | `copycrypt` stays a leaf and every `Suite` is a `keycustody.Format` | `core/internal/keycustody/format.go`, where the compile-time assertion lands | Remove a `Format` method from `Suite`; the build must fail | BLOCKED (#1310) | #1312 (T1.2) |
| TC-03 | The one-active rule holds per suite through migration `0020`, or through the side table, as the pull request says | `core/internal/state/encryption_test.go` | Drop the recreated index | BLOCKED (#1310) | #1312 (T1.2) |
| TC-04 | No auto-rotation, the refusal names the rotate command, the patch answers `rotation_needed` | `core/internal/placement/encryption_test.go`, `core/cmd/retnd/encryption_993_test.go` | Mint a key on a default change | BLOCKED (#1310) | #1314 (T1.4), #1315 (T1.5) |
| TC-05 | One guard per downgrade path | The `TV.n` ledger below | Remove any one guard; its row must go red | BLOCKED (#1310) | #1312 to #1317 |
| TC-06 | One vector suite over every registered suite, with its anti-vacuity control | `core/internal/copycrypt/copycrypt_test.go` | The planted truncation adapter | BLOCKED (#1310) | #1313 (T1.3) |

## The planted-violation ledger (`TV.n`)

| ID | Guard | Executable proof | Falsification | Status | Waits on |
|---|---|---|---|---|---|
| TV.1 | The suite table is pinned against a golden list | `core/internal/copycrypt/copycrypt_test.go`, beside which `TestSuiteIDsAreStableAgainstTheGoldenList` lands | Change a status without editing the golden list | BLOCKED (#1310) | #1312 (T1.2) |
| TV.2 | Exactly one default, and it is supported | As TV.1, `TestTheRegistryHasExactlyOneDefaultAndItIsSupported` | Mark a second default, or deprecate the default | BLOCKED (#1310) | #1312 (T1.2) |
| TV.3 | The recorded suite comes from the registry, never config text | `core/internal/state/encryption_test.go` | Write `cfg.Suite` text into `RecordPlacementEncryption` | BLOCKED (#1310) | #1312 (T1.2) |
| TV.4 | An unknown suite on a record is never plaintext and never `content` | `core/internal/placement/encryption_test.go`, `core/internal/app/catalog_encrypted_test.go` | Treat an unknown `format_id` as `age/v1` | BLOCKED (#1310) | #1312 (T1.2) |
| TV.5 | One active managed key per suite | `core/internal/state/encryption_test.go` | Recreate the index over `(provider)` only | BLOCKED (#1310) | #1312 (T1.2) |
| TV.6 | A key names a registered suite | `core/internal/keycustody/custody_test.go` | Let `AddKey` accept an unregistered suite | BLOCKED (#1310) | #1312 (T1.2) |
| TV.7 | A key of another suite is refused by `Open` and `Recipient` | `core/internal/copycrypt/copycrypt_test.go` | Make one adapter accept the other's identity | BLOCKED (#1310) | #1313 (T1.3) |
| TV.8 | The shared vectors catch a truncation-accepting adapter | `core/internal/copycrypt/copycrypt_test.go` | The planted adapter returning EOF at a chunk boundary | BLOCKED (#1310) | #1313 (T1.3) |
| TV.9 | `Describe()` carries no marketing words | `core/internal/copycrypt/copycrypt_test.go` | Add "256-bit" to a sentence | BLOCKED (#1310) | #1313 (T1.3) |
| TV.10 | An unknown or retired policy suite fails `Load` | `core/internal/config/retainedcopyencryption_test.go` | Accept any non-empty id | BLOCKED (#1310) | #1314 (T1.4) |
| TV.11 | No active key for the policy suite refuses before any destructive step | `core/internal/placement/encryption_test.go`, `core/tests/movecrash/encrypted_cells_test.go` | Fall back to any active managed key | BLOCKED (#1310) | #1314 (T1.4) |
| TV.12 | The default-changed event fires once per change | `core/internal/app/keyalerts.go`'s package tests | Emit it every cycle | BLOCKED (#1310) | #1314 (T1.4) |
| TV.13 | Suite routes and verbs agree | `core/cmd/retnd/routeparity_test.go`, `core/cmd/retnd/cliecho_test.go` | Drop the suites route's `cliecho` entry | BLOCKED (#1310) | #1315 (T1.5) |
| TV.14 | Key import refuses an unknown suite | `core/internal/keycustody/bundle_import_1289_test.go` | Accept a bundle whose `format` names no registered suite | BLOCKED (#1310) | #1315 (T1.5) |
| TV.15 | `decrypt` takes the suite from the record and refuses to guess | `core/cmd/retnd/encryptiondecrypt_1289_test.go` | Default to `age/v1` with no sidecar and no record | BLOCKED (#1310) | #1315 (T1.5) |
| TV.16 | No key material on any suite surface | `core/internal/keycustody/surfaces_test.go`, `core/tests/encryptedretention/canary_test.go` | Plant a canary identity in the suites response | BLOCKED (#1310) | #1315 (T1.5) |
| TV.17 | The `suite` metric label is an enumerated id | `core/internal/metrics/keycustody_993_test.go` | Label the copy-count family by key id | BLOCKED (#1310) | #1315 (T1.5) |
| TV.18 | A retired suite never writes and still reads | `core/internal/placement/encryption_test.go` | Write under a retired suite, or refuse to open its copies | BLOCKED (#1310) | #1316 (T2.1) |
| TV.19 | Re-encryption retires a source only after decrypt-verify | `core/tests/movecrash/retro_cells_test.go` | Retire the source first | BLOCKED (#1310) | #1317 (T2.2) |

## The limits of this matrix

It does not prove anything about a quantum adversary, and it cannot: the `mlkem768x25519` stanza follows an Internet-Draft and the age spec pins it. It proves the copy is written under the suite the policy names, opens only under that suite's key, and opens with the stock reader. A row that says more than that is wrong.

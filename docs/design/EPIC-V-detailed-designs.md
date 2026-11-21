# EPIC V: detailed designs for V1.1 to V2.2

**Spec:** `docs/EPIC-V-encryption-residue.md` and `docs/conformance/epic-v-matrix.md` (#1511, draft PR #1519).
**Tracker:** #1512. **Sub-issues:** V1.1 #1513, V1.2 #1514, V1.3 #1515, V2.1 #1516, V2.2 #1517.
**Read on:** 2026-11-19, against the spec branch `docs/epic-v-encryption-residue` at `bc5f0ed5` (code as on `origin/main` at the spec's base). Every `path:line` below is from that tree. Main has moved since: the code those lines cite is unchanged except in `core/internal/placement/retroencrypt.go` and `engine.go`, which #1545 (PR #1634) grew for `--from-local`. The two citations about the medium-to-medium rule and the two-ACTIVE-placements refusal are updated to the new lines; the other `retroencrypt.go` lines cite the same code a few lines to fifty lines further down (`EncryptExisting` is at `:321`, the `WithKey` at the source open at `:531`).
**Decisions:** D-1 to D-6 below were answered on 2026-11-19, and the spec in #1519 was rewritten to them and to the list of spec changes.

This is one design per sub-issue, written so that whoever picks an issue up can build it without coming back to ask. Before designing anything I put each sub-issue through seven review lenses, with the rule to reject first. The verdicts and the dissents that survived are under each section. Where the code showed the spec to be wrong, I say so and say what has to change.

---

## Summary: what changes, and what the owner has to decide

### Verdict per sub-issue

| Sub-issue | Verdict | In one line |
|---|---|---|
| V1.1 #1513 | **Keep, re-scoped** | Most of the guards it promises already exist with mutation controls. The real work is the one nobody wrote down: a `command` key source runs about ten times per re-encrypted copy and on every page load of the keys card, and its stderr reaches alert emails. |
| V1.2 #1514 | **Keep, re-scoped (bigger)** | Wiring `Store.RotateExternal` is the wrong fix. In the common topology (one external reference, every set inheriting it) there is nowhere to keep the old reference declared, so the old copies stop opening the moment the reference moves. The fix is a decrypt-only reference list plus a `supersede` verb, and that needs one config field (D-1, accepted). |
| V1.3 #1515 | **Shrink** | The per-key count already exists (`copies_recording` on `GET /encryption/keys`). Drop the health field and the metric. Keep the CLI print, the retire hint, the dry-run byte totals and the "nothing starts a re-encryption" guard. |
| V2.1 #1516 | **Shrink and fix a bug** | The backlog is already on the key list. The keys card draws a superseded key as "Retired" next to a Retire button. The work is that fix, the supersede, keep and forget controls, re-encrypt by key, and the captures (moved here from V2.2). No per-copy chip. |
| V2.2 #1517 | **Keep, slightly smaller** | The e2e, documentation, gate and closing check stay. Captures move to V2.1. The managed-key e2e case already passes on `main` except for the two new assertions, and the spec should say so. |

The skeptic wanted V1.3 folded into V1.2. The others kept them apart because the two touch different code and can be built in parallel; that dissent is recorded under V1.3.

### What must change in the spec

All eleven are applied in the spec's 2026-11-19 revision (#1519), together with D-1 to D-6.

1. **FR-106 is the wrong model.** "Supersedes the old key's row and registers the new one in one durable write" assumes one active external slot. There isn't one: migration 0015 only constrains managed keys (`core/migrations/0015_retained_copy_encryption.sql:94-100`), every external row starts and stays `active` (`core/internal/keycustody/externalregistry.go:97`), and nothing ever moves one out. Rewrite FR-106 as: *an external key can be superseded once no key provider yields it; nothing new is written under a superseded external key; its copies keep opening through a reference declared for reading; it can be retired once no ACTIVE copy records it.*
2. **The "no config key" invariant cannot hold if FR-106 is to work for the ordinary deployment.** See V1.2's problem statement. The spec already reserved compat fixtures `15` and `70` for exactly this case.
3. **VC-04 and V-04 contradict the code.** `secretref` surfaces a failing command's stderr on purpose (`core/internal/secretref/secretref.go:106-111`, `:627`). The rule that can hold is narrower: stdout never surfaces (true today); stderr surfaces only on the interactive `provider test` answer, never in an alert, a health reason, a feed row, a log line or a metric, and any token in it that parses as a key is withheld everywhere.
4. **V-02's guard points at the wrong field set.** A KMS provider would arrive as a third field on `config.RetainedCopyKeyProvider` or a third provider value, not as a fourth field on `secretref.Ref`. The real guards already exist: `TestRefFieldSetMatchesTheConfiguredOnes` (Ref shape), the state vocabulary test (`core/internal/state/encryption_test.go:266`, two providers) and the migration CHECK. V-02 should cite those and add one shape test for `RetainedCopyKeyProvider`.
5. **V-05's fixture is described backwards.** A stub that fails on its second call does not "hide" a cache; it proves the cache exists, because only an uncached path calls it twice.
6. **V-11's crash cell is meaningless.** "A kill between the two leaves exactly one active key" has no subject for external keys (several may be active by design). Replace it with the boundaries that do exist: the config write, the supersede write, and the engine's refusal to write under a superseded external key.
7. **FR-107 shrinks.** The per-key count is on the API already (`core/apicontract/contract.gen.go:3213`, filled at `core/service/encryption.go:465-469`). Drop "the health report and as a metric"; keep the CLI, the retire hint and the dry-run bytes. A backlog is normal after every routine rotation, so a metric nobody can alert on and a health field that cannot degrade are features without a reader.
8. **"No new event" becomes one new event.** `key_rotated` says "a new active key superseding the previous one" (`core/internal/obs/events.go:382-384`); a supersede has no new key. Add `key_superseded`.
9. **V2.1's "backlog distinct from encrypted and verified recoverable" was about chips.** The backlog belongs on the key row, not on every copy: a per-copy "old key" chip would sit on every copy written before any routine rotation, for ever.
10. **"Each e2e case fails against main first"** is false for the managed-key case: rotate, `reencrypt --key` and retire all work on `main`. It fails only on the two new assertions (the retire hint and the dry-run bytes), and the spec should say that is the red.
11. **Small citation fixes.** `RetireKey` refuses the active key at `core/internal/placement/keyretire.go:111` (`:59` is the sentinel). The "seven guards" are mostly existing controls in `scripts/bdtools/encryption/controls_n_a.py` and `controls_n_b.py`, and the matrix should cite them by label.

### The decisions, answered 2026-11-19

- **D-1. One config field, `retained_copy_encryption.previous_external_keys`: yes, as designed in V1.2.** Opt-in: only `--keep-previous` writes it, `omitempty` keeps every other file byte for byte, and the way back is deleting the key by hand, which the CHANGELOG says. Compat fixtures `15` and `70`. The per-run fallback below stays written down as the rejected alternative. Keeping the reference in the journal or in a state-directory file was also rejected: a `command` reference is argv the daemon executes, and it belongs in the file an operator reviews.
- **D-2. Delete `keycustody.Store.RotateExternal`: yes.** It has no caller and encodes the one-active-slot model that is wrong.
- **D-3. One new event, `key_superseded`: yes.** One constant, one `Logger` method, one feed sentence, a `Result` on success and refusal.
- **D-4. Re-encrypting artifacts that still have a local plaintext copy: a follow-up issue, #1545, outside V. It has since landed as `retnd encryption reencrypt --from-local` (PR #1634).** The move engine refuses an artifact with two ACTIVE placements (`core/internal/placement/engine.go:528-531`), so recent copies under a compromised key stay under it until their local copy expires. The fix (upload a fresh copy from the local plaintext, no egress) is a new run mode with its own crash cells, and both of V's phases are full. The issue says the upload reads the local plaintext copy the engine already holds and adds no new place plaintext lands.
- **D-5. FR-107's shrink: yes.** No health field, no metric. The CLI print, the retire hint and the dry-run bytes stay.
- **D-6. `not_checked` in the key list's `resolvability`, plus `checked_at`: yes**, so the read surface stops running commands. `checked_at` is a timestamp, and the `reason` for `not_checked` is a fixed sentence, never anything a resolver printed.

---

## How I ran the review

Seven lenses, told to reject any item whose benefit could not be read off the code:

1. **The crypto lens:** formats, key lifecycle, what a status means.
2. **The custody lens:** where key material and secret-adjacent text can travel.
3. **The data-path lens:** streaming, crash safety and resume at every durable boundary.
4. **The egress lens:** what a step costs in reads, writes, calls and hours.
5. **The recovery lens:** disaster recovery as a procedure a stressed person follows.
6. **The parity lens:** API, CLI and web parity, and security UX.
7. **The skeptic:** conformance and tests, and whether each thing should exist at all.

Each gave an initial verdict per sub-issue. Then I took a consensus position and recorded what still disagreed. Nobody accepted a claim from the spec without the line that backs it.

## Findings in the code that drive every design below

These are cited from the sections that use them.

- **F-1. External keys never leave `active`.** `RegisterExternal` writes `Status: StatusActive` (`externalregistry.go:97`) and returns any existing row whatever its status (`:92-93`). The only path to `superseded` is `RotateExternal` (`:113-133`), which has no caller (`git grep RotateExternal` over non-test files finds only its definition). `RetireKey` refuses an active key (`keyretire.go:111`). So an external key, once used, can never be retired, and its retire refusal tells the operator to "rotate to a new key", which does nothing for an external key (`keyretire.go:59`).
- **F-2. There is nowhere to keep an old external reference declared.** The declared references are the deployment's provider plus each set's override (`core/service/keycustody.go:72-93`), and a set's override is only allowed under `required` (`core/internal/config/retainedcopyencryption.go`, `RetainedCopyEncryptionOverride.KeyProvider`). With one global reference and sets inheriting it, replacing the reference drops the old one, and every copy under the old key stops opening (`withExternalRecord`, `externalregistry.go:138-176`). The docs' advice at `docs/encrypted-copies.md:295` ("keep the old reference declared under a different name or path") is only achievable by giving every set its own override.
- **F-3. The engine would write new copies under a superseded or retired external key.** `externalKey` takes whatever row `RegisterExternal` returns (`core/internal/placement/encryption.go:182-195`) and nothing checks its status, and the journal does not check the key's status when it records a copy (`core/internal/state/encryption.go:480-486` checks only that the row exists).
- **F-4. A `command` key source runs many times per copy.** Every `WithKey` on an external id walks the declared references and runs each one until one matches (`externalregistry.go:144-171`). A re-encrypted copy resolves keys at `CheckEncryptExisting` (`retroencrypt.go:182`, `:204`, `:264`), at the copy (`encryption.go:240` via `activeKey`, which is `RegisterExternal` plus a probe at `:187` and `:191`), at the source open (`retroencrypt.go:483`), at the first verification (`encryption.go:386` and `:477`) and at the pre-delete re-read (`core/internal/placement/engine.go:1093-1099`). By my reading that is about ten resolutions per copy, each up to the number of declared references. The dry run alone (`core/service/reencrypt.go:189`) resolves three times per copy.
- **F-5. The keys card runs commands on every load.** `keyStatus` calls `store.Probe` for every key (`core/service/encryption.go:460`), and for an external key that resolves every declared reference (`store.go:433` into `externalregistry.go:138`). `core/service/keycustody.go:234-236` says such resolution is "for the alert tick only, never for a request".
- **F-6. A command's stderr reaches durable and outbound surfaces.** `secretref` puts stderr into the error (`secretref.go:627`). `withExternalRecord` copies `err.Error()` into its refusal (`externalregistry.go:169`), which becomes `ExternalKeyAtRisk.Reason` (`core/service/keycustody.go:293-300`), the health report, and the alert's detail through `UnresolvableCondition` (`core/internal/keycustody/conditions.go:56-64`), which the notifier sends.
- **F-7. The per-key count exists.** `EncryptionKeyStatus.CopiesRecording` (`contract.gen.go:3213`), computed per key (`core/service/encryption.go:465-469`) and drawn in the keys card's Copies column with a link to the filtered Backups list (`ui/shared/src/encryption/EncryptionCard.tsx:331-337`). The CLI's `key list` does not print it (`core/cmd/retnd/encryption.go:626-635`).
- **F-8. The keys card draws `superseded` as "Retired".** `KEY_STATUS_WORDS` maps both to "Retired" (`EncryptionCard.tsx:81`), and the Retire button is offered on exactly those rows (`:339`). The rotate dialog repeats it (`:447`). Design note 994 chose the word (`docs/design/994-retained-copy-encryption.html:205`) before retirement existed as a separate state, and it is now a contradiction on screen.
- **F-9. The key status has no provider.** `KeyRecord.Provider` is `json:"-"` (`core/internal/keycustody/registry.go:55`) and `EncryptionKeyStatus` has no field for it (`contract.gen.go:3209-3222`), so the browser cannot tell an external key from a managed one. The table is labelled "Managed keys" (`EncryptionCard.tsx:315`) though it lists both.
- **F-10. Re-encryption reaches only single-copy artifacts with a second medium.** Medium to medium only (`retroencrypt.go:207-210`), and an artifact with two ACTIVE placements is refused (`engine.go:528-531`). A deployment with one off-site medium cannot re-encrypt at all; recent artifacts with a local copy are refused until the local copy expires, unless the run says `--from-local` (#1545, landed after this was written).
- **F-11. Most of V1.1's guards exist.** `scripts/bdtools/encryption/controls_n_a.py` already plants "a fourth source field on the external key reference", "the automatic revalidation ceiling raised to content" and "the object key of a copy gains an encrypted suffix", and `controls_n_b.py` plants "NV an unresolvable key downgraded to an existence pass". The journal already refuses a stored size no larger than the plaintext (`core/internal/state/encryption.go:475`), which stops a compressing path on compressible data.

---

## V1.1 #1513: ADR 0036, the seven dispositions, and the `command` key source made honest

### Verdicts

| Lens | Initial verdict | Consensus position |
|---|---|---|
| Crypto | **Reject.** A "worked example" that is a stub proves retnd's error handling, not a KMS integration; calling it a provider example oversells it. | Accept if the docs call it what it is: a stub command shaped like a resolver, and a sample script nobody here ran against a real service. |
| Custody | **Reject.** The spec promises stderr reaches nothing; the code sends it to alert emails (F-6). And a per-run cache keeps material in memory longer. | Accept with the stderr boundary below and a cache that is per operation, `command` only, zeroed at close, never global. Dissent survives on the cache (below). |
| Data path | **Reject.** A cache that outlives a config reload or a run is a correctness bug, and a negative result retried per copy turns a 15 second timeout into hours. | Accept: the session is keyed by the whole reference, dies with the operation, and caches failures too. |
| Egress | **Reject** the spec's framing that the cost is a KMS bill (KMS calls are cheap). | Accept on the real costs: wall time (a 1 to 3 second resolver times ten per copy), human-presence resolvers, KMS audit-log noise, and the alert tick running every reference each poll interval. |
| Recovery | **Reject.** None of this helps a person on a bad night. | Accept the docs part: the worked example becomes the page an operator copies, with the failure modes written next to it. |
| Parity | **Reject.** Withholding stderr from alerts leaves an operator with less to go on. | Accept because the full stderr stays on `provider test`, which the alert text names. |
| Skeptic | **Reject.** Five of the nine rows certify guards that already have controls; writing them again is padding. | Accept a smaller V1.1: cite the existing controls, add the two guards that are missing (kopia, compression equation), and spend the effort on F-4, F-5 and F-6. |

**Consensus: keep, re-scoped.** **Surviving dissent:** the custody lens would rather cache only "this reference yields recipient R" and not the material, accepting two command runs per copy instead of zero. The others held that two runs per copy of a thousand-copy pass is the failure the spec named, and that the material already sits in the same process for every copy's decrypt.

### The problem in the code today

1. The dispositions live in prose only (spec section 3); nothing a later reader finds in `docs/adr/` says why the seven items are not built.
2. F-4: a `command` source runs about ten times per re-encrypted copy, at least five per fresh copy in a cycle, three per copy in a dry run, and once per declared reference per required set on every alert tick (`core/internal/keycustody/conditions.go:56` into `store.go:440-468`, which resolves twice: `ProbeExternal` at `external.go:76` and again at `store.go:454`).
3. F-5: `GET /encryption/keys` and `retnd encryption key list` run commands.
4. F-6: stderr reaches health, alerts and logs. A resolver script with `set -x` would print the key itself to stderr, and that line would go out by email.
5. `docs/encrypted-copies.md:301` and `:534` describe a capability with no example and no test behind it.

### Design

#### ADR 0036

`docs/adr/0036-the-encryption-cut-list-and-external-key-lifecycle.md`. Sections:

- **Context:** EPIC N's section 8 (`docs/EPIC-N-encrypted-retained-copies.md:332-342`) and what changed since.
- **The seven dispositions**, one subsection each, with the reason and the control label that holds it:

| # | Item | Disposition | Held by |
|---|---|---|---|
| 1 | KMS or Vault provider | Rejected as a provider; the `command` source is the supported route, with the worked example | `TestRefFieldSetMatchesTheConfiguredOnes`; the state vocabulary test at `core/internal/state/encryption_test.go:266`; new `TestTheKeyProviderHasExactlyTwoKinds_1513` |
| 2 | Re-encryption on rotation | Already built, explicit only (EPIC T); automatic rejected | `TestNothingStartsAReencryptionOnItsOwn_1515` (V1.3) |
| 3 | Second cipher for kopia | Rejected | `TestSetPolicyOfARepositoryEncryptedSetSaysSatisfiedByTheRepository_1296` (`core/service/encryption_1296_test.go:25`) plus a new control |
| 4 | Object-name confidentiality | Rejected | existing control "the object key of a copy gains an encrypted suffix" |
| 5 | Scheduled decrypt-verify | Rejected | existing control "the automatic revalidation ceiling raised to content" |
| 6 | Key-usage audit surface | Rejected | none (nothing is built that could fail) |
| 7 | Compression | Rejected | the journal's size refusal plus new `TestStoredSizeIsPlaintextPlusTheSuiteOverhead_1513` |

- **D1, one custody model.** Two provider kinds, three reference sources, no interface.
- **D2, the resolution session** (below).
- **D3, where a command's stderr may appear** (below).
- **D4, the external key lifecycle:** active, superseded, retired; decrypt-only references. V1.2 implements it, and the ADR records it here so the lifecycle and the dispositions are one decision a reader finds in one place.
- **Re-proposing a rejected item** needs a new epic naming an operator who is blocked without it.

#### The resolution session (D2)

**Data model:** none in the journal. In memory only.

```go
// keycustody
type session struct {
    mu      sync.Mutex
    entries map[string]*sessionEntry // refKey(ref) -> entry
    closed  bool
}
type sessionEntry struct {
    once     sync.Once
    material []byte // a copy, zeroed at close; nil on failure
    err      error  // the brief error, cached for the rest of the run
}

// WithResolutionSession returns a context carrying a fresh session and the
// function that ends it. The caller defers end().
func WithResolutionSession(ctx context.Context) (context.Context, func())
```

**Behaviour.** `WithExternalKey(ctx, ref, fn)` looks for a session in `ctx`. If there is none, or `ref.Command` is empty, it behaves exactly as today (`external.go:91-99`): file and env sources are cheap and keep the property that material lives only inside the callback. For a `command` reference inside a session:

1. The entry is `refKey(ref)` (`store.go:97-106`), the hash over file, env and the command with every argument, so two references running one executable with different arguments are two entries, and a reload that changes a reference is a new entry.
2. The first caller runs `secretref.Resolve` under `once`; concurrent callers wait for it (no second run).
3. On success the entry keeps a copy of the material. Every caller gets a fresh copy, handed to `fn` and zeroed after it, so `fn`'s own zeroing never touches the cache.
4. On failure the entry keeps the brief error (D3) and every later call in the session returns it, wrapped with "(as resolved earlier in this run)". A resolver that timed out once is not run again for the next 999 copies.
5. `end()` zeroes every cached copy and marks the session closed; a closed session falls through to uncached behaviour, so a goroutine that still holds the context after the operation ended gets no stale material.

**Where sessions are opened** (each with `defer end()` in the function that owns the operation):

| Operation | Function |
|---|---|
| A cycle's moves | `placement.Engine.RunCycle` (`core/internal/placement/engine.go:378`) |
| Encrypt-existing and re-encrypt runs | `placement.Engine.EncryptExisting` (`retroencrypt.go:284`) |
| The re-encrypt dry run | `service.BackupService.PlanReencrypt` (`reencrypt.go:174`) |
| A scrub pass and a decrypt-verify | `app.Service.Scrub` (`core/internal/app/scrub.go:170`), which both the scheduled tick and the explicit verify go through |
| One alert tick | `app.Service.AlertTick` (`core/internal/app/alerts.go:117`) |
| Supersede (V1.2) | `service.BackupService.SupersedeEncryptionKey` |

Nothing opens a session for longer than one of these, and there is no session on `Store` or in a package variable. The data-path lens asked for a test that a session cannot be stored: `TestNoResolutionSessionOutlivesItsOperation_1513` greps non-test files for `WithResolutionSession(` and requires each call to be followed by a `defer` of its end function in the same function.

**Count after the change.** Per operation, each distinct `command` reference runs at most once. The ten resolutions per re-encrypted copy remain as calls, but nine of them are memory lookups.

**Crash and resume.** Nothing durable. A crash loses the cache, which is the point; the next run resolves again.

**Egress and cost.** A thousand-copy re-encryption under one external key goes from about ten thousand resolver runs to one. An alert tick goes from two runs per required set plus one per external key per reference to one per distinct reference.

#### The read surface stops resolving (F-5)

`keyStatus` (`core/service/encryption.go:443-484`) keeps `store.Probe` for managed keys (a local file read) and, for an external key, reads the last alert-tick answer instead:

- `KeyCustody` keeps, beside `probed` (`core/service/keycustody.go:57-60`), the full map `UnreadableExternalKeys` returned (not only the keys with copies) and when it ran.
- In that map: `resolvability: "unreachable"` with its reason.
- Probed and not in it: `resolvability: "resolves"`, and a new `checked_at` field.
- Never probed in this process: `resolvability: "not_checked"`, no `checked_at`, and the fixed `reason: "not checked yet; the next alert pass checks it, or run retnd encryption provider test"`. The reason is never anything a resolver printed. This is D-6. In `api/v1/openapi.json`, `resolvability` is a documented string ("resolves, unreachable or not_declared"), not an enum, so the change is one more value in that description, both bindings, and every consumer that switches on the string (the keys card, V2.1).

#### Where a command's stderr may appear (D3)

`secretref` changes shape, not text, for everyone else:

```go
// secretref
type CommandError struct {
    Executable string
    Cause      error  // exit status, signal, or the timeout
    Stderr     string // bounded at maxCapturedStderr
}
func (e *CommandError) Error() string // exactly today's text (secretref.go:625-627)
func Brief(err error) error          // same chain, Error() without stderr
```

`fromCommand` returns a `*CommandError` where it returns the formatted error today, so the passphrase and medium-credential paths print what they print now. `Brief` wraps so that `errors.Is` still sees every sentinel, and its `Error()` reads `secretref: command <exe>: exit status 1 (its stderr is shown by retnd encryption provider test)`.

In `keycustody`, every error that leaves `WithExternalKey` goes through `Brief`, so `withExternalRecord`'s refusal, `UnreadableExternalKeys`, the alert detail, the health reason, the engine's refusal text, the logs and the feed carry no stderr. The one place that shows it is `TestEncryptionProvider` (`core/service/encryption.go:687`), through a new `keycustody.DiagnoseExternal(ctx, ref) Probe` that keeps the full `CommandError`.

Even there, any whitespace-delimited token in stderr that some registered suite accepts as a secret key (`suite.Recipient(token)` succeeds, for each suite in `copycrypt.Suites()`) is replaced by `[a key was here and is withheld]`. That catches a resolver that echoes its own output, which is the likeliest real leak, without a prefix list to maintain.

#### The worked example

A new section in `docs/encrypted-copies.md`, "An external key from a secrets manager, worked through", replacing the sentence at `:301` and the bullet at `:534`. It has:

- A sample `/usr/local/libexec/retnd-age-key` script for Vault (`vault kv get -field=identity secret/retnd/age`) and one for AWS KMS (`aws kms decrypt ... | base64 -d`), each with `set -eu`, no `set -x`, nothing on stderr but diagnostics, and a note that the command runs with `PATH=/usr/local/bin:/usr/bin:/bin` and no other environment (`secretref.go:596`), so credentials come from files the script reads.
- The config block that names it as `key_provider.external.command`.
- What happens on each failure: exit non-zero or no output is "key does not resolve" and the copy is refused before anything is written; a 15 second timeout is the same; the run resolves it once per operation; stderr shows only on `provider test`.
- The honest line: these scripts were not run against a real Vault or KMS here; the test runs a stub shaped like them.

The stub lives in `core/internal/keycustody/testdata/resolver.sh`: it takes a mode and a counter path as arguments, appends one line to the counter per run, and prints a fixture age identity (from `testdata/`), or exits 1 with a canary on stderr, or sleeps, or succeeds once and fails after.

### Alternatives considered

- **A provider interface.** Rejected for the reasons ADR 0036 item 1 records.
- **Cache material for the daemon's lifetime.** Rejected: a key that survives a config reload or the operator's revocation in Vault is a correctness and custody bug.
- **Cache only reference to recipient (the custody lens's option).** Cuts ten runs to two per copy. Rejected by the majority as still per copy; kept as the recorded dissent.
- **Leave stderr as is and document it.** Rejected: alert emails and webhooks leave the machine, and a resolver that prints the key is one `set -x` away.
- **Strip stderr everywhere, including `provider test`.** Rejected by the parity lens: an operator debugging a resolver needs it, and `provider test` is the explicit, interactive place to ask.

### Red-first test plan

New tests, written and committed red before the change (`CI_LOCAL_RED` on a test-only commit):

| Test | Package | Red today because | Row |
|---|---|---|---|
| `TestACommandKeySourceRunsOncePerOperation_1513` (re-encrypt dry run and run over five copies, counting stub) | `core/service` | about ten runs per copy (F-4) | V-05 |
| `TestASecondFailureIsNeverReachedInsideOneRun_1513` (stub succeeds once, fails after) | `core/internal/placement` | the second copy runs the stub again and fails | V-05 |
| `TestAFailedResolutionIsNotRetriedPerCopy_1513` (stub exits 1; five copies; one run) | `core/internal/placement` | five runs | V-05 |
| `TestTheKeyListRunsNoCommand_1513` | `core/service` | `keyStatus` probes (F-5) | V-05 |
| `TestACommandsStderrReachesNoDurableSurface_1513` (stderr canary; asserts absence in the engine refusal, alert detail, health reason, feed rows, captured log output, key list reason) | `core/internal/app` | F-6 | V-04 |
| `TestProviderTestShowsStderrAndWithholdsAKey_1513` (positive control: canary present; a fixture identity on stderr is withheld) | `core/service` | no redaction | V-04 |
| `TestATimeoutIsUnresolvableNeverWeaker_1513` (`commandTimeout` shortened through `export_test.go`) | `core/internal/secretref` | passes today: guard, proven by planting | V-03 |
| `TestTheCommandKeySourceWorkedExample_1513` (valid key resolves, copy written and decrypt-verified; exit 1 refuses before staging) | `core/internal/placement` | passes today: guard | V-03 |
| `TestTheKeyProviderHasExactlyTwoKinds_1513` (field set of `config.RetainedCopyKeyProvider` is `{Managed bool; External *SecretSource}`) | `core/internal/config` | passes: guard | V-02 |
| `TestStoredSizeIsPlaintextPlusTheSuiteOverhead_1513` (every suite in `copycrypt.Suites()`, sizes 0, 1, 65535, 65536, 65537 and 3 MiB of zeros; stored = header end + 16 + P + 16 times max(1, ceil(P/65536)), header end measured from the object as `copycrypt_test.go:98-109` does) | `core/internal/copycrypt` | passes: guard | V-08 |
| `TestNoResolutionSessionOutlivesItsOperation_1513` | `core/internal/keycustody` | no sessions yet (red on missing call sites) | V-05 |

Planted violations, added as controls in a new `scripts/bdtools/encryption/controls_v.py` so the matrix can cite them by label:

| Label | Plant | Must turn red |
|---|---|---|
| `V the resolution session never caches` | `WithExternalKey` ignores the session | the three V-05 tests |
| `V a failed resolution retried per copy` | do not cache `err` | `TestAFailedResolutionIsNotRetriedPerCopy_1513` |
| `V the key list probes an external key again` | restore `store.Probe` for external rows | `TestTheKeyListRunsNoCommand_1513` |
| `V a command's stderr in the brief error` | `Brief` returns the full text | `TestACommandsStderrReachesNoDurableSurface_1513` |
| `V a key on stderr shown by provider test` | drop the redaction | `TestProviderTestShowsStderrAndWithholdsAKey_1513` |
| `V a timeout read as a resolved key` | treat `ctx.Err()` as success in `fromCommand` | `TestATimeoutIsUnresolvableNeverWeaker_1513` |
| `V a third key provider kind` | add `KMS *SecretSource` to `RetainedCopyKeyProvider` | `TestTheKeyProviderHasExactlyTwoKinds_1513` |
| `V a kopia set reaches the re-encrypt selector` | delete the `model.EngineKopia` skip at `reencrypt.go:115` and the `SatisfiedBy` line at `core/service/encryption.go:139` | the 1296 kopia tests and `TestReencryptByKeySelectsOnlyThatKeysCopies_1317` with a kopia set added to its fixture |
| `V the copy path compresses` | wrap the plaintext reader in `gzip.NewWriter` before `EncryptToStaging` | `TestStoredSizeIsPlaintextPlusTheSuiteOverhead_1513` |

Existing controls the matrix cites unchanged: "a fourth source field on the external key reference" (V-02), "the object key of a copy gains an encrypted suffix" and "NV the encrypted copy uploaded under a forked object key" (V-06), "the automatic revalidation ceiling raised to content" (V-09), "NV an unresolvable key downgraded to an existence pass" (V-03).

**Negative controls:** a stub that returns a valid key resolves and the copy is written (V-03); a session over a `file` reference reads the file every call (the cache is `command` only); two references with the same executable and different arguments are two entries; a closed session resolves again.

**Fixture choice, against the wrong implementation.** The count fixture has two copies under one key and three under another, with both keys reached through two declared references in an order where the matching one is second, so "once per copy" (5), "once per key" (2) and "once per reference" (2 references) give three different numbers.

### Checklist sections

1 (the dispositions in ADR 0036), 9 (ADR 0036; the two sentences in `encrypted-copies.md`; the package doc of `keycustody` gains the session paragraph, so `scripts/docs/package-doc.baseline` moves), 11 (no key in a log or error, now with stderr), 12 (a control per new guard, negative controls, red first). Section 6 for the one enum value and `checked_at`. No setting, no migration, no fixture.

### Risks

- **Material held for a whole run.** Bounded by the operation and zeroed at end; documented in ADR 0036 D2. The dissent stands.
- **A resolver whose output changes mid-run** (a secret rotated in Vault during a run) keeps the run on the key it started with. That is a property (one run, one key), and the docs say it.
- **`not_checked` in the browser** needs a word on the keys card; V2.1 owns it.
- **A changed `Error()` breaking a test that compares text** in another package. `CommandError.Error()` keeps today's text byte for byte, which is why it is a type and not a new format.

### Order of work

1. Commit the red tests above (test-only commit).
2. `secretref.CommandError` and `Brief`; `keycustody.WithExternalKey` wraps; `DiagnoseExternal`; redaction. V-04 goes green.
3. The session; open it at the six operations. V-05 goes green.
4. The key list reads the tick's probe; `not_checked` in `openapi.json`, both bindings regenerated.
5. The guards for kopia, compression and provider kinds; `controls_v.py`.
6. ADR 0036 and the docs section. Matrix rows V-01 to V-09 flip with dated control runs.

---

## V1.2 #1514: external key lifecycle, a reference kept for reading, and supersede

### Verdicts

| Lens | Initial verdict | Consensus position |
|---|---|---|
| Crypto | **Reject** wiring `RotateExternal`: it models one active external slot, which migration 0015 deliberately does not have. And a superseded key that a config edit can quietly reactivate is not superseded. | Accept a status-only supersede plus a hard refusal to write under a non-active external key. A decrypt-only key list is the standard keyring pattern. |
| Custody | **Reject** any route that takes a reference to keep for the duration of a background run: a command reference held by an operation is a new place one lives. | Accept references kept in config, where every reference already lives, entered once and read back only as descriptions. |
| Data path | **Reject.** The spec's crash cell has no subject (F-1) and the real crash windows (config write against journal write) are undesigned. | Accept the state machine below: every intermediate state is readable and every step is safe to re-run. |
| Egress | **Reject** nothing on cost grounds: supersede and forget read no medium. | Accept; the costs are command runs, bounded by V1.1's session. |
| Recovery | **Reject** the spec's version, because it does not let a person finish: in the ordinary deployment the old copies stop opening when the reference moves (F-2), so the re-encryption that is supposed to drain them cannot open them. | Accept: keep the old reference for reading, drain, retire, forget, in that order, with each step refusing when done out of order. |
| Parity | **Reject** a design where the browser cannot keep the old reference: references are write-only, so a browser user cannot retype one they never saw. | Accept `keep_previous` on the same write that replaces the reference, so nobody retypes anything. |
| Skeptic | **Reject.** How many deployments use external keys? If few, document the limitation and drop V1.2. | Accept, because the stuck state is real and permanent: today an external key can never be retired, and a dropped reference raises an alert that cannot be cleared except by restoring it. |

**Consensus: keep, re-scoped around a decrypt-only reference list and a supersede verb. D-1 was accepted on 2026-11-19.**
**Surviving dissents:** the skeptic still thinks the number of external-key deployments may not justify a config field, and would ship the fallback below first. The parity lens wanted a "rotate external" convenience route that does the config move and the supersede in one call; the others rejected it because the two writes are to different stores and the verb would need its own resume rule (see Alternatives).

### The problem in the code today

1. An external key can never be retired (F-1), and its retire refusal gives advice that cannot work (`keyretire.go:59`).
2. Replacing the external reference drops the old one, so its copies stop opening (F-2). The CLI and API make this a one-flag mistake: `PatchEncryptionPolicy` replaces the provider wholesale (`core/service/encryption.go:299-301`).
3. If a key were superseded, the engine would still write new copies under it (F-3), and the alert would not notice, because `Resolvability` only asks whether a reference resolves (`store.go:446-464`).
4. `RotateExternal` exists, is tested (`externalregistry_test.go:199-231`) and is called by nothing.
5. The browser cannot tell an external key from a managed one (F-9).

### Design

#### Config: `retained_copy_encryption.previous_external_keys` (D-1, accepted)

```yaml
retained_copy_encryption:
  mode: required
  key_provider:
    external: { command: ["/usr/local/libexec/retnd-age-key", "2026"] }
  previous_external_keys:
    - command: ["/usr/local/libexec/retnd-age-key", "2025"]
    - file: /etc/retnd/keys/age-2024.txt
```

- Type: `PreviousExternalKeys []SecretSource yaml:"previous_external_keys,omitempty"` on `RetainedCopyEncryptionBlock`, resolved into `RetainedCopyEncryption.PreviousExternalKeys []secretref.Ref`. `omitempty`, so a file that never mentions it re-marshals byte for byte.
- `Validate`: each entry names exactly one source (`SecretSource.validateExactlyOneSource`, as `retainedcopyencryption.go:146` does for the provider); no entry equal to a provider reference, the deployment's or any set's ("it is already the key provider; a reference is either written under or kept for reading"); at most 16 entries (a bound so a typo'd loop cannot grow the alert tick's work without limit). "Equal" means the same file, env name and command argv, which is what `keycustody.refKey` hashes; `refKey` is unexported in `keycustody` and `config` does not import that package, so `Validate` compares the resolved `secretref.Ref` fields itself rather than calling it.
- **Deployment-wide only.** Checklist section 2 asks for a per-set override; the written answer is no, because a reference kept for reading is a property of a key, and any set's copy may be under any key.
- **Never written under.** `activeKey` and `externalKey` never read this list. `KeyCustody.externalRefs()` (`core/service/keycustody.go:72-93`) appends it, so `withExternalRecord`, `UnreadableExternalKeys` and the alert's at-risk check see these references and nothing else changes.
- **One-way door.** Under `KnownFields(true)` an older build cannot load a file that carries the key. The CHANGELOG says so in the sentence checklist section 2 asks for.
- **Compat fixtures:** `core/tests/compat/testdata/configs/15-previous-external-keys.yaml` (valid: a file and a command entry) and `70-invalid-previous-external-key-two-sources.yaml` (one entry naming `file` and `env`). Both are numbers the spec already reserved.

#### API, CLI and the policy view

| Surface | Change |
|---|---|
| `EncryptionPolicyPatch` | `keep_previous: bool`. With `key_provider` changing from one external reference to another, the replaced reference is appended to `previous_external_keys` in the same config write (one call to `writeEncryptionConfig`, `core/service/encryption.go:395-426`, which writes atomically). Ignored, and refused with `ErrInvalidRequest`, when the replaced provider is not external. |
| `EncryptionPolicyPatch` | `forget_previous: {index: int, source: string}`. Removes one entry. `source` must equal that entry's description, so two edits racing cannot remove the wrong one. Refused (below) if the entry still opens copies. |
| `EncryptionOverride` (`PUT /backup-sets/{source}/{set}/encryption`) | `keep_previous: bool`, same meaning, appending to the deployment's list. |
| `EncryptionPolicy` (`GET /encryption/policy`) | `previous_external_keys: [{index, source, last_key_id}]`. `source` is the description (`Ref.String`, write-only rule, `core/service/encryption.go:39-47`). `last_key_id` comes from a new `lastKeyIDs` map on `Store`, filled wherever a resolution happens (the pattern `lastSuites` uses at `store.go:80-143`), empty when nothing has resolved it in this process. Nothing on this read resolves anything. |
| `EncryptionKeyStatus` | `provider: "managed" \| "external"` (F-9). |
| CLI | `retnd encryption policy patch --external-file NEW --keep-previous`; `--forget-previous N` (the CLI reads the policy first and sends the description it shows at index N); the same `--keep-previous` on `encryption backup-set`. `encryption policy` prints the kept list with index, description and last key id. |

**Forget's refusal.** Forget resolves that one entry (an explicit write, which may resolve: `core/service/keycustody.go:109-112` sets the rule). If it yields a registered key that ACTIVE copies record, it is refused with `ErrEncryptionKeyInUse` (HTTP 409 `ENCRYPTION_KEY_IN_USE`, exit 1): "the reference at index 1 (/etc/retnd/keys/age-2024.txt) still opens 12 ACTIVE copies under key x3f…; move them first with `retnd encryption reencrypt --key x3f… --dry-run`, or keep it". If it does not resolve, or yields a key with no ACTIVE copies, it goes. Forgetting a broken reference changes nothing about what opens today, so it is never refused.

#### The supersede verb

- **Route:** `POST /encryption/keys/{id}/supersede`, CSRF, no body, `200` with `EncryptionKeyStatus`. Agents: covered by the existing deny pattern `{"", "/encryption/keys/*"}` (`core/agenttools/denied.go:37`); the catalogue gets the route in the `no(whyKeys, ...)` row (`core/agenttools/tools.go:394-396`).
- **CLI:** `retnd encryption key supersede <id>`, added to `cmdEncryptionKey`'s operand switch beside `retire` (`core/cmd/retnd/encryption.go:321-333`) and to its usage strings, the dispatch table and `docs/site/reference.html`.
- **cliecho:** `key("POST", "/encryption/keys/{id}/supersede")` building `newCmd("encryption", "key", "supersede", a.Params["id"])`, example id `x0123456789abcdef`, beside the retire entry (`core/cliecho/routes.go:1866-1871`).
- **Service:** `BackupService.SupersedeEncryptionKey(ctx, id)`, wrapped by `classifyEncryption`, opens a V1.1 resolution session, then calls a new `keycustody.Store.SupersedeExternal(ctx, id, writers []secretref.Ref) (KeyRecord, error)` where `writers` is the deployment's provider reference and every set's override reference, whatever the mode (a staged provider under `off` would be written under the day the mode flips).
- **Journal:** `state.Journal.SupersedeEncryptionKey(ctx, id)`: one transaction, `UPDATE encryption_keys SET status = 'superseded' WHERE id = ? AND provider = 'external' AND status = 'active'`, with the row read in the same transaction to tell the refusals apart.
- **Event:** `EventKeySuperseded = "key_superseded"` and `Logger.KeySuperseded(ctx, keyID, result)` (D-3), emitted on success and on refusal with `ResultError`, as `KeyRetired` does (`keyretire.go:95-99`). The constant joins the pinned table in `core/internal/obs/events_test.go:75-79`. Feed text in `ui/shared/src/encryption/feedText.ts`: "External key x3f… superseded: nothing new is written under it". The same file's `key_retired` sentence is "managed key retired:" (`feedText.ts:60-61`), which turns false the first time an external key retires, so it drops "managed" in the same change.

**Refusals, in the order they are checked:**

| Case | Sentinel | HTTP | Exit | Message names |
|---|---|---|---|---|
| Unknown id | `keycustody.ErrUnknownKey` | 404 `ENCRYPTION_KEY_NOT_FOUND` | 1 | the id |
| A managed key | new `keycustody.ErrManagedKeySupersede` (classified `ErrEncryptionKeyState`) | 409 `ENCRYPTION_KEY_STATE` | 1 | "`retnd encryption key rotate` supersedes a managed key" |
| Already retired | `ErrEncryptionKeyState` | 409 | 1 | the retirement time |
| Already superseded | no refusal, writes nothing, no event | 200 | 0 | the status, unchanged |
| A writer could not be resolved | `ErrEncryptionKeyUnresolvable` | 409 `ENCRYPTION_KEY_UNRESOLVABLE` | 1 | which provider, and its brief reason: "could not check, so nothing was changed" |
| A writer yields this key | new `keycustody.ErrKeyStillWritten` (classified `ErrEncryptionKeyInUse`) | 409 `ENCRYPTION_KEY_IN_USE` | 1 | the provider (deployment or set id) and "point it at the new key first: `retnd encryption policy patch --external-file NEW --keep-previous`" |

A key that no declared reference yields at all (stranded) supersedes normally: it is not being written under.

`routeparity_test.go` pins that each refusal has the same text and exit code on the CLI's local path and through the API.

#### The engine refuses to write under a non-active external key

In `externalKey` (`core/internal/placement/encryption.go:182-195`), after `RegisterExternal`, a row whose status is not `active` is refused:

```go
var ErrExternalKeyNotActive = errors.New("placement: the external key the reference yields has left service")
// %w: %w: %w: backup set %s: its external key from %s yields key %s, which is %s;
// nothing new is written under a key that left service. Point the reference at the new key.
```

wrapping `ErrClassUnavailable` and `ErrKeyUnresolvable`, so the copy is refused at the existing refusal point, before anything is staged, uploaded or deleted (the same place an unresolvable key is refused, `encryption.go:118-165`). Re-homes, verifies, scrubs and re-encryption sources go through the recorded key id and never through `activeKey`, so a superseded key keeps opening its copies.

`Store.Resolvability` (`store.go:440-468`) gains the same check after it derives the recipient: if `externalKeyID(recipient)` names a row that is not active, the probe is `Unreachable` with that sentence. `AlertConditions` (`conditions.go:39-67`) then raises `ENCRYPTION_KEY_UNRESOLVABLE` for the set, so a refusal is never silent.

There is no un-supersede. The crypto lens held this line: a key superseded after a compromise must not come back because somebody pasted an old path. An operator who superseded the wrong key mints or points at a new one; the docs say so.

#### `RotateExternal` is deleted (D-2)

`externalregistry.go:109-133` and `TestRotatingAnExternalKeySupersedesTheOldOneAndKeepsItDecrypting` go. The new key registers lazily at its first copy, exactly as today (`encryption.go:187`). The test's useful half (a superseded external key still opens its copies) moves into `TestACopyUnderASupersededExternalKeyOpensThroughAKeptReference_1514`.

#### The retire refusal for an external key

`RetireKey`'s `ErrKeyStillActive` branch (`keyretire.go:111-112`) reads the key's provider and, for an external key, says: "key x3f… is an external key that is still active; supersede it first: `retnd encryption key supersede x3f…`". This is the red-first test's subject (below).

#### The state machine an operator walks, and what a crash leaves at each boundary

```
 A  provider -> ref R1 (key K1 active)
 |  B1: policy patch --external-file R2 --keep-previous      (one atomic config write)
 v
 B  provider -> R2, kept: [R1]; K1 active; K2 registered at the first copy under R2
 |  B2: key supersede K1                                      (one journal transaction)
 v
 C  K1 superseded; nothing new under K1; K1's copies open through R1
 |  B3: reencrypt --key K1 --to M2                            (EPIC T's moves, one per copy)
 v
 D  no ACTIVE copy records K1
 |  B4: key retire K1                                         (one journal transaction, re-checked inside)
 v
 E  K1 retired
 |  B5: policy patch --forget-previous 0                      (one atomic config write)
 v
 F  R1 gone from config
```

| Boundary | Crash before it commits | Crash after it commits | Re-run |
|---|---|---|---|
| B1 | config unchanged (`writeConfigBytesAtomically`) | B reached | the same patch is idempotent: R1 is already in the list (deduplicated by `refKey`) and R2 already the provider |
| between B1 and B2 | (state B) K1 stays active and copies under R2 go to K2. Nothing is unreadable and nothing is refused. The only symptom is that K1 cannot be retired yet, and the retire refusal names `supersede` | | B2 |
| B2 | K1 still active | C reached | superseding a superseded key is a no-op |
| B3 | EPIC T's cells: before VERIFIED the move is abandoned and the source is untouched; after VERIFIED the next run or cycle finishes (`retroencrypt.go:60-71`, `core/tests/movecrash/reencrypt_cells_test.go:88`) | some copies moved | the dry run lists what is left |
| B4 | K1 superseded | E reached | retiring a retired key writes nothing (`keyretire.go:109-110`) |
| B5 | config unchanged | F reached | the index is gone; the CLI re-reads the list |

Out of order: B2 before B1 is refused (a writer yields K1). B5 before B3 is refused while R1 still opens K1's copies. B4 before B3 is refused naming the copies. Dropping R1 by hand-editing config before B3 is the one unguarded move, and the existing at-risk alert covers it within a tick (`core/service/keycustody.go:228-304`), naming K1 and its copies.

Every state from A to F keeps every ACTIVE copy readable, provided R1 is not dropped by hand. That is the property the recovery lens asked for.

#### Egress and cost

No medium is read or written by B1, B2, B4 or B5. Command runs: B1 none (config only); B2 one per distinct writer reference (inside a session); B5 one (the forgotten entry). B3 is V1.3's subject.

#### The leak surface

- References go in once (patch body, config file) and come out only as descriptions (`Ref.String`, which leaves a command's arguments out: `core/internal/secretref/secretref.go` and `TestRefNeverRendersTheResolverArgvOnAnyPath`).
- `last_key_id` is a key id, which is derived from the public recipient (`externalregistry.go:49-52`) and is not secret.
- The new refusal texts name key ids, set ids and reference descriptions. The V-22 canary corpus (`core/tests/encryptedretention/canary_test.go`) gains rows for the supersede response, the policy view's kept list, the `key_superseded` feed row and the forget refusal.

#### The fallback that was not chosen for D-1

`StartReencryptRequest` gains `source_key: EncryptionKeyReference` (write-only), and `reencrypt --key K1 --source-key-file R1` passes it. The run adds it to its V1.1 session as an extra reference for that operation only. Supersede and the engine refusal stay as above. Costs, which is why the review did not choose it:

- Routine rotation does not work: until every old copy is re-encrypted, the daemon cannot open them, so scrubs report "Key missing" and the at-risk alert fires for weeks.
- A run that dies before VERIFIED needs the operator to pass the reference again; the next ordinary cycle cannot finish it.
- A command reference held by a background operation is a new place one lives (the custody lens's objection).

### Alternatives considered

- **Wire `RotateExternal` as the spec says.** It registers and supersedes in one write, but the reference it registers has to be in config anyway for any copy to be written under it, and the old reference still drops out of config, so F-2 is untouched. Rejected.
- **Supersede implicitly when the policy patch changes the reference.** Two durable writes to two stores in one request, a resume rule nobody can see, and a hand edit of `config.yaml` bypasses it. Rejected; the UI chains the two explicit steps instead (V2.1).
- **A "rotate external" route doing config move and supersede together** (the parity lens's proposal). Same two-store problem; a crash between them leaves a state the route's re-run cannot recognise, because the old reference is no longer the provider. Rejected, recorded as a dissent.
- **Adopt the external key into the managed store** (copy its material into `retained_copy_keys/`). Readable for ever with no config. Rejected by the custody lens: the operator chose to keep the key off the host, and a routine retirement must not move it onto the host.
- **Let `retire` accept an active external key that no writer yields.** Saves a verb, but leaves a status ("active") that means nothing for an external key, and the backlog (copies under keys no longer written) cannot be counted by status. Rejected.
- **Per-set kept lists.** Rejected: a kept reference belongs to a key, not a set.

### Red-first test plan

Committed red first:

| Test | Package | Red today because | Row |
|---|---|---|---|
| `TestTheOldExternalKeyCanBeRetiredOnceItsReferenceMoved_1514`: register K1 through R1, move the provider to R2, no copies under K1, retire K1. Expects either success or a refusal naming `supersede`; then supersede and retire succeed | `core/service` | the refusal says "rotate to a new key", which cannot help (F-1), and there is no supersede | V-10 |
| `TestKeepPreviousMovesTheReplacedReferenceIntoTheList_1514` | `core/service` | no field | V-12 |
| `TestACopyUnderASupersededExternalKeyOpensThroughAKeptReference_1514`: write a copy under K1, keep R1, move to R2, supersede K1, content-verify the old copy | `core/internal/placement` over a real journal | no kept list | V-12 |
| `TestNothingNewIsWrittenUnderASupersededExternalKey_1514`: supersede K1 while a set's reference still yields it (planted directly in the journal); the next copy is refused before staging | `core/internal/placement` | F-3 | V-11 |
| `TestASetWhoseProviderYieldsASupersededKeyRaisesTheAlert_1514` | `core/internal/app` | `Resolvability` checks no status | V-11 |
| `TestSupersedeIsRefusedWhileAProviderStillYieldsTheKey_1514` (deployment provider, then a set override) | `core/service` | no verb | V-11 |
| `TestSupersedeRefusesAManagedKeyAndNamesRotate_1514` | `core/service` | no verb | V-11 |
| `TestForgettingAReferenceThatStillOpensCopiesIsRefused_1514`, and its control `TestForgettingABrokenReferenceIsAllowed_1514` | `core/service` | no field | V-13 |
| `TestRetiringASupersededExternalKeyIsRefusedNamingTheCopiesThenSucceeds_1514` | `core/internal/placement` | cannot reach superseded | V-13 |
| `TestTheKeyStatusSaysWhichProvider_1514` | `core/service` | F-9 | V-21 |
| Compat cells `15-previous-external-keys` and `70-invalid-previous-external-key-two-sources` | `core/tests/compat` | fixtures absent | V-25 (rewritten) |

Controls for `controls_v.py`:

| Label | Plant | Must turn red |
|---|---|---|
| `V a superseded external key written under` | delete the status check in `externalKey` | `TestNothingNewIsWrittenUnderASupersededExternalKey_1514` |
| `V supersede ignores the writers` | skip the writer check | `TestSupersedeIsRefusedWhileAProviderStillYieldsTheKey_1514` |
| `V kept references not declared for reading` | drop the append in `externalRefs()` | `TestACopyUnderASupersededExternalKeyOpensThroughAKeptReference_1514` |
| `V keep_previous ignored` | do not append on patch | `TestKeepPreviousMovesTheReplacedReferenceIntoTheList_1514` |
| `V forget strands copies` | skip forget's refusal | `TestForgettingAReferenceThatStillOpensCopiesIsRefused_1514` |
| `V the alert misses a superseded provider key` | drop the status check in `Resolvability` | `TestASetWhoseProviderYieldsASupersededKeyRaisesTheAlert_1514` |
| `V the supersede route dropped from the CLI echo table` | delete the cliecho entry | `TestEveryAPIRouteNamesItsCLIEquivalentOrTheGap` |

**Negative controls:** a patch without `keep_previous` writes the config it writes today, byte for byte (compat capture unchanged); superseding a stranded key succeeds; superseding twice writes once; a set whose reference yields an active key raises nothing.

**Fixture choice.** The writer-check fixture has the deployment provider on R2 (yielding K2) and one set's override on R1 (yielding K1), so a check that looks only at the deployment provider passes the wrong implementation and this fixture catches it. The kept-list fixture has two kept references where the first yields an unrelated key, so a lookup that stops at the first kept entry fails.

### Checklist sections

2 (one deployment-wide field, no per-set override, with the reason; the one-way door sentence; compat fixtures `15` and `70`), 3 (cliecho entry; dispatch table; `reference.html` row for `key supersede`; parity of refusals and exit codes), 4 (`key_superseded` with a `Result`; low volume, so paging is unaffected, asserted by the existing feed tests), 6 (`openapi.json`: the route, `keep_previous`, `forget_previous`, the kept list, `provider`; both bindings; `docs/api/contract.md`; agent catalogue row), 7 (no migration: `superseded` is already in the vocabulary and the CHECK, `core/migrations/0015_retained_copy_encryption.sql:86`), 9 (ADR 0036 D4), 11, 12.

### Risks

- **The one-way door.** Only a deployment that uses `--keep-previous` writes the key; the CHANGELOG names the version.
- **Supersede races a config reload** that points a set back at K1 between the check and the write. The engine refuses that set's next copy and the alert names the fix. Fail closed, and documented.
- **A staged provider under `off` blocks supersede.** Deliberate; the refusal names it.
- **Deleting `RotateExternal`** breaks nothing outside its own test (F-1's grep).

### Order of work

1. Red tests (test-only commit).
2. The engine refusal and the alert check (independent of config; they make F-3 safe on their own).
3. `SupersedeExternal`, the journal transaction, the service method, the route, the CLI verb, cliecho, the agent row, the event. Retire's message for external keys.
4. The config field, `Validate`, the compat fixtures, `externalRefs()`, `lastKeyIDs`.
5. `keep_previous` and `forget_previous` on both write routes; the policy view.
6. `provider` on the key status. Delete `RotateExternal`.
7. Controls; matrix rows V-10 to V-13 flip.

---

## V1.3 #1515: the backlog, made readable, and the way out made honest

### Verdicts

| Lens | Initial verdict | Consensus position |
|---|---|---|
| Crypto | **Reject** a metric: a backlog under a superseded key is the normal state after every routine rotation, so the number means nothing without the operator's intent. | Accept the key list as the backlog's one source, with status and count. |
| Custody | **Reject** nothing; worries only that a label could carry a key id. | Moot once the metric is dropped; the key list carries ids, which are not secret. |
| Data path | **Reject** the spec's "re-read EPIC T's cells" as work: reading is not a deliverable. | Accept citing the cells in the matrix; V1.3 changes nothing in the run. |
| Egress | **Reject** a re-encryption plan that hides its bill. It reads every byte from the source medium and reads the new copy back from the destination, about twice the data in reads. | Accept the per-medium byte totals in the dry run, with the resume caveat and the unknown-size count. |
| Recovery | **Reject** a retire refusal that lists copies and stops. | Accept the hint that names the dry run first, since a run needs `--to` and the dry run is free. |
| Parity | **Reject** a CLI that does not print what the API returns (F-7). | Accept the print and a footer. |
| Skeptic | **Reject** the health field and the metric as features with no reader; also wanted V1.3 merged into V1.2. | Accept the shrink. Merge rejected by the others (different code, parallel work). |

**Consensus: shrink.** Drop the health field and the metric (D-5). **Surviving dissents:** the egress lens would keep the metric for people who graph a drain across a fleet; the skeptic would merge V1.3 into V1.2.

### The problem in the code today

1. `retnd encryption key list` prints status but not the count (`core/cmd/retnd/encryption.go:626-635`), and says "no managed keys" for a list that includes external ones (`:638-640`).
2. The retire refusal names copies (`keyretire.go:72-86`) but not how to clear them.
3. The dry run lists copies and verdicts (`core/service/reencrypt.go:174-239`, `contract.gen.go:4284-4288`) with no bytes. The CLI prints only counts (`core/cmd/retnd/encryption.go:848-857`).
4. Nothing asserts that a re-encryption starts only from the CLI or the route; the guarantee is a comment (`reencrypt.go:35-36`).

### Design

#### The key list prints the backlog

`printEncryptionKey` appends `, N ACTIVE copies` (always, including 0, since zero is an answer and not an absence). `printEncryptionKeys` says "no keys" when empty and, when any key that is not active has copies, ends with:

```
12 ACTIVE copies are still under keys that are no longer written under (k1a2…: 9, x3f4…: 3).
To see which can move under the active key, and what that would read and write:
  retnd encryption reencrypt --key <id> --dry-run
```

The footer is computed from the response, so `--json` is unchanged apart from V1.2's `provider`.

#### The retire refusal names the way out

`KeyInUseError.Error()` (`keyretire.go:72-86`) appends: `; to move them under the active key, start with retnd encryption reencrypt --key <id> --dry-run, which says per copy whether it can move and what it would read and write`. The browser shows the same sentence (`writeEncryptionError` passes the text whole, `apps/common/webhost/handlers_encryption.go:463-466`). `placement` already hard-codes the binary name in refusals (`encryption.go:146`), so this follows that precedent.

The hint does not promise the copies can move: a key that is lost cannot open them, an artifact with a local copy is refused unless the run says `--from-local` (F-10, #1545), and a deployment with one medium has nowhere to write. The dry run says which, per copy.

#### The dry run states its bill

`media`, `sizes_unknown`, the bound and the CLI lines landed with #1545 (PR #1634), which also adds `path` per candidate and a `local` row for a copy written from the local disk. V1.3 cites `TestTheDryRunStatesBytesPerMediumForBothPaths_1545` and `TestTheDryRunSizeBoundHoldsForEveryRegisteredSuite_1545` and owns only the retire hint, the key-list print, `TestNothingStartsAReencryptionOnItsOwn_1515` and the plant `V the dry run drops the verification read`.

`ReencryptPlan` gains:

```json
"media": [
  {"medium": "s3-old", "bytes_read": 41231234567, "bytes_written": 0},
  {"medium": "s3-new", "bytes_read": 41240000000, "bytes_written": 41240000000}
],
"sizes_unknown": 0
```

Computed in `PlanReencrypt` from journal numbers only, over the candidates whose verdict is `would_reencrypt`:

- `bytes_read[source] += enc.StoredSize`: exact, the recorded ciphertext size.
- `bound = P + 16 * max(1, ceil(P / 65536)) + 16 + 4096`, where `P` is the copy's recorded plaintext size (`state.Placement.Size`). This is an upper bound: payload, one 16-byte tag per 64 KiB chunk, the 16-byte nonce, and a header bound of 4 KiB. `bytes_written[dest] += bound` and `bytes_read[dest] += bound`, the second for the decrypt-verify read-back the run must do.
- A copy whose `P` is unknown adds to `sizes_unknown` and to nothing else.

`TestTheDryRunSizeBoundHoldsForEveryRegisteredSuite_1545` seals an empty plaintext under every suite in `copycrypt.Suites()` and asserts its header is under 4 KiB, so adding a suite with a bigger header turns this red rather than making the bound a lie.

The CLI prints after the groups:

```
would read 41.2 GB from s3-old (the copies as stored)
would write at most 41.3 GB to s3-new, and read it back once to verify it
  (a run that resumes after an interruption reads it once more before retiring the old copy)
```

and, when `sizes_unknown > 0`, "N copies have no recorded size and are not in these totals". It prints bytes, never money: the product has no price table.

The OpenAPI description of `media` says: bytes, upper bound for the destination, the verification read included, a resumed run reads the destination once more (`core/internal/placement/predelete.go` explains why that second read stays, #439).

#### Nothing starts a re-encryption on its own

`TestNothingStartsAReencryptionOnItsOwn_1515` walks every non-test `.go` file under `core/` and `apps/` and fails if `StartReencrypt(`, `RunReencrypt(` or a composite literal setting `Reencrypt: true` appears outside an allow-list: `core/service/reencrypt.go`, `core/cmd/retnd/encryption.go`, `core/cmd/retnd/encryptionroute.go`, `core/internal/apiclient/calls.go`, `apps/common/webhost/handlers_encryption.go`. EPIC T's crash cells (`TestCrashMatrixOverReencryptingAnEncryptedCopy_1317` and its anti-vacuity partner `TestTheReencryptCrashCellsReallyReencryptSomething_1317`) are cited in row V-17 as they stand.

### Alternatives considered

- **A health field and a metric** (the spec). No reader: nonzero is normal after any routine rotation, so nothing can alert on it, and the dashboard already degrades for a superseded managed key with no confirmed bundle (`store.go:503-514`). Dropped.
- **Adding a `key_status` label to `retained_copy_encrypted_copies`.** Changes an existing family's label set, which the spec forbids as a rename. Rejected.
- **An exact written-size figure** from a per-suite header measurement at run time. Needs a throwaway keygen per suite per dry run for a number the operator rounds anyway. Rejected for the bound.
- **Money instead of bytes.** No price table, and a wrong price is worse than none. Rejected.
- **Showing medium type (free `local_dir` against billed) beside each line.** The egress lens wanted it; the others held that `costOf` lives in `recoverytest` (`core/internal/recoverytest/recoverytest.go:673`) and duplicating its rule here is a copy that drifts. Left out; the docs say a `local_dir` medium costs nothing to read.

### Red-first test plan

| Test | Red today because | Row |
|---|---|---|
| `TestTheRetireRefusalNamesTheDryRun_1515` | no hint | V-16 |
| `TestTheDryRunStatesBytesPerMediumForBothPaths_1545` (landed with #1545, cited as it stands) | no field | V-16 |
| `TestTheDryRunSizeBoundHoldsForEveryRegisteredSuite_1545` (landed with #1545, cited as it stands) | no bound | V-16 |
| `TestTheKeyListPrintsTheCountAndTheWayOut_1515` (CLI golden) | no print | V-14 |
| `TestTheKeyListCountsCopiesPerKeyNotPerSuite_1515`: three keys of one suite, two superseded with 4 and 7 copies, one active with 5 | passes today (the code exists): a guard, proven by planting | V-14 |
| `TestNothingStartsAReencryptionOnItsOwn_1515` | passes today: a guard | V-17 |

Controls:

| Label | Plant | Must turn red |
|---|---|---|
| `V the backlog counted by suite` | `CopiesRecording` from `ActiveEncryptedCopiesBySuite[rec.SuiteID]` | `TestTheKeyListCountsCopiesPerKeyNotPerSuite_1515` |
| `V the retire refusal loses its way out` | drop the hint | `TestTheRetireRefusalNamesTheDryRun_1515` |
| `V the dry run drops the verification read` | omit `bytes_read[dest]` | `TestTheDryRunStatesBytesPerMediumForBothPaths_1545` |
| `V a rotation starts a re-encryption` | call `b.StartReencrypt` from `RotateEncryptionKey` | `TestNothingStartsAReencryptionOnItsOwn_1515` |

**Negative controls:** a key with zero copies prints `0 ACTIVE copies` and no footer; a dry run with nothing selected has an empty `media` list, not an absent one; refused candidates add no bytes.

**Fixture choice.** Sizes straddle the 64 KiB chunk boundary (65536 and 131072 are exactly one and two chunks; an off-by-one in the chunk count moves the bound by 16), and the stored sizes are not the plaintext sizes, so a sum over the wrong column fails. The per-key fixture uses one suite for all three keys, so a count by suite gives 16 for every key and fails.

### Checklist sections

3 (the CLI print; the refusal identical on both routes), 4 (nothing new in the feed; a dry run is a read), 6 (the retire refusal on both routes; `media` and `sizes_unknown` already in `openapi.json` from #1545), 7 (no migration; computed), 12.

### Risks

- **The bound overstates** by up to about 4 KiB per copy, which is noise next to the payload; it says "at most".
- **Recorded sizes can be missing** on old rows; the count says so rather than guessing.

### Order of work

1. Red tests.
2. The retire hint.
3. (Landed early with #1545, PR #1634: `media`, `sizes_unknown`, `PlanReencrypt`, the CLI print.)
4. The CLI key list print.
5. The source guard. Controls. Matrix V-14, V-16, V-17 flip; V-15 is removed with the metric.

---

## V2.1 #1516: the design note and the keys card

### Verdicts

| Lens | Initial verdict | Consensus position |
|---|---|---|
| Crypto | **Reject** shipping anything on a card that calls a superseded key "Retired" (F-8). | Accept with three distinct words and states: Active, Superseded, Retired. |
| Custody | **Reject** a keep-previous checkbox that defaults off: the default today strands copies silently. | Accept default on when the replaced reference is external and its key has ACTIVE copies. |
| Data path | **Reject** a two-step flow (save reference, then supersede) that the UI could lose half of. | Accept: the second step is offered from durable state (an active external key no provider yields), not from dialog memory, so a closed tab or crash re-offers it. |
| Egress | **Reject** a re-encrypt button that hides the bill. | Accept the byte lines from V1.3 in the dialog, before the run is armed. |
| Recovery | **Reject** a retire dialog that lets you click into a refusal. | Accept: the dialog shows the copy count and the dry-run link before the confirm, from the row's own count. |
| Parity | **Reject** a per-copy "old key" chip with no CLI twin and no clear meaning after routine rotations. | Accept no per-copy chip; every action has its CLI line in `CommandEcho`. |
| Skeptic | **Reject** most of it: the key list already shows the count and links to the copies. | Accept the bug fix, the three controls V1.2 needs, re-encrypt by key, and the captures. Nothing else. |

**Consensus: shrink and fix the bug.** **Surviving dissent:** the recovery lens would still like a per-copy marker in the Backups list during a compromise response; the others held that the key filter link (`EncryptionCard.tsx:333`) already shows exactly those copies.

### The problem in the code today

- F-8: the status word lies, and the Retire button sits on rows labelled Retired.
- F-9: the card cannot tell external keys apart, and labels the table "Managed keys".
- No control supersedes an external key, keeps the old reference, or forgets one.
- `ReencryptDialog` takes a suite only (`ui/shared/src/encryption/ReencryptDialog.tsx:45-50`), so "move this key's copies" is not reachable from the key row.
- The site stills `encryption-08-rotated` and `encryption-21-key-copies-count` show "Retired" for a superseded key.

### Design

#### The design note

`docs/design/1516-external-rotation-and-backlog.html` and `.png`, dated, in light, dark and narrow (375 px), drawn against `docs/design/Backup Manager.dc.html`. It states the decisions below and why, and it lands before any `.tsx` change (checklist section 5).

#### The keys card: states

Table renamed "Keys"; columns Key, Holder, Created, Status, Copies, Actions.

| Row state | Holder | Status cell | Copies cell | Actions |
|---|---|---|---|---|
| Active managed | Managed | badge **Active** (ok edge) | count, link | (none; Rotate and Export stay above the table) |
| Active external, a provider yields it | External | **Active** | count, link | none |
| Active external, no provider yields it (left over from a reference change) | External | **Active, no longer written by any provider** | count, link | **Supersede** |
| Superseded with copies (the backlog) | either | badge **Superseded** (warn edge, warn-quiet fill) | "N copies still under it", link, and **Re-encrypt…** | **Retire** (opens the dialog, which shows the count first) |
| Superseded, 0 copies | either | **Superseded** | 0 | **Retire** |
| Retired | either | **Retired** (dashed edge, secondary ink) and the time | 0 | none |
| Does not resolve | either | the state word plus ", does not resolve" and the reason banner (as today, `EncryptionCard.tsx:274-284`) | count | as above |
| External, not checked yet (`not_checked`) | External | state word plus ", not checked yet" | count | as above |

"No longer written by any provider" needs a fact the browser does not have. V1.2's policy view carries `last_key_id` for the provider and each set override, so the card compares ids it already has; nothing resolves on the read.

#### The provider area

Under "Key holder" (`EncryptionCard.tsx:195`), when `previous_external_keys` is not empty, a readout **Kept for reading** lists each entry: description, last key id (or "not resolved yet"), and **Forget**, which is disabled with its tooltip while that key's row shows copies, and enabled otherwise. The server's refusal is still the authority.

#### Dialogs

1. **External dialog** (`ExternalDialog`, `EncryptionCard.tsx:391-400`): a checkbox **Keep the current reference for reading copies already written**, ticked by default when the current provider is external and its key has copies, hidden when the current provider is managed. Save sends `keep_previous`.
2. **Then, chained** (the `SuiteChangeFlow` pattern, `EncryptionCard.tsx:467-475`): when the saved policy leaves an active external key that no provider yields, the card opens **Supersede x3f…?** at once, and the same prompt sits on that key's row until done.
3. **Supersede dialog:** "Nothing new is written under x3f… from now on. Copies already under it stay readable through the reference kept for reading. This cannot be undone." `CommandEcho`: `retnd encryption key supersede x3f…`. The refusal shows in place, whole.
4. **Retire dialog** (`RetireDialog`, `EncryptionCard.tsx:579-609`): when the row's count is above zero, the body leads with "12 active copies still record x3f…, so retiring it now is refused." and a **Re-encrypt these copies…** button; the confirm stays enabled, since the server decides. `CommandEcho` shows both `key retire` and `reencrypt --key … --dry-run`.
5. **Re-encrypt dialog:** `ReencryptDialog` takes `{suite} | {key}` and passes it to `planReencrypt` and `startReencrypt`; the byte lines from V1.3 sit under the dry-run table and update with the chosen destination. `CommandEcho` uses `reencryptCommand({key, to, dryRun})` (`encryptionCommands.ts:118`).
6. **Forget dialog:** "Remove /etc/retnd/keys/age-2024.txt from the references kept for reading? Copies under its key would stop opening." `CommandEcho`: `retnd encryption policy patch --forget-previous 0`.

#### Copy for the status words

- Active: "New copies are written under this key."
- Superseded: "Nothing new is written under this key. Copies already under it stay readable."
- Retired: "Out of service. No active copy records it."

These are readouts, not pop-ups (the 994 note's rule for registry facts, `docs/design/994-retained-copy-encryption.html:272`), and render as visually hidden text beside the badge for screen readers.

#### Tooltips and field help

New registry entries in `ui/shared/src/tooltips/tooltips.json`, each stated from the code:

| Id | Label | Copy |
|---|---|---|
| `encryption.key.supersede` | Supersede | Marks an external key as no longer written under. Copies already under it stay readable through any declared reference that yields it, including one kept for reading. Refused while the deployment's or a set's key provider still yields it, because the next copy would then be refused. It cannot be undone. |
| `encryption.key.reencrypt` | Re-encrypt these copies | Opens the dry run for the copies recorded against this key: which can move to another medium under the active key, which cannot and why, and what a run would read and write per medium. Nothing moves until you pick a destination and start it. |
| `encryption.previous.keep` | Keep the current reference for reading | Moves the reference you are replacing into the list kept for reading, in the same configuration write. Copies under its key stay readable, verifiable and re-encryptable, and nothing new is written under it. Unticked, the reference is dropped and those copies stop opening until it is declared again. |
| `encryption.previous.forget` | Forget | Removes this reference from the list kept for reading. Refused while it still yields a key that active copies record, because they would stop opening. A reference that no longer resolves can always be removed. |

`encryption.key.retire` (`tooltips.json:2094-2097`) gains one sentence: "An external key is superseded first." `encryption.key.rotate`'s copy is unchanged, but the rotate dialog's bullet at `EncryptionCard.tsx:447` becomes "The current key stays in the registry as Superseded while any copy records it."

`fieldHelpCopy.ts` (`ui/shared/src/components/fieldHelpCopy.ts`) gets one three-part entry, for the keep checkbox, the only new input:

- **For:** keeping the reference you are replacing, so copies written under its key still open.
- **Example:** ticked, while replacing `/etc/retnd/keys/age-2025.txt` with `/etc/retnd/keys/age-2026.txt`.
- **Effect:** the 2025 file moves into "Kept for reading"; new copies go under the 2026 key; the 2025 copies stay readable until you re-encrypt them and retire their key.

**Refused a pop-up, with the reason:** the Holder column and the Kept-for-reading readout are facts drawn as text; the byte lines are numbers whose meaning is in the line itself.

The opt-out preference test runs with the preference ON over the new ids (checklist section 5).

#### Parity with the CLI

| Browser action | CLI | Route |
|---|---|---|
| Save a new external reference, keeping the old | `encryption policy patch --external-… NEW --keep-previous` | `PATCH /encryption/policy` |
| Supersede | `encryption key supersede <id>` | `POST /encryption/keys/{id}/supersede` |
| Re-encrypt these copies (dry run, then run) | `encryption reencrypt --key <id> [--to M] [--dry-run]` | `GET`/`POST /encryption/reencrypt` |
| Retire | `encryption key retire <id>` | `POST /encryption/keys/{id}/retire` |
| Forget | `encryption policy patch --forget-previous N` | `PATCH /encryption/policy` |
| Read the backlog | `encryption key list` | `GET /encryption/keys` |

The ungated direction (every new verb reachable from the browser) is walked by hand against the mock API and recorded in matrix row V-19 with name and date.

#### Measured contrast plan

New pairs, added as rows to `scripts/contrast/check-ui-pairs.py` so the gate holds them in both themes. Ratios I computed from `ui/shared/src/design-system/tokens.css` (light `:root`, dark `[data-theme="dark"]`), oklch converted to sRGB, WCAG relative luminance:

| Pair | Use | Needs | Light | Dark |
|---|---|---|---|---|
| `--text` on `--warn-quiet` | Superseded badge label | 4.5 | 15.46 | 11.80 |
| `--text-2` on `--warn-quiet` | secondary ink inside it | 4.5 | 5.88 | 5.59 |
| `--warn` on `--surface` | Superseded badge edge | 3.0 | 4.76 | 8.34 |
| `--warn` on `--surface-2` | same edge on the quiet fill | 3.0 | 4.56 | 7.88 |
| `--text-2` on `--surface` | Retired badge, Holder column | 4.5 | 6.64 | 6.89 |
| `--edge-3` on `--surface` | Retired badge dashed edge | 3.0 | 3.48 | 3.85 |
| `--text` on `--ok-quiet` | Active badge label | 4.5 | 15.45 | 12.27 |

One trap the design avoids: `--warn` text on `--warn-quiet` is 4.22 in light, under 4.5, so the badge never draws its label or a text glyph in `--warn`; the colour is only on the edge. The PR that adds the surface re-measures these in a rendered page with `scripts/contrast/check.sh` and reports the numbers.

#### Captures

A step in `docs/site/tools/capture-encryption.mjs` and `encryption-captures.json` (moved here from V2.2, because this PR is the one that changes the screens): new stills for the backlog row, the supersede dialog, the kept list and the re-encrypt dialog with bytes, light, dark and narrow; `encryption-08-rotated` and `encryption-21-key-copies-count` re-recorded because their word changes. Mock API (`mockEncryption.ts` grows an external key, a superseded key with copies and a kept reference), clock pinned, `capture-first-run.mjs` run once unchanged first, two consecutive runs byte-identical.

### Alternatives considered

- **Keep "Retired" for superseded** (the 994 decision). Now contradicts the Retire button and the API; rejected.
- **A per-copy chip "under a superseded key".** Rejected as above, with the recovery lens's dissent.
- **Supersede automatically after saving a new reference.** Rejected in V1.2; the chained prompt keeps it a separate, visible act.
- **A dedicated "Rotate external key" wizard.** More surface than the two existing dialogs plus one prompt; rejected.

### Red-first test plan

Vitest (frontend gate), written red first:

- `EncryptionCard.supersededIsNotRetired.test.tsx`: a superseded row reads "Superseded", a retired row "Retired" (red today, F-8).
- `EncryptionCard.supersede.test.tsx`: Supersede appears only on an active external key no provider yields; posts the route; shows a refusal whole.
- `EncryptionCard.keepPrevious.test.tsx`: the checkbox defaults on when copies exist and off otherwise; the patch carries `keep_previous`.
- `EncryptionCard.forget.test.tsx`: disabled while the key has copies; sends index and source.
- `ReencryptDialog.byKey.test.tsx`: plans with `key`; shows the byte lines; arms only after a scoped dry run.
- `RetireDialog.backlog.test.tsx`: count and re-encrypt link before confirm.
- The tooltip suite's orphan check and allowlist cover the four new ids.

Controls in `scripts/bdtools/encryption/controls_ui.py`:

- `UI a superseded key drawn as Retired` (restore the old map) turns `EncryptionCard.supersededIsNotRetired` red.
- `UI keep previous defaults off with copies` turns the keep test red.
- `UI the superseded badge label drawn in the warn ink` (a stylesheet plant) turns `check-ui-pairs.py` red in the light theme.

**Negative control:** a deployment with only a managed key shows no Holder distinctions beyond "Managed", no kept list and no Supersede.

### Checklist sections

3 (browser direction by hand, V-19), 5 (design note first; the encryption card on `SettingsPage.tsx`; per-set card unchanged, since the kept list is deployment-wide, stated; frontend gate green), 5 Tooltips (four entries, one field-help entry, a refusal list, opt-out ON), 6 (generated client consumed), 10 (the capture step), and the contrast rule.

### Risks

- **Stale stills** if V2.1 merges without its captures; they are in the same PR for that reason.
- **The chained prompt reappearing** on a key the operator means to keep active (a per-set override somewhere still yields it): it appears only when no provider yields the key, which is exactly when supersede is allowed.

### Order of work

1. Design note with `.png`, committed alone.
2. Red vitest specs.
3. The status-word fix and the Holder column.
4. Supersede, keep, forget; the chained prompt.
5. `ReencryptDialog` by key with bytes; the retire dialog.
6. Tooltips and field help; contrast rows; captures; V-19 walked and recorded; V-20 flips.

---

## V2.2 #1517: end to end, documentation and the gate

### Verdicts

| Lens | Initial verdict | Consensus position |
|---|---|---|
| Crypto | **Reject** an e2e that only proves the managed path, which already works. | Accept with the external path as the main case. |
| Custody | **Reject** unless the canary corpus covers every new surface. | Accept V-22 extended over V1.1's and V1.2's surfaces. |
| Data path | **Reject** an e2e that never interrupts a run. | Accept one interrupted re-encrypt case; the kill-at-each-boundary cells stay EPIC T's. |
| Egress | **Reject** docs that do not say a re-encryption reads about twice the data and needs a second medium. | Accept those sentences in `encrypted-copies.md`. |
| Recovery | **Reject** docs that describe verbs instead of a procedure. | Accept two procedures: retiring an external key, and responding to a compromised key, each in order with the refusal you hit if you skip a step. |
| Parity | **Reject** nothing new. | Accept; the gated parity rows are re-run. |
| Skeptic | **Reject** captures here (they belong with the screen change) and a docs check that greps an ADR for seven headings. | Accept captures moved to V2.1; the ADR check stays as one line in the closing check because it is cheap. |

**Consensus: keep, slightly smaller.**

### The problem in the code today

Nothing exists yet for this epic: no suite in `retnd-tests`, no closing check, no matrix registration, and the docs still say there is no external rotation (`docs/encrypted-copies.md:301`).

### Design

#### End to end, in `retnd/retnd-tests`

One suite, `encryption-external-rotation`, with `scripts/e2e/tests-repo.pin` moved in the same PR. Two mediums (the harness's MinIO and a `local_dir`), because re-encryption needs a second medium (F-10).

1. **External, full:** provider R1, back up three artifacts, let their local copies expire so each has one ACTIVE placement; `policy patch --external-file R2 --keep-previous`; a new backup lands under K2; `key supersede K1`; `reencrypt --key K1 --dry-run` (asserts the byte lines); run to the second medium; `key retire K1`; `policy patch --forget-previous 0`. Asserts at each step what the CLI prints and that every copy content-verifies.
2. **External, out of order:** supersede before moving the provider (refused, exit 1, names the provider); forget before draining (refused, names the copies); retire before draining (refused, names the dry run).
3. **External, interrupted:** cancel a re-encrypt run with `assurance cancel` while a move is before VERIFIED; the source copy is intact; the next run finishes.
4. **Managed:** rotate, `reencrypt --key`, retire. This passes on `main` except the hint and the bytes; those two assertions are its red.
5. **Browser and CLI agree** on one refusal (forget while copies exist) and one success (supersede), through Playwright against the same deployment.

#### Documentation

- `docs/encrypted-copies.md`: replace `:291-302` with the lifecycle (active, superseded, retired; the kept list); two procedures ("Retiring an external key" and "After a key is compromised"); the honest sentences: re-encryption writes to another medium and needs one; it reads the old copy and reads the new one back, about twice the data in reads; an artifact that still has a local copy is refused unless the run says `--from-local` (#1545, landed); it cannot open copies under a key that is lost, and those copies keep the key from being retired; it does not recall copies a provider retains (bucket versioning, provider backups), so keep the old key offline until those are purged. Update `:530-540`.
- `docs/recovery.md` and `docs/recovery-without-a-terminal.md`: one paragraph each pointing at the compromise procedure, and the note that a copy under a superseded key still decrypts with the stock tool and its own key.
- `README.md` subcommand list; `docs/site/reference.html` (`key supersede`, `--keep-previous`, `--forget-previous`); `docs/site/index.html#honest` (no real KMS or Vault was operated; the sample scripts were not run against one; no fleet; no real bucket at scale); `docs/api/contract.md`; `CHANGELOG.md` under `[Unreleased]` naming #1512 and the sub-issues, what an existing deployment sees (nothing, unless it uses an external key; the keys card says Superseded where it said Retired; the key list stops running commands), and the one-way door sentence for `previous_external_keys`.
- `docs/compliance/` and `docs/submission/`: a sentence each that what is stored did not change, except references in config, which were already stored there.
- `docs/epic-checklist.md` section 1: next free FR is FR-108.

#### Gate and closing check

- `scripts/release/check-epic-v-closed.sh` in the shape of `check-epic-u-closed.sh`: no `BLOCKED` row in the matrix; ADR 0036 names all seven items; the canonical deployment unchanged via `scripts/release/contract-unchanged.sh` against the epic's start commit; `core/migrations/` still ends at `0022_agent_access.sql`; and only the two reserved compat fixtures added (`15`, `70`).
- `scripts/tests/epic-v-closure.test.sh`: plants a `BLOCKED` row, a moved compose file, a migration and a third compat fixture in a throwaway copy, and requires each to be named; a clean copy is the control.
- `scripts/ci-local.sh`: a `gate_step` for the closing check; a stub in `scripts/tests/ci-local-gate.test.sh`.
- `scripts/bdtools/encryption/controls_v.py` registered with the selftest runner; `core/tests/encryptiongate/matrix_test.go` reads `docs/conformance/epic-v-matrix.md` in open mode the way it reads EPIC T's (`:44-45`), so a `PASS` row must quote a real control label and a date.
- `core/tests/compat/matrix_test.go` registers the matrix beside EPIC U's (`:192`), after reshaping it to `V1.n` gate ids, `VV.n` ledger ids and a `### Phase 1 exit gate` heading.

### Alternatives considered

- **Captures here.** Moved to V2.1, for the stale-still reason.
- **A real KMS in the e2e.** Nobody here operates one, and a test that depends on a cloud account is a test that does not run. Rejected, and said so on `#honest`.

### Red-first test plan

Cases 1 to 3 fail against `main` (no verb, no field). Case 4 fails on its two new assertions. The closure self-test is red until the check exists. Each is committed red in `retnd-tests` before the pin moves.

### Checklist sections

1, 8 (empty canonical deployment diff, asserted), 9, 11, 12. Section 10 moved to V2.1.

### Risks

- **The pin moving in lockstep** with a `retnd-tests` change that needs this repo's verbs; the usual two-PR dance, merged together.
- **The matrix reshape** touching rows other PRs cite; done last, in this PR only.

### Order of work

1. Red e2e cases in `retnd-tests`.
2. Docs.
3. Closing check, self-test, gate step, stub.
4. Matrix reshape and registration; every row flips or is rewritten.
5. Pin bump; CHANGELOG last.

---

## Appendix: the matrix after these designs

| Row | Today | Proposed |
|---|---|---|
| V-01 | ADR names seven items | unchanged |
| V-02 | fourth field on `Ref` | cite the existing control and `TestTheKeyProviderHasExactlyTwoKinds_1513` |
| V-03 | timeout or exit is unresolvable | unchanged; cite the existing control too |
| V-04 | stdout and stderr reach nothing | stdout nowhere; stderr only on `provider test`; a key on stderr withheld there too |
| V-05 | runs per pass documented | at most one run per distinct `command` reference per operation; the key list runs none |
| V-06, V-07, V-09 | guards | cite existing controls; V-07 gets `V a kopia set reaches the re-encrypt selector` |
| V-08 | size equation | unchanged, with compressible input |
| V-10 | can the old key be retired | the red test as designed in V1.2 |
| V-11 | one write, kill leaves one active key | supersede refused while a writer yields the key; nothing written under a superseded external key; the alert says so |
| V-12 | old copies still open | through a reference kept for reading |
| V-13 | retire refused then allowed | plus forget refused while copies remain |
| V-14 | backlog on service, health, CLI, API | key list on API and CLI; per key, not per suite |
| V-15 | metric with no key label | **removed** (D-5) |
| V-16 | retire hint and dry-run bytes | unchanged, with the bound and the unknown-size count |
| V-17 | nothing starts a re-encryption | unchanged |
| V-18 to V-21 | parity, tooltips, contract | add the supersede route and the new fields |
| V-22 | no key material on new surfaces | add stderr and the kept list |
| V-23 | e2e | the five cases above |
| V-24 | canonical deployment untouched | unchanged |
| V-25 | no migration, no config key | no migration; config fixtures `15` and `70` only |
| V-26 | docs tell the truth | add the two procedures and the four honest sentences |
| V-27 | captures | moved to V2.1 |
| V-28 | gates | unchanged |

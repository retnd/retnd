# ADR 0033: Suite agility for encrypted retained copies, the default suite, and where encryption can enter

- Status: accepted for the documents; the code lands in EPIC T phase 1 (#1312 to #1315) and
  phase 2 (#1316 to #1320). Until those merge this ADR describes behaviour that is specified
  and not yet shipped.
- Date: 2026-11-10
- Scope: EPIC T (#1310), FR-83 through FR-90. Recorded by T1.1 (#1311), measured on `main` at
  `477aec5a` (the whole EPIC N stack merged).
- Number: `0031` and `0032` are reserved to EPIC P (#955) in prose on `main`, so this record
  takes `0033`. #1310 and its sub-issues say "0031"; read that as this file.
- Supersedes one sentence of ADR 0027 and one of `docs/encrypted-copies.md`: "the format is a
  property of the build, never a config field" (`docs/adr/0027-streaming-aead-format-and-the-upload-seam.md:131`)
  and "There is no cipher, nonce length, chunk size, KDF cost or algorithm setting at either
  level, and none is planned" (`docs/encrypted-copies.md:87-88`). Everything else in 0027 and
  0028 stands. The one-line notes in those two files land with T2.5 (#1320).

## The owner's four requirements, in my words

1. retnd offers encryption suite options, and ships a default that is defensibly strong.
2. New suites can be added later and old ones replaced, without orphaning a copy.
3. Every suite is an established library's construction. Nothing is designed here: no envelope, no chunk
   framing, no KDF, no nonce scheme.
4. Encryption happens as early as possible relative to the moment retnd first sees a file on the remote host.

Requirements 1 to 3 are a design problem I can solve inside the existing model. Requirement 4 collides with
the product definition EPIC N wrote down, and the honest answer is a decision for the owner, not a design
trick. I take them in that order.

## Context: what is true today (verified in the code)

### The format and where it is named

- One format, written as a plain age v1 stream with one X25519 recipient by `age.Encrypt`
  (`core/internal/copycrypt/copycrypt.go:219`). `FormatID = "age/v1"`, `FormatVersion = 1` (:70-71). The
  64 KiB chunk, the nonce layout and the HKDF step are the library's.
- The copy record is `placement_encryption(format_id TEXT CHECK (format_id <> ''), format_version INTEGER
  CHECK (>= 1), key_id, stored_hash, stored_hash_alg, stored_size)` (migration 0015). The vocabulary of
  `format_id` is **not** CHECKed, so a second id needs no schema change for the copy record. Absence of a
  row means plaintext.
- The key registry is `encryption_keys(id, provider, recipient, status, created_at, retired_at)`. There is
  **no format or suite column on a key**: a key is an age X25519 identity by construction, and
  `idx_encryption_keys_one_active_managed` allows exactly one active managed key deployment-wide.
- `keycustody.Format` (`format.go`) is already the seam: `ID()`, `Generate()`, `Recipient(secret)`,
  `Open(secret, r)`, deliberately with no Seal. `AgeFormat{}` is the only implementation. The bundle carries
  `format` and `Bundle.Decrypt` refuses a mismatch. The sidecar carries `format_id`/`format_version`.
- Config: `retained_copy_encryption: {mode, key_provider}` and a per-set `{mode: inherit|off|required,
  key_provider}`; `KnownFields(true)`; NC-19 gave a written "no" to a per-set format knob.
- The engine: `copyEncryptedToMedium` (`placement/encryption.go:195`) resolves the one active managed key
  (`activeKey`, :112), encrypts the local plaintext file into `.moves` staging, uploads, decrypt-verifies,
  and records `FormatID`, `FormatVersion`, `KeyID` and the stored digest in the committing write (:449).
- Retroactive encryption (#1291, `placement/retroencrypt.go`, migration 0019) is an ordinary journaled move
  to a **different** medium, because the journal holds one placement per artifact per medium and the object
  key is a function of the artifact, so an encrypted sibling cannot sit beside its source. Plaintext streams
  from the medium into the encrypting pass; nothing plaintext lands locally.

### How retnd first sees a remote file, and what happens to the bytes

- Transport is embedded rclone used as a library, with the local, s3 and sftp backends registered
  (`transport/rclone/adapter.go:25-27`). There is no direct `pkg/sftp` or ssh-exec on the data path.
- Discovery is a full recursive listing per poll (`adapter.go:401`, `discovery/discovery.go:278`), gated by a
  completion strategy (rename, marker or stable mtime). The poll interval is global with a per-set override
  and a one-minute floor. A new file is "seen" when it is listed and complete; its remote size, mtime, id and
  a best-effort `sha256sum` over SSH are journalled as `DISCOVERED`.
- The transfer (`lifecycle/transfer.go:319`) removes any stale `.partial`, then `CopyToLocal` writes
  `<local_path>/<name>.partial` through rclone `operations.Copy` (`adapter.go:600`), which uses its own temp
  name and renames. **Not resumable**: a retry starts at byte zero. No range reads. Size must equal the
  discovered remote size.
- Verification (`lifecycle/verify.go:465, :644`) is a **second full read of the `.partial` from disk** into
  sha256, compared with the remote `sha256sum` when `validation.hash: sha256` is set (mismatch quarantines),
  then the optional validator runs on the plaintext path (:711).
- Commit (`lifecycle/commit.go:653`) is fsync, `os.Link` to the final name (no clobber), remove `.partial`,
  fsync the directory, record `COMMITTED` plus the local placement, write the sidecar with the plaintext
  checksum.
- In the **same cycle**, after every set's pass, `RunHomeMoves` (`app/cycle.go:367-371`) plans moves from
  each set's retention home plan and the move engine encrypts and uploads. So the earliest ciphertext today
  exists one cycle after first sight, and only when the artifact's home medium is a remote one.
- The remote source is deleted after the **local** copy alone is re-hashed and the remote identity is
  re-confirmed (`lifecycle/remotedelete.go:265, :633`). No off-site copy is required first.
- Plaintext readers of the local copy, all of which compare to the plaintext digest: verify's read-back;
  validators; the scrub (`revalidate/checks.go:630`) and its restore-check hook; the move engine's
  `verifyLocalCopy`; the pre-delete `verifyLocalFinal`; reconcile; the recovery download's `Seek`-based
  range reader and its whole-file index and `ETag` (`service/recover_stream.go:155`,
  `recovery/export/chunks.go:117-129`); recovery tests and the built-in structure check that sniffs
  zstd/gzip magic (`recoverytest/builtin.go:467`); `copyToMedium` when encryption is off.
- There is no deduplication, no hard-linking between artifacts, no reflink. Every `os.Link` is a
  temp-to-final publish.
- Kopia-engine sets stream the source into the repository with no local path
  (`transport/rclone/sourcestream.go:34`); the repository is always encrypted by kopia
  (`backupengine/kopia/repository.go:714`). `required` on such a set is satisfied by kopia (ADR 0028 D4).
- ADR 0003 (pre-EPIC N) describes a producer that age-encrypts upstream and a manager that pulls opaque
  ciphertext. That is still a valid operating mode: retnd stores whatever bytes it is given.

### Libraries, verified 2026-11-10

| Candidate | Version, date | Licence | Go floor | Construction | FIPS | PQ | Reader outside retnd | Notes |
|---|---|---|---|---|---|---|---|---|
| `filippo.io/age` (in tree) | v1.3.2, 2026-08-29 | BSD-3 | 1.25 | age v1: STREAM over ChaCha20-Poly1305, 64 KiB chunks, HKDF-SHA-256, HMAC'd header, **16-byte file key** | none; ChaCha20 is not approved | since v1.3.0 a native `mlkem768x25519` recipient (HPKE, X-Wing-style, draft-ietf-hpke-pq), `age1pq1...` / `AGE-SECRET-KEY-PQ-1...`, 1120-byte stanza, recipient string about 2000 chars; spec says do not mix PQ and non-PQ recipients in one file; `DecryptReaderAt` for seeking | `age` CLI 1.3+ on any machine | one maintainer (Geomys); C2SP spec with CCTV vectors; v1.3.2 is a hardening release |
| `filippo.io/hpke` (indirect already) | v0.4.0, 2025-12-05 | BSD-3 | | RFC 9180 HPKE, KEMs incl. MLKEM768X25519, MLKEM1024, MLKEM1024P384; AEADs AES-GCM, ChaCha20 | no claim | yes | n/a | pre-1.0; one-shot, no chunking; age depends on it |
| Go stdlib (`go 1.27.0`) | 1.27.3, 2026-10-19 | BSD-3 | | AES-GCM, HKDF, SHA-2/3, `crypto/mlkem`, `crypto/hpke` (1.26) | **CMVP #5247** (Go Cryptographic Module v1.0.0, validated 2026-04-27, Level 1); v1.26.0 in coordination; `GOFIPS140=certified`/`inprocess` | ML-KEM-768/1024 approved on the certificate | n/a | X25519 and ChaCha20-Poly1305 are refused in `fips140=only`; no streaming AEAD, so using it means framing chunks myself, which rule 3 forbids |
| `golang.org/x/crypto` (in tree) | v0.58.0, 2026-10-09 | BSD-3 | | chacha20poly1305 (+X), argon2, nacl/secretbox | not in the module | no | n/a | primitives, no streaming format |
| Tink Go `github.com/tink-crypto/tink-go/v2` | v2.9.0, 2026-10-15 | Apache-2.0 | 1.25 | streaming AEAD `AES256_GCM_HKDF_4KB/1MB`, `AES256_CTR_HMAC_SHA256`; keyset with primary and key-id prefix; **no XChaCha20 streaming template in Go** | primitives are approved; Tink Go itself claims nothing (FIPS mode is C++ only) | symmetric only, so nothing to break | `tinkey` (JVM) or a Go program | protobuf becomes a direct dependency; breaking changes about every two months; v2.8.0 fixed a silent-truncation bug in the streaming reader; seekable decrypt built in |
| `github.com/minio/sio` (DARE 2.0) | v0.5.1, 2026-04-29 | Apache-2.0 | 1.24 | 64 KiB packages, AES-256-GCM or ChaCha20, final-package bit | no claim | no | none | raw 32-byte key, no key id, no wrapping; quiet project |
| `github.com/secure-io/sio-go` | v0.3.1, 2020 | MIT | | | | | | stale, out |
| kopia / restic internals | | | | per-block, not reusable as a stream library | | | | out |

Standards: CNSA 2.0 is AES-256, SHA-384/512, ML-KEM-1024, ML-DSA-87 (secondary sources; NSA PDFs were
not reachable). FIPS 203 final 2024-08-13. draft-ietf-hpke-pq-05 and X-Wing are Internet-Drafts, not RFCs.
SP 800-38D's GCM bound (2^32 invocations per key with random IVs) is why no suite here puts one GCM
nonce over one artifact; both streaming constructions derive per-segment keys or nonces inside the library.
Licence gate: `distribution/packaging/compliance.json` allows MIT, BSD-2, BSD-3, Apache-2.0, CC0-1.0 and
names two MPL-2.0 modules. Every candidate above passes it.

## The objections I weighed

I argued the design from seven angles and let the skeptic's objections shape it.

- **Applied cryptography.** The age file key is 16 bytes. Whatever the payload cipher's key length, every
  age suite is a 128-bit security level construction, and an ML-KEM-768 recipient is NIST level 3, not the
  level 5 CNSA 2.0 asks for. "AES-256" and "256-bit security" are claims only a symmetric AES-256 suite can
  make. Mixing PQ and non-PQ recipients in one file is discouraged by the spec, so a suite is one recipient
  type, not a list.
- **Go engineer.** `keycustody.Format` is already the interface; it lacks `Seal` on purpose. Adding a
  registry around it and a `Seal` in `copycrypt` (not in `keycustody`) keeps the "custody never encrypts"
  rule. A new suite is one file implementing one interface plus a row in a table that a test pins.
- **Security and threat model.** The off-site threat is the bucket holder with time: harvest now, decrypt
  later. That is a key-wrapping problem, which the PQ recipient addresses and a symmetric-only suite
  side-steps entirely. The local threat (whoever owns the host) is unchanged by any suite, because the
  identity lives on the host for decrypt-verify. Downgrade must be refused at the policy, at the key, at the
  copy record and at the bundle.
- **Storage architect.** Format id per copy already exists. What does not exist is a suite per key, and
  one-active-managed-key is deployment-wide. The additive change is a key side column (or side table) and a
  per-suite active-key rule. Re-encryption in place is blocked by one-placement-per-artifact-per-medium; the
  medium-to-medium path exists.
- **Performance.** The PQ recipient adds about 1.2 KB per copy and one ML-KEM operation per file; the
  payload cost is unchanged. Tink's 1 MiB segment is a 1 MiB buffer. ChaCha20 stays the right payload
  cipher on NAS-class ARM without AES-NI, which ADR 0027 already argued.
- **UX and operations.** Every suite change is a key change, and every new managed key is a new bundle to
  export and confirm. A suite select that silently mints keys creates unconfirmed bundles, so the flow is
  "choose suite, rotate, export, confirm" with the degraded health state in between. The select's tooltip
  has to state the effect from the code, per the checklist, so each suite carries a `Describe()`.
- **Skeptic: keep age v1 and add one extra suite, change nothing else.** Objections, and what they changed:
  1. "A registry for two suites is ceremony." Accepted in part: the registry is a compile-time table and
     an interface that already exists, not a plugin system. No dynamic loading, no `algorithm: auto`.
  2. "Per-set suite means per-set keys and per-set bundles." Accepted: per-set suite is deferred, the suite
     is global, and the per-suite active-key rule is the only schema change.
  3. "Tink drags protobuf into the graph for a suite nobody asked for." Accepted: Tink is not in phase 1.
     It is the one suite that can honestly carry "AES-256" and a FIPS-primitives story, so it is a named
     owner decision, not a default.
  4. "Encrypt-at-ingest buys nothing while the local copy is plaintext by definition." Accepted. See below.
  5. "Deprecation machinery before there is anything to deprecate." Partly accepted: the status vocabulary
     and the write-side refusal land with the registry (they are three constants and one check); the
     re-encryption verb reuses #1291's machinery; nothing else.

## Decisions

### D1. A suite is a recorded, enumerated property of a copy, and `format_id` is the suite id

The existing `placement_encryption.format_id` **is** the suite id. `age/v1` keeps its meaning exactly: age
v1 container, one X25519 recipient, ChaCha20-Poly1305 payload. A value once written is never renamed
(EPIC N §11 compat surface). New suites get new ids. `format_version` stays the container version and stays
1 for every suite introduced here.

Suite ids and their status in the first build that ships this:

| Suite id | Library construction | Status | Key material | Reader outside retnd |
|---|---|---|---|---|
| `age/v1` | age v1, X25519 recipient, ChaCha20-Poly1305 STREAM, HKDF-SHA-256 | supported | `AGE-SECRET-KEY-1...` | `age -d -i key.txt` (any age) |
| `age-pq/v1` | age v1, `mlkem768x25519` hybrid recipient (ML-KEM-768 + X25519 via HPKE), same payload | **default** | `AGE-SECRET-KEY-PQ-1...` | `age -d -i key.txt` (age 1.3+) |
| `tink-aes256gcm-hkdf-1mb/v1` | Tink streaming AEAD, AES-256-GCM, HKDF-SHA-256, 1 MiB segments, keyset with a 256-bit primary | **not shipped unless the owner decides D2(b)**; listed so the id is reserved | cleartext Tink keyset JSON, 0600 | `tinkey` or `retnd decrypt` |

The id is not a knob name; it names bytes. A copy's bytes are readable by anyone holding its key and a
reader for that id, with no retnd catalog, which is C5 of ADR 0027 kept.

### D2. The default, and what "military-grade" means here

"Military-grade" is a marketing phrase. It has no standard behind it. The owner asked for a default that
deserves the word, so I define it as a short list of checkable properties and say which the default meets:

| Property | `age-pq/v1` (recommended default) | `tink-aes256gcm-hkdf-1mb/v1` (alternative) |
|---|---|---|
| Authenticated encryption, per chunk, with position and termination bound into every tag | yes (age STREAM, final-chunk flag) | yes (segment counter, last-segment flag) |
| Payload cipher key length | 256-bit ChaCha20 key, derived from a **128-bit** file key | 256-bit AES-GCM key |
| Security level (SP 800-57 terms) | 128-bit | 256-bit |
| Key establishment survives a quantum adversary recording ciphertext today | yes: ML-KEM-768 + X25519 hybrid | yes: there is no key establishment, the key is symmetric |
| CNSA 2.0 symmetric parameters (AES-256) | no | yes (HKDF-SHA-256 is below CNSA 2.0's SHA-384) |
| FIPS 140-3 validated module available for the primitives | no (ChaCha20 and X25519 are not approved) | yes for the primitives under `GOFIPS140`; Tink Go claims nothing itself |
| Recoverable on a clean machine with a stock tool | yes, `age` 1.3+ | `tinkey` (JVM) or a small Go program; `retnd decrypt` |
| New modules in `core/go.mod` | none | `tink-go/v2` direct, `protobuf` becomes direct |
| Per-copy overhead | about 1.4 KB header + 16 B per 64 KiB | about 50 B header + 16 B per 1 MiB |

**Recommendation: `age-pq/v1` is the default.** It addresses the one threat the off-site copy actually faces
over a ten-year horizon (a bucket holder who waits for a quantum computer), it adds no module, it keeps the
clean-machine recovery story byte for byte (`age -d`), and it is the smallest possible change on top of
EPIC N: a recipient type. What it is not, in writing on every surface that uses the word: it is not AES, it
is not FIPS-validated, and its security level is 128-bit because age's file key is 16 bytes.

**Owner decision D2(b):** if the product must print "AES-256" or sell to a FIPS-shaped buyer, the Tink suite
is the only candidate that can carry it without designing a container, and it should then be the default
with `age-pq/v1` the second suite. The cost is protobuf in the direct graph, a JVM or Go program for
recovery outside retnd, Tink's two-monthly breaking releases, and a symmetric key file that is the whole
secret (no public half to publish). I would not pay that for the word alone, but it is the owner's word.

Why not "AES-256-GCM from the standard library with my own chunking": that is designing a streaming
construction (segment nonces, final-segment binding, header authentication), which is rule 3 and ADR
0027 C9. The same goes for wrapping a Tink key under an HPKE recipient to get a public half: the envelope
that carries the wrapped key beside the ciphertext would be a format of this project's own.

### D3. A key belongs to a suite; the active managed key is per suite

Migration 0020 (additive): `ALTER TABLE encryption_keys ADD COLUMN suite_id TEXT NOT NULL DEFAULT
'age/v1'`, drop `idx_encryption_keys_one_active_managed`, create it again over `(provider, suite_id)` with
the same partial predicate. Every existing key reads as `age/v1`, which is what it is. `KeyRecord` gains
`SuiteID`; the bundle's `format` field already holds it; `Bundle.Decrypt` already refuses a mismatch.

The rule "one active managed key" becomes "one active managed key **per suite**". New copies are written
under the active key of the **policy's** suite (D4), never under "whatever key is active". A rotation
within a suite supersedes within that suite. Changing the suite is a rotation to a new suite: the old
suite's active key becomes superseded, so there is still exactly one key new copies go under.

Open check for the implementer: whether compat cell `03-migrated-schema` treats an index drop as additive.
If it does not, the alternative is a side table `encryption_key_suites(key_id PRIMARY KEY, suite_id)` with
absence meaning `age/v1`, and the per-suite active rule enforced in `state` on the write path, the way
`registry.go` already enforces the status vocabulary.

**Outcome (#1312): the side table.** The cell is additive-only over `sqlite_master`, and both `ADD COLUMN`
(which rewrites the stored `CREATE TABLE` text) and the index drop change a line an existing deployment has.
Migration `0020` creates `encryption_key_suites` and nothing else; `state` writes a key's suite in the key's
own transaction and refuses a suite the registry does not know. 0015's index stays, so the rule is one
active managed key deployment-wide, stricter than one per suite and exactly what the rotation flow above
produces.

### D4. One global `suite` policy field; fail closed; no auto-rotation

```yaml
retained_copy_encryption:
  mode: required
  suite: default            # default | age/v1 | age-pq/v1   (default: see below)
  key_provider:
    managed: true
```

- `default` (or writing nothing, which is the same) **keeps the deployment on the suite it already uses**:
  it resolves to the suite of the deployment's active managed key when there is one, and to the build's
  default suite only when there is no active managed key. So an EPIC N deployment upgrading with an active
  `age/v1` key stays on `age/v1`, nothing is refused and behaviour is byte-identical, and a fresh deployment
  mints its first key under `age-pq/v1`. Moving an existing deployment to `age-pq/v1` is an explicit
  `suite: age-pq/v1` plus `retnd encryption key rotate --suite age-pq/v1`. (Owner decision, 2026-11-10,
  replacing the first draft's "default always means the build default, and a mismatch is refused until the
  operator rotates", which would have stopped every upgraded deployment's copies on the first cycle.) A
  build that changes its default announces it in the CHANGELOG, and the first cycle under the new build
  emits one activity event naming the old and the new default. It never mints a key by itself.
- A pinned id is honoured exactly. `Validate` refuses an id this build does not know and an id whose
  status is retired, and warns (load succeeds, health degrades) on a deprecated one.
- **Mismatch is a refusal, not a downgrade.** If the resolved suite has no active managed key (the operator
  pinned a suite and has not rotated into it), the engine refuses the copy
  before any destructive step, exactly as `required` with an unresolvable key does today, with the sentence
  "no active key for suite X; run `retnd encryption key rotate --suite X`". The local copy is untouched and
  the source is not deleted, because remote deletion needs only the local copy (verified above). Health
  shows it; the alert pass raises the existing unresolvable-key kind with this reason (#1294's wiring).
- Per-set `suite` is **not** offered in this epic. The reasons are the skeptic's: a per-set suite is a
  per-set key and a per-set bundle to confirm, which is the custody burden EPIC N spent an epic avoiding,
  and NC-19's "effect the code can state" test has no good sentence for "this set uses a weaker suite". The
  resolved field exists on `BackupSet` (inherit only) so a later override is a vocabulary edit, not a
  model change.
- `KnownFields(true)` makes `suite:` a one-way door for configs that write it. The CHANGELOG names the
  version. Writing nothing stays byte-identical to EPIC N.

### D5. The registry, the interface, and how a suite is added

Package `core/internal/copycrypt` grows a compile-time table and an interface:

```go
type Status string // "supported", "deprecated", "retired"

type Suite interface {
    // keycustody.Format's four methods, declared here rather than embedded
    ID() string
    Generate() (secret []byte, recipient string, err error)
    Recipient(secret []byte) (string, error)
    Open(secret []byte, ciphertext io.Reader) (io.Reader, error)

    Version() int                           // format_version
    Status() Status
    Describe() Description                  // the sentence the UI tooltip and `retnd encryption suites` print
    Seal(dst io.Writer, recipient string) (io.WriteCloser, error)
}

func Suites() []Suite                 // ordered, the whole table
func Default() Suite                  // exactly one, status supported; a test pins it
func Lookup(id string) (Suite, bool)
```

The four `Format` methods are declared, not embedded, because `keycustody` already imports
packages that must not import it back, and #1312 makes `keycustody.AgeFormat` an alias of the
`copycrypt` adapter: an embedding would make `copycrypt` import `keycustody` and the alias would
close the cycle. `copycrypt` stays a leaf (age and the standard library), and `keycustody`
asserts at compile time that every `copycrypt.Suite` is a `Format`.

`keycustody` keeps `Format` (no Seal) and is handed a `Suite` by the caller, but only ever calls its
`Format` half, so "custody never encrypts" holds by what custody calls. `EncryptToStaging` takes a `Suite` and a recipient; `DecryptVerify` and `DecryptToFile` take the
suite named by the copy's record (journal or sidecar), never guessed from the policy. `Staged` carries
`SuiteID` and `Version`, and `verifyEncryptedContent` writes them.

Adding a suite is one pull request: one file implementing `Suite` over a library, one row in the table, and
the shared conformance vectors going green for it, **red first**. The vectors live once, in
`copycrypt/suitetest`, and run for every registered suite:

1. round trip of fixtures that cross a chunk boundary and end in a short final chunk, none all zero;
2. the five negatives: bit flip, truncated tail, reordered chunk, swapped header, wrong key, each failing
   closed with no plaintext byte released (a writer that records any write before the error fails the test);
3. a key of another suite is refused by `Open` and by `Recipient`;
4. two encryptions of one plaintext under one recipient differ in their headers (no keystream reuse);
5. `stored size > plaintext size` for the empty, one-byte and boundary fixtures;
6. a multi-GiB fixture through `Seal` and through `Open` inside the allocation budget, in the shape of
   `core/internal/backupengine/source/adapter_test.go:1055`, with the failure message naming the defect;
7. the ids: stable string, version at least 1, exactly one default, the default is `supported`, no two
   suites share an id, and the table is pinned against a golden list so a status change is a deliberate edit;
8. for age suites, a fixture file decrypts with the stock `age` CLI pinned in `scripts/ci/pins.env`, so the
   recovery claim is tested and not asserted (skipped, not passed, when the binary is absent). That pin
   does not exist on `main` today; #1313 adds it.

### D6. Replacing a suite: deprecate, keep reading, re-encrypt, retire

| Status | New copies | Decrypt-verify, recover, `decrypt`, move of ciphertext | Config `suite: <id>` | Health |
|---|---|---|---|---|
| supported | yes | yes | accepted | quiet |
| deprecated | yes, and health says so | yes | accepted with a warning at load | degraded: "N active copies and the active key are under a deprecated suite; retirement is announced for version V" |
| retired | **refused** | yes | **refused** at load | degraded while any active copy records it, with the count |
| removed | never in this epic | | | a removal is a major version and needs the count at zero |

Re-encryption under the active suite and key generalises #1291's `EncryptExisting`: the source is an
encrypted copy under the old suite, streamed through `Open` straight into `Seal` (no plaintext on disk),
to **another medium**, source retired only after decrypt-verify, opt-in per move. A dry run lists copies by
suite. **Same-medium rewrite is not offered** for the reason retroencrypt.go gives (one placement per
artifact per medium, object key derived from the artifact); offering it would mean a move whose source and
destination are one row, with an overwrite window the scrub would misread. That is a Later item with its
own crash matrix. When a plaintext local copy still exists, the ordinary path already writes the new copy
under the active suite on the next move, so the common case needs no verb at all: the verb is for copies
whose local copy is gone.

### D7. Downgrade protection, the whole list

1. Policy: an unknown or retired id is refused at `Load`; `default` cannot name a deprecated suite (the pin
   test). No `auto`.
2. Key: a copy is written only under the active key **of the policy's suite**; a mismatch refuses, never
   substitutes.
3. Record: suite id and version are written by the committing attempt only, from the registry's constants,
   never from config text; `state` refuses an id the registry does not know.
4. Read: the suite used to open a copy is the one its record names; `Open` with another suite's key fails
   by construction (test 3 above), and the age header's stanza type is checked against the record for
   the two age suites.
5. Bundle and import: `Bundle.Decrypt` refuses a format mismatch (exists); key import (#1289, merged as #1300) refuses a bundle
   naming a suite this build does not know, and accepts a retired suite's bundle read-only.
6. Sidecar and rebuild: `catalog rebuild` records a copy under an unknown suite as "unknown suite", never as
   plaintext and never as verified.
7. Surfaces: the `Describe()` sentence is the only text about a suite, so no surface can say more than the
   code does.

### D8. The DR bundle and the decrypt verb

The bundle container stays `retnd-key-bundle/1`; `format` already holds the suite id and the instructions
array becomes per suite (both age suites print the same `age -d -i` line; a Tink suite would print the
`retnd decrypt` line and the `tinkey` alternative). `retnd encryption decrypt` (#1289, merged as #1300) takes the suite from the
sidecar or the journal and refuses to guess when neither is present and the bytes do not self-identify.
Key import checks the suite as in D7.5.

### D9. Where encryption can enter, relative to first sight

Today: first sight is the listing; the bytes land as plaintext in `<local_path>/<name>.partial` through
rclone; they are read back, hashed, compared, validated, committed as a plaintext local copy; then in the
same cycle the move engine encrypts the local file into staging and uploads. The plaintext local copy is
permanent by product definition (ADR 0027 rejected option (c) "outright"; `docs/encrypted-copies.md`:
"Your local copy stays plaintext").

Three places encryption could enter earlier, and what each costs:

**E1. Encrypt in the same pass as the pull.** Open the remote object as a stream (the kopia path's
`OpenSourceStream` shape) and `io.MultiWriter(partial, sha256, Seal)` so the ciphertext for the medium is
staged in the same network pass. It saves one disk read (the encrypting pass over the local file) and has
ciphertext ready at commit. It costs: replacing rclone's `operations.Copy` with a hand-driven stream on the
one path that guards production backups (retries, partials, server-side hashing); either losing the
read-back-from-disk verification (`readAndHashLocal` reads what the disk holds, not what the kernel was
handed) or keeping it and saving nothing; encrypting bytes that have not yet been compared to the remote
hash, so a quarantined artifact leaves ciphertext to discard; a staged ciphertext that must survive from
transfer to the move phase, which `sweepStagingTemps` would otherwise remove on restart, so a new journaled
fact; ciphertext-sized free space at ingest instead of at move. And it changes nothing about
confidentiality, because the plaintext still lands and stays. **Not in this epic.**

**E2. A ciphertext-only local copy** (`local_copy: ciphertext` per set, the real gain: no plaintext at rest
on retnd's disk, plaintext only in memory during the pass). This reverses ADR 0027 (c) and touches every
reader in the list above: verify's read-back becomes a decrypt-verify; validators and the structure check
need a decrypting reader or a temporary plaintext under the recovery staging rules; the scrub, the
pre-delete gate and the move engine's read-back become decrypts (so the **key is now required to delete a
source**, and losing the key loses the only copy, not just the off-site one); the recovery download's
`Seek`-based range reader needs `age.DecryptReaderAt` (exists in 1.3) or Tink's seekable reader; the
sidecar's checksum semantics split in two as the medium copy's did; the local disk needs ciphertext-sized
space. The host still holds the identity, so this protects against an offline disk or a share read, not a
compromised host. It is a product definition change the owner has to make, and at nine readers it is an
epic of its own, not a sub-issue. **Later; decision D10.**

**E3. Before retnd sees it.** The producer encrypts (the ADR 0003 model) and retnd stores opaque bytes with
no key on the host at all. This is the only option that is earlier than first sight, it exists today, and
it costs the producer the disk and the key custody. The docs should name it beside E2 as the answer for
"the archives are credentials".

**Where encryption enters in this epic: unchanged.** The move engine, same cycle as the commit, after the
plaintext local copy is verified. That is the earliest point that does not change what retnd promises
about the local copy.

### D11. Why ADR 0027 was not amended in #1279

**It was followed up, as this ADR, on top of the merged stack.** The reasons, as they stood when the
choice was made:

- Nothing in phase 1 needs a change to 0027's tables or names: `format_id` is free text and `age/v1` keeps
  its meaning; the only schema change (a key's suite) is a new additive migration.
- #1279 had eight pull requests stacked on it (#1280 through #1288 and the follow-ups). Rewriting a
  decision record under them means re-reading every dependent sentence (`docs/encrypted-copies.md`, the
  sidecar comment, the bundle comment, the matrix) in a stack the owner wanted merged in hours.
- The one sentence 0027 gets wrong ("never a config field") is superseded by a one-line note in 0027's
  Consequences, added by T2.5 (#1320), which is how this repository has handled 0007's frozen comment.
- Amending now would be cheaper only if the suite id vocabulary or the key table shape had to change under
  EPIC N's own tests. They do not.

What T2.5 has to touch in EPIC N's text: ADR 0027 Consequences (one line), `docs/encrypted-copies.md`
("none is planned"), `recovery/manifest.go` comment ("age/v1" and 1), the bundle instructions, the
CHANGELOG's one-way-door line.

## Owner decisions, and the answers on record

The owner had not answered any of these in the tracker when this ADR was filed. #1310 as filed
builds on the recommendation in every row, so the "answer" column says what the epic implements and
that it stands until the owner says otherwise. A different answer to D2 changes T1.3's content (the
Tink suite becomes the default and `age-pq/v1` moves to Later); a different answer to any other row
changes scope, not code already written.

| # | Decision | My recommendation | Answer on record |
|---|---|---|---|
| D2 | Default suite: `age-pq/v1` (PQ-hybrid, 128-bit level, no new module, `age` CLI recovery) or the Tink AES-256 suite (256-bit, FIPS-shaped primitives, protobuf, JVM or Go reader) | `age-pq/v1`; Tink only if the product must print "AES-256" | not answered; the epic implements `age-pq/v1` |
| D2' | Whether to ship the Tink suite at all in this epic | no; reserve the id, decide after phase 1 | not answered; not shipped, id reserved |
| D3 | Index change vs side table for the key's suite, pending the compat cell check | index change if the cell allows it | an implementation call, made and stated in #1312's pull request |
| D4 | Per-set `suite` override | not in this epic | not answered; not in this epic |
| D6 | Same-medium re-encryption | Later, with its own crash matrix | not answered; Later |
| D10 | Ciphertext-only local copy (`local_copy: ciphertext`) as a product definition | a separate epic, after this one, only if the owner wants the key to be required for source deletion | not answered; out of this epic |
| D11 | Amend 0027 now or follow up | follow up | settled by events: EPIC N merged unamended, so this ADR is the follow-up |
| D12 | Whether the word "military-grade" appears on any surface | no; print the `Describe()` sentence and the security level | not answered; it appears on no surface and a test pins that |

## Consequences

- The surfaces that today say "age v1" say the copy's suite id, and the Settings card gets a select with
  one tooltip sentence per suite, stated from `Describe()`.
- Every suite change is a key rotation and therefore a bundle export and confirmation. The health state
  that already exists carries that.
- A deployment that writes nothing and already has an active `age/v1` key is byte-identical to EPIC N: it
  stays on `age/v1` until the operator pins `age-pq/v1` and rotates. A deployment with no active key mints
  its first key under `age-pq/v1`, which changes the recipient string length (about 2000 characters) and the
  header size (about 1.4 KB). Fixtures that assume a 200-byte header move.
- `age/v1` copies stay readable for as long as the age library reads X25519 stanzas, which is forever by
  the age spec's own compatibility promise.
- `GOFIPS140` builds are not promised by this ADR. If D2(b) is taken, a separate ADR covers the build mode,
  the module version (#5247 is v1.0.0; v1.26.0 is in coordination) and what `fips140=only` refuses.

## What this ADR does not decide

The ciphertext-only local copy (D10). Per-set suites (D4). Same-medium re-encryption (D6). Any KMS or
Vault provider. Compression. Object-name confidentiality. A FIPS build mode.

# ADR 0027: The streaming AEAD format for retained copies, the Go implementation that writes it, and where encryption sits in the upload path

- Status: accepted for the format, the library, the upload seam, the chunking, the two-digest
  model and the configuration shape. It **depends on the code** in EPIC N Phase 1 (#990 for
  key custody, #991 for the byte path, #992 for lifecycle and rotation). This ADR is the first
  commit of #989 on purpose: the issue asks for the cryptography to be decided before any is
  written, and the configuration model and migration 0015 that follow it in the same pull
  request read their names from here. Nothing in this pull request encrypts anything.
- Date: 2026-11-09
- Scope: EPIC N (#967), FR-59 and FR-60. Recorded by N1.1 (#989).

## Context

An operator who sends retained copies to a bucket they do not own has no way to make them
unreadable to whoever holds the bucket. The artifact engine writes the producer's bytes
verbatim, and the only encryption this tree has ever written is the whole-buffer envelope in
`core/internal/transport/rclone/keyencryption.go`: AES-256-GCM under `crypto/cipher`'s
`NewGCM`, `encryptedKeyMagicV1`/`V2` as the version, a 16-byte salt and a 12-byte nonce in V2,
an Argon2id-derived key, and the plaintext held in memory from read to `Seal`. That file's own
justification is that #298 asked for "AES-GCM or equivalent", "specifically so this project
does not take on a new crypto dependency for one feature".

That envelope is the right answer for what it protects and the wrong one for what this epic
protects. The distance between the two is why this ADR comes before a line of code:

1. **It buffers the whole message.** `encryptKeyMaterial` and its reader take and return
   `[]byte`. An SSH key is a few kilobytes; a retained copy is a multi-gigabyte file and on
   some deployments a multi-terabyte one. Streaming is the whole claim of FR-61 and FR-62.
2. **One nonce over one message is outside AES-GCM's safe limits at that size.** GCM's block
   counter is 32 bits, so a single `Seal` cannot exceed about 64 GiB, and the forgery bound
   degrades with message length well before that. The key envelope never gets near either
   limit. An artifact does.
3. **One tag over a multi-TB message authenticates nothing until the last byte.** A streaming
   decrypt would have to buffer the artifact (back to point 1) or release unauthenticated
   bytes, which FR-62 forbids.
4. **It is a bespoke container.** A magic string, a salt, a nonce and a tag is a format this
   project designed. That was fine for a key file with one reader. It is not something to
   extend to a format a clean-machine recovery procedure has to read without this binary.

So `keyencryption.go` is **cited as the precedent for key material and ruled out for artifact
material**. The pressure in its justification ("no new crypto dependency for one feature")
argues the other way here: the alternative to a dependency is designing a chunked AEAD
construction in this tree, and the epic's hard rule is that nobody here does that.

The criteria, named so the tables below can be read against them:

| # | Criterion | Why it is on the list |
|---|---|---|
| C1 | Streams without buffering the artifact, in both directions | FR-61/62; the bounded-memory assertion #991 owes (NC-11) |
| C2 | Authenticated header | A swapped header must fail closed, and the format id and version must be readable from the bytes |
| C3 | Deterministic version detection with randomised encryption | Two copies of one artifact must never share a keystream, and the reader must still know which format it holds |
| C4 | Truncation, reordering and tamper detection, with no partial plaintext released | FR-62's five negatives: bit flip, truncated tail, reordered chunk, swapped header, wrong key |
| C5 | Recovery tooling usable outside a running deployment | A procedure that needs this binary and its catalog to learn which key a copy needs is not a recovery procedure |
| C6 | A re-wrapping story for rotation | FR-65: a new active key for new copies, every historical copy still readable through its recorded key id |
| C7 | Binary size and memory impact | Distroless image, NAS-class hosts |
| C8 | Licence compatibility and maintenance | The module graph, `NOTICE` and `provenance/third-party-licenses.json`; a library nobody has touched in years is a liability on a format people need to read in ten |
| C9 | Established primitives only, nothing designed here | The epic's hard rule; also C5, since a bespoke container has no second reader |

## The candidates

Six were looked at. Versions and dates are what the Go module proxy reported on 2026-11-09.

| Candidate | Latest | Licence | Construction | Notes |
|---|---|---|---|---|
| **age** (`filippo.io/age`) | v1.3.2, 2026-08-29 | BSD-3-Clause | STREAM over ChaCha20-Poly1305, 64 KiB chunks, per-chunk nonce is an 11-byte counter plus a final-chunk byte; HKDF-SHA-256 payload key from a per-file 16-byte nonce; HMAC-SHA-256 over the header; X25519 or scrypt recipients, and since 1.3.0 an ML-KEM-768 + X25519 hybrid recipient | Specified at `https://c2sp.org/age`, with CCTV test vectors; the `age` CLI and independent implementations read the same bytes; v1.2.1 was a security release (GHSA-32gq-x56h-299c, execution of an unintended binary) and v1.3.2 is a hardening release |
| **Tink streaming AEAD** (`github.com/tink-crypto/tink-go/v2`) | v2.9.0, 2026-10-15 | Apache-2.0 | AES-GCM-HKDF segments, configurable segment size, segment index and last-segment flag in the nonce, random access | Keysets are protobuf and the management tool (`tinkey`) is Java; pulls `google.golang.org/protobuf` into the direct graph plus a keyset layer this product has no other use for |
| **DARE 2.0** (`github.com/minio/sio`) | v0.5.1, 2026-04-29 | Apache-2.0 | 64 KiB packages, AES-256-GCM or ChaCha20-Poly1305, sequence number in the package header, final-package flag | Raw 32-byte key: no key wrapping and no key id in the stream; no reader outside Go |
| **rclone crypt** (`github.com/rclone/rclone/backend/crypt`) | v1.76.0 (v1.75.2 already in this graph) | MIT | NaCl secretbox, 64 KiB blocks, 24-byte nonce incremented per block, `RCLONE\x00\x00` magic | **No final-block marker**: a file truncated on a block boundary decrypts cleanly to a shorter plaintext. The cipher is derived from an rclone password and salt by rclone's own scrypt step, not from a key this product holds, and its constructor is unexported behind rclone's `configmap` |
| **AWS Encryption SDK for Go** (`github.com/aws/aws-encryption-sdk/releases/go/encryption-sdk`) | v0.4.0, 2026-05-06 | Apache-2.0 | Framed message format with key commitment | Pre-1.0 API, KMS-shaped keyrings |
| **miscreant** (`github.com/miscreant/miscreant.go`) | pseudo-version, 2020-02-14 | MIT | AES-SIV STREAM | No release in six years |

Scored:

| | C1 stream | C2 header | C3 version | C4 negatives | C5 recovery | C6 rotation | C7 size | C8 licence/maint | C9 no design |
|---|---|---|---|---|---|---|---|---|---|
| age | yes | HMAC | `age-encryption.org/v1` literal, fresh file key per encryption | all five, final-chunk flag in the nonce | **`age` CLI on any machine** | header stanza per recipient, payload untouched | small; `x/crypto` already present | BSD-3, active | yes |
| Tink | yes | length-prefixed, HKDF salt | keyset id in the prefix | all five, last-segment flag | `tinkey` (JVM) or Go | keyset rotation, primary key id | protobuf plus keyset layer | Apache-2.0, active | yes |
| sio | yes | per-package header | version byte | all five, final flag | **none** outside a Go program | **none in the format**: raw key, no id | small | Apache-2.0, quiet | yes |
| rclone crypt | yes | magic and nonce, unauthenticated | magic | **truncation at a block boundary is silent** | `rclone` CLI with a password | none; password derived | zero (linked already) | MIT | wrong primitive for this use |
| AWS ESDK | yes | authenticated | version byte | all five | separate CLI | keyrings | large | Apache-2.0, pre-1.0 | yes |
| miscreant | yes | | | | none | none | small | unmaintained | yes |

Rows for Tink, sio and the AWS SDK describe the published format as I understand it from its
documentation. I did not run them. age is the one #991 will exercise, and the five negatives
are asserted there, not here.

## Choosing among the three that survived

**age**, **Tink streaming AEAD** and **DARE via minio/sio** survived. rclone crypt is out on C4,
the AWS SDK on C7 and its pre-1.0 API, miscreant on C8. The four lenses this repository reviews
by were applied to the three.

**Architecture.** The format has to outlive this product's catalog, because the recovery story
is "the bundle, the shipped binary and a page of instructions", and ideally not even the
binary. age is a published, independently implemented format with its own CLI. sio has no
reader outside Go and no key id in the stream, so every piece of "which key" bookkeeping would
be this product's own, which is a container by another name. Tink's keyset layer is a second
custody model beside `secretref`, and the epic cut a provider interface for exactly that
reason. age.

**Go systems.** All three stream in bounded memory. age's `Encrypt` returns an `io.WriteCloser`
and `Decrypt` an `io.Reader`, the shape `io.MultiWriter(file, digest)` already composes with
(`core/internal/backupengine/kopia/localrestore.go:481`). Tink brings `protobuf` into the
direct graph for a feature that needs none of it. sio has the leanest API and would be the pick
if C5 and C6 did not exist. ChaCha20-Poly1305 is the right default on hosts without AES-NI,
which NAS-class ARM boxes often are. age, with sio as the runner-up on API shape.

**Safety.** The hard negative is truncation, and it is why rclone crypt lost. age binds a
final-chunk flag into the nonce and its specification says reaching EOF without a final chunk
is an error. The header is MAC'd under a key derived from the file key, so a swapped header
fails at the header, before any payload is touched. The scrypt recipient is not used: the
managed key is an X25519 identity from a CSPRNG, the recipient type with no passphrase and no
work factor to get wrong, which is also what the acceptance criterion about "no cipher, nonce
length, chunk size or KDF-cost value" asks for. One risk to write down: age releases plaintext
per authenticated chunk, so a consumer that streams decrypted bytes to a destination before EOF
has written authenticated-but-possibly-truncated data. #991 therefore decrypt-verifies into a
digest and never into a file, and any export writes to a temporary name and renames only after
the final chunk authenticated. age.

**API.** The operator-facing surface is `mode` and `key_provider`. For age the external secret
is one line (`AGE-SECRET-KEY-1...`), which fits every `secretref` resolver and its size bound
without a second parser. The public half, the recipient, is non-secret and goes in the key
registry, so "which key does this copy need" is answerable from the journal and the sidecar
without touching material (FR-64). For sio all of that plumbing would be invented here. age.

## Decision

1. **The format is age v1** as specified at `https://c2sp.org/age`, written and read through
   **`filippo.io/age` v1.3.x** (BSD-3-Clause). Each stored copy records `format_id = "age/v1"`
   and `format_version = 1` in `placement_encryption` (migration 0015). The format is a
   property of the build, never a config field, and a second format is a new id and version
   per copy, not a knob.
2. **A key is an X25519 identity.** A managed key is generated with
   `age.GenerateX25519Identity` and held as a 0600 file in a 0700 directory under the custody
   rule `secretref.checkSecretFileCustody` enforces (#990). An external key is the same
   identity string behind a `secretref`-shaped `file`/`env`/`command` reference and nothing
   else. The recipient (public half) is stored in `encryption_keys.recipient` and is the only
   key-related string that reaches a surface. No scrypt recipient.
3. **Nothing cryptographic is designed here.** No envelope, no KDF, no nonce scheme. The one
   place a wrapper was tempting, carrying the key id beside the ciphertext, is not needed: the
   key id lives in the journal and in the recovery sidecar (#992), and age's header already
   binds the recipient the copy was written for.
4. **`key_encryption:` is untouched.** `config.KeyEncryption` (#298) protects secret material
   this manager persists, and `TestRefFieldSetMatchesTheConfiguredOnes` pins the shape of the
   references it shares. The new policy is one new top-level key, `retained_copy_encryption:`,
   with `mode` (`off | required`) and `key_provider` (`managed: true` or an `external`
   reference), and a per-set override of `mode` (`inherit | off | required`) and `key_provider`
   in the three-field pattern `RetentionConfig` and `AssuranceConfig` use. The per-set answer
   to rotation cadence, format and chunk size is no, in writing, and `KnownFields(true)` is
   what refuses them. Two key concepts under one name in a file read with `KnownFields(true)`
   would be unrecoverable, which is why the existing name is left alone.
5. **The dependency lands with the first encryption code (#991), not with this pull request.**
   See "Licence, toolchain and the module graph" for exactly what comes in.

## Chunking

The chunk size is **64 KiB of plaintext, fixed by the age specification**. It is not a
configuration value, not a per-set value and not a constant this repository defines: the
library owns it, and an operator cannot get it wrong because there is nothing to set.

- Each chunk is sealed under ChaCha20-Poly1305 with a 12-byte nonce made of an 11-byte
  big-endian counter and one final-chunk byte (`0x01` on the last chunk only). That binds
  position and termination into every tag, which is what makes reordering and truncation
  detectable. The payload key is derived once per file from the file key and a fresh 16-byte
  nonce, so every encryption of the same artifact has its own keystream (C3).
- The last chunk may be short, and an empty plaintext is one empty final chunk. A stream with
  no final-flagged chunk, or with bytes after it, is an error in the reader.
- Overhead for an artifact of `n` plaintext bytes is the header (a couple of hundred bytes with one
  X25519 recipient, larger with a hybrid one), a 16-byte
  payload nonce, and 16 bytes of tag per chunk: about 0.024 percent. A 64 KiB boundary is
  therefore not a size at which the stored size and the plaintext size can coincide, which is
  what `RecordPlacementEncryption` relies on when it refuses `stored_size <= plaintext size`.
- Fixtures for #991 and #996 must cross a chunk boundary and end in a short final chunk, and
  none may be all zero.

## The two-digest model

An encrypted copy has **two digests that are never the same number**, and the journal keeps
them in different places so that no code path can confuse them (#662 was exactly that
confusion):

| Digest | Where it lives | Meaning | Who may write it |
|---|---|---|---|
| plaintext | `artifacts.local_hash` (and `placements.hash` on every copy, plaintext or not) | what the artifact hashes to, so what a restore must reproduce | unchanged from today; an encryption step never overwrites it |
| stored-bytes | `placement_encryption.stored_hash` / `stored_hash_alg`, with `stored_size` | what the bytes **at the object key** hash to: header and ciphertext | only the attempt that reached the durable phase advance, in that same write |

Consequences that are decisions, not observations:

- `placements.hash` is documented in 0007 as "the content hash recorded FOR THIS COPY" and is in
  fact the artifact's digest, copied verbatim by `destinationPlacement` (`placement/engine.go`)
  and by `measureCommitted` (`lifecycle/commit.go`). Migration 0015 states the correction in its
  own header, because 0007 is frozen, and the two comments are fixed in the commit that
  lands the code. For a plaintext copy nothing changes.
- A side table, not new `placements` columns: `placements` stays exactly as 0013 left it,
  compat cell `03-migrated-schema` stays purely additive (migration 0016's header makes the
  same argument), and **absence of a row means "this copy is unencrypted"**, so a pre-migration
  database needs no backfill and no existing placement changes meaning.
- Ciphertext is not deterministic, and `UploadFromLocal` converges on an occupied key because
  the thing there is "an earlier attempt at the SAME artifact". A retry is a fresh encryption,
  never a resumed one, and **nothing ever compares a recorded stored-bytes digest to a freshly
  encrypted object**. A digest recorded by attempt one describes an object attempt two has
  replaced, so it is recorded by the committing attempt only. `ON DELETE CASCADE` ties the
  record's life to the placement row's.
- `state` refuses a stored-bytes digest equal to either plaintext digest
  (`ErrStoredDigestIsPlaintext`), and a stored size that does not exceed the plaintext's.
- `verification_class: content` over an encrypted copy means a streaming decrypt whose
  plaintext digest equals `artifacts.local_hash`. An unresolvable identity is
  `ErrClassUnavailable`, never a downgrade. A medium that can only attest the stored bytes
  (`stored_hash`) proves the object is intact, not that it decrypts, and must not be reported
  as `content`. `revalidate.automaticMediumClass` stays `Existence` (#991 asserts it).

## The upload seam

`MediumStore.UploadFromLocal` is addressed by a local path on purpose
(`core/internal/transport/medium.go:262-265`: "a reader would put this process in the middle
of every byte for no gain"), and encryption needs this process in the middle of every byte.
Three ways to resolve that were weighed.

- **(a) A reader-shaped upload beside `UploadFromLocal`.** Cleanest in the long run, but it
  changes an interface that was written with a reason, moves the digest computation into the
  transport, and an unsized stream into rclone may be spooled to disk by rclone itself, which would
  bring the extra pass back without the engine seeing it. The convergence contract on an occupied key was argued for deterministic bytes,
  which ciphertext is not, and (a) would reopen that argument in the transport rather than in
  the engine.
- **(b) An encrypted staging pass.** Encrypt the local artifact into a staging file on the
  backup set's own disk, hashing the ciphertext as it is written with the
  `io.MultiWriter(file, digest)` pattern, then hand that path to `UploadFromLocal` unchanged.
  The stored-bytes digest is measured over exactly the bytes the transport reads. Capacity
  and cleanup extend what `core/internal/placement/staging.go` already owns for
  medium-to-medium moves (`roomToStage`, `sweepStagingTemps`, `discardStaging`,
  `Engine.discardDestination`) rather than replacing it, and the transport interface does not
  move. The cost is one extra write pass and ciphertext-sized free space under the set's
  `local_path`, both of which `staging.go` documents and refuses honestly when short.
- **(c) Encrypt at commit so the local copy is ciphertext too.** Rejected outright: the
  epic's objective is one plaintext local copy and an unreadable off-site one, and this would
  make every local restore a decrypt.

**(b) is the decision**, with (a) recorded as the follow-up if the staging cost proves wrong.
Constraints #991 inherits: the staging file is structurally outside what a local placement can
name (`StagingDirName`, `.moves`, is the existing answer), it is never a placement, and an
interrupted pass leaves nothing behind after a restart. A medium-to-medium move of an already
encrypted copy stages ciphertext, so `stagedCopyIsTheArtifact` (which compares to the
journal's plaintext digest today) must compare to `stored_hash` for such a source, and the
destination row carries the same `placement_encryption` record, because the bytes and the key
are the same. That is a #991/#992 item and is listed below.

## Rotation and the re-wrapping story (C6)

A rotation is a new X25519 identity registered as a new key id and used for **new copies only**.
A historical copy's header names the recipient it was written for, and its
`placement_encryption.key_id` names the registry row, so every old copy stays readable through
its recorded key while that key is retained. Retiring a key is refused while any ACTIVE copy
names it (#992; `idx_placement_encryption_key` is what makes that question cheap).

Re-wrapping history to a new key means rewriting the header, and so rewriting the object. The
epic cuts re-encryption of historical copies, and so does this ADR. The format leaves the door
open (a header holds several recipient stanzas, so a later decision could write each new copy
to two recipients, or re-wrap in place with the old identity), and neither requires a schema
change.

## Licence, toolchain and the module graph (C8, and the NOTICE item)

This pull request adds no module, so `NOTICE`, `provenance/third-party-licenses.json`, `go.mod`
and `go.sum` are unchanged here. Checked on 2026-11-09 against `core/go.mod` (`go 1.27.0`,
`golang.org/x/crypto v0.58.0`, `golang.org/x/sys v0.49.0`, `golang.org/x/term v0.47.0`,
`google.golang.org/protobuf v1.36.12`):

| Module | Version | `go` directive | Licence | Already in `core/go.mod` |
|---|---|---|---|---|
| `filippo.io/age` | v1.3.2 | 1.25.0 (toolchain line `go1.27.0`) | BSD-3-Clause | no, **new direct** |
| `filippo.io/edwards25519` | v1.2.0 | | BSD-3-Clause | no, new indirect |
| `filippo.io/hpke` | v0.4.0 | | BSD-3-Clause | no, new indirect |
| `filippo.io/nistec` | v0.0.4 | | BSD-3-Clause | no, new indirect |
| `golang.org/x/crypto` | age wants v0.55.0 | | BSD-3-Clause | v0.58.0, already newer |
| `golang.org/x/sys` | age wants v0.47.0 | | BSD-3-Clause | v0.49.0, already newer |
| `golang.org/x/term` | age wants v0.45.0 | | BSD-3-Clause | v0.47.0 indirect, already newer |

Module selection therefore adds four modules and moves no existing one, and no `go` directive
in the new set exceeds the repository's pin. Licence texts were read from each repository's
`LICENSE` on the same day (copyright holders: The age Authors and Google LLC for age; The Go
Authors for the others). A Tink choice would have added `google.golang.org/protobuf` as a
direct dependency and sio would have added one module, which is the size difference C7 records.

**Items for #991, which is the issue that adds the module:**

1. `go get filippo.io/age@v1.3.2` in `core/`, `go mod tidy`, and confirm the diff is exactly the
   four new modules above and no version of an existing one moves.
2. Regenerate `NOTICE` and `provenance/third-party-licenses.json` through the repository's own
   generator (not by hand) and check that all four show `BSD-3-Clause` with a licence-text
   hash, and that `docs/compliance/release-provenance.md`'s verification still passes.
3. Read the licence texts again at the version actually vendored, because "BSD-3-Clause" on the
   day of this ADR is a statement about v1.3.2.
4. State in #991's pull request that `go.mod`'s `go` line did not move.

## Consequences

- A clean-machine recovery needs the bundle (the identity), the object, and either the shipped
  binary or the `age` CLI: `age -d -i key.txt copy.age > artifact`. The procedure goes in
  `docs/recovery-without-a-terminal.md` through #992 and #997.
- Object names stay visible. `MediumKey`'s FR-28 layout does not change: age encrypts content,
  not names, and the documents say so.
- An X25519 recipient is not post-quantum. A bucket holder who keeps ciphertext for years can
  decrypt it later if the curve falls. age 1.3 defines an ML-KEM-768 + X25519 hybrid recipient
  that would answer this inside the same format, with a larger header; it needs no schema
  change (`recipient` is text, the header is self-describing). I did not choose it because
  every external reader in the wild is the plain CLI of the day, and because the key string
  is much longer; it is listed as a decision for the owner.
- A `kopia` engine set's repository is already encrypted by kopia's own format inside
  `repo.WriteSession`; `required` on such a set is satisfied by that and layers nothing. #992
  writes the sentence where operators read it.
- `retained_copy_encryption:` is a one-way door under `KnownFields(true)`: an older binary
  refuses a file that has it. The CHANGELOG names the version.
- Superseded in one sentence by [ADR 0033](0033-encryption-suite-agility.md): the format is no
  longer only a property of the build. A global `suite:` field chooses among the suites the build
  registers, `age/v1` keeps its exact meaning, and the default for new keys is `age-pq/v1`.
  Everything else here stands.
- `0015_retained_copy_encryption.sql` is applied after 0013 whatever its neighbours are:
  `0014` is EPIC M's and not in the tree, `0016` and `0018` landed first, and `migrate`
  applies any unapplied version regardless of order.

## What this ADR does not decide

Who generates and exports the managed identity and how the bundle is confirmed (#990). The
byte path, its memory budget and its five negatives (#991). Retention, sidecar contents,
catalog rebuild and the retirement refusal (#992). Any KMS or Vault provider: the external
reference's `command` already reaches one, and a provider interface would be a separate ADR.
Whether the hybrid recipient should be the default.

## Names the next issues depend on

**Clearly marked: #990 and #991 read these. A rename is an edit to this section and a message
to both.**

| Thing | Name | Owner |
|---|---|---|
| Format id and version | `"age/v1"`, `1` (recorded per copy) | #991 writes, #989 stores |
| Go library | `filippo.io/age` v1.3.x, imported only by `core/internal/keycustody` (identity generation and parsing) and `core/internal/copycrypt` (the byte path) | #990, #991 |
| Key custody package | `core/internal/keycustody` | #990 |
| Byte-path package | `core/internal/copycrypt`: streaming `Encrypt` into an `io.Writer` and decrypt-verify into a digest, plus the staging-pass helper the engine calls | #991 |
| Config (top level) | `retained_copy_encryption:` with `mode` (`off\|required`) and `key_provider` (`managed: true` or `external: {file\|env\|command}`) | #989 |
| Config (per set) | same key under a backup set, `mode` (`inherit\|off\|required`) and `key_provider`; nothing else | #989 |
| Config Go types | `config.RetainedCopyEncryptionBlock`, `RetainedCopyEncryptionOverride`, `RetainedCopyKeyProvider`, resolved `config.RetainedCopyEncryption` (field `Config.RetainedCopyEncryption`, `BackupSet.RetainedCopyEncryption`), `Enabled()` | #989 |
| Consumers read | the resolved fields only, never the pointer fields | everyone |
| Tables (migration `0015`) | `encryption_keys(id, provider, recipient, status, created_at, retired_at)`, `placement_encryption(placement_id, format_id, format_version, key_id, stored_hash, stored_hash_alg, stored_size)`, `recovery_bundle_exports(key_id, digest, exported_at)`, `recovery_bundle_confirmations(key_id, bundle_digest, confirmed_at, confirmed_by)` (a confirmation references an export of the same key and digest) | #989 |
| Provider values | `managed`, `external` (`config.KeyProviderManaged/External` and `state.KeyProviderManaged/External`) | #989 |
| Key status values | `active`, `superseded` (a rotation moved it out of the active slot, no retirement time), `retired` (`state.EncryptionKeyActive/Superseded/Retired`); at most one active managed key | #989 |
| Journal API (`core/internal/state/encryption.go`) | `RegisterEncryptionKey`, `AddEncryptionKey` (register and supersede in one transaction), `ActivePlacementsRecordingKey`, `RetireEncryptionKey` (refused with `ErrEncryptionKeyInUse` while the key is active or an active copy records it), `EncryptionKeys`, `EncryptionKey`, `RecordPlacementEncryption`, `PlacementEncryption` (read, `ok` false means unencrypted), `RecordRecoveryBundleExport`, `RecoveryBundleExports`, `ConfirmRecoveryBundle`, `RecoveryBundleConfirmations`; errors `ErrEncryptionKeyNotFound`, `ErrRecoveryBundleExportNotFound`, `ErrEncryptionKeyExists`, `ErrStoredDigestIsPlaintext` | #989 |
| Transactional half for #991 | `recordPlacementEncryptionTx`, to run inside the same transaction as the phase advance that makes a copy durable | #991 |
| Digests | plaintext: `artifacts.local_hash` and `placements.hash`; stored: `placement_encryption.stored_hash` | all |
| Staging | `placement.StagingDirName` (`.moves`) under the set's `local_path`; the encrypted pass is never a placement | #991 |
| Compat fixtures | `09-encryption-global-and-per-set.yaml` (valid), `62-invalid-inline-key-material.yaml`, `63-invalid-encryption-required-without-provider.yaml` | #989 |
| ADR numbers | 0027 (this), 0028 is #997's | |
| Not in this issue | no new `alert.Kind` (#990), no `health.Report` field (#990), no `obs` event (#991) | |

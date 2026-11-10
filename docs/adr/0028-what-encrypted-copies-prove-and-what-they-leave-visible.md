# ADR 0028: What an encrypted copy proves, what it leaves visible, where the bundle warning lives, and what `required` means for a repository set

- Status: accepted. It records decisions the code of EPIC N (#967) already carries, so
  that a later reader does not have to reconstruct them from four pull requests. It
  changes no behaviour.
- Date: 2026-11-09
- Scope: EPIC N, FR-60 and FR-61. Recorded by N2.4 (#997). ADR 0027 (#989) chose the
  format, the library, the upload seam and the two-digest columns; this ADR is its
  companion for the four questions 0027 left to the code.

## Context

ADR 0027 fixed age v1 through `filippo.io/age`, an encrypted staging pass in front of
`UploadFromLocal`, and a stored-bytes digest beside the plaintext digest. Building on it
raised four questions that nobody would be able to answer from the diff alone, because in
each case the obvious answer was the wrong one and the code only shows the one that
shipped. They are written down here, each with the option I did not take.

## Decision 1: the two digests, and what `verification_class: content` means over ciphertext

An artifact has one **plaintext digest**, `artifacts.local_hash` (and `placements.hash` on
every copy, because that column has always held the artifact's digest, which the header of
migration 0015 says out loud since 0007's wording is frozen). An encrypted copy also has a
**stored-bytes digest**, `placement_encryption.stored_hash` with `stored_hash_alg` and
`stored_size`, the hash of the bytes at the object key: header plus ciphertext. The
columns are never reused for each other, `state` refuses a stored-bytes digest equal to
either plaintext digest (`ErrStoredDigestIsPlaintext`), and an encryption step never
writes `artifacts.local_hash`.

`verification_class: content` over an encrypted copy means **a streaming decrypt whose
plaintext digest equals the recorded plaintext digest**, nothing weaker.
`copycrypt.DecryptVerify` sends plaintext only into a digest, never into a file. In the
same pass it hashes the bytes it read, so a successful verification also yields the
stored-bytes digest of the object that is there now.

- When the move engine accepts a copy it cannot use the `existence` or `attested` rungs
  for an encrypted one: both are `ErrClassUnavailable` there. Existence proves the object
  is there, and the only attestation a medium could give is about ciphertext, which says
  nothing about whether it decrypts. Neither is a quiet fallback for a content check
  that could not run. (The unattended scrub is a separate thing: it still HEADs the
  object against its stored size, and says no more than that.)
- An unresolvable key is `ErrClassUnavailable` too. It is found before anything is
  encrypted or uploaded, and it is never recorded as a weaker class.
- The unattended ceiling stays `existence` (`revalidate.automaticMediumClass`). A
  decrypt-verify is a `Content` check, so it reads the whole object and costs egress,
  which makes it something an operator starts (`retnd encryption verify`, `retnd
  validate --content`, the browser's Verify recoverable dialog) and never something a
  schedule does.
- A copy in an archive storage class is not read at all by a content check: the
  verification records "requires a restore" and starts none.

Not taken: letting `content` over ciphertext compare the stored-bytes digest, since that
proves the bytes are the ones uploaded and says nothing about recoverability, while the
product's own words for `content` ("Proves") promise more than that.

## Decision 2: `MediumKey` was not changed, so object names are not confidential

The object key stays `<prefix>/<source>/<set>/<artifact-name>` (FR-28). Encryption
protects object **content**. The source name, the set name, the file name, the size
(plus the small, fixed overhead) and the timestamps stay visible to whoever holds the
bucket, and the documents say so where an operator picks an untrusted destination.

I did not take a "safe suffix" or an opaque-name scheme. Changing the key layout for
encrypted copies forks `MediumKey`, so every plaintext-era object in the same bucket
would be orphaned from the rule that finds it. Convergence on an occupied key and the
sidecar both assume the key is derived, not looked up. It would also make the sidecar
the only map from an object to an artifact, which turns a bucket listing into something
a recovery on a clean machine cannot read. If names ever need hiding, that is a
migration of its own with its own ADR. It is not a flag on this feature.

## Decision 3: the recovery-bundle state is a durable health fact, not an `alert.Kind`

"Managed encryption is on and nobody has confirmed holding a recovery bundle" is a fact
only a person can clear. `alert.Dispatcher` models a fact that is true for a while,
resolves on its own, and may recur, and none of the thirteen existing kinds is a state
that cannot resolve itself. As a kind it would ride the retry ladder forever. So the
confirmation is a row in the key registry (`recovery_bundle_confirmations`, migration
0015), the degraded verdict is `health.Report.KeyCustody` (zero value: this deployment
encrypts nothing), and a restart reports the same thing it did before.

Exactly one new kind exists, `alert.EncryptionKeyUnresolvable`, because that fact does
resolve by itself: the file comes back, the secrets manager answers again. The alert pass
raises it (`app.Service.KeyCustodyAlertConditions`, called from `evaluateAlerts`) for each
alertable set that resolves to `required`, is not a `kopia` set and whose copies the engine
would refuse for want of a key: a managed key with no active key or an unreadable file, an
external reference this build cannot write under yet, or no key custody at all. It fires
once, stays quiet while the fact holds, resolves when the key resolves again, and is noticed
within one alert tick. A pass that cannot read the registry reports the set as not looked at
rather than resolving it. The refusal itself (the copy is not written, the source and the
medium are untouched) and the "Key missing" state on the copy are unchanged.

## Decision 4: what `required` means for a `kopia`-engine set

A set on `model.EngineKopia` writes into a repository that kopia already encrypts under
its own format, inside its own write session, under the repository passphrase. The
artifact copy path, where `retained_copy_encryption` acts, is not the path that set
takes, and it writes no `placements` rows. So `required` on such a set is **satisfied by
kopia's own encryption and layers nothing on top**: no age pass, no per-copy record, no
key id, and a managed key is not consulted for it. The web UI draws the control disabled
with that sentence in place of an inert switch. I did not make `required` mean "reject
the set at load" because an operator who writes the deployment-wide default and has one
incremental set among forty should not have their config refused for it, and I did not
make it silently double-encrypt because the second layer would add a key to lose and
protect nothing the first does not.

The decision has one home in the code, `config.BackupSet.EncryptsRetainedCopies()`
(resolves to `required` and is not a `kopia` set). The placement engine, the key-custody
health section, the metrics gauge and the key alert read it, so none of them counts a kopia
set as one a managed key guards. `GET /encryption/policy` and `retnd encryption policy show`
say so with `satisfied_by: "repository"` on the set, in the sentence the web UI draws.
`required` is still accepted at load, on the deployment and as a per-set override. Pinned by
`TestRequiredOnAKopiaSetIsAcceptedAndDoesNotEncryptRetainedCopies_1296` (config),
`TestARequiredKopiaSetNeverAsksKeyCustody_1296` and
`TestAKopiaOnlyDeploymentUnderRequiredIsNotDegradedKeyCustody_1296` (app),
`TestAKopiaSetUnderRequiredIsNotAnEncryptedSetToTheCopyPath_1296` (placement),
`TestSetPolicyOfAKopiaSetSaysSatisfiedByTheRepository_1296` (service) and the compat fixture
`10-encryption-kopia-set-under-required.yaml`.

## Consequences

- The documents use `artifacts.local_hash` and `placement_encryption.stored_hash` and no
  other names for the two digests.
- A reader of any surface that says "verified recoverable" can rely on it meaning a
  decrypt that matched, and "Encrypted" alone meaning only that the copy is ciphertext.
- Whoever holds a bucket can read names. The documents say it next to the choice of
  destination, not only in this ADR.
- An unconfirmed bundle shows in the health report and the status surfaces on every
  look, and it does not page anybody.

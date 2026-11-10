# Encrypted retained copies

This is the operations guide for retained-copy encryption (EPIC N, #967, and the encryption
suites of EPIC T, #1310): what it does to your bytes, what it leaves readable, who holds the
key, and what happens if you lose it. The decisions behind it are in
[ADR 0027](adr/0027-streaming-aead-format-and-the-upload-seam.md),
[ADR 0028](adr/0028-what-encrypted-copies-prove-and-what-they-leave-visible.md) and
[ADR 0033](adr/0033-encryption-suite-agility.md). The
byte-for-byte recovery steps for a machine with nothing on it are in
[recovery without a terminal](recovery-without-a-terminal.md#6-an-encrypted-copy-has-to-be-opened-on-a-machine-with-no-state).

I wrote this against what is in the code. Where the code does not do something yet, the
section says so instead of describing the plan.

## The short version

- It is off until you turn it on, and off is byte-for-byte what it was before.
- When a backup set requires it, the copy sent to a storage medium is written as an
  [age](https://c2sp.org/age) v1 file under an **encryption suite**, `age-pq/v1` unless you pin
  another. Your local copy stays plaintext.
- The key is generated and kept by the product ("managed"), and you export a **recovery
  bundle** and keep it somewhere that is not this machine.
- **If you lose the key, you lose the encrypted copies.** There is no reset, no recovery
  and no support path that recreates it.
- Encryption hides what is inside an object. It does not hide the object's name, and it
  does not hide the name of the source or the backup set.

## What is encrypted, and what is not

| Thing | Encrypted? |
|---|---|
| A retained copy on a storage medium, for a set whose resolved policy is `required` | Yes. The object is an age v1 file, written under the suite the copy records ([below](#encryption-suites)). |
| The local copy under the set's `local_path` | No. Local copies are plaintext by design: the point is a readable local copy beside an unreadable off-site one. If a retention tier removes the local copy once the upload is verified (the storage-mediums disclosure), the encrypted copy is then the only one, and the key is all that stands between you and it. |
| The producer's artifact, at the source | Never touched. Encryption is a copy transform. Nothing rewrites, renames or re-encrypts what a producer wrote. |
| The object **name** on the medium | **No.** It stays `<prefix>/<source>/<backup set>/<artifact name>`. |
| The sidecar object beside each copy (`.manifest/<name>.json`) | **No.** It is plain JSON. For an encrypted copy it adds the suite id (`format_id`), the key id (never a key), the plaintext digest and size, the stored-bytes digest and size, and when the copy last decrypted to the artifact. |
| A copy that was already on a medium in plaintext before you turned this on | **Not until you ask.** Turning the policy on does not touch existing copies, and the retention cycle never does. `retnd encryption encrypt-existing` is the one thing that encrypts them ([below](#encrypting-copies-that-already-exist)). |
| An incremental (`kopia`) set | Not by this feature. Its repository is already encrypted by its own format under the repository passphrase, and `required` on it layers nothing on top (ADR 0028). The policy answers `satisfied_by: "repository"` for such a set (`GET /encryption/policy`, `retnd encryption policy show`), and key-custody health and the metrics gauge do not count it. |

### Object names are visible, and that matters when you choose a destination

Whoever holds the bucket can list it. With encryption on they can read the source name,
the backup-set name, each file name, the size (the plaintext size plus a small fixed
overhead), the timestamps, and the sidecars described above. They cannot read what is in
the file. If the names themselves are sensitive, put nothing sensitive in them, or pick a
destination you trust with them. The layout does not change for encrypted copies, because
changing it would orphan every plaintext object already in the bucket (ADR 0028).

The plaintext digest in the sidecar also lets someone who already has a candidate file
check whether it is the one you backed up. That is a property of keeping the digest where
a recovery with no database can find it.

## Two digests

An encrypted copy has two different digests, and the product keeps them in different
places so they cannot be mixed up:

| Digest | Column | What it hashes |
|---|---|---|
| plaintext | `artifacts.local_hash` (and `placements.hash` on every copy) | The artifact. What a restore has to reproduce. Never overwritten by an encryption step. |
| stored bytes | `placement_encryption.stored_hash`, with `stored_hash_alg` and `stored_size` | The bytes at the object key: the age header plus the ciphertext. Written only by the attempt that finished and committed the copy. |

Encryption is not deterministic, so two encryptions of one artifact never produce the same
stored bytes. A retry is a new encryption, never a resumed one, and nothing compares a
recorded stored-bytes digest to a freshly encrypted object.

**What "verified" means here.** `verification_class: content` on an encrypted copy means a
streaming decrypt whose plaintext digest equals `artifacts.local_hash`. "Encrypted" alone
says only that the object is ciphertext. If the key cannot be found, the check says it
could not run. It does not fall back to a weaker answer.

## Smart defaults, and the things you do not choose

The whole operator surface is a **mode**, a **key provider** and, deployment-wide only, a
**suite**.

```yaml
retained_copy_encryption:
  mode: required          # off (the default, and what you get if you write nothing) | required
  suite: default          # default (the build's default suite, today age-pq/v1) | age/v1 | age-pq/v1
  key_provider:
    managed: true         # or external: { file: ... } / { env: ... } / { command: [...] }

sources:
  - id: production
    backup_sets:
      - id: nightly
        retained_copy_encryption:
          mode: inherit   # inherit (default) | off | required; and a key_provider if you want one
```

The suite is the one cryptographic choice you get, and it is a choice between whole
constructions the build already ships, never a set of parameters. There is no cipher, nonce
length, chunk size or KDF cost setting at either level, and no per-set suite: a per-set suite
would mean a per-set key and a per-set bundle to keep, which is the burden this feature exists
to avoid. Writing nothing is the same as writing `suite: default`. The config reader rejects
unknown fields, so a per-set rotation cadence, chunk size or suite is refused outright rather
than ignored.

`retnd encryption policy` shows what each set resolves to. Everything below is also a button in
the web UI (Settings, then "Retained-copy encryption", and a card under Retention on each
backup set's page). Each copy shows as Plaintext, or Encrypted beside a second chip (Verified
recoverable, Not decrypt-verified, Key missing or Decrypt failed) on a backup's own page, where
the plaintext digest and each copy's stored-bytes digest are also listed under their own names.
The Backups list gets an Encryption column only when a copy on it is encrypted.

### A change you cannot take back by rolling back the version

A `config.yaml` that carries `retained_copy_encryption:` cannot be read at all by a build that
predates the feature: the config reader refuses an unknown field. That is the one-way door
the [changelog](../CHANGELOG.md) names. Do not write the key until going back below this
version is off the table, and keep a copy of the previous `config.yaml` if you want a way
out.

`suite:` is a second door of the same kind. A file that writes it cannot be read by a build
that predates suites (anything up to and including 0.7.2, and any build of EPIC N alone). If
you never write the field you get the build's default and no door at all.

## Encryption suites

A suite names a whole construction from an established library: the container, the kind of
key that opens it, and the payload cipher. Every encrypted copy records the suite it was
written under (its `format_id`, in the journal and in the sidecar beside it), and that record
decides how the copy is opened, never the policy of the day. This build ships two, both from
`filippo.io/age`, both read by the stock `age` tool:

| Suite | Status | What the product says about it (its `Describe()` sentence, word for word) |
|---|---|---|
| `age-pq/v1` | supported, **the default** | "age v1 file with a hybrid ML-KEM-768 + X25519 recipient and ChaCha20-Poly1305 payload: post-quantum hybrid key establishment, 128-bit security level, not AES, not FIPS-validated; opens with age 1.3 or newer." |
| `age/v1` | supported | "age v1 file with an X25519 recipient and ChaCha20-Poly1305 payload: 128-bit security level, no post-quantum key establishment, not AES, not FIPS-validated; opens with any age release." |

That sentence is the only thing any surface says about a suite: `retnd encryption suites`,
`GET /api/v1/encryption/suites`, the tooltip on the Settings select and this table print the
same words, and a test holds them to the code. I would rather print less than the code can
back.

**Why `age-pq/v1` is the default.** The threat an off-site copy actually faces over ten years is
whoever holds the bucket keeping the ciphertext and waiting. `age-pq/v1` wraps each copy's file
key under a hybrid of ML-KEM-768 and X25519, so recorded ciphertext stays closed even if one day
the curve falls to a quantum computer, as long as ML-KEM holds (and the other way round). It adds
no module to the build, and a clean machine still opens a copy with `age -d`. What it does and
does not meet, plainly:

| Property | `age-pq/v1` |
|---|---|
| Authenticated encryption per 64 KiB chunk, with position and the end of the file bound into every tag | meets it |
| Key establishment that survives a quantum adversary recording ciphertext today | meets it (hybrid ML-KEM-768 + X25519) |
| Security level | 128-bit, because age's file key is 16 bytes whatever the payload cipher |
| AES | does not use it (ChaCha20-Poly1305, which is also the faster choice on NAS processors without AES instructions) |
| A FIPS 140-3 validated module | does not meet it, and the product makes no FIPS claim for any suite |
| CNSA 2.0 parameters | does not meet them (they ask for ML-KEM-1024 and AES-256) |
| Opens on a clean machine with a stock tool | meets it, with `age` 1.3 or newer |

The product does not call any suite "military-grade" or "256-bit", on any surface, because no standard stands behind the first and no age suite can honestly claim the second; it prints the `Describe()` sentence above instead.

### Status: supported, deprecated, retired

A suite carries one of three statuses in the build's own table. None is ever removed while a
copy records it.

- **supported**: new copies may be written under it, and its copies read, verify, move and
  recover as usual.
- **deprecated**: still reads and still writes, and the health report reads degraded with the
  number of active copies (and the active key, if any) under it, so you know to move off it. A
  `suite:` pinned to it loads with a warning.
- **retired**: reads only. Decrypt-verify, scrub, moving its ciphertext, `retnd encryption
  decrypt` and recovery all keep working on its copies, but no new copy is written under it, a
  `suite:` pinned to it is refused at load, and health stays degraded while any active copy
  records it. A build that removes a suite is a major version, and only once no copy records it.

Both suites in this build are supported. The statuses are there so that the day one has to go,
nothing you already stored is orphaned.

### Changing the suite: choose, rotate, export, confirm

A key belongs to one suite, and there is one active managed key per suite. New copies go under
the active key **of the policy's suite**, never under whatever key happens to be active, and the
product never mints a key behind your back. So a change of suite is four steps, and the Settings
card walks you through them in this order:

1. **Choose.** `retnd encryption policy patch --suite age-pq/v1` (or the select on the card). The
   answer says `rotation_needed` and prints the exact command for the next step; nothing is
   written to the key store yet.
2. **Rotate.** `retnd encryption key rotate --suite age-pq/v1` mints the new suite's key and
   supersedes the old suite's active key in the same write, so there is still exactly one key new
   copies go under.
3. **Export.** `retnd encryption key export <new-key-id> --out <file>`, somewhere that is not
   this machine.
4. **Confirm.** `retnd encryption key confirm <new-key-id>`. Until you do, health carries the "no
   confirmed recovery bundle" state for the new key.

Between steps 1 and 2 a `required` set's new copies are **refused, not downgraded**: "no active
key for suite age-pq/v1; run `retnd encryption key rotate --suite age-pq/v1`". The refusal comes
before anything destructive, so the local copy stays and the source is not deleted. The same
happens when a new build moves the default while your active key is under the old one: the
first cycle says so once, in an `EncryptionDefaultSuiteChanged` event naming both suites, and
copies wait for the rotation.

Copies already written stay under the suite and key they record, and keep opening through them.
Moving them to the new suite is `reencrypt`, below, and is optional while their suite is
supported.

### Re-encrypting copies under the active suite

`retnd encryption reencrypt` takes copies recorded under an older suite (or an older key) and
writes them again under the active suite and key. It is the same journaled move
`encrypt-existing` uses, with an encrypted source:

```
retnd encryption reencrypt production/postgres --suite age/v1 --to offsite --dry-run
retnd encryption reencrypt production/postgres --suite age/v1 --to offsite
```

- The old copy is streamed off its medium through its own suite's reader straight into the new
  suite's writer. Plaintext is never written to local disk.
- The new copy is uploaded to **another medium** and decrypted back in full against the
  artifact's plaintext digest before it is recorded. Only then is the old copy marked for
  retirement and deleted. A crash at any point resumes from the journal.
- Same-medium rewrite is not offered, for the reason `encrypt-existing` gives: one copy per
  backup per medium, and an object key fixed by the backup.
- `--dry-run` lists the copies under that suite and says, for each, whether a run would
  re-encrypt it or refuse, and why. A refusal changes nothing.
- When the plaintext local copy still exists, you usually need no verb at all: the next ordinary
  move writes a fresh copy under the active suite. `reencrypt` is for copies whose local copy is
  gone.

## Managed keys and external keys

**Managed.** The product generates an identity of the key's suite from the system random
source (an age X25519 identity for `age/v1`, an age ML-KEM-768 + X25519 hybrid identity for
`age-pq/v1`) and
writes it as a `0600` file in a `0700` directory called `retained_copy_keys`, beside
`config.yaml`. It applies the same custody rule it applies to any secret file you point it at:
owner-only mode, no symlinked final component, no group- or world-writable ancestor. Every key
has an id, a created time, a retired time (when retired) and a status of `active`,
`superseded` or `retired`. Nothing returns an existing key to you: the one way key material
leaves is the recovery bundle below.

Be straight about what that file protects. Anyone who can read the state directory can read
the key, so a managed key does **not** protect your copies from whoever owns this machine. It
protects them from whoever owns the **bucket**. That is the whole claim.

**External.** You can declare a key as a reference in the same shape every other secret in the
config uses: exactly one of `file`, `env` or `command`. The reference is write-only: the API
and the CLI read it back as a description of its source (a path, a variable name, an
executable), never as something that could be pasted back in. An inline key in the config has no
field to land in and is refused at load. `retnd encryption provider test` tells you whether a
reference resolves right now, without printing what it resolved to.

A set that requires encryption under an external reference writes encrypted copies through it.
The first time the reference resolves, the product records the key as a registry row: its id
(derived from the key's public recipient, so the same key reached through another reference is
the same row), the recipient, a status and a created time. It never records the reference
itself or the key. The reference is resolved only while a copy is written or verified, and a
reference that does not resolve or is not an age identity is refused before anything is
encrypted or uploaded: no copy, no recorded class, nothing deleted.

An external key is yours to keep. It is not in the recovery bundle (a bundle is the one moment
a managed key leaves this machine, and an external key already lives somewhere you chose), so
`retnd encryption key export` refuses it, and it does not count towards the "no confirmed
bundle" state. Opening a copy elsewhere is the stock `age` tool and your own key.

The row records no reference, so the product cannot tell that a reference now yields a different
key. If you point a reference at another key, new copies register that key as a new row and are
written under it, and the copies already written stay recorded against the old row. They open
only while some declared reference (the global provider or a set's) still yields the old key, so
**keep the old reference declared** under a different name or path; one that is removed from the
configuration is a key this deployment can no longer read. If you do drop one while ACTIVE copies still
record its key, the health section reads degraded and `ENCRYPTION_KEY_UNRESOLVABLE` is raised for each
affected set, naming the key and the copies (never the reference or the key), and retiring that key is
refused naming the copies. A verify of an old copy through a
reference that now yields another key is refused and says so. There is no command that rotates an
external key yet. There is no KMS or Vault provider; an external `command` is as close as it gets.

## The recovery bundle, and where it must not live

A managed key lives on the host. If the host is lost, so is the key, so the product has you
export a **recovery bundle** once and put it somewhere else.

```
retnd encryption key create
retnd encryption key export <key-id> --out /somewhere/not-the-backup-volume/bundle.json
retnd encryption key confirm <key-id>
```

(The web UI does the same under Settings, with a Download bundle button and then a confirmation.) The export file
holds the key, so it is created `0600` and never over a file that exists, and it is never
printed. The bundle is a small JSON file: a version, the key id, the suite (its `format`
field, which is the first thing to check on a clean machine), the public
recipient, the created and exported times, the key itself in a field called `material`, an
integrity digest, and plain instructions. What the command prints is a receipt, the key id,
the digest and the time, and no key.

**Confirming is a statement, not an upload.** `key confirm` records that you hold a bundle
with that digest. It cannot check that the bundle is somewhere safe. Until you confirm, the
health report carries a degraded "no confirmed recovery bundle" state for the key. It is
stored in the key registry, so it survives a restart, and it is a health state and not an
alert: only a person can clear it, so nothing pages anybody.

The Dashboard shows the same fact as a warning you cannot dismiss, "No confirmed recovery
bundle", with a link to the Settings card, until you confirm.

What the installer does with the key directory on an upgrade, a factory reset and an
uninstall is in [the install guide](install.md).

Where to put it:

- **Not on the volume the backup shares export.** The same mistake as the one
  [`ssh-setup.md`](ssh-setup.md#encrypting-the-key-store-at-rest-298) warns about for the
  key-encryption key: a key stored on a volume that is also reachable over a share, or that
  is the same disk the backups sit on, is not custody.
- **Not in the bucket the copies go to.** That puts the lock and the key in the same hands.
- Somewhere that survives this machine: a password manager's secure attachment, an offline
  drive in another room, a printed copy in a safe. Keep the digest from the receipt with it.

Export and confirm each key you rely on. After a rotation, the old key still guards every
copy written under it, and the health report lists it as unconfirmed until you confirm a bundle
for it too.

## Encrypting copies that already exist

If a set already had plaintext copies on a medium when you turned `required` on, those copies are
still plaintext, and the move engine refuses to hop one of them to another medium (the hop would
send it in the clear). `retnd encryption encrypt-existing <source/set> --to <medium>` is how I
deal with them, and nothing else does: not the policy change, not a retention cycle, not a
schedule.

```
retnd encryption encrypt-existing production/postgres --to offsite --dry-run
retnd encryption encrypt-existing production/postgres --to offsite
```

What it does, per backup that has an ACTIVE copy on a medium:

1. It streams the plaintext object off its medium straight into the encrypting pass, which
   checks it against the artifact's recorded plaintext digest as it goes. Plaintext is never
   written to local disk, and only the ciphertext sits in the `.moves` staging file.
2. It uploads the ciphertext to `--to` and decrypts it back, in full, against that recorded
   plaintext digest. Only then is the encrypted copy recorded, with its key id and stored-bytes
   digest, in one write.
3. Only after that does it mark the plaintext copy for retirement, and only then delete it. Until
   step 2 has passed, the plaintext object and its ACTIVE record are untouched, so the plaintext
   copy is the only good copy for exactly as long as it has to be.

Every step is a phase of the same journaled move the engine uses for any copy, so a crash at any
point resumes from the journal when you run the command again, and a half-written ciphertext is
discarded and rewritten, never trusted. If the retirement is refused (a retention tier still
selects the plaintext medium, say), the encrypted copy is kept and the plaintext copy stays, and
the command says so.

Things worth knowing before you run it:

- **`--to` has to be another medium.** The journal holds one copy per backup per medium, and the
  object key on a medium is fixed by the backup, so an encrypted copy cannot sit beside its own
  plaintext source on the same medium. If you want the encrypted copy on the plaintext's medium,
  encrypt onto a second medium first and move it back: moving an encrypted copy is a re-home and
  needs no opt-in.
- **`--dry-run` lists every backup of the set that has a copy on a medium** and says, for each, whether
  a run would encrypt it or refuse, with a code and a reason: `not_required`, `already_encrypted`,
  `no_plaintext_copy`, `key_unavailable`, `destination_unverifiable` (the destination can only
  verify at a class weaker than a full decrypt), `no_room` (the staging disk cannot hold the
  ciphertext), `source_unreadable` (an archive class with no restore in effect) or
  `not_eligible` (the move engine's own reasons). A refusal changes nothing.
- **It reads each plaintext copy once and each new copy twice** (the verify, and the re-read before
  the plaintext is deleted). If your medium bills for reads, that is the cost.
- **A run is bounded** by the deployment's `max_moves_per_cycle` and does one backup at a time.
  Run it again to carry on; refusals do not use up the bound.
- **Cancel.** Beside a serving engine the command starts an operation, and `retnd assurance
  cancel <operation-id>` and the browser's Cancel are the same call. Run in a terminal with nothing
  serving, Ctrl-C does the same. Before the new copy is verified, a cancel discards it and leaves
  the plaintext exactly as it was. After that point the encrypted copy is already a verified copy
  and is not thrown away: the next run (or the next ordinary cycle) finishes retiring the
  plaintext.
- **There is no button for it.** The encryption screens do not show one, so it is a command and a
  route (`POST` and `GET /api/v1/encryption/encrypt-existing`) and nothing else.

## Rotation affects new copies only

`retnd encryption key rotate` creates a new active key and marks the previous one
`superseded`. With `--suite` it rotates to another suite ([above](#changing-the-suite-choose-rotate-export-confirm)).

- New copies are written under the new key.
- Every copy already stored stays readable through the key id recorded against it.
- **Nothing is re-encrypted by a rotation.** Moving old copies under the new key is
  [`reencrypt`](#re-encrypting-copies-under-the-active-suite), and only when you run it.
- `key retire <id>` is refused for the active key and, naming the copies, while any active
  copy still records the key. A key you retire is one you no longer need for any live copy.

## If you lose the key

Stated plainly, because a softer sentence would be a lie:

**There is no reset, no recovery and no support path that recreates a lost key. If the host
and every copy of the bundle are gone, the encrypted copies are unreadable for good.** The
object is a standard age file, so nobody can read it without the key, and that includes the
people who wrote this product. The plaintext copies you still have, and the producer's own
artifacts, are not affected. Only the encrypted copies are.

Check you hold a bundle for every key that guards a copy, and use **Verify recoverable**
(below) from time to time to see that the copies still open with the keys you hold.

## Opening a copy

**With a working catalog.** `retnd encryption copies <source/set/backup>` lists which copies
of a backup are encrypted, under which key and suite, and whether that key resolves here.
`retnd encryption verify <source/set/backup>` decrypts a copy and compares the plaintext
digest to the recorded one; this is the check behind "Verified recoverable".

What this build does **not** do: `retnd recover`, `retnd fetch` and `retnd restore` do not
decrypt. `retnd recover` reads a medium copy and compares what it reads to the plaintext digest,
which a ciphertext object cannot match. Opening a copy to a file is its own verb, below.

**Without a catalog, or without a deployment at all.** `retnd encryption decrypt` opens one copy
into one file and reads no configuration and no state database, so the binary, the object, its
sidecar and the bundle are enough:

```
retnd encryption decrypt <object> --bundle bundle.json --sidecar <sidecar.json> --out <artifact-name>
```

It hashes the object and compares that with the stored-bytes digest in the sidecar before it
uses the key, decrypts, and creates the output only after every chunk authenticated and the
plaintext digest equals the recorded one. On any failure (wrong key, a cut-short or altered
copy, a digest that does not match) it exits 1 and leaves nothing in the destination, not
even a partial file. It refuses to replace an existing file, and it cannot write to standard
output. Instead of a bundle it takes `--identity FILE` (an age identity file), and instead of
a sidecar `--digest HEX` with an optional `--stored-digest HEX`.

To make a deployment know a key it has lost (a rebuilt host, say) run
`retnd encryption key import --bundle bundle.json`. The key is registered as superseded, so it
opens old copies and new copies keep using the active key. Importing is safe to repeat, it
counts as confirming the bundle, and it never prints the key. It needs the deployment's own
journal, so it refuses while an engine serves that deployment. It takes a bundle and not a bare
identity, because an identity does not carry the key id the copies record.

Neither verb has an API route. Both take key material as input, and a request body is not a
place for it.

**The independent route.** The copy is an age file and the bundle holds an age identity, so the
stock `age` command line tool works on any machine, with no `retnd` at all. I kept it as the
fallback on purpose, so recovery does not depend on this binary alone. Both suites open with
the same command; an `age-pq/v1` copy needs **age 1.3 or newer** (older releases do not know its
recipient type and say they found no identity that matches), an `age/v1` copy opens with any
release:

1. Open the bundle JSON, check its `format` field (`age/v1` or `age-pq/v1`), and copy the value
   of its `material` field (one line starting `AGE-SECRET-KEY-1` for `age/v1`, or
   `AGE-SECRET-KEY-PQ-1` for `age-pq/v1`) into a new file `identity.txt` that only you can read.
2. `age -d -i identity.txt <object> > <artifact-name>.tmp`
3. Compare the file's digest with the `plaintext_digest` in the sidecar beside the object
   (the sidecar names the algorithm). Rename the file only when they match.

`age` checks neither digest and writes plaintext as it authenticates each chunk, which is the
reason for the `.tmp` name and the rename.

`retnd catalog rebuild` reads the sidecars and tells you, per encrypted copy, which key it needs
and whether this deployment knows that key. It never opens a key file and never runs an external
key command. See [recovery](recovery.md#when-the-catalog-is-gone-and-some-copies-are-encrypted).

## Archive storage and thaw

A decrypt-verify reads the whole object, so it is a `Content` check and **it costs egress**.
That is why an unattended scrub never does it: the scheduled scrub HEADs each object against its
recorded size and says no more. `retnd encryption verify` refuses to read a copy a medium would
bill for until you pass `--allow-egress`. If the copy sits in an archive storage class, the
content check is not attempted: it records that a restore is needed first and starts none.
Thaw the object yourself, then verify. Moving an encrypted copy from one medium to another
copies the ciphertext as it is and accepts the destination only after decrypting it with the
key it was written under, so a move also needs the key.

## What it costs

- **Space:** the age header plus 16 bytes per 64 KiB (about 0.024 percent). The header is a
  couple of hundred bytes under `age/v1` and about 1.4 KB under `age-pq/v1`, whose recipient
  stanza carries an ML-KEM ciphertext. An `age-pq/v1` public recipient (`age1pq1...`) is about
  2000 characters long, against 62 for `age/v1`; the bundle and the key list carry it, and the
  screens shorten it.
- **Local free space:** the copy is encrypted into a staging file under the set's `local_path`
  (the `.moves` directory) and the staged file is what is uploaded, so you need room for a
  ciphertext-sized file there while a copy is in flight. The staging file is removed whether
  the upload worked or not.
- **Time:** one extra pass over the artifact on the way out.
- **Memory:** designed to stream, so it does not grow with the artifact. I have not measured
  throughput on any hardware, and the multi-gigabyte memory budget test has not been run by me.

## Progress while a copy is made

A copy under a `required` policy reports two stages with their own byte counters, in the
terminal, the activity strip and the operation panel, the same ones from the CLI and the browser:

- `encrypting`: bytes of the artifact read into the encrypting pass, out of the artifact's size.
- `decrypt-verifying`: bytes of the stored, encrypted object read back, out of its stored size
  (a little bigger than the artifact).

Each starts at a measured zero, never goes backward, never passes its total, and ends when the
stage ends, however it ended: a cancel or a failure clears the stage, so nothing keeps saying
"Encrypting" after the copy is over. There is no rate (nothing measures one), and no reading
ever carries a key or a key id. A plaintext copy never reports either stage.

## What this does not do yet

- No live byte progress for a standalone `retnd encryption verify`. It is a scrub operation and
  reports `bytes_read` in its result. The Decrypt-verifying stage above is the check a copy
  goes through when it is made.
- No KMS or Vault provider (an external `command` can reach either).
- No re-encryption onto the same medium. `reencrypt` and `encrypt-existing` both write the new
  copy to another medium, and only when you run them.
- No per-set suite, no `auto` suite and no automatic rotation when the default moves.
- No FIPS build mode, and no claim of FIPS validation for any suite.
- `retnd encryption decrypt` opens one copy at a time. There is no bulk "decrypt everything" verb,
  and no API route for it or for key import.
- `ENCRYPTION_KEY_UNRESOLVABLE` fires when a `required` set's copies are being refused for want
  of a key (a missing or unreadable managed key file, no active managed key, or, until external
  providers can write copies, an external reference), once, and clears when the key resolves
  again. It reaches the notifier only if alerts are enabled in `config.yaml`. Key custody health
  on the Dashboard still does not show resolvability; the key list does.

## Where encryption enters, and how to have it earlier

If your archives are sensitive enough that you care which disk ever holds them in the clear,
this is the part to read before you pick a setup.

**What happens to a new file today.** Each poll lists the remote directory. A file that is
complete is pulled whole, over SFTP, into a `.partial` file under the set's `local_path`. That
file is read back from disk and hashed, compared with the remote's own `sha256sum` when you
asked for that, and handed to your validator if you named one. Then it is committed as the
**local copy**, in plaintext. In the same cycle, once every set has had its pass, the move engine
encrypts the local copy under the policy's suite into the `.moves` staging file, uploads it,
decrypts it back to check it, and only then records the encrypted copy. So the first ciphertext
exists in the cycle the file arrived in, and only for a set whose copies go to a storage medium.

**Why the local copy is plaintext.** Because that is the product's promise: a readable local copy
beside an unreadable off-site one. Everything that checks the local copy (the read-back, your
validator, the scrub, the check before a source is deleted, recovery tests and downloads) reads
it as plaintext, and the source is deleted on the strength of that local copy alone, without
needing a key. Encrypting earlier in the same pass would save one read of the local disk and
change nothing about who can read it, so this build does not.

**If plaintext on this machine's disk is not acceptable,** there are two ways to have encryption
earlier, and only one exists today:

1. **Encrypt before retnd sees it (available now).** Have the producer encrypt its own output,
   with `age` or anything else, before it lands in the directory retnd watches. retnd stores the
   bytes it is given and never needs the key: the local copy, the off-site copies and every check
   are of ciphertext, and nothing on this machine can open them. This is the model
   [ADR 0003](adr/0003-pull-encrypted-runs-to-the-nas.md) started from. The cost is the
   producer's: its disk, its CPU, and the key custody, which is yours to arrange, because retnd
   has no key to export for you. Pick this when the archives are credentials or anything else
   whose plaintext must never sit on the backup host.
2. **A ciphertext-only local copy (not built, a possible later epic).** retnd would encrypt as it
   pulls and keep only ciphertext locally. It would protect against someone who reads the disk
   or the share, not against whoever owns the host (the key still lives there for the checks),
   and it would make the key necessary to delete a source, so losing the key would lose the only
   copy, not just the off-site one. It is a change to what the product promises, and it is not in
   this build.

# Encrypted retained copies

This is the operations guide for retained-copy encryption (EPIC N, #967): what it does to
your bytes, what it leaves readable, who holds the key, and what happens if you lose it.
The decisions behind it are in [ADR 0027](adr/0027-streaming-aead-format-and-the-upload-seam.md)
and [ADR 0028](adr/0028-what-encrypted-copies-prove-and-what-they-leave-visible.md). The
byte-for-byte recovery steps for a machine with nothing on it are in
[recovery without a terminal](recovery-without-a-terminal.md#6-an-encrypted-copy-has-to-be-opened-on-a-machine-with-no-state).

I wrote this against what is in the code. Where the code does not do something yet, the
section says so instead of describing the plan.

## The short version

- It is off until you turn it on, and off is byte-for-byte what it was before.
- When a backup set requires it, the copy sent to a storage medium is written as an
  [age](https://c2sp.org/age) v1 file. Your local copy stays plaintext.
- The key is generated and kept by the product ("managed"), and you export a **recovery
  bundle** and keep it somewhere that is not this machine.
- **If you lose the key, you lose the encrypted copies.** There is no reset, no recovery
  and no support path that recreates it.
- Encryption hides what is inside an object. It does not hide the object's name, and it
  does not hide the name of the source or the backup set.

## What is encrypted, and what is not

| Thing | Encrypted? |
|---|---|
| A retained copy on a storage medium, for a set whose resolved policy is `required` | Yes. The object is an age v1 file. |
| The local copy under the set's `local_path` | No. Local copies are plaintext by design: the point is a readable local copy beside an unreadable off-site one. If a retention tier removes the local copy once the upload is verified (the storage-mediums disclosure), the encrypted copy is then the only one, and the key is all that stands between you and it. |
| The producer's artifact, at the source | Never touched. Encryption is a copy transform. Nothing rewrites, renames or re-encrypts what a producer wrote. |
| The object **name** on the medium | **No.** It stays `<prefix>/<source>/<backup set>/<artifact name>`. |
| The sidecar object beside each copy (`.manifest/<name>.json`) | **No.** It is plain JSON. For an encrypted copy it adds the format, the key id (never a key), the plaintext digest and size, the stored-bytes digest and size, and when the copy last decrypted to the artifact. |
| A copy that was already on a medium in plaintext before you turned this on | **No, and it stays that way.** Turning the policy on does not encrypt existing copies. |
| An incremental (`kopia`) set | Not by this feature. Its repository is already encrypted by its own format under the repository passphrase, and `required` on it layers nothing on top (ADR 0028). |

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

The whole operator surface is a **mode** and a **key provider**.

```yaml
retained_copy_encryption:
  mode: required          # off (the default, and what you get if you write nothing) | required
  key_provider:
    managed: true         # or external: { file: ... } / { env: ... } / { command: [...] }

sources:
  - id: production
    backup_sets:
      - id: nightly
        retained_copy_encryption:
          mode: inherit   # inherit (default) | off | required; and a key_provider if you want one
```

There is no cipher, nonce length, chunk size, KDF cost or algorithm setting at either level,
and none is planned. The format is fixed by the build (age v1, 64 KiB chunks, ChaCha20-Poly1305,
an X25519 key) and recorded against each copy, so a later format can be added without a
setting. The config reader rejects unknown fields, so a per-set rotation cadence or chunk size
is refused outright rather than ignored.

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

## Managed keys and external keys

**Managed.** The product generates an age X25519 identity from the system random source and
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

What is not there yet: **this build writes new copies only under a managed key.** An external
reference can be declared, saved and tested, but a set that requires encryption under one
cannot produce a copy yet. The copy is refused, before anything is deleted, with a message
saying the engine cannot resolve that provider. There is no KMS or Vault provider either; an
external `command` is as close as it gets.

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
printed. The bundle is a small JSON file: a version, the key id, the format, the public
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

## Rotation affects new copies only

`retnd encryption key rotate` creates a new active key and marks the previous one
`superseded`.

- New copies are written under the new key.
- Every copy already stored stays readable through the key id recorded against it.
- **Nothing is re-encrypted.** Re-encrypting history is not a feature of this build.
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
of a backup are encrypted, under which key and format, and whether that key resolves here.
`retnd encryption verify <source/set/backup>` decrypts a copy and compares the plaintext
digest to the recorded one; this is the check behind "Verified recoverable".

What this build does **not** do: `retnd recover`, `retnd fetch` and `retnd restore` do not
decrypt. An encrypted copy is opened with the key outside the product, as below. I did not find
a path in the code that decrypts a copy to a file for you, and `retnd recover` reads a medium
copy and compares what it reads to the plaintext digest, which a ciphertext object cannot match.

**Without a catalog, or without a deployment at all.** The copy is an age file and the bundle
holds an age identity. On any machine, with the stock `age` command line tool:

1. Open the bundle JSON and copy the value of its `material` field (one line starting
   `AGE-SECRET-KEY-1`) into a new file `identity.txt` that only you can read.
2. `age -d -i identity.txt <object> > <artifact-name>.tmp`
3. Compare the file's digest with the `plaintext_digest` in the sidecar beside the object
   (the sidecar names the algorithm). Rename the file only when they match.

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

- **Space:** the age header (a couple of hundred bytes) plus 16 bytes per 64 KiB, about 0.024
  percent on the medium.
- **Local free space:** the copy is encrypted into a staging file under the set's `local_path`
  (the `.moves` directory) and the staged file is what is uploaded, so you need room for a
  ciphertext-sized file there while a copy is in flight. The staging file is removed whether
  the upload worked or not.
- **Time:** one extra pass over the artifact on the way out.
- **Memory:** designed to stream, so it does not grow with the artifact. I have not measured
  throughput on any hardware, and the multi-gigabyte memory budget test has not been run by me.

## What this does not do yet

- No progress stages for "Encrypting" or "Decrypt-verifying". A run reports its existing
  stages, and a decrypt-verify is reported as a scrub.
- No external key provider that can write copies (above), and no KMS or Vault.
- No retroactive encryption of copies that already exist in plaintext, and no re-encryption
  of old copies after a rotation.
- No `retnd` command that decrypts a copy for you.
- The alert kind for an unresolvable `required` key is defined, and nothing raises it yet. You
  see the refusal and the "Key missing" state on the copy instead.

# Recovery: retained backup data leaving through a new door

Until EPIC Q, no route in this product returned a retained backup's bytes. A
backup went in over SFTP, was verified, was kept, and came back out only by an
operator reaching the storage underneath it by hand. `retnd recover` and the
recovery routes are the first way the product itself hands that data back, so
this is a statement of what the new door lets through and what it does about
it. It is written as claims about the shipped code, each with the check that
holds it.

## What leaves

The bytes of one artifact, to exactly one of three places the requester named
when the grant was minted:

- the HTTP client that redeems the grant (`download`),
- a new file under the configured recovery staging root (`staging`),
- another storage destination the operator has already configured (`storage`).

Nothing else. A recovery returns no credential, no key, no source host and no
path from outside the staging root: its responses carry the artifact id, the
copy's medium id, a size, a SHA-256 and, for a staging target, the path under
the staging root that the requester themselves chose. A failure carries a
sentence this product wrote, and the underlying cause goes to the engine log
only, never to a response, the `--json` output or the activity feed
(`TestAnUnexpectedErrorIsNeverEchoedToTheClient`).

## Who may ask

Every authenticated caller, for every artifact. There is no role on
`capabilities.AuthContext` and this change does not invent one. It is the same
position `PUT .../edit-hold` already documents for itself, and it is paid for
with audit rather than authority:

- a grant is bound to the session that minted it (the actor and the instant that
  authentication lapses), is single-use, lives minutes, and is revoked when the
  session ends;
- every mint, every served byte range and every refusal writes one row to the
  action log naming the actor, the artifact, the copy, the target and the
  outcome (`TestEveryMintAndEveryServedRange...`);
- an authenticated user can therefore not recover something without leaving a
  record, but nothing stops them recovering it. If your threat model needs the
  second half, this is not the control for it, and a role model is a separate
  decision.

## How it is bounded

- **It reads, never writes the source.** The route pair is registered without
  the destructive gate on purpose, because a recovery deletes and overwrites
  nothing. The recovery path has no call to delete an object, to list a
  destination, or to replace anything: an upload target refuses an existing key,
  a staging output gets a unique name, and the destination double used in the
  tests fails the test if `DeleteObject` or `ListObjects` is ever called.
- **It runs no program.** The recovery packages import no `os/exec` and a test
  holds them to it. (A storage medium whose credentials are fetched by a
  configured command is the transport's existing behaviour and is not new here.)
- **It cannot be tricked into sending bad bytes as good.** All of the copy is
  read and compared with the size and SHA-256 recorded at ingestion before any
  byte is delivered. A copy with the right size and flipped bytes fails with
  its own code, and a local and a remote failure read differently in the log.
- **It spends only when told to.** A retrieval that bills egress, or needs a
  provider restore first, is refused naming the cost unless the request carries
  an explicit acknowledgement, and the refusal starts no provider job.
- **It leaves nothing behind.** In-flight files have exclusive unique names, are
  removed on every failure and on cancellation, are swept after a TTL, and are
  reconciled at engine start. A removal that fails is reported separately from
  a recovery that failed, because one means no bytes and the other means a
  filling disk.

## Where a copy rests while it is being verified

In a private file under the recovery staging root, beside the state directory
(`<state dir>/recovery`), created with mode 0600 inside a directory of mode
0700. It is as sensitive as the artifact itself: it is the artifact. It exists
from the start of a recovery to its end (a download) or until the operator
collects it (a `staging` target, kept for seven days and then swept). If the
backup is encrypted at rest by the operator's own tooling, the staged copy is
whatever that tooling produced. This product does not decrypt anything.

## What this does not claim

- It does not claim end-to-end confidentiality in transit. The download is as
  private as the connection in front of the engine, which the operator runs.
- It does not claim a staged file is erased from the disk rather than unlinked.
- It does not claim the log holds nothing sensitive: it holds artifact ids and
  medium ids, which an operator already sees.

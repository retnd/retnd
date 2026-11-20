# #1594: a FAILED backup with its own copy at the final name and its source gone

Builds on #1528 (retiring a FAILED backup whose source is gone, PR #1536).

## The shape

#662's shape is a FAILED row whose own durable copy sits at its final name. A row only records
that path when it commits, so the row was a good backup once. Something quarantined it, an
operator retried it, the retry sent it back to DISCOVERED, and the next transfer met the FR-12
collision guard on its own file and landed in FAILED.

#1528 left this shape alone on purpose: `retry` rescues it. Retry compares the file with the remote
object (`completeIngestionInPlace`), and if the two match it trusts the copy through the
reinstatement path. So `retire` refused the shape with `RETIRE_OWN_COPY_AT_FINAL_NAME`, "retry
instead", and nothing overrode that.

Once the host deletes the source, that rescue is gone. The remote hash call fails, retry falls
through to an ordinary retry, the collision guard sends the row straight back to FAILED, and
retire keeps refusing. The set reads FAILING forever, which is #1528's bug again.

## What I weighed

1. **Retire it, with an acknowledgement, when the final-name copy verifies.** I threw this one
   out. A copy that verifies is a good backup, and retiring it stops counting a restore point.
   Nothing is deleted, but the set would read as if the backup were lost when it isn't.
2. **Complete it from the copy it already has, when the copy verifies.** This is the right answer
   for a good copy. "Verifies" means the evidence `quarantine reinstate` already accepts: the file
   still hashes to what was recorded when it was verified, or the set's validator runs and passes
   on it now. That's no new trust rule. Going through the reinstatement path means the remote
   delete is forfeited forever, which costs nothing here because the source is gone anyway.
3. **A clear refusal that names the next step.** This is the right answer when the copy can't be
   verified. Sending the row round an ordinary retry is the loop, so retry should say no, write
   nothing and say why.

Option 3 alone would still leave a dead end for a copy that really is bad (bit rot, or #662's
literal empty record over a file nobody can vouch for any more). That backup is lost, and a set
with newer good backups shouldn't read FAILING over it forever. That's exactly the case #1528
retires, except here there's a file on disk. So retire takes it, but only with an explicit
acknowledgement, and the file stays where it is.

## The behaviour

None of this applies unless the source is **confirmed gone**: the same fresh listing (identity by
basename) plus a `Stat` classified `not_found` that #1528 uses. With the source still there, retry
compares against it exactly as before. With the source not answering, nobody knows it's gone, so
nothing changes either.

With the source confirmed gone:

| The copy at the final name | `retnd retry` | `retnd retire` |
|---|---|---|
| still matches its recorded hash, or the set's validator passes on it | kept through the reinstatement path, back to the durable state it was quarantined from (REMOTE_RETAINED or COMMITTED) | refused, `RETIRE_OWN_COPY_AT_FINAL_NAME`, no override: retry keeps it |
| doesn't match, or nothing could have failed (no recorded hash and no validator) | refused, `RETRY_OWN_COPY_UNVERIFIED` (409), writes nothing, names both ways out | without the acknowledgement: `RETIRE_OWN_COPY_AT_FINAL_NAME`, overridable with `accept_unverified_copy`; with it: retired as usual (successor rules still apply), the file is kept |

The preview without the acknowledgement doesn't read the file. The backup page asks for that
preview every time it shows a FAILED backup, and hashing a multi-gigabyte dump on every page load
isn't acceptable. So the plain preview says "own copy at the final name, source gone, retry keeps
it if it still verifies, or retire with `--accept-unverified-copy`", and only a request that carries
the acknowledgement (or a retry) checks the copy. If the copy turns out to verify, the
acknowledged request is refused with no override and points at retry.

The automatic pass never retires this shape. It skips own-copy rows before it asks the source, so
it doesn't spend a listing on them, and it never reads the file. Whether a copy that can't be
proven is worth anything is a person's call.

Agents: `accept_unverified_copy` joins `accept_source_present` and `no_successor` as a field an agent
may not send, refused with `AGENT_RETIRE_ACK_REFUSED` by the proposer (in any letter case) and again
on replay. An agent's approved retry is replayed through the same handler, so it reads the same
`RETRY_OWN_COPY_UNVERIFIED` a person does.

## The surfaces

- CLI: `retnd retire --accept-unverified-copy`, and `retnd retry` prints the refusal and exits 1.
  When retry keeps the copy on local evidence it says so, rather than claiming it compared against
  the remote.
- API: `accept_unverified_copy` on `RetireArtifactRequest` and as a query parameter on the preview;
  `RETRY_OWN_COPY_UNVERIFIED` declared on `POST /backups/{source}/{set}/{name}/retry`;
  `overridable_with` gains `accept_unverified_copy`. cliecho echoes the flag on both routes.
- Web UI: the Recovery card keeps Retry on for this shape and says retry keeps the copy if it still
  verifies; a refused retry shows the engine's sentence; the Retire dialog offers the
  unverified-copy acknowledgement and, if the copy turns out to verify, shows Retry instead.

## Left as it was

- A retired own-copy row keeps its `local_path`, and FAILED and RETIRED both count as holding no
  local copy for the capacity guard. That was already true of the FAILED row; retiring it doesn't
  make it worse. Retention never prunes the file. The operator who acknowledged it owns it now,
  and the retirement's transition detail names the path.
- If the source comes back, #1528's way back puts the row into DISCOVERED, the transfer meets the
  collision guard, and the row is the source-present #662 shape again, where retry compares against
  the remote.
- A copy that doesn't match the remote while the source is still there is still a loop (retry
  compares, falls through, collides). That's a different shape from this issue and needs its own.

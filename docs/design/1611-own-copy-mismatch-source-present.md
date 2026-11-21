# #1611: a FAILED backup whose own copy doesn't match a source that is still there

Builds on #1594 (the same row with the source gone, PR #1598).

## The shape

#662's shape again: a FAILED row whose own durable copy sits at its final name. This time the source
is still on the host, and the copy doesn't match it. Two ways to get there: the host rewrote the
object after the copy was made (the copy still matches what was recorded, the source moved on), or
the copy is damaged (it no longer matches what was recorded either).

Retry compares the copy with the remote object (`completeIngestionInPlace`). When they match it keeps
the copy. When they don't, it used to fall through to an ordinary retry: the row goes to DISCOVERED,
the next transfer meets the FR-12 collision guard on the row's own file, and the row is FAILED again.
The same three lines in the log every cycle, and nothing for the operator to act on.

## What I weighed

1. **Replace the copy from retry** (write `.partial`, verify, rename over). I threw this out. The copy
   may be the only record of what the source held when it was backed up, if the host rewrote the
   object, and a rename over it is a delete. The collision guard is right to refuse that, and #662
   says so.
2. **Move the copy aside from retry and fetch fresh.** Same outcome as the refusal below, without the
   operator's say, and it needs a place for the aside file that retention and the capacity guard
   know about. That is a custody decision, so it isn't in this PR. If it's wanted, it stacks on the
   refusal as a follow-up.
3. **Refuse by name, say what was found and the way out.** This is the one I built. Retry writes
   nothing and answers `RETRY_OWN_COPY_MISMATCH` (409). The operator moves the file aside by hand
   (it is kept, nothing here deletes it), and the retry after that is an ordinary one: the stat of the
   final name fails, so retry skips the own-copy path, the row goes to DISCOVERED and the next cycle
   fetches a fresh copy. A test walks exactly that, and checks the aside file is untouched.

## The behaviour

Only a definite mismatch is refused: the source answered the checksum request and the answer differs
from the hash of the file at the final name. Everything else is as it was:

| Situation | `retnd retry` |
|---|---|
| copy matches the remote | kept in place through reinstatement (#662) |
| copy differs from the remote, source answered | refused, `RETRY_OWN_COPY_MISMATCH`, nothing written |
| source gone (confirmed) | #1594: kept on local evidence or `RETRY_OWN_COPY_UNVERIFIED` |
| the remote can't give a checksum (not answering, hash unsupported) | ordinary retry, as #1594 pinned |

The last row can still loop if the source keeps not answering. Nobody knows what the source holds
then, so there is nothing true to refuse on, and it clears when the source answers. I left it alone.

The sentence says one measured fact about the local disk: whether the copy still matches the checksum
recorded when it was verified ("so the object on the source has changed since"), doesn't match it ("so
it may be damaged"), or has none recorded. It never quotes the remote object's checksum: that is the
source's string. It does name the local final path, which is the file the operator has to move and
which ends in the artifact's name, like #1594's sentence does.

## The surfaces

- CLI: `retnd retry` prints `refused (RETRY_OWN_COPY_MISMATCH): ...` and exits 1, through the refusal
  printer #1594 added.
- API: `RETRY_OWN_COPY_MISMATCH` declared on `POST /backups/{source}/{set}/{name}/retry` (409). An
  agent's approved retry is replayed through the same handler and reads the same code.
- MCP: no new route and no new field, so the catalogue guard has nothing to add; the retry tool
  already carries the 409.
- Web UI: the Recovery card says what retry does for this shape (it was falling back to the generic
  "not quarantined" paragraph) and the refused retry shows the engine's sentence. The retire refusal's
  sentence stops promising that retry keeps the copy: it only does when they match.

## Left as it was

- Retire still refuses this shape while the source is there. Retiring a backup whose source can still
  be fetched isn't the answer; moving the copy aside and retrying is.
- The collision guard is untouched.

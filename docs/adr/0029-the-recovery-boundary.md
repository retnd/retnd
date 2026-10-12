# ADR 0029: The recovery boundary, how it is authorised, and where the old "no download" claim went

- Status: accepted for the documents, and **depends on the code** in EPIC Q Phase 1
  (#976 and #983 for the recovery path, #978 and #980 for what scrubbing records, #982 for the
  configuration). Until those merge, this ADR describes a behaviour that is specified and not
  yet shipped, and the pull request that carries it says which sentence waits on which.
- Date: 2026-10-10
- Scope: EPIC Q (#953), FR-67. Recorded by Q2.4 (#987).

## Context

For as long as this product has had documents, it has told operators that it will not do
the thing EPIC Q does. `docs/recovery.md` called restoring an artifact "not an automated
command" and "the permanent shape rather than a workaround". `docs/site/reference.html` said
there was "no restore here and no download", twice, and a third time in the note about the
two restore verbs. `docs/site/index.html` listed "no restore execution" under what has not
been proven, and the store listing copy said it "does not restore into production for you".

That is not a sentence to edit, it is a promise to reverse. Four decisions had to be made
before the reversal could be written down honestly.

1. **Where the boundary is.** An operator who reads "it does not restore" and then finds a
   download button will assume one of the two statements was a mistake. The boundary has to be
   drawn so both are true.
2. **How a read path that hands out backup data is authorised, in a tree with no roles.**
   `capabilities.AuthContext` carries `Authenticated`, `Username`, `Mode` and
   `SessionExpiresAt` and no authority (`apps/common/platform/capabilities/capabilities.go:85-90`).
   `core/service/edithold.go:34-42` already states the consequence for its own route: any
   authenticated caller can hold any backup set.
3. **Which route it is on.** `POST /api/v1/operations` is behind `requireCSRF,
   requireDestructiveGate(gate)` (`apps/common/webhost/router.go:395`), and the only gate
   this repository ships is `NotYetImplementedGate`, whose `Passed()` returns false with no
   flag that can change it (`apps/common/webhost/gate.go:111-114`).
4. **What it is called.** The word "restore" already names two different commands.

## Decision

**The boundary sentence is the whole of the decision's public face:** this product recovers
and exports the retained artifact; restoring it into an application remains the operator's
responsibility. Recovery gives back the bytes, checked. It never runs a program against them
(no subprocess is started on any recovery path), and it knows nothing about the application
the bytes came from. That is what keeps "does not restore" and "has a download" both true.

**Recovery is its own pair of routes, outside the destructive gate.** A `POST` mints a
grant and a `GET` streams the bytes, both under `requireCSRF` and neither under
`requireDestructiveGate`, in the read tier `handlers_artifacts.go` already uses. A recovery
filed as a seventh `POST /operations` action would have shipped refused on every deployment
this repository can build, and `scripts/e2e/tests-repo.pin` already records the browser cases
that fail on exactly that 403 for the existing actions. The gate stays shut and #92 stays its
own work: nothing here implements, weakens or works around it.

**The answer to "no role" is audit, not authorisation.** A grant is short-lived, single-use,
bound to the session that minted it, and dead when that session ends. Every mint and every
served range writes an action-log row naming actor, artifact, copy, target and outcome. The
consequence is stated out loud in the register `edithold.go` uses, in the docs and in the
privacy disclosure: any signed-in operator can recover any retained artifact. An RBAC model is
an explicit non-goal and not a deferral, because inventing one inside this epic would be a
second authorisation system with one consumer.

**Neither existing verb is renamed or absorbed.** `retnd restore` stays FR-34's archival thaw
and `retnd snapshot restore` stays the repository extract. The new vocabulary is `recover`,
`export` and `scrub`, and "restore" is not used as a label for anything new. The reference
page and `docs/recovery.md` put all three side by side, with the case each is for.

## Where the deleted claim went

When a claim is deleted rather than renamed there is no new name to substitute, so a search for
its words reports itself done while every sentence, picture and fixture built on it survives
(`docs/epic-checklist.md` §9, the #788 lesson). So this records what the claim was split into
and follows each piece, item by item.

**Piece 1: the recovery boundary** (what the product does and does not do with the bytes).

| Where it was | What it said | Where it is now |
|---|---|---|
| `docs/recovery.md`, the opening | "restoring an artifact is not an automated command ... the permanent shape" | The boundary sentence, then the three verbs side by side, then a section on `retnd recover` |
| `docs/site/reference.html`, the Backups note | "no restore here and there is no download" | The boundary sentence, and the **Recover artifact** action it now sits beside |
| `docs/site/reference.html`, the Backup detail note | "no download and no restore button to be missing" | The boundary sentence, and why reading the screen changes nothing while the export is a separate, asked-for action |
| `docs/site/reference.html`, the "two different things are called restore" note | "neither one restores an artifact into an application ... deliberately does not carry it out" | Replaced by the three-verbs table at `#cli-restore-verbs` |
| `docs/site/index.html#honest` | "There is no restore execution" | The boundary sentence, and a separate bullet for what no recovery has been measured on |
| `docs/submission/description.md` | "does not restore into production for you ... tells you which local artifact is trustworthy" | The boundary sentence, in the store listing's own register |
| `README.md` | (silent: it only pointed at `recovery.md` for the 3am case) | A paragraph for the person who needs the bytes out, naming all three verbs |

**Piece 2: the three-claim assurance model** (the old claim let "verified" mean one thing,
because nothing could be recovered, so nothing needed saying about what verification proved).

| Where | What landed |
|---|---|
| `docs/recovery.md` | Stored, Content verified and Recovery tested as three separately-aged claims; the eight not-a-pass states; what a scheduled pass can and cannot reach; configuration resolution |
| `docs/recovery-without-a-terminal.md` | Section 5, the browser-side reading of each failure shape |
| `docs/storage-mediums.md` | A scrub is an existence check against a medium; an archive copy is `requires_restore`, never thawed on a timer |
| `docs/incremental-runbooks.md` | A `kopia` set's claim is its existing achieved level; there is no second drill |
| `docs/site/reference.html` | The Backups note names the three claims |
| [ADR 0030](0030-three-claims-and-existence-is-not-content.md) | The decision itself |

**Piece 3: the explicit non-goal about application restore** (the half of the old claim that
is still true).

| Where | What happened |
|---|---|
| `ui/shared/src/pages/BackupsPage.tsx`, the table footer (formerly "does not perform application restore") | Changed by the UI work in #985: the footer now reads the boundary sentence itself ("This product recovers and exports the retained artifact; restoring it into an application remains the operator's responsibility."), and the reference note quotes the same sentence |
| `docs/design/Backup Manager.dc.html` | Left alone: a design record, "deliberately not kept in step with later work" (`docs/epic-checklist.md` §5) |
| `docs/EPIC-E-alternative-storage.md` ("not a restore browser ... a full restore workflow remains future work") | Left alone: it is that EPIC's own scope statement, dated to it, and still accurate about what EPIC E built |
| `docs/runtime-contract.md` | New section on validators and recovery staging, because a recovery test is the first thing that runs an operator's executable against recovered data |
| `docs/compliance/privacy-policy.md` | New subsection: recovery is the one feature that moves retained data out of the deployment, and to whom |

**What was deliberately not swept.** Screenshots, GIFs and the `docs/submission/` captures are
Q2.5's. This ADR makes no provider-hardware claim: `index.html#honest` says none has been
measured, which is the true statement until somebody measures one.

## Consequences

- The reference page's command table gets `recover`, and `scrub`, when those verbs land,
  from the pull requests that land them. `distribution/packaging/site_reference_test.go`
  holds that table to the binary's dispatch table in both directions, so adding a row early
  would fail the build. The prose around the table does not wait.
- Every sentence in these documents that describes `recover`, `scrub`, the `assurance:`
  block, `next_due_at`-driven due-ness or the browser actions is a statement about code in
  another pull request. They are written against the specifications and the drafts of those
  pull requests, and the one that merges last owns re-reading them against the real thing.
- The grant model is audit-shaped, so it is only as good as the action log's retention. That
  is a deployment fact an operator should know, and it is in the privacy disclosure.

## Alternatives considered

- **Recovery as a `POST /operations` action.** Rejected: it ships refused (see above).
- **A role model.** Rejected as out of proportion: it would be a second authorisation system
  whose only consumer is this feature, and the audit trail already gives an after-the-fact
  answer to who took what.
- **A backend-signed direct URL.** Rejected: it moves the bytes out of the process that
  audits and budgets them.
- **Renaming `restore` to `thaw`.** Rejected: FR-34 and every existing runbook name it, and a
  rename is a breaking change with no operator benefit.
- **Softening the old claim** ("restoring an artifact is not usually automated"). Rejected:
  the acceptance criteria say the old claims are gone, not softened, because a softened
  promise is one an operator cannot tell is reversed.

## Addendum, 2026-10-11: a download is streamed, not staged

The review of the open EPIC Q pull requests put two questions to the owner, and the answers
change how the bytes reach a browser. They do not change the boundary sentence, the routes,
the grant or the audit.

**A download is streamed, not staged, and can be resumed.** It used to be copied whole to the
staging disk, hashed there and only then sent, which cost a staging disk per download, a wait
for the whole read before the first byte, and a transfer that could not be resumed. It is now
read from the retained copy one verified 4 MiB chunk at a time. Memory is a chunk, nothing is
staged, and `ETag` (derived from the artifact id and the recorded digest, the same for every
grant) with `Range` and `If-Range` resumes under a new grant. A staging or storage target is
unchanged and still stages, because its promise is that nothing is placed until the whole
copy has matched.

**A download that cannot complete is refused before it starts.** The tension was that "prove
the download will complete" cannot be met by hashing the copy before sending it without
staging it. It is met by hashing the copy once, keeping only a digest per chunk, and checking
the stream against those digests: a pre-flight (the copy is there, is the recorded size, its
medium is readable, and one verifying read matched the recorded SHA-256) before the status
line, every chunk checked before any of it is sent, a stop at the first bad chunk, and an
aborted connection short of its `Content-Length` rather than a clean end. What it proves is
that the bytes were the recorded bytes when the digests were made and are, chunk by chunk, as
they are sent. It does not prove the copy cannot rot between the preparation read and the
stream (the per-chunk check bounds the loss to the chunk that failed), that the client
stays connected, or that a provider keeps answering. The cost is that the first download of a
billed remote copy reads it twice, once to prepare and once to send.

Rejected: sending first and verifying at the end (a corrupt copy would cost the client's
whole bandwidth before anyone knew), and keeping the staged download with a bigger budget
(it moves the cost to the disk and still cannot resume).

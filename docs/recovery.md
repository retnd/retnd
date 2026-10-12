# Recovery and the restore procedure

This is the page to read when a backup didn't arrive, an artifact looks wrong, or you're
trying to figure out whether you can still get a file back.

It was written when this product had no operator commands at all, and it works entirely
against the SQLite journal and the NAS filesystem. That is still the ground truth, and it
is still the right thing to read at 3am when you do not trust a summary — but it is no
longer the only interface. `retnd status`, `retnd sources`, `retnd artifacts`,
`retnd activity`, `retnd validate`, `retnd retention`, `retnd quarantine`,
`retnd retry` and `retnd catalog rebuild` all exist and answer most of the questions
below without a SQL prompt; [the reference
page](https://retnd.github.io/retnd/reference.html#cli-commands) has every one
of them. Where a query below and a command disagree, the query is right about the journal
and the command is right about what the serving process believes, and the difference itself
is a finding.

**This product recovers and exports the retained artifact; restoring it into an
application remains the operator's responsibility.** `retnd recover` (and **Recover
artifact** in the browser) reads one retained copy, proves the bytes are the ones the
journal recorded, and hands them to one place you named. What it never does is run
anything against those bytes: putting a database dump back into a database, or an image
back onto a hypervisor, is still yours to do, and the rest of this page is written for
the day the product is not in a state to help you do even the first half. (A backup set on
the incremental engine has a verb of its own, `retnd snapshot restore`, and the next
section puts all three side by side.)

> **No terminal?** Everything below assumes a shell on the NAS. If you have only the web
> interface, which is the normal case on a NAS appliance and the case every provider store
> assumes, read [recovery without a terminal](recovery-without-a-terminal.md) instead. It
> covers the same three failures this page starts with, through the interface, and it is
> the page the submission bundle's support materials point a reviewer at.

> **Is this backup set on the incremental engine?** This page is about
> **artifacts**: whole files, one per backup, sitting on storage where the
> journal says they are. A backup set whose `engine` is `kopia` has no
> artifacts at all — it has snapshots inside an encrypted repository, and
> nothing on this page applies to it. Read
> [incremental-runbooks.md](incremental-runbooks.md) instead: it covers a
> repository that will not open, a snapshot that verified and one that did
> not, credential recovery, and getting data back out with `retnd snapshot
> restore`. The set's `engine:` key in `config.yaml` says which engine it
> runs, as does `engine` on `GET /api/v1/backup-sets/{source}/{set}` and the
> badge on its page in the web interface. (`retnd sources` does not report
> it.)

## Three verbs that sound alike, side by side

There are three commands in this product that an operator at 3am could reasonably mistake
for each other, and picking the wrong one costs either money or time. They are not
renamed and none of them absorbs another one. The new one is called `recover` on
purpose: the other word already means two different things here.

| | `retnd recover` | `retnd restore` | `retnd snapshot restore` |
|---|---|---|---|
| What it does | Reads one retained copy of an **artifact**, checks it against the digest the journal recorded, and delivers it to one target | Asks a storage provider to make an **archived object** readable again (FR-34's thaw) | Extracts a **snapshot** out of an encrypted repository into a local directory |
| Applies to | Artifact-engine backup sets, any copy that can be read | A copy sitting in an archive storage class (`requires_restore`) | Backup sets whose `engine` is `kopia` only |
| Writes bytes | Yes: to a browser download, a path under the staging root, or another configured destination | No, nothing anywhere; it changes what the provider will let you read | Yes: into a directory you name on the engine host |
| Costs money | Only if the copy is chargeable to read, and then it refuses until you pass `--acknowledge-cost` | Always; it is billed and takes hours, so it refuses without `--acknowledge` | Reads the repository; no thaw |
| Run it when | You need the file and the normal route to it (the share, the NAS file manager) is gone or not trusted | The copy you need is archived and you have decided to pay to read it | A `kopia` set, and you need files back |

If the artifact is plainly on the NAS's disk and you can reach the share, copying it out of
the share is still the fastest recovery there is, and nothing here asks you to do it any
other way. `recover` is for the cases where that is not available, where you want the bytes
re-checked on the way out, or where you are in the browser and have nothing else.

Neither `recover` nor `restore` puts anything back into an application. Where the two meet
an archived copy, the order is: `restore` first (and wait), then `recover`. A scheduled
scrub never does the first step for you, which the cost section below explains.

## The one fact everything else depends on

The journal is a plain SQLite file at whatever path `state.database` names in your config
(`core/internal/config/testdata/full.yaml` shows the shape). Query it with the `sqlite3` CLI,
no Go required:

```bash
sqlite3 /path/to/state.db ".schema artifacts"
```

The columns that matter for recovery: `source`, `backup_set`, `artifact_name`,
`remote_path`, `local_path`, `state`, `updated_at`, `remote_hash`, `local_hash`,
`retry_count`, `last_error`, `remote_deleted_at`, `remote_delete_error`. The full schema is
`core/migrations/0001_init.sql` plus `core/migrations/0002_quarantined_lost.sql`.

## Step 1: is this actually broken?

There's no `status` command to answer this yet, so answer it directly. For one backup set:

```bash
sqlite3 /path/to/state.db "
  SELECT state, count(*), max(updated_at)
  FROM artifacts
  WHERE source = 'production' AND backup_set = 'postgres-primary'
  GROUP BY state
  ORDER BY max(updated_at) DESC;
"
```

What `retnd status` reports, and what `core/internal/health` decides it from:

- If the newest row across the whole set is `COMMITTED`, `REMOTE_DELETE_PENDING`,
  `COMPLETE` or `REMOTE_RETAINED`, and it's recent enough for your `stale_after` window,
  that's a healthy set. Nothing to do.
- If there's no row in one of those four states inside the freshness window, treat it as
  stale: something has stopped landing new backups for this set, whether or not any single
  attempt has technically "failed" yet.
- If any row is `QUARANTINED_LOST`, that's an unconditional alarm regardless of anything
  else in the set, because it means an irrecoverable loss happened. See Step 3.
- If the newest row is `FAILED` with no `next_retry_at`, the retry budget for that attempt
  is exhausted and it needs a human, not another automatic pass. See Step 7 for what that
  human actually does, which until recently was nothing this product offered.

## Step 2: finding a restore point

Only four states are ever a valid restore point. This isn't a convention this document is
inventing, it's the exact set `core/internal/health/compute.go` calls `knownGood`:

- `COMMITTED`
- `REMOTE_DELETE_PENDING`
- `COMPLETE`
- `REMOTE_RETAINED`

`REMOTE_RETAINED` is the one to check for first if the backup set is declared `read_only`,
because it is the only one of the four those artifacts ever reach: FR-15's delete step is
never offered them, so they stop at a terminal state of their own instead of moving on to
`COMPLETE`. A query that leaves it out answers "this set has no restore points at all" for
a set that is working exactly as declared.

Everything else is not a restore point, with no exceptions:

- `DISCOVERED`, `TRANSFERRING`, `TRANSFERRED`, `VERIFYING`, `VERIFIED`, `COMMITTING` all
  mean the durable commit (fsync, atomic rename, directory fsync) hasn't happened yet.
- `FAILED` and `QUARANTINED` mean something is actively wrong with this attempt.
- `QUARANTINED_LOST` means the loss already happened and is permanent (Step 3).
- Any file on disk with a `.partial` suffix, no matter what the journal says about a
  *different* row, is disposable by design (FR-12) and must never be treated as a restore
  point, even if it looks complete. If you're ever tempted to grab a `.partial` file because
  nothing else looks recent enough, stop and read Step 4 instead: that's a stale or stuck
  set, not a missing-but-actually-there backup.

To find the actual restore point:

```bash
sqlite3 /path/to/state.db "
  SELECT artifact_name, local_path, state, updated_at
  FROM artifacts
  WHERE source = 'production'
    AND backup_set = 'postgres-primary'
    AND state IN ('COMMITTED', 'REMOTE_DELETE_PENDING', 'COMPLETE', 'REMOTE_RETAINED')
  ORDER BY updated_at DESC
  LIMIT 1;
"
```

The `local_path` in that row is the file. It was fsynced and atomically promoted to that
name by `core/internal/lifecycle/commit.go` before `COMMITTED` was ever recorded (see the
`core/internal/lifecycle/commit.go`'s own doc comment), so treat it as trustworthy on the
strength of that alone; you don't need to re-verify it before copying it out, though
re-running whatever validator the backup set's config names is never wrong if the stakes
are high enough to justify the time.

Copy it wherever the restore actually needs to happen. There is no `retnd restore`
command to do this for you; a plain `cp`, `scp`, or whatever your restore target needs is
the entire remaining procedure once you have the right path.

## Step 3: the newest good row is `QUARANTINED_LOST`

`QUARANTINED_LOST` is reachable only from `COMPLETE`, the one state that confirms the remote
copy was already deleted, so by the time an artifact reaches it the manager believes there is
no copy anywhere: not on the remote (already deleted), not intact locally (that's why it's
here). Nothing automatic ever routes an artifact back out of it
(`core/internal/lifecycle/machine.go`).

**Check the obvious thing first, because the finding itself can be wrong.** The local check
that put the artifact here fails identically for "the bytes are corrupt" and "the volume was
not mounted when the check ran", and an unmounted volume takes every `COMPLETE` artifact in
the backup set down with it. If the file is there and reads correctly now, ask the manager to
re-check it and trust it again rather than treating this as a loss:

```
POST /api/v1/quarantine/<source>/<set>/<name>/reinstate
```

It re-runs the checks itself and only moves the artifact if they pass and the evidence is
conclusive (a recorded hash the copy still matches, or the backup set's validator running and
passing now). A reinstated artifact goes back to `COMPLETE` and counts as a restore point
again. See `docs/adr/0004-reinstating-a-quarantined-backup.md`.

If the local copy really is bad, the backup is gone. What to actually do then:

1. Query for the next-newest row in `COMMITTED`, `REMOTE_DELETE_PENDING`, `COMPLETE` or
   `REMOTE_RETAINED` for the same backup set (same query as Step 2, drop the `LIMIT 1` and look further down, or
   add `AND state != 'QUARANTINED_LOST'` if it's mixed in with rows you do want). Restore
   from that instead.
2. Treat the gap as a real, permanent loss of that specific restore point when you report
   this, not as "we're still working on retrieving it." Nothing is retrieving it.
3. If your retention window means the previous good backup is now further back than
   whatever RPO you're working against, that's the actual incident: the corruption plus
   the already-completed remote delete together cost you the interval between the two
   restore points. Say so plainly rather than letting "we have a backup" imply "we have as
   recent a backup as you think."

## Step 4: the newest row is `QUARANTINED` (not `_LOST`)

This is different and better: the remote copy may still exist. `QUARANTINED` is reachable
from `VERIFYING` (a hash mismatch or a failing validator caught it before commit), from
`COMMITTED`, or from `REMOTE_DELETE_PENDING` (reconciliation found the durably committed
local copy had gone bad after the fact, while the remote side was still there or unconfirmed
gone). Its one exit is back to `DISCOVERED`, meaning a fresh attempt has a real chance of
succeeding.

This self-heals the next time discovery and reconciliation run against this backup set,
which `retnd daemon` does on the poll interval and `retnd reconcile` does on demand.
On a deployment with nothing serving it, that pass does not happen on its own. Your options, in order of how much you should trust the result:

1. If you or someone else has already wired a runner against these packages (calling
   `discovery.Discover`, `reconcile.Reconcile`, and the `core/internal/lifecycle` steps
   yourself), run it against this backup set.
2. Otherwise, fetch the artifact by hand: `sftp` in with the same key and `known_hosts`
   `docs/ssh-setup.md` describes, confirm it's still there under the `remote_path` the
   journal recorded, and pull it down. Re-run whatever the backup set's `validation.command`
   names against the file before trusting it, since that's the check that flagged it in the
   first place.
3. Don't manually flip the journal row's `state` column back to `DISCOVERED` by hand unless
   you understand exactly what `core/internal/state/journal.go`'s idempotency-key scheme expects;
   an ad hoc `UPDATE` can leave `state_transitions` and `artifacts` disagreeing in a way the
   append-only log was specifically built to prevent.

## Step 5: a row has been sitting at `REMOTE_DELETE_PENDING` for a long time

Check `remote_delete_error` on that row before assuming anything is stuck:

```bash
sqlite3 /path/to/state.db "
  SELECT artifact_name, remote_path, updated_at, remote_delete_error
  FROM artifacts
  WHERE state = 'REMOTE_DELETE_PENDING'
  ORDER BY updated_at ASC;
"
```

If `remote_delete_error` is non-empty, this is very likely not a bug. The reason is the TOCTOU protection on delete
(`core/internal/model/identity.go`): against the
shell-less SFTP account this project's own setup guide recommends, `CompareIdentity` can
usually only reach `ConfidenceWeak` on the remote side, because there's no remote hash and
usually no backend-stable identifier to check against, only size and modification time. A
weak-confidence comparison always preserves the remote object rather than deleting it, per
the rule at the top of the README. So a persistent refusal here means: the remote copy is
still sitting on the source server, unpruned, on purpose, and it will very likely stay that
way for every backup in this deployment shape, not just this one artifact.

That's a real operational consequence, not a cosmetic one:

- The backup itself is not at risk from this. If anything, an unpruned remote is an extra
  copy you didn't have to ask for.
- **The remote source disk is.** If nothing else prunes it (a separate retention job on the
  producer side, manual cleanup, a shorter retention window configured at the source), it
  will fill up on a long enough timeline, in every deployment that follows this project's
  own hardening advice. Monitor remote disk usage independently of this project; don't
  assume `retnd` is freeing space on the source just because backups keep landing
  successfully on the NAS.
- If you need remote pruning to actually happen in this deployment shape, the honest options
  are: relax the SFTP account's hardening to allow a remote hash command (trading delete-
  safety confidence for automatic pruning), add a backend-reported stable identifier the
  comparison can use instead of a hash, or prune the remote through some mechanism entirely
  outside this project's control (a retention job on the producer side that doesn't depend
  on this project's confidence bar). Weakening `CompareIdentity`'s bar itself is not a
  fix worth taking without a new ADR; it exists at that bar on purpose.

## Step 6: retention decided this backup should be deleted, but it's still there

That's expected, not a bug. A verdict and a deletion are two different things here, and
nothing crosses between them on its own. [`docs/storage-mediums.md`](storage-mediums.md) and `retnd retention` are the longer
version:

- `core/internal/retention.GFSDecide` only classifies artifacts into keep/not-kept-by-GFS. It
  contains no deletion code at all. A `Keep: false` verdict is a candidate, not an order.
- Deletion is real, and it is deliberately somewhere else. FR-20 (issue #21) landed in
  `core/internal/retention/prune.go`: `PruneApply` removes the positively identified local
  file, and since #239 the object on a storage medium beside it. FR-19's last-known-good
  protection (issue #20) landed with it and does protect the newest good backup.
- Nothing schedules that. The only thing that runs `PruneApply` is the API's retention
  preview/apply pair (`core/service`): `PreviewRetention` issues a `plan_id`, and
  `ApplyRetentionPlan` deletes only against that `plan_id`, and only while the plan it
  re-derives still matches the one an administrator reviewed. No cycle, no daemon and no
  timer ever calls it, so local disk usage grows until somebody applies a plan.
- `retnd retention` is a preview in both of its modes and deletes nothing, with or
  without `--dry-run`. That is not a gap waiting to be filled: a CLI that deleted backups
  without the `plan_id` confirmation the HTTP path insists on would be a second, weaker
  authorisation path to the same act (issue #431).
- `retnd retention apply <source/backup-set> --acknowledge` is the terminal's own
  way in (issue #602), and it is not that second path: it goes through the same
  `PreviewRetention`/`ApplyRetentionPlan` pair, prints the plan it is about to apply, and
  refuses with `RETENTION_PLAN_STALE` and zero deletions if the set moved in between.
  `--acknowledge` is required, and the refusal without it says what it consents to.

So if a file survives a `Keep: false` verdict, the question is who was supposed to apply the
plan, not whether deletion works. The verdicts above are the source of truth for "what would
be safe to remove"; applying them is somebody's deliberate act, through the API or through
that verb.

One thing to know before reading a preview taken AFTER an apply: deleting a file does not
change the journal, so `retnd retention` goes on listing a pruned artifact as
`DELETE` (it reads FR-18/FR-19 classification and never looks at the disk), while the API's
own preview reports `REFUSE` for it, because FR-20's checks stat the path and find nothing
there. Both are describing the same backup set; only one of them has looked.

## Step 7: the newest row is `FAILED` and nothing is happening

`FAILED` means an attempt did not finish. It does **not** mean the backup is bad, and it is
not the same thing as quarantine: quarantine is where a backup waits for somebody to decide
whether it can be trusted, and `FAILED` is where one stopped part-way.

Nothing moves it on its own, and that is deliberate rather than an oversight. Re-running the
attempt means re-running the transfer, which for most backups means downloading the whole
thing again, and there is nothing recorded on a failed row that says whether the cause has
cleared. A manager that guessed would spend that download on a coin flip. So it waits for
you.

First, read why:

```
retnd artifacts production/postgres/dump-2026-09-04.zst
```

The `reason` line is the literal sentence the manager recorded at the moment it gave up.
Three shapes come up most:

- **A transient failure that ran out of attempts** ("copy failed: transient: ..."). The
  source was unreachable or the link dropped. If it is back, there is nothing else to fix.
- **A final-name collision.** A file is already sitting where this backup's final copy
  belongs. Move or remove it first; a retry re-checks before it copies a byte, so retrying
  without dealing with it costs nothing and changes nothing.
- **A hash policy the backend cannot serve** ("hash verification required (sha256) but the
  backend could not supply a comparable remote hash"). This one will not clear on its own,
  and a retry against an unchanged configuration will download the whole backup again and
  reach the identical verdict. Change `validation.hash`, or configure an application
  validator, *before* you retry. See `docs/ssh-setup.md`: a hardened SFTP account cannot
  compute a remote hash at all, by design.

Then put it back into the pipeline:

```
retnd retry production/postgres/dump-2026-09-04.zst --note "the NAS came back"
```

That moves the row from `FAILED` to `DISCOVERED` and the next cycle picks it up like any
other new backup. The note is recorded with the transition, so if the same backup fails
again, whoever looks next can see what was tried. The count of attempts spent on the backup
goes up by one, which is the same counter a release from quarantine moves, so a backup that
keeps coming back here is visible as one rather than looking like a fresh failure every
time.

It is safe from every way a backup can reach `FAILED`, and the reason is worth knowing:
`FAILED` is only reachable *before* the local copy is committed, so the remote source has
never been deleted and is presumptively still there to fetch again. The retry touches no
remote object and removes no durable copy.

The one thing it refuses is a backup whose backup set is no longer in the configuration.
Sending it back to a set no cycle walks would leave it somewhere nothing picks up and no
recovery path reaches. Create a backup set with the same source and name first.

## Step 8: a backup set is refusing to run and says its workflow needs recovery

This one is not about an artifact. Nothing in `artifacts` is wrong, and every query above
will report the set as healthy right up to the moment its next backup does not happen.

What you see is a refusal: a manual run of the set is refused, the scheduler stops visiting
it, and a `WorkflowRecoveryRequired` condition is raised naming the set and the run. Any
command that opens the data plane — `retnd fetch`, `retnd daemon` — says so once on the
way up, before it does anything:

```
retnd: 2 workflow cleanup(s) from an interrupted run are outstanding; the affected backup sets refuse to run until each is resumed or acknowledged (`retnd workflow recovery show`)
```

`retnd status` will not tell you. It has no workflow section at all; it reports the set's
artifacts, and they are fine. The command that answers is:

```
retnd workflow recovery show
```

One block per outstanding scope: the run id, the backup set, whether it is the global or the
set-scoped half, when that scope was entered and how long ago that is by this host's clock,
where the scripts a resume would execute are retained, and the two commands that end it.
Beside a serving engine it asks that process, because the refusal a run will actually meet
lives in that process's memory as well as in the journal; with nothing serving the
deployment it reads the durable rows instead.

**What it means.** A workflow run has five stages around the backup — global before, this
set's before, the backup, this set's after, global after — and the "after" stages are the
ones that put the machine back: thaw the database, unmount the snapshot, restart what was
stopped. This product records that it has ENTERED a scope before it runs the first hook in
that scope, and then the process died. On the next start, reconciliation found a step still
sitting at `running`, whose exit status nobody observed and nobody ever will, and recorded
it as `interrupted` rather than `failed`. Those are not the same finding: "this script
reported failure" and "this script's outcome is unknown and its side effects may be
half-applied" lead to different next moves, and `core/internal/workflow/states.go` keeps
them apart deliberately. The run moved to `recovery_required`, its captured script bytes
were retained instead of being reclaimed, the backup set was blocked, and **nothing was
replayed**. No hook ran. That is the whole of what the restart did.

So the source machine may be sitting quiesced right now, with a perfectly good backup beside
it. That pairing — a healthy artifact and a machine that was never put back — is why this is
a hold rather than a warning, and why no amount of waiting clears it.

It is worth knowing what this is *not*. A cleanup that ran and failed is a different row and
blocks nothing: the obligation was attempted and what happened is recorded, so it is
discharged (`ObligationFailed` in `core/internal/workflow/obligation.go`), the run is
`cleanup_failed`, and the set keeps running. That case still means the machine may not be
back the way the workflow found it — it just means a person, not this product, is the only
one who can decide that, and there is no hold to lift afterwards.

**What to do.** There are exactly two exits and no third. There is no dismiss, no "ignore",
and `--skip-workflow-scripts` is not a way past it: a run carrying that flag is refused for
a blocked set exactly as an ordinary run is, and even where it does run it leaves the
obligations exactly as it found them, which is what stops a flag from settling a recovery
it knows nothing about (`core/internal/state/workflowlifecycle.go`).

1. **Read the hold.** `retnd workflow recovery show`, above. If you want it out of the
   journal instead — because nothing is serving the deployment, or because you do not trust
   a summary:

   ```bash
   sqlite3 /path/to/state.db "
     SELECT o.backup_set_id, o.run_id, o.scope, o.entered_at, r.script_spool_ref
     FROM workflow_cleanup_obligations o
     JOIN workflow_runs r ON r.run_id = o.run_id
     WHERE o.state = 'recovery_required'
     ORDER BY o.entered_at ASC;
   "
   ```

   `entered_at` is the column to read first. It is when the scope was entered, which is the
   nearest thing the journal has to "how long has this database been left quiesced". Oldest
   first, for that reason.

2. **Find out what was actually left half-done**, which is a question about the steps:

   ```bash
   sqlite3 /path/to/state.db "
     SELECT step_order, step_id, scope, phase, target, script_name, state, started_at, exit_code
     FROM workflow_steps
     WHERE run_id = 'wfr_01HX...'
     ORDER BY step_order ASC;
   "
   ```

   The `interrupted` row is the script that was mid-flight. `exit_code` is NULL for it and
   that is not a gap in the record: no process status ever reached this product, and NULL and
   0 are emphatically different answers here. `pending` rows after it are hooks that never
   started. `retnd workflow run log <run-id> --step <step-id>` prints whatever that script
   managed to say before the process went away, which is usually the fastest way to find out
   how far it got.

3. **Resume the cleanup**, if the right answer is for this product to finish what it owes:

   ```
   retnd workflow recovery resume-cleanup wfr_01HX...
   ```

   It runs only the eligible "after" stages, out of that run's own captured bytes, each one
   re-verified against the sha256 recorded when the plan was taken. The hooks are told
   `RETND_RECOVERY=1` and `RETND_CLEANUP_REASON=interrupted_run`, so a script that wants
   to be careful about a half-applied state can tell this apart from an ordinary unwind. It
   exits non-zero if the run is still not settled afterwards, because then the backup set is
   still blocked. Beside a serving engine it is handed to that process, and beside one this
   command cannot reach it is refused rather than performed here — a resume in a second
   process would write the journal and leave the serving engine still refusing the set it
   just unblocked.

4. **Or acknowledge it**, if you have already put the machine back by hand:

   ```
   retnd workflow recovery acknowledge wfr_01HX... --reason "thawed the database and unmounted /snap by hand"
   ```

   This executes nothing. It records that a person took responsibility, unblocks the set, and
   the reason is required — a blank one is refused. That is not ceremony: the entire value of
   an acknowledgement is answering, six months later, why a backup set was unblocked without
   its cleanup ever having run. It is stored beside the run, with the actor the surface that
   took it knows about, and you can read it back:

   ```bash
   sqlite3 /path/to/state.db "
     SELECT run_id, scope, acknowledged_at, acknowledged_by, acknowledge_reason
     FROM workflow_cleanup_obligations
     WHERE state = 'manually_acknowledged'
     ORDER BY acknowledged_at DESC;
   "
   ```

The run's own row is the other half of the picture, and the two axes on it are separate on
purpose — a run can be terminal with its recovery still outstanding, which is exactly the
pair the spool must not be reclaimed under:

```bash
sqlite3 /path/to/state.db "
  SELECT run_id, backup_set_id, state, recovery_state,
         backup_status, workflow_status, cleanup_status,
         started_at, finished_at, script_spool_ref
  FROM workflow_runs
  WHERE recovery_state IN ('required', 'in_progress');
"
```

Mind the two spellings, because they are easy to misread as a typo: the run's `state` is
`recovery_required`, and the `recovery_state` beside it is `required`. The schema is
`core/migrations/0012_workflow_runs.sql` and `core/migrations/0013_workflow_lifecycle.sql`;
every one of these vocabularies is plain TEXT with no CHECK constraint, enforced in Go on the
write path, so a typo in a `WHERE` clause here gets you an empty result rather than an error.

What a resume does not do, and what it cannot promise:

- **It does not re-run the backup.** Only the "after" stages that are still owed.
- **It does not read `/workflows`, and it does not pick up a config edit made while the
  daemon was down.** Everything it executes comes out of that run's own spool, verified
  against the hashes taken when the plan was made, with the environment the run was planned
  with and its secret references re-resolved. Editing a hook script after the interruption
  changes nothing about what a recovery executes. If the hook itself is what is broken, a
  resume will faithfully run the broken one again; fix the machine by hand and acknowledge
  instead.
- **A resume that cannot account for everything lands back at `recovery_required`**, durably,
  and the set stays blocked. It does not half-settle and it does not give up quietly.
- **Two processes cannot unwind one run at once.** A scope moves to `in_progress` durably
  before a hook runs, and a second caller that finds it there is refused.
- **A hook that deliberately detached a child is outside the termination guarantee.**
  `nohup`, `setsid`, a double fork: such a process is outside the group the reaper can reach,
  so the step records termination as `unconfirmed` rather than claiming a clean stop, and the
  per-step working directory is deliberately kept as the forensic record of what may still be
  writing in there. See `docs/ssh-setup.md` and `docs/adr/0021-remote-ssh-exec.md`.

The hard case this whole mechanism was built for is a power cut, or anything else that
kills the host outright. It is worth spelling out end to end, because the sequence is not
obvious from the outside:

1. The machine comes back and `retnd` starts. Its first act on the data plane is the
   workflow reconciliation, and a failure there ends the invocation rather than proceeding —
   a process that cannot work out which sets are blocked must not take a backup over a
   machine that may still be quiesced.
2. Every run that was in flight is marked, its interrupted steps recorded as `interrupted`,
   its unsettled scopes moved to `recovery_required`, its spool retained. Sets with an
   outstanding scope are blocked; every other set runs normally. The startup line quoted at
   the top of this step is printed once.
3. Go and look at the source machines named by `retnd workflow recovery show` **before**
   you clear anything. The journal can tell you which hooks never ran; it cannot tell you
   what state the other end is actually in. A `df`, a `mount`, and whatever the "before" hook
   does in reverse is the check.
4. Clear each hold with one of the two exits above — resume if the scripts should finish the
   job, acknowledge with a reason if you did it yourself. That is the only way recovery state
   is ever cleared: nothing ages out, no restart settles anything, and a clean run of the set
   cannot happen in the meantime because the set is refused.

What this product does **not** promise is that the machine was put back. Power-loss cleanup
is not guaranteed and cannot be: the hooks that would have un-quiesced the source were never
going to run with the power off. What is guaranteed is that the fact is durable, that the set
stops rather than taking a backup over a half-applied state, and that clearing it takes a
person saying so. If the "after" hooks are load-bearing for your source — a database left in
backup mode, a filesystem left frozen — the source's own startup is where that has to be made
safe, not here.

## Getting an artifact back with `retnd recover`

```bash
retnd recover <artifact-id> --to <name-under-the-staging-root>
retnd recover <artifact-id> --copy <medium> --acknowledge-cost
retnd recover <artifact-id> --json
```

`<artifact-id>` is what `retnd artifacts` prints. `--copy` names a storage medium, and the
default, `best-available`, picks the readable copy with the strongest verification class
the journal has recorded for it. `--to` names where the bytes go. There are three kinds of
target: a stream to your browser or terminal (the **Recover artifact** dialog's *Download in
this browser*, or `--to -`), a path under the configured staging root, and another configured storage
destination. The staging root is a host setting and is not something a single recovery
can move. Unless `assurance.staging_root` says otherwise it is `recovery-staging` beside
the state database, and recoveries and recovery tests share that one directory. A change
to the root, its TTL or the read ceilings is read once, so it takes effect when the
serving process next starts.

What happens, in order, and why the order is the point:

1. **A grant is minted.** It is short-lived, single-use and tied to the session that asked
   for it. It is refused on a second use, after it expires, and once that session ends.
2. **The copy is chosen from the journal.** The work list is the `placements` rows for the
   artifact, never a listing of a destination. A copy the journal records as pending
   deletion or gone is not offered.
3. **Cost is decided before anything is read.** If the only readable copy is chargeable to
   read, `recover` refuses and says which copy and what reading it costs. If the only copy
   is archived, it refuses and starts no provider job. Passing `--acknowledge-cost` is the
   explicit yes, and an acknowledged archive recovery starts exactly one thaw and reports
   that the recovery has not happened yet.
4. **The bytes are checked against the recorded digest.** For a staging or storage target
   they are read through a bounded buffer into a staging file and the digest is compared
   with the artifact's recorded digest; a copy whose size and recorded class are unchanged
   and whose bytes were flipped mid-file fails here, before anything is placed. For a
   download nothing is staged. The host first checks that the copy is there, is the size it
   recorded and can be read, and (once per artifact, then remembered as a file of digests, 32
   bytes per 4 MiB) reads the whole copy against the recorded digest, so a damaged copy is
   refused with its own error before your browser is sent a byte. It then sends the copy
   straight from where it lives, 4 MiB at a time, checking each piece against that digest
   before it goes out.
5. **Only then is the file handed over**, to exactly one target. A staging or storage target
   is placed under an atomic unique name: a client that disconnects halfway, or a process
   killed mid-write, leaves nothing that looks like a finished file, staging names are swept
   on a TTL and reconciled at startup, and a failure to clean up is reported separately from
   a failure to recover, because one means you did not get your bytes and the other means a
   disk is filling. A download that fails partway is cut off: the connection is aborted
   short of the length it promised, so an incomplete file is never mistaken for a whole one.
   Every download ends in exactly one of three ways, and each writes its own row to the
   action log and the activity feed, so a page never has to guess which one it is looking
   at:

   | Outcome | Logged as | Means |
   |---|---|---|
   | Completed | `recovery verified` | The ranges served added up to the whole artifact and the digest matched. The row names it |
   | Interrupted at N of M | `recovery interrupted` | Something other than a bad byte stopped it, most often the client going away. `bytes` is what this request had sent, `total_bytes` is M, and `reached` is the offset N where a resume starts |
   | Failed verification | `recovery failed verification` | A piece or the whole did not match what the journal recorded, or the copy lost bytes. `bytes` and `total_bytes` say how far it got, and `error_code` says which copy is damaged |

   Only the first is a success. An interrupted download is not a failed copy, and a failed
   verification is not a dropped connection: the first calls for asking again, the second
   for recovering from another copy.

What a download's checks prove, and what they do not. They prove each piece sent matched the
digest of the same piece when the whole copy was last read, and that whole read matched the
digest recorded when the backup was ingested. They do not prove the copy cannot change
between that read and the transfer (the per-piece check stops it at the first bad piece, and
what was sent before was good), that your connection holds, or that a storage provider keeps
answering. A download that was interrupted can be resumed: its `ETag` is the same for every
link, so a client sending `Range` with `If-Range` under a new link gets the bytes the first
attempt would have. The first download of a billed remote copy reads it twice, once to
prepare and once to send.

Some things this is not:

- **It is not a seventh action on `POST /api/v1/operations`.** That route is behind the
  destructive-operations gate, which is shut on every deployment this repository can build
  (see [what has not been proven](https://retnd.github.io/retnd/index.html#honest)), so a
  recovery filed there would be refused. Recovery has its own pair of routes outside that
  gate, and it completes on a deployment where the gate reports false.
- **It is not authorised per person.** This product has one tier of signed-in identity and
  no roles, so anyone signed in can recover any retained artifact. I would rather say that
  here than imply a boundary that is not there. What stands in for authorisation is audit:
  every grant and every range served writes an action-log row naming the actor, the
  artifact, the copy, the target and the outcome.
- **It does not run a program.** No subprocess is started on any recovery path.
- **It does not apply to an `engine: kopia` set.** It refuses with a sentence naming
  `retnd snapshot restore`.

Two optional features change what a "copy" can be, and `recover` works with both absent:

- **Encrypted retained copies (EPIC N, #967), when present.** A recovery decrypts and then
  requires the *plaintext* digest to equal the artifact's recorded digest. Ciphertext that
  exists and decrypts to a digest matching nothing is not a recovery. With N absent there
  is nothing to decrypt and every claim on this page is still computable.
- **Offline and removable media (EPIC P, #955), when present.** Whether a destination that
  is expected to be offline counts as unreadable or as simply away is P's policy, and this
  page does not restate or second-guess it. What `recover` guarantees is only that a copy it
  cannot read is never counted as one it could.

## What "verified" does and does not mean: three claims, not one

A tick that says "verified" is the dangerous thing here, because a HEAD request that found
an object of the right size and a validator that ran your own restore check against the
real bytes are very different amounts of evidence. So the product keeps three claims, in
three places, with three ages, and nothing is allowed to merge them:

| Claim | What it says | What it does not say |
|---|---|---|
| **Stored** | The journal holds a placement for this artifact on that medium | That anyone has read the bytes since |
| **Content verified**, with an age | A check **read the bytes** and they matched the recorded digest | That the file is usable by your application |
| **Recovery tested**, with an age | The newest known good copy was recovered to an isolated path, its digest matched the artifact's, and then **your validator** accepted it | That your application will come up, or that older copies are good |

A checksum pass with a validator failure therefore reads as **Content verified / Recovery
test failed**, and never as one green tick. A class achieved without reading bytes (an
`existence` check) is recorded as what it was and is never shown as content verification;
the scheduled pass cannot go above `existence` against a storage medium.

"Not a pass" has more than one reason, and each reason is its own value rather than a
quiet default to good:

| State | Means |
|---|---|
| `unknown` | Nothing has checked this copy. It is the zero value, it is **due**, and a rebuilt catalog yields it for every copy |
| not configured | Assurance is off for this set |
| never run | It is configured and has not run yet |
| overdue | It last passed, and is later than its cadence |
| refused for cost | The scheduler declined because reading would cost egress and nobody acknowledged it |
| could not complete | The check started and did not finish. That is a fact about the endpoint, not about the copy |
| `failed` | The check finished and the bytes were wrong |
| `requires_restore` | The copy is archived. It never counts toward a satisfied policy |

## Scrubbing and recovery tests: what runs on its own, and what it costs

A scrub is a scheduled re-check of the copies the journal records. It is driven from the
copy's own record (`next_due_at`), so a reconciliation or a catalog rebuild cannot reset
the clock on a copy nobody re-read. A **recovery test** is the stronger thing: recover,
digest, then your validator.

### What runs on its own, with nothing configured

Both `retnd serve` and `retnd daemon` run an assurance pass at every poll, with no
`assurance:` block and no `revalidation:` block:

- **The scrub.** It takes the copies that are due (a copy with no record is due now, and a
  rebuilt catalog makes every copy due) and checks them, at most 50 per set per pass.
  A copy is due again after `assurance.scrub.cadence`, 7 days unless you say otherwise.
  A local copy is re-read and re-hashed (`auto` and `content`; `existence` only checks it
  is there), and a copy on a storage medium gets an existence check, whatever the strategy
  says. Reads run under the host's ceilings, two full reads at once and one per
  destination by default.
- **The recovery test, only where you asked for one.** The default mode is `off`, and with
  it no test runs. A set whose mode is `required` or `custom` and whose validator script is
  in place is tested on `assurance.recovery_test.cadence`, 30 days unless you say
  otherwise. An unattended test never pays: if the newest known good copy can only be read
  at a cost, the test is recorded as refused for cost and nothing is read. Under
  `retnd daemon` the copy tested is a local one.
- **A set that requires a test is degraded until one passes.** A mode of `required` that
  nobody has satisfied is not a quiet default to good.

The deployment-wide `assurance:` block has a default good enough that you should not need
to set it. A backup set can say `inherit` (the default), `off`, `required` or `custom`
for its recovery test, and inherits the scrub cadence and strategy unless it names its
own. Resolution happens in `Validate`, so a change to the global default moves every set
that inherits it on the next pass, with no restart. The host-scoped settings in the table
below are the exception: they are read once, so they take effect when the serving process
next starts.

| Knob | Per set? |
|---|---|
| Scrub cadence and strategy (`auto`, `existence`, `content`) | Yes |
| Recovery-test mode (`inherit`, `off`, `required`, `custom`) and its validator | Yes |
| Recovery staging root and TTL | **No.** One staging filesystem, shared by recoveries and recovery tests |
| Concurrency ceilings (full reads, per destination) | **No.** One host |

Writing a block that names nothing under a set is refused, because it would silently mean
"inherit" under a different spelling. A set whose recovery-test mode is `required` or
`custom` needs a validator, on the set or on the deployment, and that is checked when the
configuration loads.

**A configuration that uses the `assurance:` key cannot be read by a build that predates
it.** The loader rejects unknown keys, so downgrading after you have added the block fails
closed with a message naming the key. That is deliberate, and it is the one thing to know
before you reach for this feature in the middle of an incident: add the block while
things are calm.

### Cost: a scheduled pass never thaws

Every read that bills is a refusal at the moment of spend, not a note in a document. An
unattended scrub reaches at most `existence` against a storage medium, never downloads and
never starts a thaw; an archived copy is recorded as `requires_restore`. Recovering an
archived copy is always a person saying yes to a named cost.

### Running one by hand: scrub now, recovery test, follow and cancel

The scheduler is not the only way to start either check. Both can be started on demand, and
a running one can be stopped.

- **Scrub now.** `POST /api/v1/backup-sets/{source}/{set}/scrub`, or `retnd scrub
  <source>/<set>`, which runs the same code. It checks every copy of the set whether or not
  the copy is due, at the strongest class that costs nothing, and skips a set that is held
  for editing, exactly as the scheduler does. The route answers `202` at once with an
  operation id, and the pass carries on in the background: a browser that navigates away does
  not cancel it. Only one scrub of a set runs at a time, so a double click does not become
  two passes over the same copies.
- **Recovery test now.** `POST /api/v1/backup-sets/{source}/{set}/recovery-test`, or `retnd
  recovery-test <source>/<set>`. It also answers `202` with an operation id. If a test of
  that set is already running and the new request asks for the same thing (same artifact,
  same cost acknowledgement), the request **coalesces**: it gets `202` with the *running*
  operation's id, the validator runs once, and every caller still gets a receipt of its own.
  A request that asks for something different is refused rather than quietly joined.
- **Follow.** `retnd recover <artifact> --follow` and `retnd recovery-test <set> --follow`
  print the stages as the engine passes through them, in the order it emits them:
  `resolving_copy`, `reading`, `verifying`, `writing_target`, `cleaning_temporary_files`,
  `complete`. The stage lines go to **standard error**, so `retnd recover <artifact> --to -
  --follow` still writes nothing but the artifact's bytes to standard output, and a pipe
  into `sha256sum` stays right. `--follow` leaves the exit status alone.
- **Cancel.** `retnd assurance cancel <operation-id>`, or `POST
  /api/v1/assurance/operations/{op}/cancel`. The operation's kind is `scrub` or
  `recovery_test`. The answer carries `status: "cancelling"`: it says the operation was told
  to stop, not that it has stopped, and the operation's own end line on the activity feed is
  what says it did. A cancel stops the read, the hash of the staged copy and the validator's
  whole process group. A cancelled recovery test ends with the result `warn`: it is not
  recorded as a failed copy, because nobody saw the validator answer. An id that nothing is
  running under (never started, already finished, or another process's) is not found, never
  a success. Operations live in the memory of the process serving the deployment, so the CLI
  needs `RETND_API_URL` and the two credential variables. The route is deliberately not
  under `/operations`, so it answers on a deployment whose destructive gate is shut.

### What the validator printed

The newest recovery test's output is kept, one test's worth per backup set: a later test of
the same set replaces it. It is bounded, because it is untrusted: at most 200 records, at
most 1 KiB per record, at most 32 KiB in all, and when anything was cut a single
`truncation` record is the **last** one. Only the runner writes a truncation record, so a
reader can branch on a record's `kind` (`output` or `truncation`). No raw control byte is
stored.

Read it with `retnd recovery-test <source>/<set> --show-output`, which runs no test, or
`GET /api/v1/backup-sets/{source}/{set}/recovery-test/output`. Before any test of the set has
ended, the answer says nothing was recorded.

### Validators are untrusted

The validator is a script of yours: `validator_id` is the file name of a `NAME.local.sh` in
`recovery-test/<source>/<set>` under the workflow root, written without the suffix. It is
not picked from a list of registered validators, and a name that could not be a script's
file name is refused when you save it. It runs the way every other hook runs, through the
host workflow runner, with the same custody checks, shell verification, resolved
environment and fail-closed timeout. It is run **against a recovered copy in an isolated
path**, never against the durable file, and the path reaches it as `RECOVERY_TEST_FILE`.
The runner's container cannot see the staging directory unless you declare it with
`--hook-mount`, and a script that cannot open the file fails rather than passing. Its output is untrusted terminal input: no raw control byte reaches a
structured field, a notification or the docked terminal, and a truncation is marked in a
way its own output cannot forge. The newest test's output is kept
and bounded (see *What the validator printed* above). What it can and cannot reach is in
[the runtime contract](runtime-contract.md#validators-and-recovery-staging).

## When the catalog itself is gone

Everything above assumes the journal answers. When it does not, this page's reason for
existing applies in full: the retained artifacts are ordinary files in the backup root, and
each one has a sidecar `<name>.manifest.json` written beside it when it was committed. Two
ways forward, in this order:

1. **Rebuild the catalog** with `retnd catalog rebuild`, which reconstructs the journal
   from those manifests. Afterwards every copy reads `unknown`, which means due and never
   fresh, so a scrub will re-establish what was actually checked. A rebuild does not make
   anything look verified that was not.
2. **Recover by hand.** Pick the file the way Step 2 describes and copy it out of the share.
   You lose the digest check on the way out (compare the file against the manifest's
   `checksum` yourself, using the hash its `checksum_algorithm` names), and you have not lost anything else. `recover` was never
   the only way to this file, only the way that checks it for you.

## When a scrub or a recovery test fails

| What you see | What it means | What to do |
|---|---|---|
| Scrub: `failed`, `existence` class | An object of the wrong size, or none, is at that key | Run `retnd validate <artifact> --content` for a read of the bytes; if the copy is damaged, recover from another copy |
| Scrub: `could not complete` | The endpoint did not answer. It says nothing about the copy | Fix reachability. Three failed or incomplete checks in a row degrade the set's health and raise `SCRUB_FAILING` once through your alert channels, and it clears on a later pass |
| Scrub: `requires_restore` | The copy is archived | `retnd restore` (billed, hours), then `recover`. A scrub never starts that for you |
| Scrub skipped for a set | The set is under an edit hold | The skip is visible; release the hold |
| Recovery test: `Content verified`, `Recovery test failed` | The bytes are right and your validator said no | `RECOVERY_TEST_FAILING` is raised through your alert channels. The browser and `retnd recovery-test <source/backup-set>` show the verdict and the reason the engine recorded, and `retnd recovery-test <source/backup-set> --show-output` shows what the validator printed. The product is telling you the file is intact and your check disagrees |
| Recovery test: ended `warn` after a cancel | Someone ran `retnd assurance cancel`, so nobody saw the validator answer | Not a failed copy. Run the test again if you still want the answer |
| Download: `recovery interrupted` | The transfer stopped short, most often because the client went away | Ask for a new link and resume from the `reached` offset. The copy is not implicated |
| Download: `recovery failed verification` | A piece or the whole did not match the recorded digest | Treat that copy as damaged and recover from another copy |
| Recovery test: failed before the validator ran | The digest did not match | Treat the copy as damaged. This is a content failure, not a validator one |
| Recovery test: pass, but a cleanup failure is shown | The recovered copy could not be removed from staging | The claim is unaffected; the staging disk is filling. Clear it and check the staging TTL |
| A validator that always exits 0 | It never opened the file, and the digest gate is what the pass was recorded against | Fix the validator; it proves nothing about the application |

## Quick reference

| Symptom in the journal | Meaning | What to do |
|---|---|---|
| Newest row is `COMMITTED`/`REMOTE_DELETE_PENDING`/`COMPLETE`/`REMOTE_RETAINED`, recent | Healthy | Nothing |
| No good row inside `stale_after` | Stale | Investigate why new backups aren't landing |
| `FAILED`, no `next_retry_at` | The attempt did not finish and nothing will try again on its own | Read the reason, fix it, then `retnd retry <id>` (Step 7) |
| `QUARANTINED_LOST` anywhere | Irrecoverable loss | Restore from the next-newest good row; report the gap honestly |
| Newest good row is `QUARANTINED` | Content suspect, source may still exist | Manual re-fetch or re-run reconciliation yourself |
| `REMOTE_DELETE_PENDING` stuck, `remote_delete_error` set | Expected refusal under a hardened SFTP account | Monitor remote disk directly; this is not corrupting anything |
| `Keep: false` from GFS but the file is still there | Expected; a verdict is not a deletion, and nothing applies one on a timer | Apply a retention plan through the API if the space is needed |
| You need the file now and the share is not an option | The artifact still exists | `retnd recover <artifact-id>` (see above), after `retnd restore` if the only copy is archived |
| A copy reads `unknown` after a catalog rebuild | Nothing has checked it since | Expected; it is due, and a scrub settles it |
| A `workflow_cleanup_obligations` row is `recovery_required` | An interrupted run's cleanup is unaccounted for, and that set is blocked | `retnd workflow recovery show`, then resume or acknowledge (Step 8) |
| A `workflow_steps` row is `interrupted`, `exit_code` NULL | Nobody saw that script exit; its side effects may be half-applied | Look at the source machine before clearing anything (Step 8) |
| Run is `cleanup_failed`, `recovery_state` is `none` | The cleanup ran and did not succeed; nothing is blocked and the machine may not be back | Read the step's log and put the machine back yourself; there is no hold to lift |

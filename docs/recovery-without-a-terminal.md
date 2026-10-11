# Recovery without a terminal

This is the recovery page for an administrator who has the web interface and nothing else:
no shell on the NAS, no SSH into it, no ability to open a database file by hand. That is
the normal case on a NAS appliance, and it is the case every one of the provider stores
this application is submitted to assumes.

`docs/recovery.md` is the other half. It covers the same ground for somebody who does have
a shell, and it goes further than this page can, because reading the catalog directly
answers questions the interface does not ask. Neither page replaces the other. If you have
a terminal, read that one; if you do not, everything below is reachable from the interface
and none of it needs one.

Three failures account for almost every message this project receives, a fourth
(rarer, and the only one here that stops a backup set until you act) arrives with the
scripted workflows, and a fifth is the day you try to get a backup out through the
interface and it does not come. Each gets a section below: what you see, what it actually
means, and what to do about it in the interface.

One sentence sets the boundary for all of them: **this product recovers and exports the
retained artifact; restoring it into an application remains the operator's
responsibility.** The interface can hand you the file, checked. It cannot put it back into
whatever the file came from.

---

## 1. A backup has gone stale

### What you see

The dashboard's health summary shows one or more sets marked stale, with a warning badge
and a count. Opening the set shows its last successful run further in the past than the
schedule should allow.

### What it means

Stale is a statement about time, not about damage. It means no new artifact has arrived
for this set within the window its schedule implies. Everything already retained is still
there and still verified. What has stopped is the arrival of new material, and the
distinction matters because the fix is completely different depending on which side
stopped: this application, or the machine producing the backups.

### What to do

1. Open the set and read its recent runs. There are three shapes and they point in
   different directions.
   - **Runs are happening and finding nothing.** The source is not producing new backups,
     or is writing them somewhere other than the path this set watches. Nothing is wrong
     on the NAS. Check the machine that produces the backups, then confirm the remote path
     on this set is still the path it writes to.
   - **Runs are failing.** Read the error on the most recent one; it names which stage
     failed. If it is a connection error, section 3 is the likely cause. If it is a
     verification error, the artifact arrived but did not match the hash the source
     published, which means the transfer or the source is damaged, not this application.
   - **No runs at all since the last success.** The schedule is not firing. Confirm the
     set is enabled, and confirm the application itself is running: the dashboard is
     served by a different container from the engine, so the interface can be perfectly
     healthy while the engine is down. The health summary says so when that is the case.
2. Whatever the cause, nothing needs recovering yet. A stale set is a warning that your
   protection is ageing, not that it is gone.
3. Once new artifacts start arriving again the set clears itself. It does not need to be
   reset, and there is nothing to acknowledge.

### If you need a file back right now

Retained artifacts are ordinary files in the backup root you chose at install time, which
is a directory in your own share. You reach them the same way you reach anything else in
that share: your NAS's own file manager, or the network share on your desktop. The set's
detail page lists each artifact's name and the run that produced it, so you can identify
the one you want before you go looking for it.

Take the newest artifact whose state is retained and verified. Do not take one that is
still in flight or quarantined: those are exactly the two states that mean the application
does not vouch for the bytes.

If the share is not reachable, or you would like the file checked on the way out, the
artifact's page has **Recover artifact**, which is section 5 below.

---

## 2. A retention apply refused, or deleted nothing

### What you see

You previewed a retention plan, confirmed it, and the apply came back refusing, usually
saying the plan is stale. Nothing was deleted.

### What it means

This is the application working, not failing. Retention is deliberately two steps: it
computes a plan, shows you exactly which restore points it proposes to remove, and then
applies only that plan. If anything changed between the preview and the apply, a new
artifact arrived, a run completed, the policy was edited, the plan you confirmed is no
longer a description of the current state. Applying it anyway would delete a set of
restore points nobody looked at.

So it deletes nothing and asks again. A retention system that quietly re-derived the plan
at apply time would be one where the list you approved and the list it acted on are not the
same list, and there would be no way to tell from the outside.

### What to do

1. Preview again. You will get a fresh plan reflecting whatever changed.
2. Read it. It is a different plan from the one you read a moment ago, which is the whole
   reason the first one was refused, so it deserves the same look.
3. Confirm and apply. If it refuses a second time, something is arriving frequently enough
   to invalidate a plan between preview and apply. Pause the set's schedule, apply
   retention, then resume it.

### If the apply deleted less than the preview showed

It will not, and if you believe it has, check the preview you are comparing against: a
preview from before the last run describes a state the apply no longer saw. The set detail
page records what each apply actually removed, which is the authoritative answer.

### Nothing here can delete a backup you did not confirm

Worth stating plainly, because it is the fear behind most of these messages. Retention
never deletes on its own schedule, never deletes outside the plan you confirmed, and never
deletes anything at all when the plan has gone stale.

---

## 3. The SSH host key changed

### What you see

Runs for a set start failing with a host key error, and the health summary raises it as its
own alert rather than as a generic failure. Nothing is transferred.

### What it means

This is the most serious of the three and the one most likely to be mundane. When the set
was created you were shown the remote server's host key fingerprint and asked to confirm
it. That fingerprint was pinned. The server is now presenting a different one.

There are two explanations and they are not close together:

- The server was legitimately rebuilt, reinstalled, migrated, or had its host keys
  regenerated. Common, and usually something you or a colleague did on purpose.
- Something is between you and that server presenting itself as the server. Uncommon, and
  the entire reason the key was pinned in the first place.

The application cannot tell these apart, and it deliberately does not guess. It stops.

### What to do

1. **Do not clear the pin from the interface as a way of making the error go away.** That
   is the one action here that can turn a detected interception into a silent one.
2. Verify the new fingerprint out of band: on the source machine's own console, from the
   colleague who rebuilt it, from your provisioning system's record. Out of band means by
   a route that does not go through the connection you are trying to validate.
3. When you have the real fingerprint in front of you, open the set, choose to re-pin the
   host key, and compare the fingerprint the interface shows against the one you obtained.
   They must match character for character.
4. If they match, accept the new key. Runs resume on the next schedule.
5. If they do not match, stop and treat it as an incident. Nothing is lost: every artifact
   already retained is on your own storage, and the application refused to talk to the
   server rather than transferring anything to or from it.

### While it is unresolved

The set is stale and getting staler, and everything already retained is untouched and
still verified. You are losing new protection, not existing protection, which is why it is
safe to spend a day getting the fingerprint verified properly rather than clearing the pin
in the first ten minutes.

---

## 4. A backup set will not run until a workflow is accounted for

### What you see

The set's page carries a red banner headed **Workflow recovery**: *This backup set will not
run until a workflow run is accounted for*. It names the run, says the run stopped before
its "after" hooks finished, and gives two facts underneath — how long the hold has stood,
and where that run's scripts are being kept. The banner cannot be dismissed.

While it is there, the set's own run control is unavailable rather than merely
unsuccessful, and hovering it says why. Scheduled runs for this set stop happening. Every
other set carries on as normal; this is one set's problem, not the application's.

The same banner appears on the run's own page, which you reach from the run history in the
set's Workflow panel.

### What it means

A backup with hooks runs in five stages: two before the backup, the backup, then two after
it. The "after" stages are the ones that put the source machine back — thaw the database,
release the snapshot, restart whatever was stopped for the copy.

The application writes down that it has entered a stage before it runs the first script in
that stage. Then the engine stopped: a restart, a crash, the NAS losing power. Coming back
up, it found a script that had been running with nobody left to observe how it ended, and
recorded that as *interrupted* rather than as *failed*. Those are different findings and
the difference is the whole reason you are reading this: "the script reported failure" and
"nobody knows how the script ended, and it may have half-done its work" call for different
things next.

So it blocked the set, kept that run's scripts exactly as they were, and **ran nothing**.
Nothing was replayed, and no hook was retried on your behalf. That is deliberate: the
source machine may be quiesced, mounted or paused right at this moment, and it may have a
perfectly good backup sitting beside it. A healthy backup and a machine nobody put back is
exactly the combination that would be hidden by carrying on.

### What to do

1. Open the set and read the banner. The **held since** time is the number that matters: it
   is how long the source may have been left in that state, and it is the difference
   between a NAS that rebooted four minutes ago and a database that has been frozen since
   Tuesday.
2. Open the run from the Workflow panel's run history. It draws all five stages and every
   script in them, so you can see which one was interrupted and which ones never started.
   Selecting a step shows the output it managed to produce before the process went away,
   which is the closest thing there is to knowing how far it got.
3. **Go and look at the source machine before you clear anything.** The application can
   tell you which hooks never ran. It cannot tell you what state the other end is in, and
   that is the question.
4. Then choose one of the two controls on the banner. There are deliberately only two.
   - **Resume cleanup** runs the "after" scripts that run still owes, from the copies kept
     with the run itself, each one checked against the fingerprint taken when the run was
     planned. Nothing anybody edited in the meantime decides what executes. What it costs:
     it runs those scripts against the source machine now, so it is the right choice when
     the scripts should finish the job and the machine is reachable and in one piece. If it
     cannot account for everything afterwards, the hold comes straight back and the set
     stays blocked, which is the honest outcome rather than a failure of the button.
   - **Acknowledge** opens a box headed *What was done about this run*, and a **Record
     acknowledgement** button underneath it. It executes nothing and changes nothing on the
     source machine. What it does is unblock the set and record that a person took
     responsibility. Words are required: the button stays unavailable until you write some.
     What it costs: backups of this set start happening again with nothing having confirmed
     the machine was put back, so this is the choice *after* you have put it back yourself,
     not instead of doing so.
5. There is no third control, and the absence is on purpose. No dismiss, no ignore, no
   snooze. The alternative to those two is a source machine left in a state the application
   cannot see, and a button that hid the banner would be a button that hid that.

### Why the acknowledgement asks for words

Because it is the only way a backup set is ever unblocked without its cleanup having run,
and the question it has to answer is not today's. It is the one somebody asks six months
later, reading the record: why did this set start backing up again when the application had
said it could not account for the machine?

What you write is kept with the run. You do not type a name — the acknowledgement is filed
against the administrator whose session recorded it, which is the only attribution worth
having.

### Confirming it is clear

The banner disappears and the set goes back to its schedule. That is the confirmation; the
next scheduled run appearing in the set's history is the proof.

Do not try to confirm it by starting a run from the interface. On every deployment this
project packages, a backup started from a browser is refused outright — the interface can
configure, inspect and unblock, and the engine's own schedule is what takes backups (issue
#92). A refusal there tells you nothing about whether the hold is gone.

### While it is unresolved

Unlike a stale set, this one is not safe to leave for a day. Nothing already retained is at
risk — every artifact is on your own storage and still verified — and no new backup of this
set is being taken, which is the same loss staleness costs you. What is different is the
other end: if the interrupted hook had stopped a database or frozen a filesystem, that is
still true, and it is true for as long as the banner says *held since*.

---

## 5. A recovery or a recovery test did not complete

### What you see

You pressed **Recover artifact** on an artifact, or **Run recovery test** on a set, and it
ended somewhere other than a finished file or a passing result. It reads one of:

- **Failed: nothing saved**, or **Cancelled: nothing saved**. No download link, and no
  file left anywhere that could be mistaken for a complete one.
- A **cost decision** instead of a download: the copy you chose is archived, or reading it
  is billed, and the dialog is waiting for you to say yes or to back out.
- **No readable copy**, or **Archived: thaw needed**, on the artifact's page.
- **Content verified** beside **Recovery test failed** on the set.
- A cleanup notice, saying a recovered copy could not be removed from the staging area.

### What it means

The recover action reads one retained copy, checks it against the digest the application
recorded when it was committed, and only then hands it over. So the failures sort by where
they happened, and the difference matters because the fix is different for each:

- **Before any byte left:** the copy was unreadable, was archived, was billed and you had
  not agreed, or its bytes did not match the recorded digest. A mismatch means that copy is
  damaged. It is not a download problem and trying again will not change it.
- **During the download:** the browser disconnected or you cancelled. Nothing was kept, and
  a second try starts again from the beginning.
- **After the file was checked:** a recovery test ran *your* validator, and the validator
  said no. The bytes are intact. The set shows **Content verified** and **Recovery test
  failed** side by side on purpose, because "the file is fine" and "the file is usable by
  your check" are different claims and are never averaged into one tick.
- **A cleanup notice** is its own thing. It does not turn a good result into a bad one or
  the reverse. It says a disk is filling, which is a different problem from not having your
  file.

None of these is the application restoring anything. The boundary above holds on every
path: it can give you the file, and putting it back is yours.

### What to do

1. Read which of the five shapes you have before pressing anything again.
2. **A cost decision.** It names the copy and what reading it costs, and the buttons have
   equal weight on purpose. Tick **I accept this cost** only if you mean it; backing out
   starts nothing, and an archived copy is never thawed on a timer or by a scheduled
   check, only by somebody agreeing here.
3. **Failed before any byte left.** Open the artifact and look at its copies. If one is
   marked **Archived: thaw needed**, that is a billed request that takes hours. If a copy
   is marked failed or damaged, recover from another copy instead. If the page says **No
   readable copy**, there is nothing to recover from through the interface, and the next
   step is reaching the destination by whatever route your storage provider gives you.
4. **Cancelled or disconnected.** Start again. If it keeps dropping, a proxy in front of the
   interface is closing long downloads; that is a gateway setting, not this application.
5. **Recovery test failed, content verified.** Open the result. It shows the validator's
   exit status and a bounded, redacted tail of what it printed, and where the tail was cut
   it says so in a box the validator's own output cannot imitate. Fix the validator or the
   thing it checks. The application is telling you the file is intact.
6. **A cleanup notice.** Nothing is wrong with your result. Free space on the volume that
   holds the staging area, and the leftover is removed on its next sweep.

### While it is unresolved

Nothing already retained is at risk, and no failure here deletes or changes a retained
artifact. What is at risk is the claim: a set whose recovery test is failing or overdue is
honestly shown as untested, and it stays that way until a test passes. It never shows the
older, better result as if it were current.

---

## When none of these is it

- **The interface loads but every page is empty or errors.** The web interface and the
  engine are separate containers. The interface is up and the engine is not. Restart the
  application through your NAS's own application manager; that is the supported control
  and it needs no terminal.
- **The interface does not load at all.** The application is stopped. Start it the same
  way.
- **You have forgotten the password.** Use **Forgot password** on the sign-in page. It
  asks for the username and nothing else, and it always says the same thing back, so the
  page is not where you find out whether you typed the right name. What decides is your
  mailbox: if the name matches the administrator and that account has a confirmed
  recovery address, a single-use reset link valid for 30 minutes arrives there, sent
  through the mail server configured at setup. Following it sets a new password and signs
  every session out, including any still open elsewhere, so the next thing to do is sign
  in with the new one. Nothing arrives? The address or the mail server is the problem, not
  the password, and that is the case below.
- **The console says the account is unverified and will be removed.** That is not a
  fault: a new administrator is *provisional* until somebody opens the link in the
  verification email sent to its recovery address. The banner names the deadline, and
  if the address is not verified by it, retnd deletes the administrator, signs every
  session out and reopens enrollment — which puts you back at the setup wizard with the
  bootstrap link printed in the application's log, not in a dead end. Open the link from
  the mail (it works on a phone; it needs no session), or press **Resend link** on the
  banner while you are still signed in. It is deliberately strict: a mail server
  accepting the message only proves the *server* works, and an address with a typo in it
  is accepted just as happily as the right one, so the day a password is forgotten would
  otherwise be the day you find out. Verifying once settles it permanently; changing the
  address later asks you to verify the new one, but nothing is deleted for that.
- **You cannot sign in and no reset mail arrives.** The administrator record lives in the
  application's state directory, which survives restarts and upgrades, and so do the
  recovery address and the SMTP settings — which is also why a mail server that has since
  changed its password or stopped accepting that sender will keep failing silently from
  your side. With no terminal there is no way to edit either from outside the interface
  you cannot reach. Your platform's procedure for reinstalling the application while
  keeping its state is in that target's own documentation; reinstalling does not touch
  your retained artifacts, which live outside the application's state on purpose. Once
  you are signed in again, Settings is where the recovery address and the SMTP details
  are corrected, and it offers a test send so the next time is not another guess.
- **Anything else.** Open an issue with the set's detail page and the failing run's error
  message. https://github.com/retnd/retnd/issues

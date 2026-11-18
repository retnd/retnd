# Screenshots

The one submission material nobody can produce from a checkout. A screenshot is of this
application running on the provider's own hardware, which is why this file records what to
capture and how many, and `docs/acceptance/store-submission-preflight.md` records the
procedure that captures them. Until that procedure runs on real hardware, every target's
screenshot row is recorded as outstanding rather than as ready or as missing.

Do not substitute a mock. The shared UI has a mock API mode used by its own tests, and a
listing built from it would show data no administrator will ever see. Every screenshot
below is of a real installation against a real SFTP source.

## What each store asks for

| Store or catalog | Screenshots | Notes from that store's own requirements |
|---|---|---|
| Synology Package Center | 3 | Shown in the package's detail page. Capture at the DSM desktop's own window size rather than full screen. |
| TrueNAS Apps catalog | 3 | Referenced from `apps/truenas/catalog/app.yaml`'s `screenshots` list, which stays empty until they exist. |
| Unraid Community Applications | 2 | Community Applications shows the template's icon and overview prominently and screenshots secondarily; two is the useful number. |
| UGREEN App Center | 4 | EPIC D's submission. The count is recorded here so the material is ready when #83 produces the package; nothing about it is this work package's to capture. |
| CasaOS AppStore | 2 | The store renders one compose file's `x-casaos` block as the whole listing, so the tile and the install dialogue carry most of the presentation and two screenshots are what is left to show. |
| ZimaOS app store | 2 | The same two, captured again on ZimaOS. It is one runtime and two store registrations, and the two are submitted and certified separately, so a screenshot taken on CasaOS is not a screenshot of this listing. |

## What to capture, in this order

Capture more than the minimum and choose from them. Every one of these is a screen an
administrator sees in normal use, and none of them shows a credential.

1. **Dashboard.** At least one healthy set, one stale set and one failing set, so the
   health summary shows all three states. This is the screenshot that explains the product
   in one image.
2. **Backup set detail.** The lifecycle timeline of a real set with several runs behind
   it, including at least one failure that later recovered.
3. **Retention preview.** The confirmation dialogue listing exactly which restore points
   would be deleted, with the count visible. This is the screenshot that shows the product
   asks before it deletes.
4. **Add backup set wizard.** The host-key pinning step, showing the fingerprint the
   administrator is being asked to verify out of band.

## Before you capture

- Use a display scale that produces at least 1440 logical pixels of width. Every one of
  these stores rescales; none of them upscales well.
- Use the light theme unless the store's own listing page is dark.
- Redact nothing afterwards. If something needs redacting, the screenshot is of the wrong
  screen: use a source hostname and a backup-set name that are safe to publish, set up for
  this purpose.
- Do not capture a browser chrome with bookmarks in it.

## Where they go

Store them with the submission for that target rather than in this repository: they are
large binaries, they change with every visual revision, and no check here can tell a
current one from a stale one. Record in the acceptance run which build they were taken
from.

## Encrypted retained copies (#997), and the screens it changed

EPIC N added a "Retained-copy encryption" card to Settings, an Inherit / Off / Required card
under Retention on each backup set's page, and Encrypted / Verified recoverable chips on the
copies list of a backup's own page. One of the screens above (the Backup set detail, 2) and the Settings page,
which is not on the list, are therefore outstanding again: they are not the screens any
earlier capture showed. That moves no row, because every row already reads `PENDING_OPERATOR`
here (`OPERATOR` in the preflight ledger, same meaning) and none has a picture.
It does mean that when somebody finally captures them on real hardware, the build in front of
the camera must be one that has these cards, and a store screenshot is never taken of the
mock API or generated from the capture scripts under `docs/site/tools/`. #998 owns the
pictures on the project's own site, which are mock recordings and say so.

EPIC T (#1310) adds a Suite select to the same Settings card and a suite chip beside each
encrypted copy. Same consequence, same verdict: no row moves, the build in front of the camera
must be one with the select, and #1320's pictures on the project's site are mock recordings.

## The rename (#893), and why it does not move any row

Every screenshot this file describes shows the product's name, in the topbar of every
screen on the list above and in the sign-in and enrolment pages before them. The product
was renamed by EPIC R (#885), so every store screenshot that has
ever been captured for any of the six listings above is of the previous name.

That does not change a single verdict here, and the reason is the point of this file.
There was never a captured screenshot in this repository to go stale: this material has
read `PENDING_OPERATOR` for every store target since #90 recorded it, because a screenshot
is of the application running on the provider's own hardware and nobody working on the
repository has that hardware. The rename adds a second reason a row cannot be green
without adding anything anybody here can do about it, so every `Store screenshots` cell in
`docs/conformance/submission-preflight.md` stays outstanding. A row that moved to passing
because the *listing copy* was reworded (#892) or because the *icon* was redrawn (#893)
would be claiming a photograph that does not exist.

What the rename does change is what the operator who eventually runs
`docs/acceptance/store-submission-preflight.md` has to check before submitting: the build
in front of the camera must be one whose interface shows the new name, which means 0.4.0 or
later. A screenshot taken on `0.3.3`, the newest published image, shows the previous name and is
not submittable, however good the picture is.

And the one thing that must not happen instead. The temptation a rename creates is to
satisfy these rows from the material that *was* re-recorded: `docs/site/screens/` holds 55
captures of the renamed interface, they are reproducible, and they are the right screens.
They are also all mock data, from `createMockApi` on `ui/shared`'s own dev server, and
`docs/epic-checklist.md` §10 forbids substituting one here for exactly that reason — a
listing built from a fixture shows an administrator numbers no deployment will ever
produce. The site says on every page that its pictures come from a mock; a store listing
has nowhere to say it.

No provider store listing has been re-reviewed since the rename. The listing copy, the
icons and the release notes were rewritten in the tree (#892, #893); whether each store
still accepts the listing under a new product name — the name in the submission form, the
catalog slug, the existing registration — is an external decision this repository cannot
record, and §75 says so.

## EPIC Q (#953) changed two of the four screens, so those two are outstanding again

The recovery and assurance work changes the **Dashboard** (it gains an Assurance summary card
with what is due, overdue and degraded) and the **Backup set detail** (it gains the Assurance
section: the three claims, the per-set policy control and the
recovery actions). Items 1 and 2 in the list above therefore show something that no longer
matches the product, and any picture of either taken before this epic is stale however good it
was. Both rows stay outstanding, and so does every `Store screenshots` cell in
`docs/conformance/submission-preflight.md`: nothing here was ever captured, so there is no row to
move, and the point of saying it is that a screenshot taken from an older build must not be
reused for the new screens.

The eleven recovery and assurance surfaces in `docs/site/screens/recovery-*.png` come from the
mock API through `docs/site/tools/capture-recovery-assurance.mjs`, are reproducible, and are
documentation pictures only. They are not a substitute for item 1 or item 2, for the reason in
the previous section, and they show Scrub now, Run recovery test and the operation views that the
mock offers and a real deployment's web client does not yet (there is no route to start either).

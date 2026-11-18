// Re-shoots the pictures of EPIC Q: artifact recovery, scheduled scrubbing
// and the three assurance claims (#988).
//
// Run it from the repository root:
//
//     node docs/site/tools/capture-recovery-assurance.mjs
//
// It writes PNGs into docs/site/screens/ under the `recovery-` prefix and
// prints one line per picture. `--check` shoots everything twice and fails
// unless every file is byte-identical to the run before, which is the property
// the release gate leans on.
//
// harness.mjs holds the arrangement: the mock API instead of any deployment
// (the first-run flow claims the administrator account and burns the
// enrollment token, so a capture never has an address to point at), the auth
// stub, the borrowed Playwright, and the pinned clock, locale and timezone.
// Read that first.
//
// # What this script adds to it
//
// Every operation in the mock is driven by `Date.now()`, and the harness
// freezes that. A frozen clock therefore holds a started operation at its
// first instant forever. That is a gift rather than a problem: a mid-stage
// picture is taken by moving the frozen clock to the instant wanted
// (`at()` below), so "the verifying stage, nine seconds in" is a place the
// fixture is put, not a race the shutter has to win.
//
// # What is photographed
//
// Eight surfaces, each in three variants: light at the standard window,
// the dark theme, and a 940px-wide window in the light theme (the narrow
// layout). The names are `recovery-NN-<surface>[-dark|-narrow]`.
//
//   01 claims         the three claims on the Backup detail, three ages
//   02 recover-browser / 03 recover-staging
//                     Recover artifact, the target chooser, both targets
//   04 progress-terminal / 05 progress-activity
//                     a recovery test mid-stage in the docked terminal and
//                     in the Activity list
//   06 archive        the archival cost decision
//   07 settings       the scheduled-scrub card on Settings
//   08 per-set        the per-set "Follow the deployment" / "This set's own policy" control
//   09 scrub-failed   a scrub that failed and the degraded state after it
//   10 test-passed    a recovery test that passed
//   11 split          checksum good, validator failed
//
// # One thing these pictures cannot claim
//
// The mock offers Scrub now, Run recovery test and the operation views. A
// real deployment's web client does not yet (there is no route to start
// either, see the PR that added this file), so pictures 04, 05, 09 and 10
// show the interface the epic designed, rendered from the mock. They are
// captures of the real component tree and of nothing else; none of them
// is a store-submission screenshot, which this script never makes.

import { createHash } from "node:crypto";
import { copyFileSync, mkdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { FROZEN_TIME, openApp, optimise, quiet, SCREENS, settle, shot, VIEWPORT, withDevServer } from "./harness.mjs";

const CHECK = process.argv.includes("--check");

// How tall every window is. 2400 used to be enough, and the per-set Assurance
// section has since grown to about 1250px below a tall page header, so in a
// 2400px window its bottom ran off the window: the clip was cut at the window
// edge, with the collapsed terminal bar drawn across what was left of it
// (recovery-08, -09 and -10). 3600 holds the whole section with room to spare.
const TALL = 3600;

// The product has no responsive breakpoint at all (no @media rule in ui/shared
// beyond reduced motion), so "narrow" is the smallest window it is built and
// tested for: the 940px window retnd-tests' `small-window` project uses.
// 390px was tried first and is not a layout the product has (the cards
// collapse to a ~100px column), which is recorded in the PR rather than
// photographed as if it were a design. Tall on purpose, so no clip is ever
// cut off by the bottom of the window.
const NARROW = { width: 940, height: TALL };
const SET = "/sets/production/postgres-primary";
const DETAIL = "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst";
const ARCHIVED = "/backups/production/billing-mysql/billing-20260827.sql.gz";

/** Moves the frozen clock `secs` past the instant every capture starts at. */
async function at(page, secs) {
  await page.clock.setFixedTime(new Date(FROZEN_TIME.getTime() + secs * 1000));
}

const assurance = (page) => page.getByRole("region", { name: "Assurance", exact: true });

/** The surfaces. `prep` leaves the page in the state to photograph and
 *  returns the selector (or selectors) to clip to. It must wait for the
 *  thing itself, never for a number of milliseconds. */
const surfaces = [
  {
    name: "recovery-01-claims",
    path: DETAIL,
    async prep(page) {
      await page.locator('main [data-testid="assurance-claims"] [data-claim="recovery"]').waitFor();
      return ['main [data-testid="assurance-claims"]'];
    }
  },
  {
    name: "recovery-02-recover-browser",
    path: DETAIL,
    async prep(page) {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      const dlg = page.getByRole("dialog", { name: "Recover artifact" });
      await dlg.getByRole("radio", { name: "Download in this browser" }).check();
      return ['[role="dialog"]'];
    }
  },
  {
    name: "recovery-03-recover-staging",
    path: DETAIL,
    async prep(page) {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      const dlg = page.getByRole("dialog", { name: "Recover artifact" });
      await dlg.getByRole("radio", { name: "Under the staging root on this retnd host" }).check();
      await dlg.getByRole("textbox", { name: /Name under the staging root/ }).waitFor();
      return ['[role="dialog"]'];
    }
  },
  {
    name: "recovery-04-progress-terminal",
    keepTerminal: true,
    path: SET,
    scenario: "assurance-slow-test",
    async prep(page) {
      const section = assurance(page);
      await section.getByRole("button", { name: "Run recovery test" }).click();
      await section.getByTestId("operation-progress").waitFor();
      await at(page, 9);
      await page.locator('[data-testid="operation-progress"][data-stage="verifying"]').waitFor({ timeout: 20_000 });
      const terminal = page.getByLabel("Terminal", { exact: true });
      await terminal.getByRole("log").getByText("recovery test stage: Verifying", { exact: false }).first().waitFor();
      return ["section[aria-label='Terminal']"];
    }
  },
  {
    name: "recovery-05-progress-activity",
    path: SET,
    scenario: "assurance-slow-test",
    async prep(page) {
      const section = assurance(page);
      await section.getByRole("button", { name: "Run recovery test" }).click();
      await section.getByTestId("operation-progress").waitFor();
      await at(page, 9);
      await page.locator('[data-testid="operation-progress"][data-stage="verifying"]').waitFor({ timeout: 20_000 });
      const panel = page.locator("section[aria-label^='Activity for']").first();
      await panel.getByText("recovery test stage: Verifying", { exact: false }).first().waitFor({ timeout: 20_000 });
      return ["section[aria-label^='Activity for']"];
    }
  },
  {
    name: "recovery-06-archive",
    path: ARCHIVED,
    async prep(page) {
      await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
      const dlg = page.getByRole("dialog", { name: "Recover artifact" });
      await dlg.getByRole("group", { name: "This copy is archived" }).waitFor();
      return ['[role="dialog"]'];
    }
  },
  {
    name: "recovery-07-settings",
    path: "/settings",
    async prep(page) {
      await assurance(page).getByLabel("Scrub every").waitFor();
      return ["[aria-label='Assurance']"];
    }
  },
  {
    name: "recovery-08-per-set",
    path: SET,
    async prep(page) {
      await assurance(page).getByRole("radiogroup").waitFor();
      return ["[aria-label='Assurance']"];
    }
  },
  {
    name: "recovery-09-scrub-failed",
    path: SET,
    scenario: "assurance-scrub-fails",
    async prep(page) {
      const section = assurance(page);
      await section.getByRole("button", { name: "Scrub now" }).click();
      await section.getByTestId("operation-progress").waitFor();
      await at(page, 30);
      await page.locator('[data-claim="content"][data-state="failed"]').first().waitFor({ timeout: 20_000 });
      return ["[aria-label='Assurance']"];
    }
  },
  {
    name: "recovery-10-test-passed",
    path: SET,
    async prep(page) {
      const section = assurance(page);
      await section.getByRole("button", { name: "Run recovery test" }).click();
      await section.getByTestId("operation-progress").waitFor();
      await at(page, 40);
      await section.locator('[data-testid="operation-progress"][data-state="complete"]').waitFor({ timeout: 20_000 });
      await section.locator('[data-claim="recovery"][data-state="pass"]').filter({ hasText: /just now|0m|ago/ }).first().waitFor();
      return ["[aria-label='Assurance']"];
    }
  },
  {
    name: "recovery-11-split",
    path: DETAIL,
    scenario: "assurance-split",
    async prep(page) {
      const claims = page.locator('main [data-testid="assurance-claims"]');
      await claims.locator('[data-claim="content"][data-state="pass"]').waitFor();
      await claims.locator('[data-claim="recovery"][data-state="failed"]').waitFor();
      return ['main [data-testid="assurance-claims"]'];
    }
  }
];

// The standard window's width, made tall so that the pinned terminal dock never
// lands on top of the region being photographed (see harness.mjs on tall
// windows).
const DESKTOP = { width: VIEWPORT.width, height: TALL };

const variants = [
  { suffix: "", viewport: DESKTOP, theme: "light" },
  { suffix: "-dark", viewport: DESKTOP, theme: "dark" },
  { suffix: "-narrow", viewport: NARROW, theme: "light" }
];

async function shootAll(app) {
  const written = [];
  for (const s of surfaces) {
    for (const v of variants) {
      const { context, page } = await openApp(app, {
        path: s.path,
        scenario: s.scenario ?? "default",
        viewport: v.viewport
      });
      await page.evaluate((t) => document.documentElement.setAttribute("data-theme", t), v.theme);
      await settle(page, 400);
      // The dock is pinned to the bottom of the window and would sit on top of
      // whatever is photographed, so it is collapsed off camera, the way the
      // other captures do, except in the one picture that is about it.
      //
      // Waited for, not counted. The dock renders a moment after the page, and
      // a count() that ran first found nothing in some runs and left the dock
      // open, so --check moved 1 to 7 of 33 pictures between its two passes (the
      // open dock across recovery-05's activity panel and the bottom of -08,
      // -09 and -10, compared pass against pass, #1331). That made step 1 of
      // capture-encryption-all.sh fail on every run. It is a release blocker,
      // which is why this file is touched outside the epic that wrote it.
      if (!s.keepTerminal) {
        const hide = page.getByRole("button", { name: "Hide terminal" });
        await hide.waitFor({ timeout: 5000 }).catch(() => {});
        if (await hide.count()) await hide.click();
        await page.getByRole("button", { name: "Show terminal" }).waitFor({ timeout: 5000 }).catch(() => {});
      }
      const clip = await s.prep(page);
      // A hover tooltip from the last click would sit over the picture.
      await page.mouse.move(0, 0);
      // So could the clicked control's own tooltip. Run recovery test is
      // disabled while its test runs and re-rendered when it ends, under a
      // pointer that has not moved, and whether its host then saw the
      // pointer leave depends on timing: probed four times on the same
      // build, the tooltip was still open after the move in two
      // (recovery-10-test-passed-narrow, #1331), with nothing hovered and
      // nothing focused. Blur, and if a pop-up is still open dismiss it the
      // way an operator would, with Escape (useHoverPopover.ts), then
      // require that none is.
      await page.evaluate(() => document.activeElement instanceof HTMLElement && document.activeElement.blur());
      if (await page.locator(".tooltip__pop:not([hidden])").count()) await page.keyboard.press("Escape");
      await page.waitForFunction(() => !document.querySelector(".tooltip__pop:not([hidden])"), null, { timeout: 5000 });
      // And a page that has stopped changing, for the reason harness.mjs's
      // quiet() gives.
      await quiet(page);
      await settle(page, 400);
      await shot(page, s.name + v.suffix, clip, { pad: 16 });
      written.push(s.name + v.suffix);
      await context.close();
    }
  }
  return written;
}

function digests(dir, names) {
  const out = {};
  for (const n of names) out[n] = createHash("sha256").update(readFileSync(join(dir, n + ".png"))).digest("hex");
  return out;
}

if (!CHECK) {
  const names = await withDevServer(shootAll);
  optimise(names);
  console.log(names.length + " pictures written to docs/site/screens/");
} else {
  // Two full passes against ONE dev server (a second server in the same
  // process races the first one's port), each hashed straight after it is
  // written and quantised, because the quantised bytes are the ones that get
  // committed. Fresh browser contexts per picture, so nothing carries over.
  let first;
  let second;
  let names;
  await withDevServer(async (app) => {
    names = await shootAll(app);
    optimise(names);
    first = digests(SCREENS, names);
    // CAPTURE_FIRST_PASS_DIR keeps the first pass's files, so a picture that
    // moves can be compared with what it moved from rather than only reported.
    if (process.env.CAPTURE_FIRST_PASS_DIR) {
      mkdirSync(process.env.CAPTURE_FIRST_PASS_DIR, { recursive: true });
      for (const n of names) copyFileSync(join(SCREENS, n + ".png"), join(process.env.CAPTURE_FIRST_PASS_DIR, n + ".png"));
    }
    await shootAll(app);
    optimise(names);
    second = digests(SCREENS, names);
  });
  const moved = names.filter((n) => first[n] !== second[n]);
  if (moved.length) {
    console.error("NOT REPRODUCIBLE: " + moved.length + " of " + names.length + " pictures differ between two runs:\n  " + moved.join("\n  "));
    process.exit(1);
  }
  console.log("reproducible: " + names.length + " pictures, byte-identical across two runs");
}

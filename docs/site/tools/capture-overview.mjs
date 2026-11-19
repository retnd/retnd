// Records the tour at the top of the README: about thirty seconds across
// the main screens, light theme first and then dark.
//
// Run it from the repository root:
//
//     node docs/site/tools/capture-overview.mjs
//
// It writes docs/assets/readme-tour.gif and prints one line with its frame
// count, duration and size.
//
// harness.mjs holds the arrangement, and it is the same one every other
// capture here runs on: the real component tree against the fixture data
// in src/api/mock.ts, a pinned clock, a fixed port, and held frames rather
// than a video. That is also why this is a tour of fixtures and not of a
// deployment. A recording of a live instance would carry somebody's real
// hostnames and backup sets, and could not be re-taken to the same file.
//
// Each stop is reached by its own URL and waits for something only that
// screen renders, so a screen that fails to load stops the run naming
// itself instead of putting the previous screen into the picture twice.
//
// The holds add up to 30 seconds on purpose: long enough to read a screen,
// short enough that the README does not turn into a video.

import { resolve } from "node:path";
import { Clip, mb, openApp, settle, VIEWPORT, withDevServer, REPO } from "./harness.mjs";

const WIDTH = 1000;
const OUT = resolve(REPO, "docs/assets");

/** The same window every other capture uses, cropped explicitly so every
 *  frame is the same size whatever the page underneath it is. */
const FRAME = { x: 0, y: 0, width: VIEWPORT.width, height: VIEWPORT.height };

const text = (re) => (p) => p.getByText(re).first();
const heading = (re) => (p) => p.getByRole("heading", { name: re }).first();

/** [path, marker, seconds on screen]. The order is the order an operator
 *  meets the product in: what is happening, what is configured, what is
 *  held, what went wrong, how to get a backup out, then the settings. */
const LIGHT = [
  ["/", text(/Recent activity/i), 3.0],
  ["/sets", text(/Backup sets|backup set/i), 2.4],
  ["/sets/production/postgres-primary", heading(/Production PostgreSQL/i), 3.4],
  ["/backups", text(/Backups|artifact/i), 2.4],
  [
    "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst",
    text(/postgres-prod-20260828\.dump\.zst/),
    2.6
  ],
  ["/activity", text(/Activity/i), 2.4],
  ["/quarantine", text(/Quarantine/i), 2.0],
  ["/catalog-recovery", text(/Existing backup data detected/i), 2.6],
  ["/settings", text(/Notifications/i), 3.0]
];

const DARK = [
  ["/", text(/Recent activity/i), 3.0],
  ["/sets/production/postgres-primary", heading(/Production PostgreSQL/i), 3.2]
];

let result;
await withDevServer(async (app) => {
  const { page } = await openApp(app, { path: null, viewport: VIEWPORT });
  const clip = new Clip(page, "readme-tour", { clip: FRAME, width: WIDTH, colors: 128, outDir: OUT });

  const visit = async (path, marker) => {
    await page.goto(app.base + path);
    await marker(page).waitFor({ state: "visible", timeout: 20_000 });
    await settle(page, 700);
  };

  // The dock is open on first load, so the dashboard shows the terminal
  // the way an operator meets it. It is collapsed after that first stop
  // (its state lives in localStorage), so the screens below get the room.
  let first = true;
  for (const [path, marker, seconds] of LIGHT) {
    await visit(path, marker);
    await clip.frame(seconds);
    if (first) {
      first = false;
      const hide = page.getByRole("button", { name: "Hide terminal" });
      if (await hide.count()) await hide.first().click();
    }
  }

  await page.getByRole("button", { name: "Toggle colour theme" }).click();
  // Off the button, or its hover tooltip rides along in every dark frame.
  await page.mouse.move(1275, 520);
  for (const [path, marker, seconds] of DARK) {
    await visit(path, marker);
    await clip.frame(seconds);
  }

  result = await clip.write();
});

console.log("\n" + mb(result.bytes) + ", " + result.seconds.toFixed(1) + "s, " + result.frames + " frames");
if (Math.abs(result.seconds - 30) > 2) {
  throw new Error("the tour runs " + result.seconds.toFixed(1) + "s, and the README promises about 30");
}

// Records the animated GIF of the corrected behaviour for the issue "Recovery
// test on a set with no validator announces Complete and offers a button that
// can only refuse" (#1108). It is the pair of capture-issue-no-validator.mjs,
// which records the broken behaviour.
//
//     node docs/site/tools/capture-fix-no-validator.mjs
//
// The same real component tree over the same fixtures. Three beats: the page
// on a set with no validator (button disabled, reason beside it), the keyboard
// reaching the link to how to declare one, and the control, the same page on a
// set that declares its validator (button enabled, no reason).
import { resolve } from "node:path";
import { Clip, mb, openApp, settle, VIEWPORT, withDevServer, REPO } from "./harness.mjs";

const OUT = process.env.CAPTURE_OUT ?? resolve(REPO, "docs/assets/issues");
const NAME = process.env.CAPTURE_NAME ?? "1108-recovery-test-no-validator-fixed";
const SET = "/sets/production/postgres-primary";
const FRAME = { x: 0, y: 0, width: VIEWPORT.width, height: VIEWPORT.height };

// Puts the button 260px below the top of the window, above the terminal dock,
// so the reason beside it is in the frame and not under the dock.
const place = (locator) =>
  locator.evaluate((el) => window.scrollBy(0, el.getBoundingClientRect().top - 260));

let result;
await withDevServer(async (app) => {
  const { page } = await openApp(app, { path: SET + "?wire=1", scenario: "assurance-no-validator", viewport: VIEWPORT });
  const section = page.getByRole("region", { name: "Assurance", exact: true });
  const run = section.getByRole("button", { name: "Run recovery test" });
  await run.waitFor({ timeout: 30_000 });
  await section.getByTestId("recovery-test-no-validator").waitFor({ timeout: 30_000 });
  if (!(await run.isDisabled())) throw new Error("the button is enabled on a set with no validator: the fix is not in this build");
  await settle(page, 700);
  const clip = new Clip(page, NAME, { clip: FRAME, width: 1000, colors: 128, outDir: OUT });
  await place(run);
  await settle(page, 300);
  await clip.frame(3);
  const docs = section.getByRole("link", { name: "docs/recovery.md" });
  await docs.focus();
  await settle(page, 300);
  await clip.frame(2.5);
  // the control: a set that declares its validator
  await page.goto(app.base + SET + "?wire=1&scenario=default");
  await section.getByRole("button", { name: "Run recovery test" }).waitFor({ timeout: 30_000 });
  if (await section.getByRole("button", { name: "Run recovery test" }).isDisabled()) throw new Error("the control set's button is disabled");
  await settle(page, 700);
  await place(section.getByRole("button", { name: "Run recovery test" }));
  await settle(page, 300);
  await clip.frame(3);
  result = await clip.write();
});
console.log(mb(result.bytes) + ", " + result.seconds.toFixed(1) + "s, " + result.frames + " frames");

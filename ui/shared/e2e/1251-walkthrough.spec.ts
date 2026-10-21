/**
 * A slow, watchable pass over #1251 for the GIF in the PR. Run with
 * E2E_VIDEO=1. It ends on the strict assertion for what the recording shows:
 * the "Same list from the terminal" line carrying the received-date range.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

test.describe.configure({ timeout: 240_000 });

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
const BILLING = "production/billing-mysql";

test("#1251 walkthrough: the set filter's terminal line follows the date range", async ({ page }) => {
  await open(page, "/backups?set=" + BILLING, "backups-many-artifact-sets");
  const status = page.getByTestId("set-filter-status");
  const echo = status.locator("code");
  await expect(echo).toHaveText("retnd artifacts --backup-set " + BILLING);
  await beat(page, 1500);

  const after = page.getByLabel("Received on or after");
  const before = page.getByLabel("Received on or before");
  await after.focus();
  await beat(page, 600);
  await after.fill("2026-08-20");
  await beat(page, 1200);
  await before.focus();
  await beat(page, 600);
  await before.fill("2026-08-26");
  await beat(page, 1200);
  await echo.scrollIntoViewIfNeeded();
  await beat(page, 2500);

  await expect(status).toContainText("Showing 3 backups in");
  await expect(echo).toHaveText("retnd artifacts --backup-set " + BILLING + " --since 2026-08-20 --until 2026-08-26");
});

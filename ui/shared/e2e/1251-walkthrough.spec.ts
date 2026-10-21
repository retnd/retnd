/**
 * A slow, watchable pass over #1251 for the GIF in the PR. Run with
 * E2E_VIDEO=1. Each step ends on the strict assertion for what the recording
 * shows: the "Same list from the terminal" line carrying the received-date
 * range, then the range's own line with no set filter, then --assurance for a
 * bucket (the review of #1256).
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

  // Without the set filter the range keeps a line of its own.
  await status.getByRole("button", { name: "Show all backup sets" }).click();
  const listEcho = page.getByTestId("list-filter-echo").locator("code");
  await expect(listEcho).toHaveText("retnd artifacts --since 2026-08-20 --until 2026-08-26");
  await beat(page, 2500);

  // An assurance bucket is --assurance.
  await open(page, "/backups?set=production/postgres-primary&assurance=untested", "assurance-default-policy", { wire: true });
  await expect(page.getByTestId("assurance-filter-status")).toBeVisible();
  await beat(page, 2500);
  await expect(echo).toHaveText("retnd artifacts --backup-set production/postgres-primary --assurance untested");
});

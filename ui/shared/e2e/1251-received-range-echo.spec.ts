import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1251: with the Backups list narrowed to one set, the "Same list from
 * the terminal" line under the set chip printed `retnd artifacts --backup-set
 * S` whatever the two date inputs said, so it listed more than the screen
 * showed. Once a received-date bound is set the line carries it as --since or
 * --until (inclusive, by calendar day, like the inputs), and a range the CLI
 * would refuse has no line at all.
 *
 * `backups-many-artifact-sets` gives Billing MySQL a backup every third day of
 * August 2026 (the 2nd, 5th, ... 29th), named by its date, so a range has rows
 * on either side.
 */
const MANY = "backups-many-artifact-sets";
const BILLING = "production/billing-mysql";
const status = (page: Page) => page.getByTestId("set-filter-status");
const echo = (page: Page) => status(page).locator("code");
const after = (page: Page) => page.getByLabel("Received on or after");
const before = (page: Page) => page.getByLabel("Received on or before");
const rows = (page: Page) => page.locator("table.table tbody tr");

async function shownCount(page: Page): Promise<number> {
  const m = /Showing (\d+) backups? in/.exec((await status(page).textContent()) ?? "");
  return Number(m?.[1] ?? -1);
}

test.describe("#1251 the set filter's terminal line carries the received-date range", () => {
  test("#1251 with no dates the line is the set alone", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await expect(echo(page)).toHaveText("retnd artifacts --backup-set " + BILLING);
  });

  test("#1251 both dates set: the line names the set and the range, and the screen lists that range", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await after(page).fill("2026-08-20");
    await before(page).fill("2026-08-26");
    await expect(echo(page)).toHaveText("retnd artifacts --backup-set " + BILLING + " --since 2026-08-20 --until 2026-08-26");
    // The line is about the rows on screen: Billing MySQL's backups of the
    // 20th, 23rd and 26th, both ends of the range included.
    await expect.poll(() => shownCount(page)).toBe(3);
    await expect(rows(page)).toHaveCount(3);
    for (const d of ["20", "23", "26"]) await expect(rows(page).filter({ hasText: "billing-mysql-202608" + d })).toHaveCount(1);
    for (const d of ["17", "29"]) await expect(rows(page).filter({ hasText: "billing-mysql-202608" + d })).toHaveCount(0);
  });

  test("#1251 one bound alone is --since or --until alone", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await after(page).fill("2026-08-20");
    await expect(echo(page)).toHaveText("retnd artifacts --backup-set " + BILLING + " --since 2026-08-20");
    await after(page).fill("");
    await before(page).fill("2026-08-24");
    await expect(echo(page)).toHaveText("retnd artifacts --backup-set " + BILLING + " --until 2026-08-24");
    await before(page).fill("");
    await expect(echo(page)).toHaveText("retnd artifacts --backup-set " + BILLING);
  });

  test("#1251 an inverted range has no terminal line, since the CLI refuses it", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await expect(echo(page)).toBeVisible();
    await after(page).fill("2026-08-24");
    await before(page).fill("2026-08-20");
    await expect(status(page)).toBeVisible();
    await expect(echo(page)).toHaveCount(0);
  });

  test("#1251 an assurance bucket on top of the set filter has no artifacts line, since artifacts cannot narrow by bucket", async ({ page }) => {
    await open(page, "/backups?set=production/postgres-primary&assurance=untested", "assurance-default-policy", { wire: true });
    await expect(page.getByTestId("assurance-filter-status")).toHaveText("Showing 1 of 2 backups with no recovery test");
    await expect(status(page)).toContainText("Showing 1 backup in");
    await expect(echo(page)).toHaveCount(0);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1251 the status with the ranged line meets AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups?set=" + BILLING, MANY);
      await after(page).fill("2026-08-20");
      await before(page).fill("2026-08-24");
      await expect(echo(page)).toContainText("--until 2026-08-24");
      await setTheme(page, theme);
      expect(await measureContrast(page, "1251 ranged echo " + theme, '[data-testid="set-filter-status"]')).toEqual([]);
    });
  }
});

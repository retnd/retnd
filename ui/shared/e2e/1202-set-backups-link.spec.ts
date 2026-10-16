import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, measureEdges, open, setTheme } from "./helpers";

/**
 * Issue #1202: a backup set's page links to that set's backups, which is the
 * Backups page filtered to the set. The filter lives in the URL (`?set=`), so
 * the link, a reload and a pasted address all land on the same list, and the
 * dropdown writes the same parameter back. Paging (#1180) and the selection
 * (#1107, #1190) behave as they do for a filter chosen on the page.
 *
 * `backups-many-artifact-sets` is the many-backups fixture with every fixture
 * set presented as an artifact set (a snapshot set's history is on its
 * Snapshots page, and its page offers no link to Backups).
 */
const MANY = "backups-many-artifact-sets";
const BILLING = "production/billing-mysql";
const filter = (page: Page) => page.getByRole("combobox", { name: "Filter by backup set" });
const link = (page: Page) => page.getByRole("link", { name: "Backups in this set" });
const setCells = (page: Page) => page.locator("table.table tbody tr td:nth-child(3)");
const where = (page: Page) => page.getByTestId("backups-page-status");

async function onlySet(page: Page, name: string) {
  await expect(setCells(page).first()).toBeVisible();
  const names = await setCells(page).allTextContents();
  expect(names.length).toBeGreaterThan(1);
  expect(new Set(names)).toEqual(new Set([name]));
}

test.describe("#1202 a set links to its backups, and the set filter is in the URL", () => {
  test("#1202 the set page's link lands on Backups filtered to that set, showing only its rows", async ({ page }) => {
    await open(page, "/sets/production/billing-mysql", MANY);
    await expect(link(page)).toBeVisible();
    await link(page).click();
    await expect(page).toHaveURL(/\/backups\?(.*&)?set=production(%2F|\/)billing-mysql/);
    await expect(filter(page)).toHaveValue(BILLING);
    await onlySet(page, "Billing MySQL");
    // Arriving says where the operator is and how to get back.
    const status = page.getByTestId("set-filter-status");
    await expect(status).toContainText(/Showing \d+ backups in Billing MySQL/);
    await expect(status).toContainText("retnd artifacts --backup-set " + BILLING);
    await status.getByRole("link", { name: "Back to the Billing MySQL set" }).click();
    await expect(page).toHaveURL(/\/sets\/production\/billing-mysql/);
  });

  test("#1202 the filter status clears the filter from the URL", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await page.getByTestId("set-filter-status").getByRole("button", { name: "Show all backup sets" }).click();
    await expect(page).not.toHaveURL(/set=/);
    await expect(filter(page)).toHaveValue("");
    await expect(page.getByTestId("set-filter-status")).toHaveCount(0);
  });

  test("#1202 a direct link opens filtered, and a reload keeps the filter", async ({ page }) => {
    await open(page, "/backups?set=" + BILLING, MANY);
    await expect(filter(page)).toHaveValue(BILLING);
    await onlySet(page, "Billing MySQL");
    const before = await where(page).textContent();
    await page.reload();
    await expect(filter(page)).toHaveValue(BILLING);
    await onlySet(page, "Billing MySQL");
    await expect(where(page)).toHaveText(before ?? "");
  });

  test("#1202 the dropdown writes the filter into the URL and All takes it out, keeping the other parameters", async ({ page }) => {
    await open(page, "/backups", MANY);
    await filter(page).selectOption(BILLING);
    await expect(page).toHaveURL(/set=production(%2F|\/)billing-mysql/);
    await expect(page, "the fixture's own parameter survives").toHaveURL(/scenario=backups-many-artifact-sets/);
    await onlySet(page, "Billing MySQL");
    await page.reload();
    await expect(filter(page)).toHaveValue(BILLING);
    await filter(page).selectOption("");
    await expect(page).not.toHaveURL(/set=/);
    await expect.poll(async () => new Set(await setCells(page).allTextContents()).size, "All lists more than one set again").toBeGreaterThan(1);
  });

  test("#1202 following the link still clears a selection, and paging counts only the set's backups", async ({ page }) => {
    await open(page, "/backups", MANY);
    await page.getByRole("checkbox", { name: /^Select (?!all matching)/ }).first().check();
    await expect(page.getByTestId("selection-count")).toContainText("1");
    await filter(page).selectOption(BILLING);
    await expect(page.getByTestId("selection-notice")).toContainText("The selection was cleared because the filter changed.");
    await expect(where(page)).toContainText(/of \d+/);
    const shown = Number(/of (\d+)\s*$/.exec((await where(page).textContent()) ?? "")![1]);
    expect(shown).toBeGreaterThan(0);
    await expect(setCells(page)).toHaveCount(shown);
    await page.getByRole("checkbox", { name: "Select all matching backups" }).check();
    await expect(page.getByTestId("selection-count")).toContainText(String(shown));
  });

  test("#1202 a link to a set that was removed still lists what it left behind", async ({ page }) => {
    await open(page, "/backups?set=production/legacy-redis", "default");
    await expect(filter(page)).toHaveValue("production/legacy-redis");
    await expect(page.getByRole("row").filter({ hasText: "redis-20260812.rdb.zst" })).toBeVisible();
    await expect(page.getByRole("row").filter({ hasText: "billing-20260827.sql.gz" })).toHaveCount(0);
  });

  test("#1202 a snapshot set's page offers no link to Backups, an artifact set's page does", async ({ page }) => {
    await open(page, "/sets/production/postgres-primary", "default");
    await expect(page.getByRole("button", { name: "Snapshots", exact: true })).toBeVisible();
    await expect(link(page)).toHaveCount(0);
    await open(page, "/sets/media/weekly-archive", MANY);
    await expect(page.getByRole("button", { name: "Recover this backup set" })).toBeVisible();
    await expect(link(page)).toBeVisible();
    await expect(link(page)).toHaveAttribute("href", /\/backups\?set=media(%2F|\/)weekly-archive/);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1202 the link meets AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/sets/production/billing-mysql", MANY);
      await expect(link(page)).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1202 set link " + theme, '[data-testid="set-backups-link"]')).toEqual([]);
      expect(await measureEdges(page, "1202 set link " + theme, '[data-testid="set-backups-link"]')).toEqual([]);
    });

    test("#1202 the arrival status meets AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups?set=" + BILLING, MANY);
      await expect(page.getByTestId("set-filter-status")).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1202 set status " + theme, '[data-testid="set-filter-status"]')).toEqual([]);
    });
  }
});

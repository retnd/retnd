/**
 * A slow, watchable pass over what #1202 adds, for the GIFs in the PR. Run
 * with E2E_VIDEO=1. Each test is one GIF, and each ends on the strict
 * assertion for what the recording shows.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, row, setTheme } from "./helpers";

// The recordings are slow on purpose (and on a busy host slower), so the whole
// test, fixture setup included, gets a long limit.
test.describe.configure({ timeout: 240_000 });

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
const BILLING = "billing-20260827.sql.gz";
const PG = "postgres-prod-20260828.dump.zst";
const actions = (page: Page, file: string) => page.getByRole("button", { name: "Actions for " + file });
const menu = (page: Page) => page.getByRole("menu");
const item = (page: Page, name: string | RegExp) => menu(page).getByRole("menuitem", { name });

test("#1202 walkthrough: a row's Actions menu, its keys, a disabled reason, and a test run from it", async ({ page }) => {
  await open(page, "/backups", "default", { wire: true });
  await beat(page, 1200);

  // The menu opens on the row without opening the backup.
  await actions(page, BILLING).click();
  await expect(menu(page)).toBeVisible();
  await beat(page, 1600);
  for (const key of ["ArrowDown", "ArrowDown", "ArrowDown"]) {
    await page.keyboard.press(key);
    await beat(page, 900);
  }
  await page.keyboard.press("Escape");
  await expect(menu(page)).toHaveCount(0);
  await expect(actions(page, BILLING)).toBeFocused();
  await beat(page, 700);

  // Recover artifact opens the backup page's own dialog.
  await actions(page, BILLING).click();
  await item(page, /^Recover artifact/).click();
  const dialog = page.getByRole("dialog", { name: "Recover artifact" });
  await expect(dialog).toBeVisible();
  await beat(page, 1600);
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await beat(page, 600);

  // A set with no validator keeps the item, disabled, with the reason.
  await open(page, "/backups", "assurance-no-validator", { wire: true });
  await beat(page, 800);
  await actions(page, "redis-20260812.rdb.zst").click();
  const blocked = item(page, "Run recovery test");
  await expect(blocked).toHaveAttribute("aria-disabled", "true");
  await beat(page, 2200);
  await page.keyboard.press("Escape");

  // A recovery test started from the menu runs to its end above the table.
  await open(page, "/backups", "default", { wire: true });
  await beat(page, 800);
  await actions(page, PG).click();
  await beat(page, 800);
  await item(page, "Run recovery test").click();
  const runs = page.getByRole("region", { name: "Started from this list" });
  const panel = runs.getByTestId("operation-progress");
  await expect(panel).toHaveAttribute("data-state", "running");
  await beat(page, 2500);
  await expect(panel).toHaveAttribute("data-state", "complete", { timeout: 60_000 });
  await expect(row(page, PG).locator('[data-claim="recovery"]')).toHaveAttribute("data-state", "pass", { timeout: 10_000 });
  await beat(page, 2000);

  // The same menu in the dark theme.
  await setTheme(page, "dark");
  await actions(page, BILLING).click();
  await expect(menu(page)).toBeVisible();
  await beat(page, 2200);
});

test("#1202 walkthrough: a set's page links to its backups, and the filter rides in the URL", async ({ page }) => {
  await open(page, "/sets/production/billing-mysql", "backups-many-artifact-sets");
  await beat(page, 1500);
  const link = page.getByRole("link", { name: "Backups in this set" });
  await expect(link).toBeVisible();
  await link.click();
  await expect(page).toHaveURL(/\/backups\?(.*&)?set=production(%2F|\/)billing-mysql/);
  const status = page.getByTestId("set-filter-status");
  await expect(status).toContainText(/Showing \d+ backups in Billing MySQL/);
  await beat(page, 2200);
  // A pasted address lands on the same list.
  await open(page, "/backups?set=production%2Fbilling-mysql", "backups-many-artifact-sets");
  await expect(status).toContainText(/Showing \d+ backups in Billing MySQL/);
  await beat(page, 1500);
  await status.getByRole("link", { name: "Back to the Billing MySQL set" }).click();
  await expect(page).toHaveURL(/\/sets\/production\/billing-mysql/);
  await beat(page, 1500);
});

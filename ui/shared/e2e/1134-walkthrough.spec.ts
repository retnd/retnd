/**
 * Slow passes over the fixed #1134 surfaces for the GIFs in the PR, one per
 * theme. Same scenario as 1134-verification-model.spec.ts, walked from the
 * Dashboard to the set's page. Each ends on a DOM assertion about the state it
 * shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, preselectTheme, setTheme, spaGo } from "./helpers";

const S = "assurance-default-policy";
const NEWEST = "postgres-prod-20260829.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const beat = async (p: Page, ms = 1100) => {
  // park the pointer on empty page so no hover pop-up lingers in the frame
  await p.mouse.move(1270, 5);
  await p.waitForTimeout(ms);
};
const into = (loc: Locator) => loc.evaluate((el) => el.scrollIntoView({ block: "center" }));

test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1134 walkthrough (${theme}): Dashboard, Backups, a backup, its set, the Backup Sets card`, async ({ page }) => {
    test.setTimeout(180_000);
    await preselectTheme(page, theme);
    await open(page, "/", S, { wire: true });
    const tile = page.getByRole("region", { name: "Assurance summary" });
    await expect(tile.getByTestId("assurance-untested-note")).toBeVisible();
    await setTheme(page, theme);
    await into(tile);
    await beat(page, 2400);
    await tile.getByRole("link", { name: "Show 1 backup with no recovery test" }).click();
    await expect(page.getByTestId("assurance-filter-status")).toHaveText("Showing 1 of 2 backups with no recovery test");
    await beat(page, 2400);
    await page.getByRole("button", { name: "Show all backups" }).click();
    const chip = page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: NEWEST }) }).locator('[data-claim="recovery"]');
    await expect(chip).toContainText("Default configuration");
    await beat(page, 2400);
    await spaGo(page, NEWEST_PATH);
    await setTheme(page, theme);
    await expect(page.getByRole("listitem").filter({ hasText: "CONTENT VERIFIED" })).toContainText(/\d\d:\d\d:\d\d/);
    await into(page.getByRole("heading", { name: "Lifecycle" }));
    await beat(page, 2600);
    await into(page.getByRole("region", { name: "Copies" }));
    await beat(page, 2000);
    await spaGo(page, "/sets/production/postgres-primary");
    await setTheme(page, theme);
    const card = page.getByRole("region", { name: "Assurance" });
    await card.getByRole("button", { name: "Show validator output" }).click();
    await into(card);
    await beat(page, 2600);
    await into(page.getByTestId("validation-block"));
    await expect(page.getByTestId("validation-ran-structure")).toContainText("Built-in structure check: readable");
    await beat(page, 2600);
    await spaGo(page, "/sets");
    await setTheme(page, theme);
    const field = page.locator("article").filter({ hasText: "production/postgres-primary" }).getByTestId("field-recovery");
    await into(field);
    await expect(field).toContainText("Default configuration");
    await expect(page.locator("article").filter({ hasText: "production/postgres-primary" }).getByTestId("field-last-validation")).toHaveText(/^Readable \(built-in check\)/);
    await beat(page, 2600);
  });
}

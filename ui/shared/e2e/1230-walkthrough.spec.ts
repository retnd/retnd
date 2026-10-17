/**
 * Slow passes over #1230 for the GIFs in the PR, one per theme: the Settings
 * card with all four states, approving an edited script, and a backup's page
 * under an edited validator. Each ends on a DOM assertion about what it shows.
 * Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);
test.describe.configure({ timeout: 150_000 });

for (const theme of ["light", "dark"] as const) {
  test(`#1230 walkthrough (${theme}): Settings, the four states, approve an edited script`, async ({ page }) => {
    await open(page, "/settings", "validators-states");
    const card = page.getByRole("region", { name: "Recovery-test validators" });
    await card.scrollIntoViewIfNeeded();
    await expect(card).toBeVisible({ timeout: 30_000 });
    // After the app has booted: a theme set earlier is overwritten by its start-up.
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await beat(page, 2500);
    await card.getByRole("button", { name: "Edit manifest-check" }).click();
    const dlg = page.getByRole("dialog", { name: "Edit validator" });
    await expect(dlg.locator("[data-validator-script-state]")).toContainText("Edited on disk");
    await beat(page, 3000);
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await beat(page, 1800);
    await dlg.getByRole("button", { name: "Save changes" }).click();
    const state = card.getByRole("row").filter({ hasText: "manifest-check" }).locator("[data-script-state]");
    await expect(state).toHaveAttribute("data-script-state", "pinned");
    await beat(page, 3000);
  });

  test(`#1230 walkthrough (${theme}): a backup's page under an edited validator`, async ({ page }) => {
    await open(page, "/backups/production/billing-mysql/billing-20260827.sql.gz", "validators-state-edited");
    const region = page.getByRole("region", { name: "Assurance" });
    await expect(region.locator("[data-validator-script-state]")).toBeVisible({ timeout: 30_000 });
    await setTheme(page, theme);
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await region.scrollIntoViewIfNeeded();
    await beat(page, 4000);
    await expect(region.locator("[data-validator-script-state]")).toContainText("Edited on disk");
  });
}

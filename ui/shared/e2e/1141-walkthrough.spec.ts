/**
 * Slow passes over the #1141 surfaces for the GIFs, one per theme. Same
 * scenario as 1141-default-config-wording.spec.ts: a set on the default
 * configuration with no recovery test on either backup, walked from the
 * Dashboard to the Backup Sets card, then once more with the policy read
 * failing. Run with E2E_VIDEO=1.
 *
 * W1141=before records the behaviour on main: each stop ends on an assertion
 * that the wording is still wrong, so the recording cannot be of a fixed tree.
 */
import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { open, setTheme, spaGo } from "./helpers";

const S = "assurance-default-untested";
const NEWEST = "postgres-prod-20260829.dump.zst";
const NEWEST_PATH = "/backups/production/postgres-primary/" + NEWEST;
const before = process.env.W1141 === "before";
const beat = async (p: Page, ms = 1100) => {
  await p.mouse.move(1270, 5);
  await p.waitForTimeout(ms);
};
const into = (loc: Locator) => loc.evaluate((el) => el.scrollIntoView({ block: "center" }));

test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1141 walkthrough (${theme}): Dashboard, Backups, a backup, the Backup Sets card, an unreadable policy`, async ({ page }) => {
    test.setTimeout(180_000);
    await open(page, "/", S, { wire: true });
    await setTheme(page, theme);
    const tile = page.getByRole("region", { name: "Assurance summary" });
    await expect(tile.getByTestId("assurance-untested-note")).toBeVisible();
    await into(tile);
    if (before) await expect(tile.getByTestId("assurance-untested-note")).not.toContainText("default configuration");
    else await expect(tile.getByTestId("assurance-untested-note")).toContainText("2 of them are in sets on the default configuration.");
    await beat(page, 2800);

    await tile.getByRole("link", { name: "Show 2 backups with no recovery test" }).click();
    const chip = page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: NEWEST }) }).locator('[data-claim="recovery"]');
    await expect(chip).toContainText("Default configuration");
    await into(chip);
    if (before) await expect(chip.getByTestId("recovery-untested")).toHaveCount(0);
    else await expect(chip.getByTestId("recovery-untested")).toHaveText("No recovery test has run yet");
    await beat(page, 2800);

    await spaGo(page, NEWEST_PATH);
    await setTheme(page, theme);
    const card = page.getByRole("region", { name: "Assurance" });
    await expect(card.locator('[data-claim="recovery"]')).toContainText("Default configuration");
    await into(card);
    if (before) await expect(card.getByTestId("recovery-untested")).toHaveCount(0);
    else await expect(card.getByTestId("recovery-untested")).toHaveText("No recovery test has run yet");
    await beat(page, 2800);

    await spaGo(page, "/sets");
    await setTheme(page, theme);
    const field = page.locator("article").filter({ hasText: "production/postgres-primary" }).getByTestId("field-recovery");
    await into(field);
    await expect(field).toContainText("Default configuration");
    if (before) await expect(field.getByTestId("recovery-untested")).toHaveCount(0);
    else await expect(field.getByTestId("recovery-untested")).toHaveText("No recovery test has run yet");
    await beat(page, 2800);

    // The policy cannot be read: main called this "Not configured".
    await page.addInitScript(() => { (window as unknown as Record<string, unknown>).__retndPolicy = { fail: true }; });
    await open(page, "/backups", S, { wire: true });
    await setTheme(page, theme);
    const failed = page.getByRole("row").filter({ has: page.locator("td.mono", { hasText: NEWEST }) }).locator('[data-claim="recovery"]');
    await into(failed);
    if (before) await expect(failed).toContainText("Not configured");
    else await expect(failed).toContainText("Configuration unavailable");
    await beat(page, 3000);
  });
}

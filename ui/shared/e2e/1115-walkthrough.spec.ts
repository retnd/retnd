/**
 * Slow passes over the #1115 behaviour for the GIFs in the PR, one per theme.
 * Each ends on a DOM assertion about the state it shows. Run with E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme } from "./helpers";

const beat = (p: Page, ms = 900) => p.waitForTimeout(ms);

for (const theme of ["light", "dark"] as const) {
  test(`#1115 walkthrough (${theme}): filter to warnings, to errors, clear, and a live warning arrives`, async ({ page }) => {
    await open(page, "/", "dock-live-line");
    await setTheme(page, theme);
    const log = page.getByRole("log");
    await expect(log).toContainText("backup set updated");
    await beat(page);
    await page.getByRole("button", { name: "Show 1 warning" }).click();
    await expect(log).not.toContainText("backup set updated");
    await beat(page, 1500);
    await page.getByRole("button", { name: "Show 1 error" }).click();
    await expect(log).toContainText("cycle finished with an error");
    await beat(page, 1500);
    await page.getByRole("button", { name: "Show 1 warning" }).click();
    await page.evaluate(() => sessionStorage.setItem("retnd.mock.dock.live", "1"));
    await expect(page.getByRole("button", { name: "Show 2 warnings" })).toBeVisible({ timeout: 10_000 });
    await expect(log).toContainText("disk latency above threshold");
    await expect(log).not.toContainText("retention pass complete");
    await beat(page, 2000);
    await page.getByRole("button", { name: "Show 2 warnings" }).click();
    await expect(log).toContainText("retention pass complete");
    await beat(page, 1500);
  });
}

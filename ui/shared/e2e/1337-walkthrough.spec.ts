/**
 * Issue #1337: a recording of Settings at 360px after the fix, light then
 * dark, with the contrast of the narrow shell measured in both themes. Run
 * with E2E_VIDEO=1 to get the .webm.
 */
import { expect, test } from "@playwright/test";
import { contrastReport, open } from "./helpers";

const PHONE = { width: 360, height: 800 };
test.use({ viewport: PHONE, video: process.env.E2E_VIDEO ? { mode: "on", size: PHONE } : "off" });

test("1337 walkthrough: Settings at 360px, light then dark (#1337)", async ({ page }) => {
  for (const theme of ["light", "dark"]) {
    await page.addInitScript((t) => window.localStorage.setItem("retnd.theme", t), theme);
    await open(page, "/settings");
    await expect(page.locator("[data-settings-page] section.card").first()).toBeVisible();
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    await page.waitForTimeout(700);
    await page.locator("main").evaluate((m) => m.scrollTo({ top: 600, behavior: "instant" }));
    await page.waitForTimeout(700);
    await page.locator("main").evaluate((m) => m.scrollTo({ top: 1400, behavior: "instant" }));
    await page.waitForTimeout(700);
    for (const sel of ["header", "nav[aria-label=Sections]", "[data-settings-page]"]) {
      const rows = await contrastReport(page, sel);
      const min = rows.reduce((a, r) => (r.ratio < a.ratio ? r : a), rows[0]);
      console.log(`CONTRAST ${theme} ${sel}: ${rows.length} text nodes, lowest ${min.ratio.toFixed(2)}:1 needs ${min.need} ("${min.text.slice(0, 24)}")`);
      expect(rows.filter((r) => r.ratio < r.need), `${theme} ${sel}`).toEqual([]);
    }
  }
});

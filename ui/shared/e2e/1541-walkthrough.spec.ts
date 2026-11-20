/**
 * Issue #1541: a recording of the Backup sets page with one set whose dump
 * the producer has not published for 30 hours (the warning) and one waiting
 * two hours (the quiet count), light then dark, with the readout's tooltip
 * opened. Both sets stay Healthy. Run with E2E_VIDEO=1 to get the .webm.
 */
import { expect, test } from "@playwright/test";
import { open } from "./helpers";

const SIZE = { width: 1280, height: 900 };
test.use({ viewport: SIZE, video: process.env.E2E_VIDEO ? { mode: "on", size: SIZE } : "off" });

test("1541 walkthrough: complete but unreadable source files on the set cards (#1541)", async ({ page }) => {
  for (const theme of ["light", "dark"]) {
    await page.addInitScript(
      (t) => {
        window.localStorage.setItem("retnd.theme", t);
        window.localStorage.setItem("retnd.tooltips", "on");
      },
      theme
    );
    await open(page, "/sets", "pending-unreadable");
    await expect(page.locator("html")).toHaveAttribute("data-theme", theme);
    const warned = page.locator("article.card").filter({ hasText: "Production PostgreSQL" }).getByTestId("pending-unreadable");
    const quiet = page.locator("article.card").filter({ hasText: "Billing MySQL" }).getByTestId("pending-unreadable");
    await expect(warned).toBeVisible();
    await expect(quiet).toBeVisible();
    await warned.scrollIntoViewIfNeeded();
    await page.waitForTimeout(1200);
    await warned.hover();
    await expect(page.locator(".tooltip__pop:visible")).toHaveCount(1);
    await page.waitForTimeout(1500);
    await page.mouse.move(5, 5);
    await quiet.scrollIntoViewIfNeeded();
    await page.waitForTimeout(1200);
  }
});

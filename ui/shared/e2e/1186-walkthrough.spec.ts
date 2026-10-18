/**
 * Slow pass for the #1186 GIF, one per theme: a deployment with 40 retained
 * backups, then one with 119. Run with E2E_VIDEO=1.
 *
 * W1186=before records main: each stop ends on an assertion that the badge is
 * still the set count, so the recording cannot be of a fixed tree.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, preselectTheme, setTheme } from "./helpers";

const before = process.env.W1186 === "before";
const beat = async (p: Page, ms = 1400) => {
  await p.mouse.move(1270, 5);
  await p.waitForTimeout(ms);
};
const badge = (p: Page) => p.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: /^Backups/ }).locator("span.mono");

test.use({ viewport: { width: 1280, height: 900 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 900 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1186 walkthrough (${theme}): 40 then 119 retained backups`, async ({ page }) => {
    test.setTimeout(120_000);
    await preselectTheme(page, theme);
    await open(page, "/?artifacts=40");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    await badge(page).hover();
    await beat(page, 2200);
    if (before) await expect(badge(page)).not.toHaveText("40");
    else await expect(badge(page)).toHaveText("40");
    await beat(page, 1800);

    await open(page, "/?artifacts=119");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    await beat(page, 2200);
    if (before) await expect(badge(page)).not.toHaveText(">100");
    else await expect(badge(page)).toHaveText(">100");
    await beat(page, 2800);
  });
}

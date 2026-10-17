/**
 * Slow passes over the #1111 settings behaviour for the GIFs in the PR, one per
 * theme. Each ends on a DOM assertion about the state it shows. Run with
 * E2E_VIDEO=1.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open, setTheme, spaGo } from "./helpers";

const beat = async (p: Page, ms = 900) => {
  // park the pointer on empty page so no hover pop-up lingers in the frame
  await p.mouse.move(1200, 200);
  await p.waitForTimeout(ms);
};
const SET = "/sets/production/postgres-primary";
const card = (p: Page) => p.getByRole("region", { name: "Assurance" });
const BUILTIN = "Built-in check when no validator";
/** The card is below the fold on both pages; a GIF of the page top shows nothing of it. */
const show = (p: Page) => card(p).evaluate((el) => el.scrollIntoView({ block: "start" }));

// Tall enough that the whole card is on screen at once, so the GIF does not
// chase the controls up and down the page.
test.use({ viewport: { width: 1280, height: 1400 }, video: process.env.E2E_VIDEO ? { mode: "on", size: { width: 1280, height: 1400 } } : "off" });

for (const theme of ["light", "dark"] as const) {
  test(`1111 walkthrough (${theme}): Settings, then a set's own policy, round trip the built-in check`, async ({ page }) => {
    await open(page, "/settings");
    await expect(page.locator("main")).toBeVisible();
    await setTheme(page, theme);
    await show(page);
    await beat(page);
    await card(page).getByLabel("Recovery test", { exact: true }).selectOption("builtin");
    await beat(page);
    await card(page).getByLabel(BUILTIN).selectOption("off");
    await expect(card(page).getByRole("alert")).toContainText("builtin_check: off refuses it");
    await beat(page, 1800);
    await card(page).getByLabel(BUILTIN).selectOption("structure");
    await beat(page);
    await card(page).getByRole("button", { name: "Save" }).click();
    await expect(card(page).getByRole("status")).toHaveText("Saved");
    await beat(page, 1500);
    await spaGo(page, SET);
    await setTheme(page, theme);
    await card(page).getByRole("radio", { name: "This set's own policy" }).check();
    await show(page);
    await card(page).getByLabel("Recovery test", { exact: true }).selectOption("off");
    await beat(page);
    await card(page).getByLabel(BUILTIN).selectOption("off");
    await beat(page, 1800);
    await card(page).getByRole("button", { name: "Save policy" }).click();
    await expect(card(page).getByRole("status")).toHaveText("Saved");
    await beat(page, 1500);
    await expect(card(page).getByLabel(BUILTIN)).toHaveValue("off");
    await expect(page.locator("code").filter({ hasText: "--recovery-test-builtin-check off" })).toBeVisible();
  });
}

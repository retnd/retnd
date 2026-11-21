import { expect, test } from "@playwright/test";
import type { Locator, Page } from "@playwright/test";
import { openAgents, openMint } from "./1351-agents";

/**
 * #1619 follow-up: a tooltip a keyboard user opened inside a dialog takes the
 * first Escape, and the dialog takes the second. One press used to close both.
 * Focus that arrives from script (a refusal sending focus back to the field,
 * a dialog's own autofocus) is not a request for help, so it opens nothing.
 */
const popUp = (page: Page) => page.locator(".tooltip__pop:not([hidden])");

async function tabTo(page: Page, el: Locator) {
  for (let i = 0; i < 40; i++) {
    await page.keyboard.press("Tab");
    if (await el.evaluate((n) => n === document.activeElement)) return;
  }
  throw new Error("Tab never reached the control");
}

test.describe("#1619 Escape in the Create token dialog", () => {
  test("#1619 the first Escape closes the Name tooltip and keeps the dialog, the second closes the dialog", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    // The dialog's own autofocus (the safe button) opens nothing.
    await expect(popUp(page)).toHaveCount(0);

    await tabTo(page, dlg.getByLabel("Name", { exact: true }));
    await expect(popUp(page)).toHaveCount(1);

    await page.keyboard.press("Escape");
    await expect(popUp(page)).toHaveCount(0);
    await expect(dlg).toBeVisible();

    await page.keyboard.press("Escape");
    await expect(dlg).toBeHidden();
  });

  test("#1619 a click into the Name field opens no label tooltip", async ({ page }) => {
    await openAgents(page, "default");
    const dlg = await openMint(page);
    await dlg.getByLabel("Name", { exact: true }).click();
    await expect(popUp(page)).toHaveCount(0);
  });
});

/**
 * Issue #1337: at 360px the shell keeps its full-width navigation beside the
 * content, so the Settings column is left about 150px and every card on the
 * page is squeezed to about 47px, which pushes the page sideways. This
 * happens in the default scenario, with nothing turned on, so it is the
 * layout and not any one card.
 *
 * These are red until the shell and the Settings grid collapse on a narrow
 * screen. A 360px screen with the 16px gutter on each side leaves 328px for
 * content, and that is what the column and each card should get.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { open } from "./helpers";

const PHONE = { width: 360, height: 800 };
const CONTENT = PHONE.width - 2 * 16;

async function settingsAt360(page: Page, scenario: string) {
  await page.setViewportSize(PHONE);
  await open(page, "/settings", scenario);
  await expect(page.locator("[data-settings-page]")).toBeVisible();
  await expect(page.locator("[data-settings-page] section.card").first()).toBeVisible();
}

test.describe("#1337 Settings at 360px", () => {
  test("the page does not scroll sideways at 360px (#1337)", async ({ page }) => {
    await settingsAt360(page, "default");
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, "sideways scroll in px").toBeLessThanOrEqual(0);
  });

  test("the content column gets the screen less its gutters (#1337)", async ({ page }) => {
    await settingsAt360(page, "default");
    const width = await page.locator("main").evaluate((e) => e.getBoundingClientRect().width);
    expect(width, "main column width in px").toBeGreaterThanOrEqual(CONTENT);
  });

  for (const scenario of ["default", "encryption-on"]) {
    test(`every Settings card is as wide as the content column in ${scenario} (#1337)`, async ({ page }) => {
      await settingsAt360(page, scenario);
      const cards = await page.locator("[data-settings-page] section.card").evaluateAll((els) =>
        els.map((e) => ({ name: e.querySelector("h2")?.textContent?.trim() ?? "", width: Math.round(e.getBoundingClientRect().width) }))
      );
      // The control: the page drew its cards, so an empty list cannot pass.
      expect(cards.length).toBeGreaterThan(5);
      const narrow = cards.filter((c) => c.width < CONTENT - 32).map((c) => `${c.name} ${c.width}px`);
      expect(narrow, "cards narrower than the content column").toEqual([]);
    });
  }
});

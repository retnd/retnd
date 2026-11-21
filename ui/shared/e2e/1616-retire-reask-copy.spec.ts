import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1616, the UI items from the #1536 reviews. When asking the host again
 * about a changed choice fails, the Retire dialog:
 *
 *   - no longer promises that Retire stays off "until it answers" (nothing
 *     asked again unless the choice changed);
 *   - gives the reason as a sentence of its own, not a lowercase fragment
 *     glued on after a full stop;
 *   - offers an Ask again button, which is the thing that asks again.
 *
 * The mock's `retire-reask-fails` scenario fails every non-empty choice, so
 * the banner is reached by picking another replacement. Run with E2E_VIDEO=1
 * for the GIF: the walkthrough ends on a beat with the banner on screen.
 */
const dialog = (page: Page) => page.getByRole("dialog");
const beat = (p: Page) => (process.env.E2E_VIDEO ? p.waitForTimeout(1200) : Promise.resolve());

async function failedReask(page: Page) {
  await open(page, "/backups/media/weekly-archive/media-week34.tar", "retire-reask-fails");
  await page.getByRole("button", { name: "Retire…" }).click();
  await dialog(page).getByLabel("Replaced by").selectOption("media-week35.tar");
  const banner = dialog(page).getByTestId("retire-reask-error");
  await expect(banner).toBeVisible();
  return banner;
}

test.describe("#1616 the failed re-ask in the Retire dialog", () => {
  test("#1616 the banner does not promise that Retire waits for an answer", async ({ page }) => {
    const banner = await failedReask(page);
    await expect(banner).not.toContainText("until it answers");
    await expect(banner).toContainText("Retire is off");
  });

  test("#1616 the reason is a sentence of its own, capitalised and ended", async ({ page }) => {
    const banner = await failedReask(page);
    const text = ((await banner.textContent()) ?? "").trim();
    // Every sentence starts with a capital letter: no ". lowercase" fragment.
    expect(text, text).not.toMatch(/\.\s+[a-z]/);
    expect(text, text).toMatch(/[.!?]$/);
  });

  test("#1616 Ask again is a button in the banner, and pressing it asks the host again", async ({ page }) => {
    await failedReask(page);
    const again = dialog(page).getByRole("button", { name: "Ask again" });
    await expect(again).toBeVisible();
    await expect(again).toBeEnabled();
    await again.click();
    // The mock still refuses this choice, so the banner is back after the
    // second answer, and Retire is still off.
    await expect(dialog(page).getByTestId("retire-reask-error")).toBeVisible();
    await expect(dialog(page).getByRole("button", { name: /^Retire/ }).last()).toBeDisabled();
  });

  test("#1616 walkthrough: pick another replacement, the host does not answer, the banner stays on screen", async ({ page }) => {
    await failedReask(page);
    await beat(page);
    await expect(dialog(page).getByTestId("retire-reask-error")).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`#1616 the banner and its button are readable in the ${theme} theme`, async ({ page }) => {
      await open(page, "/backups/media/weekly-archive/media-week34.tar", "retire-reask-fails");
      await setTheme(page, theme);
      await page.getByRole("button", { name: "Retire…" }).click();
      await dialog(page).getByLabel("Replaced by").selectOption("media-week35.tar");
      await expect(dialog(page).getByTestId("retire-reask-error")).toBeVisible();
      await expect(dialog(page).getByRole("button", { name: "Ask again" })).toBeVisible();
      expect(await measureContrast(page, "1616 retire dialog, failed re-ask, " + theme, "[role=dialog]")).toEqual([]);
    });
  }
});

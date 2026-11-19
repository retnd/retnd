import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1157, the snapshot restore wizard: Start restore on a build whose
 * destructive gate is shut. A restore is the act an operator most wants to be
 * told about before they have walked four steps, so the refusal has to be
 * unmissable and has to carry the command with the answers they just gave.
 */
const PAGE = "/sets/production/postgres-primary/restore";
const refusal = (page: Page) => page.locator("[data-gate-refusal]");

async function walkToTheEnd(page: Page) {
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByRole("button", { name: "Continue" }).click();
  await page.getByLabel("Restore into").fill("/srv/restored");
  await page.getByRole("button", { name: "Continue" }).click();
}

test.describe("#1157 snapshot restore, gate shut", () => {
  test("Start restore says retrying will not help and echoes retnd snapshot restore", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await walkToTheEnd(page);
    await page.getByRole("button", { name: "Start restore" }).click();
    const block = refusal(page);
    await expect(block).toBeVisible();
    await expect(block).toContainText(/retrying will not help/i);
    await expect(block).toContainText("retnd snapshot restore production/postgres-primary --to /srv/restored");
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await walkToTheEnd(page);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(refusal(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1157 restore " + theme, "[data-gate-refusal]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });

  test("control: with the gate not in the way the restore starts", async ({ page }) => {
    await open(page, PAGE, "default");
    await walkToTheEnd(page);
    await page.getByRole("button", { name: "Start restore" }).click();
    await expect(page.getByText(/restore/i).first()).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });
});

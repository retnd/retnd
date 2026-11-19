import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1157, the retention preview dialog: applying a plan on a build whose
 * destructive gate is shut. The dialog used to print the engine's sentence
 * and nothing else. It has to say that nothing was deleted, that retrying
 * will not help, and give the command that applies retention from a shell.
 */
const PAGE = "/sets/production/postgres-primary";
const refusal = (page: Page) => page.locator("[data-gate-refusal]");

async function applyTheFirstPlan(page: Page) {
  await page.getByRole("button", { name: "Preview retention plan" }).click();
  const dialog = page.getByRole("dialog", { name: "Retention preview" });
  await expect(dialog).toBeVisible();
  await dialog.getByRole("button", { name: /^Continue/ }).click();
  await page.getByRole("button", { name: /^Delete \d+ backups$/ }).click();
}

test.describe("#1157 retention preview, gate shut", () => {
  test("applying says nothing was deleted, retrying will not help, and echoes retnd retention apply", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await applyTheFirstPlan(page);
    const block = refusal(page);
    await expect(block).toBeVisible();
    await expect(block).toContainText(/nothing was deleted/i);
    await expect(block).toContainText(/retrying will not help/i);
    await expect(block).toContainText("retnd retention apply production/postgres-primary --acknowledge");
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await applyTheFirstPlan(page);
    await expect(refusal(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1157 retention " + theme, "[data-gate-refusal]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });

  test("control: with the gate not in the way applying closes the dialog", async ({ page }) => {
    await open(page, PAGE, "default");
    await applyTheFirstPlan(page);
    await expect(page.getByRole("dialog", { name: "Retention preview" })).toHaveCount(0);
    await expect(refusal(page)).toHaveCount(0);
  });
});

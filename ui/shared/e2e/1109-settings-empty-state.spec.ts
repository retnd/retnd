import { expect, test } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

// Issue #1109: the Settings workflow card's empty state names the RETND_*
// built-ins, not the project's former name. The former name is built from
// pieces so this file carries no occurrence of it.
const FORMER = ["BACK", "UPD"].join("");
const WANT =
  "No deployment-wide workflow variables are configured. Hooks still receive the RETND_* built-ins below.";

test.describe("#1109 Settings workflow empty state", () => {
  test("says RETND_* and never the former name", async ({ page }) => {
    await open(page, "/settings");
    const note = page.getByTestId("workflow-env-empty");
    await expect(note).toHaveText(WANT);
    await expect(page.locator("body")).not.toContainText(FORMER);
    await page.waitForTimeout(600);
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the empty-state text meets 4.5:1 in the ${theme} theme`, async ({ page }) => {
      await open(page, "/settings");
      await expect(page.getByText(WANT, { exact: true })).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, `#1109 empty state, ${theme}`, '[data-testid="workflow-env-empty"]')).toEqual([]);
    });
  }
});

import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1157, the snapshots list: Verify latest on a build whose destructive
 * gate is shut. The gate is shut in every build this repository ships, so the
 * refusal is the ordinary outcome of pressing this button over HTTP, and the
 * screen has to say that retrying will not help and give the terminal command
 * that does the same thing, instead of the generic "Could not start the
 * verification" a transient failure would get.
 *
 * `scenario=gate-shut` makes the mock answer exactly what the engine answers
 * (code DESTRUCTIVE_OPERATIONS_DISABLED, the sentence in
 * apps/common/webhost/gate.go). The control scenario is the same page with
 * the gate not in the way.
 */
const PAGE = "/sets/production/postgres-primary/snapshots";
const COMMAND = "retnd snapshot verify production/postgres-primary";
const refusal = (page: Page) => page.locator("[data-gate-refusal]");

test.describe("#1157 snapshots list, gate shut", () => {
  test("Verify latest says retrying will not help and echoes the terminal command", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: "Verify latest" }).click();
    const block = refusal(page);
    await expect(block).toBeVisible();
    await expect(block).toContainText(/retrying will not help/i);
    await expect(block).toContainText(COMMAND);
    await expect(block, "it names the issue that lifts the refusal").toContainText("#92");
    await expect(page.getByRole("region", { name: "Verification in progress" })).toHaveCount(0);
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(refusal(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1157 snapshots " + theme, "[data-gate-refusal]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });

  test("control: with the gate not in the way the same press starts a verification", async ({ page }) => {
    await open(page, PAGE, "default");
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });
});

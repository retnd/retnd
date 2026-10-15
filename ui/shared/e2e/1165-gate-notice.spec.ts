import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1165: the screens read the destructive gate's state from
 * GET /system/capabilities (destructive_operations) instead of assuming it,
 * so a shut gate is said BEFORE anyone presses a gated control, and the
 * refusal that follows a press uses the reason the engine reported.
 *
 * Two scenarios shut the gate and differ only in the reason on the field.
 * The second one is the proof the words come from the field: nothing in the
 * UI contains "local-account profile", so it can only appear if it was read.
 * The default scenario leaves the gate open and is the control: no notice
 * anywhere, and the control presses through.
 */
const SET = "production/postgres-primary";
const REASON = "local-account profile: gate shut until #92";
const notice = (page: Page) => page.locator("[data-gate-notice]");
const refusal = (page: Page) => page.locator("[data-gate-refusal]");

const SCREENS: Array<{ name: string; path: string; command: string }> = [
  { name: "snapshots list", path: `/sets/${SET}/snapshots`, command: `retnd snapshot verify ${SET}` },
  { name: "snapshot detail", path: `/sets/${SET}/snapshots/run-2026-09-13-0400`, command: `retnd snapshot verify ${SET} --run run-2026-09-13-0400` },
  { name: "snapshot restore", path: `/sets/${SET}/restore`, command: `retnd snapshot restore ${SET} --to` }
];

test.describe("#1165 a shut gate is said before anything is pressed", () => {
  for (const screen of SCREENS) {
    test(`${screen.name}: the notice carries the reason the engine reported, and the command`, async ({ page }) => {
      await open(page, screen.path, "gate-shut-local-account");
      const block = notice(page);
      await expect(block).toBeVisible();
      await expect(block).toContainText(REASON);
      await expect(block).toContainText(screen.command);
      await expect(refusal(page), "nothing was pressed, so nothing was refused").toHaveCount(0);
    });
  }

  test("retention preview: the dialog says so before Continue is pressed", async ({ page }) => {
    await open(page, `/sets/${SET}`, "gate-shut-local-account");
    await page.getByRole("button", { name: "Preview retention plan" }).click();
    const dialog = page.getByRole("dialog", { name: "Retention preview" });
    await expect(dialog).toBeVisible();
    const block = dialog.locator("[data-gate-notice]");
    await expect(block).toBeVisible();
    await expect(block).toContainText(REASON);
    await expect(block).toContainText(`retnd retention apply ${SET} --acknowledge`);
  });

  test("the refusal after a press repeats the reason the field gave, not a sentence of the UI's own", async ({ page }) => {
    await open(page, `/sets/${SET}/snapshots`, "gate-shut-local-account");
    await page.getByRole("button", { name: "Verify latest" }).click();
    await expect(refusal(page)).toBeVisible();
    await expect(refusal(page)).toContainText(REASON);
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, `/sets/${SET}/snapshots`, "gate-shut-local-account");
    await expect(notice(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1165 notice " + theme, "[data-gate-notice]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });
});

test.describe("#1165 control: an open gate says nothing", () => {
  for (const screen of SCREENS) {
    test(`${screen.name}: no notice, because the field says open`, async ({ page }) => {
      await open(page, screen.path, "default");
      // Wait for the page itself so an absent notice is not just an unloaded page.
      await expect(page.getByRole("heading").first()).toBeVisible();
      await expect(notice(page)).toHaveCount(0);
    });
  }

  test("retention preview: no notice in the dialog", async ({ page }) => {
    await open(page, `/sets/${SET}`, "default");
    await page.getByRole("button", { name: "Preview retention plan" }).click();
    const dialog = page.getByRole("dialog", { name: "Retention preview" });
    await expect(dialog).toBeVisible();
    await expect(dialog.locator("[data-gate-notice]")).toHaveCount(0);
  });
});

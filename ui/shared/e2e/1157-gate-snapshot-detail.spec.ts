import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1157, one snapshot: Verify now and Place hold on a build whose
 * destructive gate is shut. Each refusal names the terminal command for that
 * act on that run, the way the engine's own echo (core/cliecho) spells it.
 */
const RUN = "run-2026-09-13-0400";
const PAGE = "/sets/production/postgres-primary/snapshots/" + RUN;
const refusal = (page: Page) => page.locator("[data-gate-refusal]");

test.describe("#1157 snapshot detail, gate shut", () => {
  test("Verify now echoes retnd snapshot verify for this run", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: "Verify now" }).click();
    const block = refusal(page);
    await expect(block).toBeVisible();
    await expect(block).toContainText(/retrying will not help/i);
    await expect(block).toContainText("retnd snapshot verify production/postgres-primary --run " + RUN);
  });

  test("Place hold echoes retnd snapshot hold with the reason that was typed", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: /Place hold/ }).click();
    await page.getByLabel("Reason").fill("legal-review");
    await page.getByRole("button", { name: "Place hold", exact: true }).click();
    const block = refusal(page);
    await expect(block).toBeVisible();
    await expect(block).toContainText(/retrying will not help/i);
    await expect(block).toContainText("retnd snapshot hold production/postgres-primary --reason legal-review --run " + RUN);
  });

  test("the words are readable in both themes (WCAG AA)", async ({ page }) => {
    await open(page, PAGE, "gate-shut");
    await page.getByRole("button", { name: "Verify now" }).click();
    await expect(refusal(page)).toBeVisible();
    for (const theme of ["light", "dark"] as const) {
      await setTheme(page, theme);
      const failures = await measureContrast(page, "1157 snapshot detail " + theme, "[data-gate-refusal]");
      expect(failures, theme + " theme").toEqual([]);
    }
  });

  test("control: with the gate not in the way Verify now starts a verification", async ({ page }) => {
    await open(page, PAGE, "default");
    await page.getByRole("button", { name: "Verify now" }).click();
    await expect(page.getByRole("region", { name: "Verification in progress" })).toBeVisible();
    await expect(refusal(page)).toHaveCount(0);
  });
});

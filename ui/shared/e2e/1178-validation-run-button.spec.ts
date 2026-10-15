import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1178: the set page's Validation pane only reported the last result.
 * It now has a Run validation button that starts the set's recovery test
 * through the existing endpoint, shows it in flight, and re-reads the pane
 * when the run ends. Driven with `wire=1` so realAssurance (the code a built
 * app runs) reads the mock's routes.
 *
 * assurance-default-untested: the PostgreSQL set follows the default policy,
 * so a manual run is the built-in structure check, and a scrub has verified content but no
 * recovery test has ever run. A mock run takes about 25 seconds.
 */
const SET = "/sets/production/postgres-primary";
const pane = (page: Page) => page.getByTestId("validation-block");
const run = (page: Page) => pane(page).getByRole("button", { name: "Run validation" });

test.describe("#1178 the Validation pane runs the validation", () => {
  test("#1178 the pane offers Run validation, enabled, beside a pane that has no test on record", async ({ page }) => {
    await open(page, SET, "assurance-default-untested", { wire: true });
    await expect(pane(page).getByTestId("validation-ran-content")).toBeVisible();
    await expect(pane(page).getByTestId("validation-ran-structure")).toHaveCount(0);
    await expect(run(page)).toBeEnabled();
    await expect(pane(page)).toContainText("retnd recovery-test production/postgres-primary");
  });

  test("#1178 pressing it shows the run in flight, disables the button with the reason, and the pane updates when the run ends", async ({ page }) => {
    test.setTimeout(120_000);
    await open(page, SET, "assurance-default-untested", { wire: true });
    await run(page).click();
    await expect(pane(page).getByTestId("operation-progress")).toHaveAttribute("data-state", "running");
    await expect(run(page)).toBeDisabled();
    await expect(pane(page)).toContainText(/already running/i);
    // The result lands in the pane without a reload.
    await expect(pane(page).getByTestId("validation-ran-structure")).toBeVisible({ timeout: 60_000 });
    await expect(run(page), "free again once the run has ended").toBeEnabled();
  });

  test("#1178 the run is on the set terminal", async ({ page }) => {
    test.setTimeout(60_000);
    await open(page, SET, "assurance-default-untested", { wire: true });
    await run(page).click();
    await expect(page.getByLabel("Terminal", { exact: true }).getByRole("log")).toContainText(/recovery test/i);
  });

  test("#1178 a set that can only refuse a test: Run validation is disabled with the reason", async ({ page }) => {
    await open(page, SET, "assurance-no-validator", { wire: true });
    await expect(pane(page)).toBeVisible();
    await expect(run(page)).toBeDisabled();
    await expect(pane(page)).toContainText(/no recovery-test validator is declared/i);
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1178 the pane with the button is readable in the " + theme + " theme (WCAG AA)", async ({ page }) => {
      await open(page, SET, "assurance-default-untested", { wire: true });
      await expect(run(page)).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1178 validation pane " + theme, '[data-testid="validation-block"]')).toEqual([]);
    });
  }
});

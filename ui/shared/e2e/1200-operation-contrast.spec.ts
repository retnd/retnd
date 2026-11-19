/**
 * Issue #1200: every text the Operation page now draws for a run read back
 * from the durable record (its lines, its outcome, the words for a pruned
 * run, the validator output and the terminal twin) meets WCAG AA in both
 * themes. The printed CONTRAST lines are what the PR quotes.
 */
import { expect, test } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const OPS = "/assurance/operations/";

for (const theme of ["light", "dark"] as const) {
  test(`#1200 a finished run read from the record is legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_passed", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "complete");
    await setTheme(page, theme);
    await expect(page.getByTestId("operation-line").first()).toBeVisible();
    await expect(page.getByTestId("validator-output")).toBeVisible();
    expect(await measureContrast(page, "1200 passed " + theme, '[data-testid="operation-page"]')).toEqual([]);
  });

  test(`#1200 a failed run read from the record is legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_failed", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "failed");
    await setTheme(page, theme);
    await expect(page.getByTestId("stage-count")).toBeVisible();
    expect(await measureContrast(page, "1200 failed " + theme, '[data-testid="operation-page"]')).toEqual([]);
  });

  test(`#1200 a running scrub and an unfinished run are legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_scrub_running", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "running");
    await setTheme(page, theme);
    expect(await measureContrast(page, "1200 running scrub " + theme, '[data-testid="operation-page"]')).toEqual([]);
    await open(page, OPS + "aop_1200_unfinished", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "unfinished");
    await setTheme(page, theme);
    expect(await measureContrast(page, "1200 unfinished " + theme, '[data-testid="operation-page"]')).toEqual([]);
  });

  test(`#1200 the looking-for-it note is legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_late", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-looking")).toBeVisible();
    await setTheme(page, theme);
    expect(await measureContrast(page, "1200 looking " + theme, '[data-testid="operation-page"]')).toEqual([]);
  });

  test(`#1200 the words for a pruned run are legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_pruned", "operation-history", { wire: true });
    await expect(page.getByTestId("operation-none")).toBeVisible({ timeout: 15_000 });
    await setTheme(page, theme);
    expect(await measureContrast(page, "1200 pruned " + theme, '[data-testid="operation-page"]')).toEqual([]);
    expect(await measureContrast(page, "1200 footer " + theme, '[data-testid="app-footer"]')).toEqual([]);
  });
}

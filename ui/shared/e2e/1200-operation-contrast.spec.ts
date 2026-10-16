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
    await setTheme(page, theme);
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "complete");
    await expect(page.getByTestId("operation-line").first()).toBeVisible();
    await expect(page.getByTestId("validator-output")).toBeVisible();
    expect(await measureContrast(page, "1200 passed " + theme, "main")).toEqual([]);
  });

  test(`#1200 a failed run read from the record is legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_failed", "operation-history", { wire: true });
    await setTheme(page, theme);
    await expect(page.getByTestId("operation-progress")).toHaveAttribute("data-state", "failed");
    await expect(page.getByTestId("stage-count")).toBeVisible();
    expect(await measureContrast(page, "1200 failed " + theme, "main")).toEqual([]);
  });

  test(`#1200 the words for a pruned run are legible (${theme})`, async ({ page }) => {
    await open(page, OPS + "aop_1200_pruned", "operation-history", { wire: true });
    await setTheme(page, theme);
    await expect(page.getByTestId("operation-none")).toBeVisible();
    expect(await measureContrast(page, "1200 pruned " + theme, "main")).toEqual([]);
  });
}

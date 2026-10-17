import { expect, test } from "@playwright/test";
import { open, row, contrastFailures, measureContrast, setTheme } from "./helpers";

// Panel findings C8 and C9 (issue #985), measured in a browser with the real
// tokens.
//
// C9: a chip being re-checked used to be dimmed with opacity: 0.6, which
// composites its text with what is behind it. The age text measured 2.50:1
// (light) and 3.05:1 (dark) and the label 4.25:1 (light), under 4.5:1, and the
// harness could not see it because getComputedStyle().color ignores opacity.
// contrastReport composites it now (helpers.ts), so this spec fails against
// the old stylesheet and passes against the shape-and-words cue.
//
// C8: a copy whose scrub failed must be shown as failed, with why.

const SET = "/sets/production/postgres-primary";
const region = (page: import("@playwright/test").Page) => page.getByRole("region", { name: "Assurance" });

test.describe("#985 a chip being re-checked keeps its contrast", () => {
  for (const theme of ["light", "dark"] as const) {
    test("every claim chip clears 4.5:1 while a scrub is running, " + theme, async ({ page }) => {
      await open(page, SET, "assurance-scrub-fails");
      const section = region(page);
      await section.getByRole("button", { name: "Scrub now" }).click();
      const content = section.locator('[data-claim="content"]');
      await expect(content).toHaveAttribute("data-checking", "true");
      await setTheme(page, theme);
      // contrastReport composites opacity (helpers.ts), so a dimmed chip is
      // measured as it is drawn; against the old opacity: 0.6 rule this is the
      // line that prints 2.50:1 and 4.25:1 (light) and fails.
      expect(await measureContrast(page, `checking chips, ${theme}`, '[data-claim="content"], [data-claim="recovery"]')).toEqual([]);
      expect(await contrastFailures(page, '[data-testid="assurance-claims"]')).toEqual([]);
      expect(await content.locator(".achip").evaluate((e) => getComputedStyle(e).opacity)).toBe("1");
    });
  }
});

test.describe("#985 a copy that failed is shown as failed", () => {
  for (const theme of ["light", "dark"] as const) {
    test("the failed copy says which copy, how often and why, and clears 4.5:1, " + theme, async ({ page }) => {
      await open(page, "/backups", "assurance-more");
      const r = row(page, "billing-20260827.sql.gz");
      await expect(r.locator('[data-claim="content"]')).toHaveAttribute("data-state", "failed");
      await setTheme(page, theme);
      await expect(r.locator('[data-claim="content"]')).not.toContainText("Never run");
      const failures = r.getByTestId("copy-failures");
      await expect(failures).toContainText("The last scrub of the local copy failed, 3 times in a row");
      await expect(failures).toContainText("sha256 differs from the one recorded at ingest");
      expect(await measureContrast(page, `copy failure, ${theme}`, '[data-testid="copy-failures"]')).toEqual([]);
    });
  }
});

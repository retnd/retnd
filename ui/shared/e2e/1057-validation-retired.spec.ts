import { expect, test } from "@playwright/test";
import { open, row, measureContrast, setTheme } from "./helpers";

// EPIC Q, owner decision 1: the old Validation column is retired. The
// ingest-time check used to be drawn as a green tick and the word "Verified"
// in a column of its own beside the three claims, so an operator saw two
// different things both called verified. The three claims are now the only
// place these pages say it, and a validation that failed is still said, as a
// failure.

test.describe("the Validation column is retired", () => {
  test("the Backups table has no Validation column and no bare Verified", async ({ page }) => {
    await open(page, "/backups");
    await expect(page.getByRole("columnheader", { name: "Assurance" })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /validation/i })).toHaveCount(0);
    await expect(page.getByRole("cell", { name: "Verified", exact: true })).toHaveCount(0);
    const r = row(page, "postgres-prod-20260828.dump.zst");
    await expect(r.getByText("Verified", { exact: true })).toHaveCount(0);
    // The claims that replace it are still on the row.
    await expect(r.locator('[data-testid="assurance-claims"] [data-claim]')).toHaveCount(3);
  });

  test("an artifact page has no green Verified badge, and the claims card is where it says so", async ({ page }) => {
    await open(page, "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst");
    await expect(page.getByRole("heading", { name: /postgres-prod-20260828/ }).first()).toBeVisible();
    await expect(page.getByText("Verified", { exact: true })).toHaveCount(0);
    await expect(page.locator('[data-claim="content"]').first()).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test("a failed validation is said as a failure, with contrast, in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups/production/auth-config/auth-config-20260826.tar.zst");
      const badge = page.getByText("Failed validation", { exact: true }).first();
      await expect(badge).toBeVisible();
      await setTheme(page, theme);
      await expect(page.getByText("Verified", { exact: true })).toHaveCount(0);
      const failures = await measureContrast(page, "failed-validation badge " + theme, "main");
      // Only the badge's own text is judged here; the rest of the page has
      // its own specs.
      expect(failures.filter((f) => f.startsWith("Failed validation"))).toEqual([]);
      // The Artifact card's eyebrow sat under 4.5:1 in both themes (#1229).
      expect(await measureContrast(page, "eyebrow " + theme, ".eyebrow")).toEqual([]);
    });
  }
});

/**
 * Issue #930: the backup set page had an "Exclude" row that could never be
 * populated on a real deployment (the wire has no exclusion field). The mock
 * does fill it for two sets, which is what let the row look alive in a
 * browser, so these two sets are the ones to open. The Include row stays.
 */
import { expect, test } from "@playwright/test";
import { open, setTheme } from "./helpers";

for (const theme of ["light", "dark"] as const) {
  for (const set of ["postgres-primary", "billing-mysql"]) {
    test(`930 ${set} has an Include row and no Exclude row (${theme})`, async ({ page }) => {
      await open(page, "/sets/production/" + set);
      await expect(page.getByText("Include", { exact: true }).first()).toBeVisible();
      await setTheme(page, theme);
      await expect(page.getByText("Exclude", { exact: true })).toHaveCount(0);
      await expect(page.getByText("*.tmp", { exact: true })).toHaveCount(0);
      await expect(page.getByText("Remote folder", { exact: true }).first()).toBeVisible();
    });
  }
}

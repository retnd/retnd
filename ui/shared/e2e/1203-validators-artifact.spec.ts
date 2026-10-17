/**
 * Issue #1203: declared from a backup's own page, a validator defaults to that
 * backup's set and to the pattern derived from the backup's own name.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const POSTGRES_PAGE = "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst";
const BILLING_PAGE = "/backups/production/billing-mysql/billing-20260827.sql.gz";
const POSTGRES = String.raw`^postgres-prod-\d{8}\.dump\.zst$`;
const BILLING = String.raw`^billing-\d{8}\.sql\.gz$`;
const assurance = (p: Page) => p.getByRole("region", { name: "Assurance" });
const dialog = (p: Page) => p.getByRole("dialog", { name: "Add validator" });

async function declare(page: Page, path = POSTGRES_PAGE, scenario = "default") {
  await open(page, path, scenario);
  await assurance(page).getByRole("button", { name: "Declare validator" }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1203 Declare validator, from a backup's page", () => {
  test("defaults to the backup's set and to the pattern its own name gives", async ({ page }) => {
    const dlg = await declare(page);
    await expect(dlg.locator("[data-validator-artifact]")).toContainText("production/postgres-primary/postgres-prod-20260828.dump.zst");
    await expect(dlg.getByLabel("Applies to")).toHaveValue("production/postgres-primary");
    await expect(dlg.getByRole("radio", { name: POSTGRES })).toBeChecked();
    await expect(dlg.getByLabel("Artifact-name pattern")).toHaveValue(POSTGRES);
    await expect(dlg.locator("[data-validator-preview]")).toContainText("postgres-prod-20260828.dump.zst");
  });

  test("the scope can still be widened to every backup set", async ({ page }) => {
    const dlg = await declare(page);
    await dlg.getByLabel("Applies to").selectOption("");
    await expect(dlg.getByLabel("Applies to")).toHaveValue("");
    await expect(dlg.getByLabel("Artifact-name pattern")).toHaveValue(POSTGRES);
  });

  test("declares it, and the page says what now checks this kind of backup", async ({ page }) => {
    const dlg = await declare(page);
    await dlg.getByLabel("Name", { exact: true }).fill("postgres-dump");
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dialog(page)).toHaveCount(0);
    const said = assurance(page).locator("[data-validator-declared]");
    await expect(said).toContainText("Uploaded postgres-dump");
    await expect(said).toContainText(POSTGRES);
    await expect(assurance(page).getByRole("button", { name: "Declare validator" })).toBeFocused();
  });

  test("a backup a validator already covers says which one", async ({ page }) => {
    await open(page, BILLING_PAGE);
    await expect(assurance(page).locator("[data-validator-covered]")).toContainText("billing-dump");
  });

  test("declaring over a covered backup at the same scope is refused before upload", async ({ page }) => {
    const dlg = await declare(page, BILLING_PAGE);
    await expect(dlg.getByRole("radio", { name: BILLING })).toBeChecked();
    await expect(dlg.locator("[data-validator-preview]")).toContainText("two validators");
    await expect(dlg.getByRole("button", { name: "Upload validator" })).toBeDisabled();
  });

  test("a backup no pattern matches shows no coverage line", async ({ page }) => {
    await open(page, POSTGRES_PAGE);
    await expect(assurance(page).getByRole("button", { name: "Declare validator" })).toBeEnabled();
    await expect(assurance(page).locator("[data-validator-covered]")).toHaveCount(0);
  });

  test("is not offered by a deployment that cannot hold validators", async ({ page }) => {
    await open(page, POSTGRES_PAGE, "validators-unavailable");
    await expect(assurance(page).getByRole("button", { name: "Declare validator" })).toBeDisabled();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the declare line and the dialog meet WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, BILLING_PAGE);
      await setTheme(page, theme);
      expect(await measureContrast(page, "1203 covered line " + theme, "[data-validator-covered]"), theme).toEqual([]);
      await assurance(page).getByRole("button", { name: "Declare validator" }).click();
      expect(await measureContrast(page, "1203 declare dialog " + theme, '[role="dialog"]'), theme).toEqual([]);
    });
  }
});

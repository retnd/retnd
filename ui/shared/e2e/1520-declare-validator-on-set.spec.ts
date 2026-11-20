/**
 * Issue #1520: a backup set's own page has a Validation card with a "Run
 * validation" button and nothing that says a validator can be declared, so it
 * looks as though per-backup-set validators are not supported. Declaring one
 * was only possible from Settings or from a single backup's recover screen.
 *
 * What I want: the same "Declare validator" entry on the set page's Validation
 * card, opening the existing dialog on this set (no artifact), with the card
 * saying what now checks the set and printing the terminal twin.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

const SET_PAGE = "/sets/production/postgres-primary";
const block = (p: Page) => p.getByTestId("validation-block");
const dialog = (p: Page) => p.getByRole("dialog", { name: "Add validator" });

async function declare(page: Page) {
  await open(page, SET_PAGE);
  await expect(block(page)).toBeVisible();
  await block(page).getByRole("button", { name: "Declare validator" }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1520 Declare validator, from a backup set's page", () => {
  test("#1520 the Validation card offers Declare validator", async ({ page }) => {
    await open(page, SET_PAGE);
    await expect(block(page).getByRole("button", { name: "Declare validator" })).toBeVisible();
  });

  test("#1520 the dialog opens on this set and on no particular backup", async ({ page }) => {
    const dlg = await declare(page);
    await expect(dlg.getByLabel("Applies to")).toHaveValue("production/postgres-primary");
    await expect(dlg.locator("[data-validator-artifact]")).toHaveCount(0);
  });

  test("#1520 declaring one says what now checks this set, and returns focus to the button", async ({ page }) => {
    const dlg = await declare(page);
    await dlg.getByLabel("Name", { exact: true }).fill("postgres-set-check");
    // No backup to derive a pattern from on a set's page, so the operator gives one.
    await dlg.getByLabel("Artifact-name pattern").fill(String.raw`^postgres-prod-\d{8}\.dump\.zst$`);
    await dlg.getByRole("button", { name: "Start from the default script" }).click();
    await dlg.getByRole("button", { name: "Upload validator" }).click();
    await expect(dialog(page)).toHaveCount(0);
    await expect(block(page).locator("[data-validator-declared]")).toContainText("Uploaded postgres-set-check");
    await expect(block(page).getByRole("button", { name: "Declare validator" })).toBeFocused();
  });

  test("#1520 the card says where the scripts live and prints the terminal twin", async ({ page }) => {
    await open(page, SET_PAGE);
    await expect(block(page)).toContainText(/recovery-test|validators? (are|is) kept/i);
    await expect(block(page)).toContainText("retnd assurance validators add");
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1520 the card's new text reads at 4.5:1 in the " + theme + " theme", async ({ page }) => {
      await open(page, SET_PAGE);
      await expect(block(page).getByRole("button", { name: "Declare validator" })).toBeVisible();
      await setTheme(page, theme);
      expect(await measureContrast(page, "1520 validation card " + theme, '[data-testid="validation-block"]')).toEqual([]);
    });
  }
});

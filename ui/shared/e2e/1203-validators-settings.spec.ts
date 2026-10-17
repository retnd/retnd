/**
 * Issue #1203, Settings: the recovery-test validators card. An operator can see
 * every custom validator (its pattern, where it applies, how many existing
 * backups it matches), read a script back, remove one, and is told so when the
 * deployment cannot hold them at all.
 *
 * The card is the one place validators are managed. Declaring one from a
 * backup's own page is 1203-validators-artifact.spec.ts.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureBorders, measureContrast, open, setTheme } from "./helpers";

const card = (p: Page) => p.getByRole("region", { name: "Recovery-test validators" });
const rowOf = (p: Page, name: string) => card(p).getByRole("row").filter({ hasText: name });

test.describe("#1203 Settings, Recovery-test validators", () => {
  test("lists each validator with its pattern, scope and match count", async ({ page }) => {
    await open(page, "/settings");
    await expect(card(page)).toBeVisible();
    const billing = rowOf(page, "billing-dump");
    await expect(billing).toContainText(String.raw`^billing-\d{8}\.sql\.gz$`);
    await expect(billing).toContainText("production/billing-mysql");
    await expect(billing).toContainText("2 backups");
    const tars = rowOf(page, "any-tar");
    await expect(tars).toContainText("Every backup set");
    await expect(tars).toContainText("1 backup");
    await expect(tars).not.toContainText("1 backups");
  });

  test("says a pattern that is not anchored matches anywhere in a name", async ({ page }) => {
    await open(page, "/settings");
    await expect(rowOf(page, "any-tar")).toContainText("Not anchored");
    await expect(rowOf(page, "billing-dump")).not.toContainText("Not anchored");
  });

  test("says in what order the checks are tried", async ({ page }) => {
    await open(page, "/settings");
    const order = card(page).locator("[data-validator-order]");
    await expect(order).toContainText("validator_id");
    await expect(order).toContainText("built-in");
    await expect(order).toContainText("scoped to the backup set");
  });

  test("an empty deployment says none are declared and offers the first", async ({ page }) => {
    await open(page, "/settings", "validators-none");
    await expect(card(page)).toContainText("No custom validators");
    await expect(card(page).getByRole("button", { name: "Add validator" })).toBeEnabled();
    await expect(card(page).getByRole("row")).toHaveCount(0);
  });

  test("a deployment that cannot hold validators says why and offers nothing", async ({ page }) => {
    await open(page, "/settings", "validators-unavailable");
    await expect(card(page)).toContainText("no workflow root is configured");
    await expect(card(page).getByRole("button", { name: "Add validator" })).toBeDisabled();
  });

  test("reads a script back", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByRole("button", { name: "View script billing-dump" }).click();
    const dlg = page.getByRole("dialog", { name: "Script billing-dump" });
    await expect(dlg).toBeVisible();
    await expect(dlg.locator("pre")).toContainText("#!/usr/bin/env bash");
    await expect(dlg.locator("[data-script-sha256]")).toHaveText(/^[0-9a-f]{64}$/);
    await dlg.getByRole("button", { name: "Close" }).click();
    await expect(dlg).toHaveCount(0);
  });

  test("removing one asks first, names it, and takes it off the list", async ({ page }) => {
    await open(page, "/settings");
    await card(page).getByRole("button", { name: "Remove billing-dump" }).click();
    const dlg = page.getByRole("dialog", { name: "Remove validator billing-dump" });
    await expect(dlg).toContainText("production/billing-mysql");
    await expect(dlg).toContainText("falls back");
    await dlg.getByRole("button", { name: "Cancel" }).click();
    await expect(rowOf(page, "billing-dump")).toHaveCount(1);

    await card(page).getByRole("button", { name: "Remove billing-dump" }).click();
    await page.getByRole("dialog", { name: "Remove validator billing-dump" }).getByRole("button", { name: "Remove validator" }).click();
    await expect(rowOf(page, "billing-dump")).toHaveCount(0);
    await expect(card(page).locator("[data-validators-status]")).toContainText("Removed billing-dump");
    await expect(rowOf(page, "any-tar")).toHaveCount(1);
  });

  test("removing is not behind the destructive gate or the password", async ({ page }) => {
    // It only stops a script from running, so it stays CSRF only (#1203).
    await open(page, "/settings", "gate-shut");
    await card(page).getByRole("button", { name: "Remove any-tar" }).click();
    const dlg = page.getByRole("dialog", { name: "Remove validator any-tar" });
    await expect(dlg.getByLabel("Your password")).toHaveCount(0);
    await dlg.getByRole("button", { name: "Remove validator" }).click();
    await expect(rowOf(page, "any-tar")).toHaveCount(0);
  });

  test("the section is a Settings anchor", async ({ page }) => {
    await open(page, "/settings#validators");
    await expect(page.locator("#validators")).toBeVisible();
    await expect(page.locator("#validators")).toBeInViewport();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the card meets WCAG AA in ${theme}`, async ({ page }) => {
      await open(page, "/settings");
      await setTheme(page, theme);
      await expect(card(page)).toBeVisible();
      expect(await measureContrast(page, "1203 settings card " + theme, '[aria-label="Recovery-test validators"]'), theme).toEqual([]);
      expect(await measureBorders(page, "1203 settings card " + theme, '[aria-label="Recovery-test validators"] button'), theme).toEqual([]);
    });
  }
});

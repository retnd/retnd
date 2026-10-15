/**
 * Issue #1191 on the set page: Recover this backup set starts on
 * "Source host (SFTP)", and the whole set goes into one new folder there.
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";
import { expectNothingPreselected, expectReadOnlyWarning, expectSourcePreselected, expectWritableAndGated } from "./1191-source-host";

const SET = "/sets/production/postgres-primary";
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover this backup set" });
const go = (p: Page) => dialog(p).getByRole("button", { name: "Recover this backup set" });

async function fromTheSet(page: Page, scenario = "assurance-set-recover") {
  await open(page, SET, scenario);
  await page.getByRole("button", { name: "Recover this backup set", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1191 set page: Recover this backup set", () => {
  test("preselects Source host (SFTP)", async ({ page }) => {
    const dlg = await fromTheSet(page);
    await expectSourcePreselected(dlg, "backup-agent@prod-db-01.internal", "/backups/postgresql/");
    await expectWritableAndGated(dlg, go(page));
    await expect(dlg.getByText(/retnd recover --set production\/postgres-primary --to source:retnd-recovered-\S+ --acknowledge-source-write/).first()).toBeVisible();
  });

  test("a read-only pull account is said before the run", async ({ page }) => {
    const dlg = await fromTheSet(page, "source-read-only");
    await expectReadOnlyWarning(dlg, go(page));
  });

  test("a non-SFTP source preselects nothing and says why", async ({ page }) => {
    const dlg = await fromTheSet(page, "source-not-sftp");
    await expectNothingPreselected(dlg, "Where should the set go?");
    await expect(go(page)).toBeDisabled();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the read-only warning meets AA in ${theme}`, async ({ page }) => {
      const dlg = await fromTheSet(page, "source-read-only");
      await setTheme(page, theme);
      await expect(dlg.getByTestId("source-preflight")).toBeVisible();
      expect(await measureContrast(page, "1191 set page " + theme, "[data-source-host]")).toEqual([]);
    });
  }
});

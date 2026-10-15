/**
 * Issue #1191 on a backup's own page: the Assurance card's Recover artifact
 * dialog starts on "Source host (SFTP)".
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";
import { expectNothingPreselected, expectReadOnlyWarning, expectSourcePreselected, expectWritableAndGated } from "./1191-source-host";

const PAGE = "/backups/production/postgres-primary/postgres-prod-20260828.dump.zst";
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const go = (p: Page) => dialog(p).getByRole("button", { name: "Recover artifact" });

async function fromThePage(page: Page, scenario = "default") {
  await open(page, PAGE, scenario);
  await page.getByRole("region", { name: "Assurance" }).getByRole("button", { name: "Recover artifact" }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1191 backup page: Recover artifact", () => {
  test("preselects Source host (SFTP)", async ({ page }) => {
    const dlg = await fromThePage(page);
    await expectSourcePreselected(dlg, "backup-agent@prod-db-01.internal", "/backups/postgresql/");
    await expectWritableAndGated(dlg, go(page));
  });

  test("a read-only pull account is said before the run", async ({ page }) => {
    const dlg = await fromThePage(page, "source-read-only");
    await expectReadOnlyWarning(dlg, go(page));
  });

  test("a non-SFTP source preselects nothing and says why", async ({ page }) => {
    const dlg = await fromThePage(page, "source-not-sftp");
    await expectNothingPreselected(dlg, "Where should the artifact go?");
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the not-SFTP reason meets AA in ${theme}`, async ({ page }) => {
      const dlg = await fromThePage(page, "source-not-sftp");
      await setTheme(page, theme);
      await expect(dlg.getByTestId("source-unavailable")).toBeVisible();
      expect(await measureContrast(page, "1191 backup page " + theme, "[data-source-host]")).toEqual([]);
    });
  }
});

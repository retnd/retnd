/**
 * Issue #1191 on the Backups list: the Recover artifact dialog a row opens,
 * and the selection bar, both start on "Source host (SFTP)".
 */
import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, row, setTheme } from "./helpers";
import { SOURCE, expectNothingPreselected, expectReadOnlyWarning, expectSourcePreselected, expectWritableAndGated } from "./1191-source-host";

const PG = "postgres-prod-20260828.dump.zst";
const GROUP = "Where should the artifact go?";
const dialog = (p: Page) => p.getByRole("dialog", { name: "Recover artifact" });
const go = (p: Page) => dialog(p).getByRole("button", { name: "Recover artifact" });
const bar = (p: Page) => p.getByRole("region", { name: "Selected backups" });

async function fromTheList(page: Page, scenario = "default") {
  await open(page, "/backups", scenario);
  await row(page, PG).click();
  await page.getByRole("button", { name: "Recover artifact", exact: true }).click();
  await expect(dialog(page)).toBeVisible();
  return dialog(page);
}

test.describe("#1191 Backups list: Recover artifact", () => {
  test("preselects Source host (SFTP), built from the set's own source", async ({ page }) => {
    const dlg = await fromTheList(page);
    await expectSourcePreselected(dlg, "backup-agent@prod-db-01.internal", "/backups/postgresql/");
    await expectWritableAndGated(dlg, go(page));
    // the other destinations are still there to pick
    await dlg.getByRole("radio", { name: "Staging path on the host" }).check();
    await expect(dlg.getByRole("radio", { name: SOURCE })).not.toBeChecked();
    await expect(dlg.getByText("retnd recover production/postgres-primary/" + PG + " --to", { exact: false })).toBeVisible();
  });

  test("the terminal line for the default is the source, with its acknowledgement", async ({ page }) => {
    const dlg = await fromTheList(page);
    await dlg.getByRole("checkbox", { name: /I understand this writes onto/ }).check();
    await expect(dlg.getByText(/retnd recover production\/postgres-primary\/postgres-prod-20260828\.dump\.zst --to source:retnd-recovered-\S+ --acknowledge-source-write/)).toBeVisible();
  });

  test("a read-only pull account is said before the run", async ({ page }) => {
    const dlg = await fromTheList(page, "source-read-only");
    await expectSourcePreselected(dlg, "backup-agent@prod-db-01.internal", "/backups/postgresql/");
    await expectReadOnlyWarning(dlg, go(page));
  });

  test("a set whose source is not SFTP preselects nothing and says why", async ({ page }) => {
    const dlg = await fromTheList(page, "source-not-sftp");
    await expectNothingPreselected(dlg, GROUP);
    await expect(go(page)).toBeDisabled();
  });

  test("a local-account profile asks for the password again before writing to the source", async ({ page }) => {
    const dlg = await fromTheList(page, "local-account-profile");
    await expect(dlg.getByRole("radio", { name: SOURCE })).toBeChecked();
    await dlg.getByRole("checkbox", { name: /I understand this writes onto/ }).check();
    await expect(dlg.getByLabel("Your password")).toBeVisible();
    await expect(go(page)).toBeDisabled();
    await dlg.getByLabel("Your password").fill("correct-horse-battery");
    await expect(go(page)).toBeEnabled();
  });

  for (const theme of ["light", "dark"] as const) {
    test(`the source-host text meets AA in ${theme}`, async ({ page }) => {
      const dlg = await fromTheList(page, "source-read-only");
      await setTheme(page, theme);
      await expect(dlg.getByTestId("source-preflight")).toBeVisible();
      const bad = await measureContrast(page, "1191 list dialog " + theme, "[data-source-host]");
      expect(bad).toEqual([]);
    });
  }
});

test.describe("#1191 Backups list: the selection bar", () => {
  const rowBox = (p: Page, f: string) => p.getByRole("checkbox", { name: "Select " + f, exact: true });

  test("one set ticked: Source host (SFTP) is the preselected choice", async ({ page }) => {
    await open(page, "/backups", "backups-many");
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await expect(bar(page)).toBeVisible();
    await expectSourcePreselected(bar(page), "prod-db-01.internal", "/backups/postgresql/");
  });

  test("backups of two sets ticked: nothing preselected, and it says why", async ({ page }) => {
    await open(page, "/backups", "backups-many");
    await rowBox(page, "postgres-primary-20260801.bak").check();
    await rowBox(page, "billing-mysql-20260802.bak").check();
    const radio = bar(page).getByRole("radio", { name: SOURCE });
    await expect(radio).toBeDisabled();
    await expect(bar(page).getByRole("radiogroup", { name: "Where should the backups go?" }).getByRole("radio", { checked: true })).toHaveCount(0);
    await expect(bar(page).getByTestId("source-unavailable")).toContainText("2 backup sets");
  });
});

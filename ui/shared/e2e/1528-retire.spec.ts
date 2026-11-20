import { expect, test } from "@playwright/test";
import type { Page } from "@playwright/test";
import { measureContrast, open, setTheme } from "./helpers";

/**
 * Issue #1528: a FAILED backup whose source is gone can be retired from the
 * browser, as the "Retire a failed backup" design draws it.
 *
 * The fixture is the forge set in miniature (mock scenario `retire-forge`):
 * media-week34.tar FAILED with its source gone, and media-week35 and 36 backed
 * up cleanly after it. `retire-source-present` is the same with the source
 * still on the host, and `backups-ingest-failed` is the set with nothing newer.
 * Everything the dialog says about a backup comes from the engine's preview
 * (GET /backups/{id}/retire), which the mock answers with the engine's own
 * sentences.
 */
const WEEK34 = "media-week34.tar";
const WEEK34_ID = "media/weekly-archive/" + WEEK34;
const BILLING = "billing-20260827.sql.gz";
const dialog = (page: Page) => page.getByRole("dialog");

async function openRetireFromBackupPage(page: Page, scenario: string) {
  await open(page, "/backups/media/weekly-archive/" + WEEK34, scenario);
  await page.getByRole("button", { name: "Retire…" }).click();
  await expect(dialog(page)).toBeVisible();
}

test.describe("#1528 retiring a failed backup whose source is gone", () => {
  test("#1528 the backup page's Retire dialog says the source is gone, names the successor, and retiring keeps the record", async ({ page }) => {
    await openRetireFromBackupPage(page, "retire-forge");
    await expect(dialog(page).getByRole("heading", { name: "Retire " + WEEK34 + "?" })).toBeVisible();
    const row = dialog(page).getByTestId("retire-row-" + WEEK34);
    await expect(row).toContainText("Gone from the host");
    await expect(row).toContainText("copy_to_local: not_found");
    const replaces = dialog(page).getByRole("region", { name: "What replaces it" });
    await expect(replaces).toContainText("media-week36.tar");
    const happens = dialog(page).getByRole("region", { name: "What happens" });
    await expect(happens).toContainText("The backup record stays, marked RETIRED");
    await expect(happens).toContainText("If the source object appears again, the next cycle puts this backup back into the pipeline.");
    await dialog(page).getByLabel("Note (optional)").fill("host retention removed it");
    await expect(dialog(page)).toContainText("retnd retire " + WEEK34_ID + " --note 'host retention removed it'");

    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await expect(dialog(page).getByRole("heading", { name: "1 backup retired" })).toBeVisible();
    await expect(dialog(page).getByRole("status")).toContainText("replaced by media-week36.tar");
    await dialog(page).getByRole("button", { name: "Close" }).click();

    // The record stays, said in the design's words, and the Retire control is gone.
    const card = page.getByTestId("retirement-card");
    await expect(card).toContainText("Retired is not a failure, and nothing is waiting on it.");
    await expect(card).toContainText("If the source file appears on the host again, the next cycle puts this backup back into the pipeline.");
    await expect(card).toContainText("Source gone from the host. Replaced by media-week36.tar.");
    await expect(card).toContainText("Note: host retention removed it");
    await expect(page.getByRole("button", { name: "Retire…" })).toHaveCount(0);
  });

  test("#1528 a source still on the host is refused with Retry instead, and Retire stays off", async ({ page }) => {
    await openRetireFromBackupPage(page, "retire-source-present");
    const row = dialog(page).getByTestId("retire-row-" + WEEK34);
    await expect(row).toContainText("Still on the host");
    await expect(row).toContainText("The source object is still there, so this backup can still be fetched. Retry it instead.");
    await expect(row.getByRole("button", { name: "Retry instead" })).toBeVisible();
    await expect(dialog(page).getByRole("button", { name: "Retire", exact: true })).toBeDisabled();
    await expect(dialog(page).getByRole("region", { name: "What happens" })).toHaveCount(0);
  });

  test("#1528 with no newer good backup the acknowledgement is required before Retire turns on", async ({ page }) => {
    await openRetireFromBackupPage(page, "backups-ingest-failed");
    await expect(dialog(page)).toContainText("This set has no newer good backup.");
    const retire = dialog(page).getByRole("button", { name: /^Retire/ }).last();
    await expect(retire).toBeDisabled();
    await dialog(page).getByRole("checkbox", { name: /I understand there is no replacement/ }).check();
    await expect(dialog(page)).toContainText("retnd retire " + WEEK34_ID + " --no-successor");
    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await expect(dialog(page).getByRole("status")).toContainText("retired with no replacement");
  });

  test("#1528 the Backups row menu offers Retire on the FAILED backup only, with its terminal twin", async ({ page }) => {
    await open(page, "/backups", "retire-forge");
    await page.getByRole("button", { name: "Actions for " + BILLING }).click();
    await expect(page.getByRole("menu").getByRole("menuitem", { name: "Retire…" })).toHaveCount(0);
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Actions for " + WEEK34 }).click();
    const item = page.getByRole("menu").getByRole("menuitem", { name: "Retire…" });
    await item.focus();
    await expect(page.getByRole("menu").getByTestId("menu-echo")).toContainText("retnd retire " + WEEK34_ID);
    await item.click();
    await expect(dialog(page).getByTestId("retire-row-" + WEEK34)).toContainText("Gone from the host");
  });

  test("#1528 after retiring, the set card counts it apart as retired", async ({ page }) => {
    await openRetireFromBackupPage(page, "retire-forge");
    await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
    await dialog(page).getByRole("button", { name: "Close" }).click();
    await page.getByRole("link", { name: /Backup sets/ }).first().click();
    await expect(page.getByText("1 retired")).toBeVisible();
  });

  for (const theme of ["light", "dark"] as const) {
    test("#1528 every text pair in the Retire dialog and the retirement card meets WCAG AA in the " + theme + " theme", async ({ page }) => {
      await open(page, "/backups/media/weekly-archive/" + WEEK34, "retire-forge");
      await setTheme(page, theme);
      await page.getByRole("button", { name: "Retire…" }).click();
      await expect(dialog(page).getByTestId("retire-row-" + WEEK34)).toContainText("Gone from the host");
      expect(await measureContrast(page, "1528 dialog " + theme, "[role=dialog]"), "pairs under AA").toEqual([]);
      await dialog(page).getByRole("button", { name: "Retire 1 backup" }).click();
      await dialog(page).getByRole("button", { name: "Close" }).click();
      expect(await measureContrast(page, "1528 retirement card " + theme, "[data-testid=retirement-card]"), "pairs under AA").toEqual([]);
    });
  }
});
